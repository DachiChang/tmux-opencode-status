// Package fakeoc is a controlled fake OpenCode service (httptest + SSE) for tests only.
// It serves the read-only endpoints and /api/event from the v2.3.0 fixtures (testdata/opencode-2.3.0/)
// and records every request it receives, so tests can assert that no write API was ever called.
package fakeoc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	User     = "opencode"
	Password = "fake-secret"
	PID      = 4242
	Version  = "2.3.0"
)

// Valid types, same as the contract document; an unknown type returns 400 (reproducing observed behavior).
var knownTypes = map[string]bool{
	"session.created": true, "session.deleted": true, "session.moved": true, "session.renamed": true,
	"session.archived": true, "session.unarchived": true, "session.forked": true,
	"session.execution.started": true, "session.execution.succeeded": true,
	"session.execution.failed": true, "session.execution.interrupted": true,
	"session.viewed": true, "permission.asked": true, "permission.replied": true,
	"form.created": true, "form.replied": true, "form.cancelled": true,
	"session.inbox.enqueued": true, "session.inbox.delivered": true, "session.inbox.cancelled": true,
	"location.shutdown": true,
}

type session struct {
	id, parent string
	title      string // empty = REST data carries no title (simulates old data; the sidebar falls back to the title in tabs.json)
	outcome    string
	idle, view int64
	running    bool
}

type item struct{ id, session string }

type event struct {
	typ  string
	body string
}

type sub struct {
	ch      chan event
	types   map[string]bool
	done    chan struct{}
	stalled bool // silent: the connection stays open but sends no more bytes (events lost, heartbeat stopped)
}

type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	sessions map[string]*session
	order    []string
	perms    []item
	forms    []item
	subs     map[*sub]bool
	clock    int64
	seq      int
	requests []string // "METHOD path"
	writes   []string // non-GET requests (must always be empty)
	connects int
	eventQs  []string // query of each /api/event connection
	broken   []string // REST GETs whose request URI contains any of these strings return 500 (simulates a transient failure)
	holds    []*hold  // Holds that have not intercepted a request yet

	heartbeat time.Duration // how often to send ": heartbeat"; 0 means never
}

func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, sessions: map[string]*session{}, subs: map[*sub]bool{}, clock: 1_000_000}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Close() {
	s.Drop()
	s.srv.Close()
}

// URL is the url that service.json would contain: the host is 0.0.0.0 (same as the real service), and callers must connect to 127.0.0.1 instead.
func (s *Server) URL() string {
	return strings.Replace(s.srv.URL, "127.0.0.1", "0.0.0.0", 1)
}

// WriteServiceFile writes service.json with mode 0600.
func (s *Server) WriteServiceFile(path string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": "svc", "version": Version, "url": s.URL(), "pid": PID, "password": Password})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// Writes returns all non-GET requests received; under the read-only contract it must be empty.
func (s *Server) Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// Requests returns all requests so far ("METHOD path").
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// SessionRequests is the number of requests to /api/session* (used to verify that no session data is queried when nothing is tracked).
func (s *Server) SessionRequests() int {
	n := 0
	for _, r := range s.Requests() {
		if strings.Contains(r, " /api/session") {
			n++
		}
	}
	return n
}

// Connects is the number of times /api/event was connected.
func (s *Server) Connects() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects
}

// EventQueries is the query of each /api/event connection (used to verify types filtering).
func (s *Server) EventQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.eventQs...)
}

// Subscribers is the number of SSE connections currently open.
func (s *Server) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Break makes REST GETs whose URI (including query) contains substr return 500 until Heal; /api/info and /api/event are unaffected.
func (s *Server) Break(substr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broken = append(s.broken, substr)
}

type hold struct {
	substr   string
	arrived  chan struct{}
	release  chan struct{}
	captured bool
}

// Hold intercepts the "next" REST GET whose URI contains substr: the service computes the response as usual (content fixed at the moment of interception),
// but delays sending it until release is called, simulating a stale response that arrives late. arrived is closed when the request is held.
func (s *Server) Hold(substr string) (arrived <-chan struct{}, release func()) {
	h := &hold{substr: substr, arrived: make(chan struct{}), release: make(chan struct{})}
	s.mu.Lock()
	s.holds = append(s.holds, h)
	s.mu.Unlock()
	var once sync.Once
	return h.arrived, func() { once.Do(func() { close(h.release) }) }
}

func (s *Server) takeHold(uri string) *hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, h := range s.holds {
		if strings.Contains(uri, h.substr) {
			s.holds = append(s.holds[:i], s.holds[i+1:]...)
			return h
		}
	}
	return nil
}

// Heal clears all Breaks.
func (s *Server) Heal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broken = nil
}

func (s *Server) isBroken(uri string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.broken {
		if strings.Contains(uri, b) {
			return true
		}
	}
	return false
}

// SetHeartbeat sets how often SSE connections created afterwards send ": heartbeat"; 0 means never.
func (s *Server) SetHeartbeat(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeat = d
}

// Stall makes all current SSE connections silent: they stay open (no FIN/RST) but send no more bytes,
// and events occurring afterwards are not replayed. It simulates a half-open connection; newly created connections are unaffected.
func (s *Server) Stall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for x := range s.subs {
		x.stalled = true
	}
}

// Drop closes all SSE connections (simulates a connection loss; events occurring while disconnected are not replayed).
func (s *Server) Drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for x := range s.subs {
		close(x.done)
		delete(s.subs, x)
	}
}

func (s *Server) now() int64 { s.clock += 7; return s.clock }

func (s *Server) get(id string) *session {
	x := s.sessions[id]
	if x == nil {
		x = &session{id: id}
		s.sessions[id] = x
		s.order = append(s.order, id)
	}
	return x
}

// emit is called with the lock held.
func (s *Server) emit(typ string, created int64, data map[string]any) {
	s.seq++
	b, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("evt_%d", s.seq), "created": created, "type": typ, "data": data})
	for x := range s.subs {
		if x.stalled || (x.types != nil && !x.types[typ]) {
			continue
		}
		select {
		case x.ch <- event{typ, string(b)}:
		default:
		}
	}
}

// ---- State changes (also emit the matching event; with no SSE connection only the state changes) ----

func (s *Server) AddSession(id, parent string) { s.addSession(id, parent, true) }

// AddSessionWithoutParentInEvent creates a descendant, but its session.created event lacks parentID
// (REST data still has the parent-child relationship): the event alone cannot place it in the family.
func (s *Server) AddSessionWithoutParentInEvent(id, parent string) { s.addSession(id, parent, false) }

func (s *Server) addSession(id, parent string, parentInEvent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.get(id)
	x.parent = parent
	d := map[string]any{"sessionID": id}
	if x.title != "" {
		d["title"] = x.title
	}
	if parent != "" && parentInEvent {
		d["parentID"] = parent
	}
	s.emit("session.created", s.now(), d)
}

func (s *Server) Start(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.get(id).running = true
	s.emit("session.execution.started", s.now(), map[string]any{"sessionID": id})
}

// Finish ends a run: outcome is succeeded | failed | interrupted.
func (s *Server) Finish(id, outcome string) { s.finish(id, outcome, "user") }

// FinishInterrupted interrupts a run with the given reason (user | shutdown | superseded | inactivity),
// and also changes the service data (no longer running, with idle and outcome).
func (s *Server) FinishInterrupted(id, reason string) { s.finish(id, "interrupted", reason) }

// EmitInterrupted emits only the session.execution.interrupted event without changing service data:
// used to verify that the sidebar does not treat the event itself as fact, but relies on reread data.
func (s *Server) EmitInterrupted(id, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit("session.execution.interrupted", s.now(), map[string]any{"sessionID": id, "reason": reason})
}

func (s *Server) finish(id, outcome, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.get(id)
	x.running, x.outcome, x.idle = false, outcome, s.now()
	d := map[string]any{"sessionID": id}
	switch outcome {
	case "failed":
		d["error"] = map[string]any{"type": "unknown", "message": "x"}
	case "interrupted":
		d["reason"] = reason
	}
	s.emit("session.execution."+outcome, x.idle, d)
}

// View simulates another client marking the session as read.
func (s *Server) View(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.get(id)
	x.view = x.idle
	s.emit("session.viewed", s.now(), map[string]any{"sessionID": id, "idle": x.idle})
}

// Seed sets session state directly without emitting events (simulates data that existed before the plugin started).
func (s *Server) Seed(id, parent, outcome string, idle, viewed int64, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.get(id)
	x.parent, x.outcome, x.idle, x.view, x.running = parent, outcome, idle, viewed, running
}

// SeedTitle sets the session's API title without emitting events (data that existed before the plugin started).
func (s *Server) SeedTitle(id, title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.get(id).title = title
}

// Rename changes the session's API title and emits session.renamed (durable; data{sessionID,title}).
func (s *Server) Rename(id, title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.get(id).title = title
	s.emit("session.renamed", s.now(), map[string]any{"sessionID": id, "title": title})
}

// ---- Pending permission requests and unanswered questions (OpenCode calls them forms) ----
// Ask*/Resolve*/Cancel* simulate "the user handling it inside OpenCode": they change service data and emit events;
// the sidebar itself never calls these write endpoints. Seed* only places data without emitting events (already present before the plugin started).

func (s *Server) AskPermission(id, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perms = append(s.perms, item{id, sessionID})
	s.emit("permission.asked", s.now(), map[string]any{"id": id, "sessionID": sessionID, "action": "bash", "resources": []string{"x"}, "save": []string{}})
}

// ReplyPermission simulates the user replying to (approving or denying) the request in OpenCode.
func (s *Server) ReplyPermission(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := ""
	s.perms, sess = remove(s.perms, id)
	s.emit("permission.replied", s.now(), map[string]any{"sessionID": sess, "requestID": id, "reply": "once"})
}

func (s *Server) AskForm(id, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forms = append(s.forms, item{id, sessionID})
	s.emit("form.created", s.now(), map[string]any{"form": map[string]any{"id": id, "sessionID": sessionID, "title": "q", "fields": []any{}}})
}

// AnswerForm simulates the user answering the question.
func (s *Server) AnswerForm(id string) { s.closeForm("form.replied", id) }

// CancelForm simulates the question being cancelled.
func (s *Server) CancelForm(id string) { s.closeForm("form.cancelled", id) }

func (s *Server) closeForm(typ, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := ""
	s.forms, sess = remove(s.forms, id)
	d := map[string]any{"id": id, "sessionID": sess}
	if typ == "form.replied" {
		d["answer"] = map[string]any{}
	}
	s.emit(typ, s.now(), d)
}

func remove(list []item, id string) ([]item, string) {
	out := list[:0:0]
	sess := ""
	for _, it := range list {
		if it.id == id {
			sess = it.session
			continue
		}
		out = append(out, it)
	}
	return out, sess
}

// SeedPermission/SeedForm place pending items without emitting events.
func (s *Server) SeedPermission(id, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perms = append(s.perms, item{id, sessionID})
}

func (s *Server) SeedForm(id, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forms = append(s.forms, item{id, sessionID})
}

// ForgetPending simulates a service restart: pending requests are not persisted and all disappear after a restart (no events emitted).
func (s *Server) ForgetPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perms, s.forms = nil, nil
}

func (s *Server) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	s.emit("session.deleted", s.now(), map[string]any{"sessionID": id})
}

// ---- HTTP ----

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path != "/api/info" && r.URL.Path != "/api/event" {
		if h := s.takeHold(r.URL.RequestURI()); h != nil {
			rec := httptest.NewRecorder()
			s.serve(rec, r)
			close(h.arrived)
			select {
			case <-h.release:
			case <-r.Context().Done():
				return
			}
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
	}
	s.serve(w, r)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	if r.Method != http.MethodGet {
		s.writes = append(s.writes, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		http.Error(w, `{"_tag":"MethodNotAllowed"}`, http.StatusMethodNotAllowed)
		return
	}
	s.mu.Unlock()
	if u, p, ok := r.BasicAuth(); !ok || u != User || p != Password {
		writeJSON(w, 401, map[string]any{"_tag": "UnauthorizedError"})
		return
	}
	path := r.URL.Path
	if path != "/api/info" && path != "/api/event" && s.isBroken(r.URL.RequestURI()) {
		writeJSON(w, 500, map[string]any{"_tag": "InternalError"})
		return
	}
	switch {
	case path == "/api/info":
		writeJSON(w, 200, map[string]any{"version": Version, "pid": PID, "hostname": "h", "urls": []string{}})
	case path == "/api/event":
		s.serveEvents(w, r)
	case path == "/api/session/active":
		s.mu.Lock()
		out := map[string]any{}
		for id, x := range s.sessions {
			if x.running {
				out[id] = map[string]string{"type": "running"}
			}
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"data": out})
	case path == "/api/session":
		s.listChildren(w, r)
	case strings.HasPrefix(path, "/api/session/"):
		rest := strings.TrimPrefix(path, "/api/session/")
		id, sub, _ := strings.Cut(rest, "/")
		s.mu.Lock()
		x := s.sessions[id]
		var body any
		switch sub {
		case "":
			if x != nil {
				body = s.sessionJSON(x)
			}
		case "permission":
			if x != nil {
				body = s.items(s.perms, id)
			}
		case "form":
			if x != nil {
				body = s.items(s.forms, id)
			}
		}
		s.mu.Unlock()
		if x == nil {
			writeJSON(w, 404, map[string]any{"_tag": "SessionNotFoundError", "sessionID": id, "message": "Session not found: " + id})
			return
		}
		writeJSON(w, 200, map[string]any{"data": body})
	default:
		writeJSON(w, 404, map[string]any{"_tag": "NotFound"})
	}
}

func (s *Server) sessionJSON(x *session) map[string]any {
	t := map[string]any{"created": 1, "updated": 1}
	if x.idle != 0 {
		t["idle"] = x.idle
	}
	if x.view != 0 {
		t["viewed"] = x.view
	}
	m := map[string]any{"id": x.id, "time": t}
	if x.title != "" {
		m["title"] = x.title
	}
	if x.parent != "" {
		m["parentID"] = x.parent
	}
	if x.outcome != "" {
		m["outcome"] = x.outcome
	}
	return m
}

func (s *Server) items(list []item, session string) []map[string]any {
	out := []map[string]any{}
	for _, it := range list {
		if it.session == session {
			out = append(out, map[string]any{"id": it.id, "sessionID": session})
		}
	}
	return out
}

// listChildren reproduces the observed pagination behavior: cursor.next exists whenever the page has data (only an empty page ends it).
func (s *Server) listChildren(w http.ResponseWriter, r *http.Request) {
	parent := r.URL.Query().Get("parentID")
	if r.URL.Query().Get("limit") == "0" {
		writeJSON(w, 400, map[string]any{"_tag": "InvalidRequestError"})
		return
	}
	s.mu.Lock()
	var kids []string
	for _, id := range s.order {
		if x := s.sessions[id]; x != nil && x.parent == parent && parent != "" {
			kids = append(kids, id)
		}
	}
	sort.Strings(kids)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		offset, _ = strconv.Atoi(strings.TrimPrefix(c, "off"))
	}
	var data []map[string]any
	for i := offset; i < len(kids) && i < offset+limit; i++ {
		data = append(data, s.sessionJSON(s.sessions[kids[i]]))
	}
	end := offset + len(data)
	s.mu.Unlock()
	next := any(nil)
	if len(data) > 0 {
		next = "off" + strconv.Itoa(end)
	}
	if data == nil {
		data = []map[string]any{}
	}
	writeJSON(w, 200, map[string]any{"data": data, "cursor": map[string]any{"previous": nil, "next": next}})
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	var types map[string]bool
	if q := r.URL.Query()["types"]; len(q) > 0 {
		types = map[string]bool{}
		for _, t := range q {
			if !knownTypes[t] {
				writeJSON(w, 400, map[string]any{"_tag": "InvalidRequestError", "message": "unknown type " + t})
				return
			}
			types[t] = true
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.WriteHeader(200)
	fl := w.(http.Flusher)
	x := &sub{ch: make(chan event, 256), types: types, done: make(chan struct{})}
	s.mu.Lock()
	s.connects++
	s.eventQs = append(s.eventQs, r.URL.RawQuery)
	s.subs[x] = true
	heartbeat := s.heartbeat
	s.mu.Unlock()
	fmt.Fprint(w, "data: {\"id\":\"evt_0\",\"type\":\"server.connected\",\"data\":{}}\n\n: heartbeat\n\n")
	fl.Flush()
	var beat <-chan time.Time
	if heartbeat > 0 {
		tk := time.NewTicker(heartbeat)
		defer tk.Stop()
		beat = tk.C
	}
	for {
		select {
		case <-r.Context().Done():
			s.mu.Lock()
			delete(s.subs, x)
			s.mu.Unlock()
			return
		case <-x.done:
			return
		case <-beat:
			if s.isStalled(x) {
				continue
			}
			fmt.Fprint(w, ": heartbeat\n\n")
			fl.Flush()
		case e := <-x.ch:
			fmt.Fprintf(w, "data: %s\n\n", e.body)
			fl.Flush()
		}
	}
}

func (s *Server) isStalled(x *sub) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return x.stalled
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
