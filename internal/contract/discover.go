package contract

import (
	"errors"
	"fmt"
)

// ErrSessionNotFound 對應 HTTP 404 `{"_tag":"SessionNotFoundError"}`：OpenCode 明確回報 session 不存在。
// 與逾時、連線失敗、5xx 等暫時性錯誤必須區分（前者 = tab 保留並標資料異常）。
var ErrSessionNotFound = errors.New("session 不存在")

// API 是發現家族所需的唯讀端點（皆為 GET）：
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

// DiscoverFamily 取得 tab 的完整基準資料：tab 本身、所有層級後代、各成員的待核准／待回答，
// 以及目前執行中的 session。任一步驟失敗都回傳錯誤（不得把部分資料當成完整資料）。
//
// 分頁結束條件是「空頁」：實測 cursor.next 在最後一個非空頁仍然存在，不能用它判斷結束。
func DiscoverFamily(api API, tab string, pageSize int) (Snapshot, error) {
	snap := Snapshot{Running: map[string]bool{}}
	root, err := api.Session(tab)
	if err != nil {
		return Snapshot{}, fmt.Errorf("讀取 tab session：%w", err)
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
				return Snapshot{}, fmt.Errorf("列出 %s 的子層：%w", cur.ID, err)
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
			return Snapshot{}, fmt.Errorf("讀取 %s 的權限請求：%w", cur.ID, err)
		}
		snap.Permissions = append(snap.Permissions, perms...)
		forms, err := api.Forms(cur.ID)
		if err != nil {
			return Snapshot{}, fmt.Errorf("讀取 %s 的問題：%w", cur.ID, err)
		}
		snap.Forms = append(snap.Forms, forms...)
	}
	running, err := api.Active()
	if err != nil {
		return Snapshot{}, fmt.Errorf("讀取執行中 session：%w", err)
	}
	snap.Running = running
	return snap, nil
}
