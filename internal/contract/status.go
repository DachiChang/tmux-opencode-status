package contract

// Session is the subset of fields of GET /api/session/{id} and GET /api/session (data[]).
// A missing time.idle/time.viewed is 0 (real timestamps are epoch milliseconds and always greater than 0).
type Session struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID"`
	// Title is the session's live title; the title in tabs.json is only a fallback.
	Title string `json:"title"`
	// Outcome appears only after the session has finished a run: succeeded | failed | interrupted.
	Outcome string `json:"outcome"`
	Time    struct {
		Busy   int64 `json:"busy"`
		Idle   int64 `json:"idle"`
		Viewed int64 `json:"viewed"`
	} `json:"time"`
}

// PermissionRequest is an item of GET /api/permission/request and /api/session/{id}/permission.
type PermissionRequest struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
}

// Form is an item of GET /api/form and /api/session/{id}/form (OpenCode calls a question a form).
type Form struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
}

// Status is the parent spec's tab status (excluding data problems; callers determine data problems separately from data completeness).
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

// Snapshot is the data read from the API at a single point in time. Running comes from GET /api/session/active
// (`{"data":{"<id>":{"type":"running"}}}`; a session not listed there is not running).
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

// TabStatus derives the tab status using the parent spec's priority order. Family = the tab's session and all descendants at any depth
// (by parentID, not by title or position); unread looks only at the tab itself, following the OpenCode TUI definition:
// time.idle is present, and time.viewed is missing or earlier than time.idle.
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
