package contract

// Tracker 以「snapshot 為基準、事件為增量」維護 tab 狀態。
//
// 契約重點（皆來自 v2.3.0 實測／官方文件）：
//   - /api/event 是 volatile：斷線期間的事件不會重播，所以 server.connected（每次連線的第一個
//     事件）必須觸發完整重讀，期間視為未同步。
//   - 事件與 snapshot 交錯時，事件較新：讀取開始之後收到事件的 session，不被 snapshot 覆蓋。
//     以事件序號（Mark）判定「讀取開始之後」，讓多個 tab 的讀取可以各自獨立進行。
type Tracker struct {
	sessions    map[string]*tracked
	permissions map[string]string // permission request id → sessionID
	forms       map[string]string // form id → sessionID
	synced      bool
	seq         uint64            // 已套用的事件數（Mark 的來源）
	touched     map[string]uint64 // session → 最後一次收到其事件的序號（server.connected 時清空）
	// 請求（權限 "p:<id>"、問題 "f:<id>"）→ 最後一次收到其事件的序號。待處理請求以請求為單位
	// 對齊：事件較新者保持事件結果，其餘一律以 snapshot 為準。不能用 session 粒度，否則
	// 同一 session 收到任一事件，連線前就存在的其他請求會被遺漏。
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

// Synced 表示目前狀態是否已由 snapshot 對齊；未同步時呼叫端應呈現「狀態未知」。
func (t *Tracker) Synced() bool { return t.synced }

// Invalidate 讓事件推導的狀態失效（連線中斷等；效果同收到 server.connected），
// 直到下一次 ApplySnapshot 前不得當成最新事實。
func (t *Tracker) Invalidate() { t.Apply(Event{Type: EventServerConnected}) }

// Title 回傳 session 目前已知的 API 標題；尚未取得（或服務沒有提供）時 ok 為 false，
// 呼叫端退回 tabs.json 的備援標題。
func (t *Tracker) Title(id string) (title string, ok bool) {
	s := t.sessions[id]
	if s == nil || s.title == "" {
		return "", false
	}
	return s.title, true
}

// Mark 回傳目前的事件序號。在開始讀取 REST 之前取得，讀取完成後傳給 ApplyFamily：
// 序號之後收到的事件比該次讀取新，不會被覆蓋。
func (t *Tracker) Mark() uint64 { return t.seq }

// Missing 表示該 session 已收到 session.deleted（tab 應保留並標資料異常，而非閒置）。
func (t *Tracker) Missing(id string) bool { s := t.sessions[id]; return s != nil && s.deleted }

// Apply 套用一個事件。
func (t *Tracker) Apply(e Event) {
	if e.Type == EventServerConnected {
		t.synced = false
		t.touched = map[string]uint64{}
		t.touchedReq = map[string]uint64{}
		return
	}
	// 服務關閉造成的中斷不是使用者中斷，也不是一次結束（TUI 同樣忽略）：事件不改變狀態，
	// 實際結果以之後的重讀為準。
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

// ApplySnapshot 以完整讀取的資料對齊整個狀態；連線後（server.connected 之後）已有事件的
// session 與請求保持事件結果。
func (t *Tracker) ApplySnapshot(s Snapshot) { t.applySnapshot(s, 0, nil) }

// ApplyFamily 套用單一 tab 的家族 snapshot，只對齊該家族（含先前已知的後代）：其他 tab 的
// 狀態不受影響。since 是開始讀取前取得的 Mark，之後收到事件的 session 與請求保持事件結果。
func (t *Tracker) ApplyFamily(tab string, s Snapshot, since uint64) {
	scope := map[string]bool{tab: true}
	for id := range s.family(tab) {
		scope[id] = true
	}
	for changed := true; changed; { // 先前已知的後代（可能已在伺服器端消失）
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

// applySnapshot：scope 為 nil 表示整體；否則只處理 scope 內的 session 與其請求。
func (t *Tracker) applySnapshot(s Snapshot, since uint64, scope map[string]bool) {
	inScope := func(id string) bool { return scope == nil || scope[id] }
	fresh := func(id string) bool { return t.touched[id] > since }
	freshReq := func(key string) bool { return t.touchedReq[key] > since }
	// 完整重讀沒有出現、讀取期間也沒有事件的 session 已經不存在（例如斷線期間被刪除的
	// 後代）；不丟掉的話，它最後已知的執行中會變成永遠不會結束的幽靈。
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
			// 事件較新，不覆蓋；但父子關係與單調資料（已讀時間、較新的結束時間）仍要補上，
			// 否則建立事件早於連線的後代會脫離家族、其他 client 的已讀會被遺漏。
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
