package contract

// Session 是 GET /api/session/{id} 與 GET /api/session（data[]）的欄位子集。
// time.idle／time.viewed 缺漏時為 0（實際時間戳記為 epoch 毫秒，恆大於 0）。
type Session struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID"`
	// Title 是 session 的即時標題；tabs.json 的 title 只是備援。
	Title string `json:"title"`
	// Outcome 只在 session 曾結束一次執行後出現：succeeded | failed | interrupted。
	Outcome string `json:"outcome"`
	Time    struct {
		Busy   int64 `json:"busy"`
		Idle   int64 `json:"idle"`
		Viewed int64 `json:"viewed"`
	} `json:"time"`
}

// PermissionRequest 是 GET /api/permission/request 與 /api/session/{id}/permission 的項目。
type PermissionRequest struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
}

// Form 是 GET /api/form 與 /api/session/{id}/form 的項目（OpenCode 稱「問題」為 form）。
type Form struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
}

// Status 是父 spec 的 tab 狀態（不含資料異常；資料異常由呼叫端依資料完整性另行判定）。
type Status int

const (
	Idle Status = iota
	FinishedUnread
	FailedUnread
	Running
	WaitingAnswer
	WaitingApproval
)

func (s Status) String() string {
	return [...]string{"Idle", "FinishedUnread", "FailedUnread", "Running", "WaitingAnswer", "WaitingApproval"}[s]
}

// Snapshot 是同一時刻對 API 讀到的資料。Running 來自 GET /api/session/active
// （`{"data":{"<id>":{"type":"running"}}}`，不在其中的 session 即非執行中）。
type Snapshot struct {
	Sessions    []Session
	Running     map[string]bool
	Permissions []PermissionRequest
	Forms       []Form
}

func (s Snapshot) family(root string) map[string]bool {
	members := map[string]bool{root: true}
	for changed := true; changed; {
		changed = false
		for _, x := range s.Sessions {
			if x.ParentID != "" && members[x.ParentID] && !members[x.ID] {
				members[x.ID] = true
				changed = true
			}
		}
	}
	return members
}

// TabStatus 以父 spec 的優先序推導 tab 狀態。家族 = tab 對應 session 與所有層級後代
// （依 parentID，不靠標題或位置）；未讀只看 tab 本身，沿用 OpenCode TUI 的定義：
// time.idle 存在，且 time.viewed 缺漏或早於 time.idle。
func (s Snapshot) TabStatus(tabSession string) Status {
	fam := s.family(tabSession)
	for _, p := range s.Permissions {
		if fam[p.SessionID] {
			return WaitingApproval
		}
	}
	for _, f := range s.Forms {
		if fam[f.SessionID] {
			return WaitingAnswer
		}
	}
	for id := range fam {
		if s.Running[id] {
			return Running
		}
	}
	for _, x := range s.Sessions {
		if x.ID != tabSession {
			continue
		}
		if x.Time.Idle != 0 && (x.Time.Viewed == 0 || x.Time.Idle > x.Time.Viewed) {
			if x.Outcome == "failed" {
				return FailedUnread
			}
			return FinishedUnread
		}
	}
	return Idle
}
