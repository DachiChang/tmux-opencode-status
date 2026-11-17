// Package fakeoc 是受控的假 OpenCode 服務（httptest + SSE），只供測試使用。
// 它依 v2.3.0 契約（docs/contracts/opencode-2.3.0.md）回應唯讀端點與 /api/event，
// 並記錄所有收到的請求，讓測試能斷言「從未呼叫任何寫入 API」。
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

// 與契約文件相同的合法 types；未知 type 回 400（重現實測行為）。
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
	stalled bool // 無聲：連線仍開著，但不再送出任何位元組（事件遺失、心跳停止）
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
	writes   []string // 非 GET 的請求（必須永遠為空）
	connects int
	eventQs  []string // 每次 /api/event 連線的 query
	broken   []string // 請求 URI 含其中任一字串的 REST GET 回 500（模擬暫時性失敗）
	holds    []*hold  // 尚未攔截到請求的 Hold

	heartbeat time.Duration // 每隔多久送一次 ": heartbeat"；0 表示不送
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

// URL 是 service.json 會寫的 url：host 用 0.0.0.0（與實際服務相同），呼叫端必須改連 127.0.0.1。
func (s *Server) URL() string {
	return strings.Replace(s.srv.URL, "127.0.0.1", "0.0.0.0", 1)
}

// WriteServiceFile 以 0600 寫出 service.json。
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

// Writes 回傳收到的所有非 GET 請求；唯讀契約下必須為空。
func (s *Server) Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// Requests 回傳到目前為止所有請求（"METHOD path"）。
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// SessionRequests 是對 /api/session* 的請求數（用來驗證沒有追蹤項目時不查詢 session 資料）。
func (s *Server) SessionRequests() int {
	n := 0
	for _, r := range s.Requests() {
		if strings.Contains(r, " /api/session") {
			n++
		}
	}
	return n
}

// Connects 是 /api/event 被連線的次數。
func (s *Server) Connects() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects
}

// EventQueries 是每次 /api/event 連線帶的 query（用來驗證 types 過濾）。
func (s *Server) EventQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.eventQs...)
}

// Subscribers 是目前連著的 SSE 數量。
func (s *Server) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Break 讓 URI（含 query）含 substr 的 REST GET 回 500，直到 Heal；不影響 /api/info 與 /api/event。
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

// Hold 攔截「下一個」URI 含 substr 的 REST GET：服務照常算出回應（內容固定在攔截當下），
// 但延遲到 release 被呼叫才送出，模擬延遲抵達的舊回應。arrived 在請求被攔住時關閉。
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

// Heal 解除所有 Break。
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

// SetHeartbeat 設定之後新建立的 SSE 連線每隔多久送一次 ": heartbeat"；0 表示不送。
func (s *Server) SetHeartbeat(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeat = d
}

// Stall 讓目前所有 SSE 連線無聲：連線維持開啟（沒有 FIN／RST），但不再送出任何位元組，
// 之後發生的事件也不補送。模擬半開連線；新建立的連線不受影響。
func (s *Server) Stall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for x := range s.subs {
		x.stalled = true
	}
}

// Drop 斷開所有 SSE 連線（模擬連線中斷；斷線期間發生的事件不會補送）。
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

// emit 在持鎖狀態下呼叫。
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

// ---- 狀態變化（同時送出對應事件；沒有 SSE 連線時只改變狀態）----

func (s *Server) AddSession(id, parent string) { s.addSession(id, parent, true) }

// AddSessionWithoutParentInEvent 建立後代，但 session.created 事件缺少 parentID
// （REST 資料仍有父子關係）：事件單獨無法把它歸入家族。
func (s *Server) AddSessionWithoutParentInEvent(id, parent string) { s.addSession(id, parent, false) }

func (s *Server) addSession(id, parent string, parentInEvent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.get(id)
	x.parent = parent
	d := map[string]any{"sessionID": id, "title": "t"}
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

// Finish 結束一次執行：outcome 為 succeeded | failed | interrupted。
func (s *Server) Finish(id, outcome string) { s.finish(id, outcome, "user") }

// FinishInterrupted 以指定的 reason（user | shutdown | superseded | inactivity）中斷一次執行，
// 同時改變服務資料（不再執行、有 idle 與 outcome）。
func (s *Server) FinishInterrupted(id, reason string) { s.finish(id, "interrupted", reason) }

// EmitInterrupted 只送出 session.execution.interrupted 事件，不改變服務資料：
// 用來驗證側欄不把事件本身當成事實，而是以重讀的資料為準。
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

// View 模擬其他 client 把該 session 標為已讀。
func (s *Server) View(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.get(id)
	x.view = x.idle
	s.emit("session.viewed", s.now(), map[string]any{"sessionID": id, "idle": x.idle})
}

// Seed 在不送事件的情況下直接設定 session 狀態（模擬插件啟動前已存在的資料）。
func (s *Server) Seed(id, parent, outcome string, idle, viewed int64, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.get(id)
	x.parent, x.outcome, x.idle, x.view, x.running = parent, outcome, idle, viewed, running
}

// ---- 待核准的權限請求與待回答的問題（OpenCode 稱 form）----
// Ask*／Resolve*／Cancel* 模擬「使用者在 OpenCode 內處理」：改變服務資料並送出事件；
// 側欄本身永遠不會呼叫這些寫入端點。Seed* 只放資料不送事件（插件啟動前就已存在）。

func (s *Server) AskPermission(id, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perms = append(s.perms, item{id, sessionID})
	s.emit("permission.asked", s.now(), map[string]any{"id": id, "sessionID": sessionID, "action": "bash", "resources": []string{"x"}, "save": []string{}})
}

// ReplyPermission 模擬使用者在 OpenCode 回覆（核准或拒絕）該請求。
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

// AnswerForm 模擬使用者回答問題。
func (s *Server) AnswerForm(id string) { s.closeForm("form.replied", id) }

// CancelForm 模擬問題被取消。
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

// SeedPermission／SeedForm 不送事件地放入待處理項目。
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

// ForgetPending 模擬服務重啟：待處理請求不持久，重啟後全部消失（不送事件）。
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
	m := map[string]any{"id": x.id, "time": t, "title": "t"}
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

// listChildren 重現實測的分頁行為：只要本頁有資料，cursor.next 就存在（空頁才結束）。
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
