package contract

import (
	"errors"
	"fmt"
)

// incomplete wraps an error for "could not read the whole family". Only a missing tab itself is ErrSessionNotFound;
// a descendant disappearing mid-walk is merely temporarily incomplete data (the next reread will be consistent) and must not be treated as the tab being missing.
func incomplete(err error, format string, args ...any) error {
	if errors.Is(err, ErrSessionNotFound) {
		return fmt.Errorf(format+"：%v", append(args, err)...)
	}
	return fmt.Errorf(format+"：%w", append(args, err)...)
}

// ErrSessionNotFound corresponds to HTTP 404 `{"_tag":"SessionNotFoundError"}`: OpenCode explicitly reports that the session does not exist.
// It must be distinguished from transient errors such as timeouts, connection failures and 5xx (the former = keep the tab and mark a data problem).
var ErrSessionNotFound = errors.New("session not found")

// API is the set of read-only endpoints needed to discover a family (all GET):
//
//	Session     GET /api/session/{id}
//	Children    GET /api/session?parentID={id}&limit={n}[&cursor={c}]   → (data, cursor.next)
//	Permissions GET /api/session/{id}/permission
//	Forms       GET /api/session/{id}/form
//	Active      GET /api/session/active
type API interface {
	Session(id string) (Session, error)
	Children(parentID, cursor string, limit int) (data []Session, next string, err error)
	Permissions(sessionID string) ([]PermissionRequest, error)
	Forms(sessionID string) ([]Form, error)
	Active() (map[string]bool, error)
}

// DiscoverFamily gets the complete baseline data for a tab: the tab itself, descendants at all levels, each member's pending approvals/answers,
// and the sessions currently running. Any failing step returns an error (partial data must not be treated as complete).
//
// Pagination ends on an empty page: cursor.next was observed to still exist on the last non-empty page, so it cannot be used to detect the end.
func DiscoverFamily(api API, tab string, pageSize int) (Snapshot, error) {
	snap := Snapshot{Running: map[string]bool{}}
	root, err := api.Session(tab)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read tab session: %w", err)
	}
	queue := []Session{root}
	seen := map[string]bool{root.ID: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		snap.Sessions = append(snap.Sessions, cur)

		cursor := ""
		for {
			page, next, err := api.Children(cur.ID, cursor, pageSize)
			if err != nil {
				return Snapshot{}, incomplete(err, "list child sessions of %s", cur.ID)
			}
			for _, c := range page {
				if !seen[c.ID] {
					seen[c.ID] = true
					queue = append(queue, c)
				}
			}
			if len(page) == 0 || next == "" {
				break
			}
			cursor = next
		}

		perms, err := api.Permissions(cur.ID)
		if err != nil {
			return Snapshot{}, incomplete(err, "read permission requests for %s", cur.ID)
		}
		snap.Permissions = append(snap.Permissions, perms...)
		forms, err := api.Forms(cur.ID)
		if err != nil {
			return Snapshot{}, incomplete(err, "read questions for %s", cur.ID)
		}
		snap.Forms = append(snap.Forms, forms...)
	}
	running, err := api.Active()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read running sessions: %w", err)
	}
	// Keep only family members: active is the whole service's list, and other sessions are unrelated to this tab.
	for _, x := range snap.Sessions {
		if running[x.ID] {
			snap.Running[x.ID] = true
		}
	}
	return snap, nil
}
