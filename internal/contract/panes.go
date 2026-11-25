// Package contract follows the observed contract of OpenCode v2.3.0 / tmux next-3.9 and
// provides decision functions that later implementation can reuse directly; it is also the entry point for contract regression tests.
package contract

import "path/filepath"

// Pane holds the identifying facts of a tmux pane. ForegroundArgv/ForegroundCwd come from the foreground process group
// of the pane's tty (pgid == tpgid), not from pane_pid (which is usually the shell).
type Pane struct {
	Name               string   `json:"name"`
	PaneCurrentCommand string   `json:"paneCurrentCommand"`
	AlternateOn        bool     `json:"alternateOn"`
	PaneCurrentPath    string   `json:"paneCurrentPath"`
	ForegroundArgv     []string `json:"foregroundArgv"`
	ForegroundCwd      string   `json:"foregroundCwd"`
}

// opencodeSubcommands are the subcommands listed by `opencode --help`; one appearing as the first
// non-flag positional value means a non-interactive TUI (mini is interactive, but does not use the TUI's tab list).
var opencodeSubcommands = map[string]bool{
	"upgrade": true, "update": true, "uninstall": true, "acp": true, "api": true,
	"debug": true, "auth": true, "mcp": true, "plugin": true, "models": true,
	"stats": true, "mini": true, "run": true, "session": true, "service": true,
	"reload": true, "pair": true, "serve": true,
}

// Flags that consume the next argv as their value (the FLAGS of `opencode --help`).
var valueFlags = map[string]bool{
	"--server": true, "--session": true, "-s": true, "--prompt": true,
	"--log-level": true, "--completions": true,
}

// IsTUICandidate decides whether a pane's foreground process is a candidate for the interactive OpenCode TUI:
// the executable basename is opencode, and argv neither points to a subcommand nor contains --help/--version.
// It does not use the pane title, scan the screen, or require the alternate screen
// (the pager of `opencode session list` also enters the alternate screen).
func IsTUICandidate(p Pane) bool {
	if len(p.ForegroundArgv) == 0 || filepath.Base(p.ForegroundArgv[0]) != "opencode" {
		return false
	}
	args := p.ForegroundArgv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help" || a == "-h" || a == "--version" || a == "-v" || a == "--wizard":
			return false
		case valueFlags[a]:
			i++
		case len(a) > 0 && a[0] == '-':
			// The remaining flags (--standalone, --auto, --continue, --print-logs, ...) take no value.
		default:
			return !opencodeSubcommands[a]
		}
	}
	return true
}
