package contract

// Tracker maintains tab status using "snapshot as baseline, events as increments".
//
// Key points of the contract (all from v2.3.0 observation / official docs):
//   - /api/event is volatile: events during a disconnect are not replayed, so server.connected (the first
//     event of every connection) must trigger a full reread, and the state counts as unsynced meanwhile.
//   - When events and snapshots interleave, events are newer: a session that received events after the read began is not overwritten by the snapshot.
//     The event sequence number (Mark) decides "after the read began", so reads for several tabs can proceed independently.
type Tracker struct {
	sessions    map[string]*tracked
	permissions map[string]string // permission request id → sessionID
	forms       map[string]string // form id → sessionID
	synced      bool
	seq         uint64            // number of events applied (the source of Mark)
	touched     map[string]uint64 // session → sequence number of the last event received for it (cleared on server.connected)
	// Request (permission "p:<id>", question "f:<id>") → sequence number of the last event received for it. Pending requests are reconciled
	// per request: whichever side has newer events keeps the event result, and everything else follows the snapshot. Session granularity cannot be used, otherwise
	// when a session receives any event, other requests that existed before the connection would be missed.
	touchedReq map[string]uint64
}

type tracked struct {
	parent  string
	title   string
	running bool
	outcome string
	idle    int64
	viewed  int64
	deleted bool
}

func NewTracker() *Tracker {
	return &Tracker{sessions: map[string]*tracked{}, permissions: map[string]string{}, forms: map[string]string{}, touched: map[string]uint64{}, touchedReq: map[string]uint64{}}
}

func (t *Tracker) get(id string) *tracked {
	s := t.sessions[id]
	if s == nil {
		s = &tracked{}
		t.sessions[id] = s
	}
	return s
}

// Synced reports whether the current state has been reconciled by a snapshot; while unsynced, callers should present "unknown status".
func (t *Tracker) Synced() bool { return t.synced }

// Invalidate invalidates the event-derived state (connection loss, etc.; same effect as receiving server.connected),
// and it must not be treated as the latest fact until the next ApplySnapshot.
func (t *Tracker) Invalidate() { t.Apply(Event{Type: EventServerConnected}) }

// Title returns the session's currently known API title; when it is not yet available (or the service does not provide one), ok is false,
// and callers fall back to the title in tabs.json.
func (t *Tracker) Title(id string) (title string, ok bool) {
	s := t.sessions[id]
	if s == nil || s.title == "" {
		return "", false
	}
	return s.title, true
}

// Mark returns the current event sequence number. Take it before starting a REST read and pass it to ApplyFamily once the read completes:
// events received after that sequence number are newer than the read and are not overwritten.
func (t *Tracker) Mark() uint64 { return t.seq }

// Missing reports that the session has received session.deleted (the tab should be kept and marked as a data problem, not idle).
func (t *Tracker) Missing(id string) bool { s := t.sessions[id]; return s != nil && s.deleted }

// Apply applies one event.
func (t *Tracker) Apply(e Event) {
	if e.Type == EventServerConnected {
		t.synced = false
		t.touched = map[string]uint64{}
		t.touchedReq = map[string]uint64{}
		return
	}
	// An interruption caused by service shutdown is not a user interruption nor a finished run (the TUI ignores it too): the event does not change state,
	// and the actual result is decided by the later reread.
	if e.Type == "session.execution.interrupted" && e.Reason == "shutdown" {
		return
	}
	t.seq++
	if e.SessionID != "" {
		t.touched[e.SessionID] = t.seq
	}
	switch e.Type {
	case "session.created":
		s := t.get(e.SessionID)
		s.parent = e.ParentID
		if e.Title != "" {
			s.title = e.Title
		}
	case "session.renamed":
		if e.Title != "" {
			t.get(e.SessionID).title = e.Title
		}
	case "session.deleted":
		t.get(e.SessionID).deleted = true
	case "session.execution.started":
		t.get(e.SessionID).running = true
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		s := t.get(e.SessionID)
		s.running = false
		s.outcome = map[string]string{
			"session.execution.succeeded":   "succeeded",
			"session.execution.failed":      "failed",
			"session.execution.interrupted": "interrupted",
		}[e.Type]
		s.idle = e.Created
	case "session.viewed":
		t.get(e.SessionID).viewed = e.Idle
	case "permission.asked":
		t.touchedReq["p:"+e.RequestID] = t.seq
		t.permissions[e.RequestID] = e.SessionID
	case "permission.replied":
		t.touchedReq["p:"+e.RequestID] = t.seq
		delete(t.permissions, e.RequestID)
	case "form.created":
		t.touchedReq["f:"+e.FormID] = t.seq
		t.forms[e.FormID] = e.SessionID
	case "form.replied", "form.cancelled":
		t.touchedReq["f:"+e.FormID] = t.seq
		delete(t.forms, e.FormID)
	}
}

// ApplySnapshot reconciles the whole state with fully read data; sessions and requests that already have events after the connection (after server.connected)
// keep the event result.
func (t *Tracker) ApplySnapshot(s Snapshot) { t.applySnapshot(s, 0, nil) }

// ApplyFamily applies the family snapshot of a single tab, reconciling only that family (including previously known descendants): the state of other tabs
// is unaffected. since is the Mark taken before the read began; sessions and requests that received events afterwards keep the event result.
func (t *Tracker) ApplyFamily(tab string, s Snapshot, since uint64) {
	scope := map[string]bool{tab: true}
	for id := range s.family(tab) {
		scope[id] = true
	}
	for changed := true; changed; { // previously known descendants (may have disappeared on the server)
		changed = false
		for id, x := range t.sessions {
			if x.parent != "" && scope[x.parent] && !scope[id] {
				scope[id] = true
				changed = true
			}
		}
	}
	t.applySnapshot(s, since, scope)
}

// applySnapshot: a nil scope means everything; otherwise only sessions in scope and their requests are processed.
func (t *Tracker) applySnapshot(s Snapshot, since uint64, scope map[string]bool) {
	inScope := func(id string) bool { return scope == nil || scope[id] }
	fresh := func(id string) bool { return t.touched[id] > since }
	freshReq := func(key string) bool { return t.touchedReq[key] > since }
	// A session absent from the full reread and without events during the read no longer exists (e.g. a descendant deleted during a disconnect);
	// if it is not dropped, its last known running state becomes a ghost that never ends.
	inSnap := make(map[string]bool, len(s.Sessions))
	for _, x := range s.Sessions {
		inSnap[x.ID] = true
	}
	for id := range t.sessions {
		if inScope(id) && !inSnap[id] && !fresh(id) {
			delete(t.sessions, id)
		}
	}
	for _, x := range s.Sessions {
		if fresh(x.ID) {
			// The event is newer and is not overwritten; but the parent-child relationship and monotonic data (viewed time, newer finish time) must still be filled in,
			// otherwise a descendant whose creation event predates the connection would fall out of the family, and reads by other clients would be missed.
			cur := t.get(x.ID)
			if cur.parent == "" {
				cur.parent = x.ParentID
			}
			if cur.title == "" {
				cur.title = x.Title
			}
			if x.Time.Viewed > cur.viewed {
				cur.viewed = x.Time.Viewed
			}
			if x.Time.Idle > cur.idle {
				cur.idle, cur.outcome = x.Time.Idle, x.Outcome
			}
			continue
		}
		cur := t.get(x.ID)
		cur.parent, cur.outcome, cur.idle, cur.viewed = x.ParentID, x.Outcome, x.Time.Idle, x.Time.Viewed
		if x.Title != "" {
			cur.title = x.Title
		}
		cur.running = s.Running[x.ID]
		cur.deleted = false
	}
	for id := range s.Running {
		if !fresh(id) {
			t.get(id).running = true
		}
	}
	for id, sess := range t.permissions {
		if inScope(sess) && !freshReq("p:"+id) {
			delete(t.permissions, id)
		}
	}
	for _, p := range s.Permissions {
		if !freshReq("p:" + p.ID) {
			t.permissions[p.ID] = p.SessionID
		}
	}
	for id, sess := range t.forms {
		if inScope(sess) && !freshReq("f:"+id) {
			delete(t.forms, id)
		}
	}
	for _, f := range s.Forms {
		if !freshReq("f:" + f.ID) {
			t.forms[f.ID] = f.SessionID
		}
	}
	t.synced = true
}

func (t *Tracker) snapshot() Snapshot {
	s := Snapshot{Running: map[string]bool{}}
	for id, x := range t.sessions {
		sess := Session{ID: id, ParentID: x.parent, Outcome: x.outcome}
		sess.Time.Idle, sess.Time.Viewed = x.idle, x.viewed
		s.Sessions = append(s.Sessions, sess)
		// A deleted session stays in the family relationship (so its descendants do not detach), but is no longer running and cannot be handled.
		if x.running && !x.deleted {
			s.Running[id] = true
		}
	}
	for id, sess := range t.permissions {
		if !t.Missing(sess) {
			s.Permissions = append(s.Permissions, PermissionRequest{ID: id, SessionID: sess})
		}
	}
	for id, sess := range t.forms {
		if !t.Missing(sess) {
			s.Forms = append(s.Forms, Form{ID: id, SessionID: sess})
		}
	}
	return s
}

// TabStatus derives the tab status from the currently tracked data.
func (t *Tracker) TabStatus(tab string) Status { return t.snapshot().TabStatus(tab) }
