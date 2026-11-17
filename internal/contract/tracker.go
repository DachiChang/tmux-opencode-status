package contract

// Tracker 以「snapshot 為基準、事件為增量」維護 tab 狀態。
//
// 契約重點（皆來自 v2.3.0 實測／官方文件）：
//   - /api/event 是 volatile：斷線期間的事件不會重播，所以 server.connected（每次連線的第一個
//     事件）必須觸發完整重讀，期間視為未同步。
//   - 事件與 snapshot 交錯時，事件較新：同步期間已收到事件的 session，不被 snapshot 覆蓋。
type Tracker struct {
	sessions    map[string]*tracked
	permissions map[string]string // permission request id → sessionID
	forms       map[string]string // form id → sessionID
	synced      bool
	touched     map[string]bool // 本輪同步開始後收到事件的 session
	// 本輪同步開始後收到事件的請求（權限 "p:<id>"、問題 "f:<id>"）。待處理請求以請求為單位
	// 對齊：事件較新者保持事件結果，其餘一律以 snapshot 為準。不能用 session 粒度，否則
	// 同一 session 在同步期間收到任一事件，連線前就存在的其他請求會被遺漏。
	touchedReq map[string]bool
}

type tracked struct {
	parent  string
	running bool
	outcome string
	idle    int64
	viewed  int64
	deleted bool
}

func NewTracker() *Tracker {
	return &Tracker{sessions: map[string]*tracked{}, permissions: map[string]string{}, forms: map[string]string{}, touched: map[string]bool{}, touchedReq: map[string]bool{}}
}

func (t *Tracker) get(id string) *tracked {
	s := t.sessions[id]
	if s == nil {
		s = &tracked{}
		t.sessions[id] = s
	}
	return s
}

// Synced 表示目前狀態是否已由 snapshot 對齊；未同步時呼叫端應呈現「狀態未知」。
func (t *Tracker) Synced() bool { return t.synced }

// Invalidate 讓事件推導的狀態失效（連線中斷等；效果同收到 server.connected），
// 直到下一次 ApplySnapshot 前不得當成最新事實。
func (t *Tracker) Invalidate() { t.Apply(Event{Type: EventServerConnected}) }

// Missing 表示該 session 已收到 session.deleted（tab 應保留並標資料異常，而非閒置）。
func (t *Tracker) Missing(id string) bool { s := t.sessions[id]; return s != nil && s.deleted }

// Apply 套用一個事件。
func (t *Tracker) Apply(e Event) {
	if e.Type == EventServerConnected {
		t.synced = false
		t.touched = map[string]bool{}
		t.touchedReq = map[string]bool{}
		return
	}
	if e.SessionID != "" {
		t.touched[e.SessionID] = true
	}
	switch e.Type {
	case "session.created":
		s := t.get(e.SessionID)
		s.parent = e.ParentID
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
		t.touchedReq["p:"+e.RequestID] = true
		t.permissions[e.RequestID] = e.SessionID
	case "permission.replied":
		t.touchedReq["p:"+e.RequestID] = true
		delete(t.permissions, e.RequestID)
	case "form.created":
		t.touchedReq["f:"+e.FormID] = true
		t.forms[e.FormID] = e.SessionID
	case "form.replied", "form.cancelled":
		t.touchedReq["f:"+e.FormID] = true
		delete(t.forms, e.FormID)
	}
}

// ApplySnapshot 以完整讀取的資料對齊狀態；同步期間已有事件的 session 保持事件結果。
func (t *Tracker) ApplySnapshot(s Snapshot) {
	// 完整重讀沒有出現、同步期間也沒有事件的 session 已經不存在（例如斷線期間被刪除的
	// 後代）；不丟掉的話，它最後已知的執行中會變成永遠不會結束的幽靈。
	inSnap := make(map[string]bool, len(s.Sessions))
	for _, x := range s.Sessions {
		inSnap[x.ID] = true
	}
	for id := range t.sessions {
		if !inSnap[id] && !t.touched[id] {
			delete(t.sessions, id)
		}
	}
	for _, x := range s.Sessions {
		if t.touched[x.ID] {
			// 事件較新，不覆蓋；但父子關係與單調資料（已讀時間、較新的結束時間）仍要補上，
			// 否則建立事件早於連線的後代會脫離家族、其他 client 的已讀會被遺漏。
			cur := t.get(x.ID)
			if cur.parent == "" {
				cur.parent = x.ParentID
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
		cur.running = s.Running[x.ID]
	}
	for id := range s.Running {
		if !t.touched[id] {
			t.get(id).running = true
		}
	}
	for id := range t.permissions {
		if !t.touchedReq["p:"+id] {
			delete(t.permissions, id)
		}
	}
	for _, p := range s.Permissions {
		if !t.touchedReq["p:"+p.ID] {
			t.permissions[p.ID] = p.SessionID
		}
	}
	for id := range t.forms {
		if !t.touchedReq["f:"+id] {
			delete(t.forms, id)
		}
	}
	for _, f := range s.Forms {
		if !t.touchedReq["f:"+f.ID] {
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
		// 已刪除的 session 保留在家族關係中（讓它的後代不脫離），但不再執行也不能被處理。
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

// TabStatus 以目前追蹤的資料推導 tab 狀態。
func (t *Tracker) TabStatus(tab string) Status { return t.snapshot().TabStatus(tab) }
