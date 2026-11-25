// Package contract follows the observed contract of OpenCode v2.3.0 / tmux next-3.9 and
// provides decision functions that later implementation can reuse directly; it is also the entry point for contract regression tests.
package contract

// Pane holds the identifying facts of a tmux pane, all read from a single `tmux list-panes`.
// tmux derives pane_current_command and pane_current_path from the foreground process group leader of the pane's tty,
// not from pane_pid (which is usually the shell).
type Pane struct {
	Name               string `json:"name"`
	PaneCurrentCommand string `json:"paneCurrentCommand"`
	AlternateOn        bool   `json:"alternateOn"`
	// PaneCurrentPath is the directory identity for a TUI client: the kernel-resolved cwd OpenCode uses for its cwd tab list.
	PaneCurrentPath string `json:"paneCurrentPath"`
}

// IsTUIClient decides whether a pane hosts an OpenCode TUI client: its foreground command is opencode and it is on the
// alternate screen. Non-interactive commands (run, serve, acp), mini, and the auth pickers stay on the main screen, and help,
// completions, and invalid arguments exit before entering it, so no argv parsing is needed. The pager of `opencode session list`
// also enters the alternate screen and is accepted as a false positive (see ADR-0006).
func IsTUIClient(p Pane) bool {
	return p.PaneCurrentCommand == "opencode" && p.AlternateOn
}
