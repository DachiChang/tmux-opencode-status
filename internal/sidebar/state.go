package sidebar

import (
	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// health 是 tab 的資料健康度。「狀態未知」與「session 不存在」在內部保持區別
// （重試與恢復的方式不同），畫面共用資料異常的灰色 !。
type health int

const (
	healthOK      health = iota
	healthUnknown        // 狀態未知：資料尚未讀到或讀取失敗（暫時性），持續重試
	healthMissing        // session 不存在：OpenCode 明確回報，不輪詢，出現（建立事件）或補漏重讀才恢復
)

// displayState 是 tab 狀態呈現的唯一對應點：資料異常（狀態未知或 session 不存在）優先，
// 否則把推導出的 contract.Status 對到顯示層狀態。
func displayState(h health, s contract.Status) render.State {
	if h != healthOK {
		return render.DataProblem
	}
	switch s {
	case contract.WaitingApproval, contract.WaitingAnswer:
		return render.Attention
	case contract.Running:
		return render.Running
	case contract.FailedUnread:
		return render.FailedUnread
	case contract.FinishedUnread:
		return render.FinishedUnread
	}
	return render.Idle
}
