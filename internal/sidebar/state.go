package sidebar

import (
	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// health is the data health of a tab. "Unknown status" and "missing session" stay distinct internally
// (they are retried and recovered differently), while the screen shows both as the gray data-problem !.
type health int

const (
	healthOK      health = iota
	healthUnknown        // unknown status: data not yet read or the read failed (transient); keep retrying
	healthMissing        // missing session: explicitly reported by OpenCode; not polled, and recovers only when it appears (creation event) or on a backstop re-read
)

// displayState is the single mapping point for presenting tab status: a data problem (unknown status or missing session) takes priority,
// otherwise it maps the derived contract.Status to a display-layer status.
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
