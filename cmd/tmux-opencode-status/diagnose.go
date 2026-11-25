package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/instance"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
	"github.com/DachiChang/tmux-opencode-status/internal/tmuxsrc"
)

// diagnose is strictly read-only diagnostics: it queries only tmux (show-options / list-sessions / display-message) and local state files,
// does not connect to OpenCode, write any tmux option, or start or stop any process.
// Output contains only counts, modes, fixed descriptions, and tmux session ids; it contains no credentials, prompts, conversations,
// OpenCode session / tab titles, or directory paths, and no raw error text or tmux session names.
func diagnose(o *options, w io.Writer) int {
	tm, err := o.client(true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }
	info, err := tm.Server()
	if err != nil {
		p("tmux server: Cannot connect (server not found or temporarily unresponsive)")
		return 1
	}
	p("tmux server: PID %d, version %s", info.PID, info.Version)

	problems := 0
	missing, merr := tm.MissingCapabilities()
	switch {
	case merr != nil:
		p("Required capabilities: Cannot verify (tmux temporarily unresponsive)")
	case len(missing) > 0:
		problems++
		p("Required capabilities: Missing %s. Native side-status from tmux 3.9 is required; there is no fallback.", strings.Join(missing, ", "))
	default:
		p("Required capabilities: side-status, side-status-width, and side-status-format are available")
	}

	if v, err := tm.GlobalValue(sidebar.ModeOption); err != nil {
		p("Configured plugin mode: Cannot read")
	} else if v == "" {
		p("Configured plugin mode: Not set (default: cwd)")
	} else if v == "cwd" || v == "global" {
		p("Configured plugin mode: %s", v)
	} else {
		problems++
		p("Configured plugin mode: Invalid value (expected cwd or global). Tracking requires a valid mode; any previous valid mode stays active.")
	}
	if raw, err := tm.Swatches(); err != nil {
		p("Theme settings: Cannot read")
	} else if _, invalid := render.ParseTheme(raw); len(invalid) > 0 {
		for _, name := range invalid {
			problems++
			p("Theme settings: Invalid value for %s%s; using its default (other colors are unaffected; no sidebar error)", sidebar.ThemeOptionPrefix, name)
		}
	}
	if v, err := tm.GlobalValue("side-status"); err == nil {
		p("Global side-status: %s%s", valueOr(v, "(not set)"), map[bool]string{true: " (sidebar hidden; tracking continues and no restart is needed to show it again)", false: ""}[v == "off" || v == ""])
	}
	problems += diagnoseDetection(tm, p)

	paths, err := instance.PathsFor(info.SocketPath)
	if err != nil {
		p("Daemon: Cannot determine status (%v)", err)
		return 1
	}
	pid, held := instance.Holder(paths.Lock)
	var st instance.State
	haveState := false
	if held {
		if s, err := instance.ReadState(paths.State); err == nil && s.PID == pid {
			st, haveState = s, true
		}
	}
	switch {
	case !held:
		problems++
		p("Daemon: Not running. Nothing is displayed; reload the tmux configuration to invoke start, or run start manually.")
	case !haveState:
		p("Daemon: Running (PID %d), status not yet available", pid)
	default:
		p("Daemon: Running (PID %d), uptime %s", pid, since(st.Started))
		if st.Tracking {
			p("Active mode: %s", st.Mode)
		} else {
			problems++
			p("Active mode: Tracking not started (%s)", diagnosticLine(valueOr(st.ModeIssue, "invalid plugin mode")))
		}
		p("TUI clients found: %d; tabs tracked: %d", st.TUIs, st.Tabs)
		p("Service connection: %s", serviceText(st))
		if st.Service == sidebar.ServiceDown {
			problems++
		}
		for _, i := range st.Issues {
			problems++
			p("Current issue: %s", diagnosticLine(i))
		}
		if len(st.Recent) == 0 {
			p("Recent errors: None")
		} else {
			p("Recent errors:")
			for _, r := range st.Recent {
				p("  %s  [%s] %s", r.At.Local().Format("15:04:05"), r.Source, diagnosticLine(r.Message))
			}
		}
	}

	problems += diagnoseJump(tm, held, haveState, st.JumpTrigger, p)

	ov, oerr := tm.SessionOverrides()
	switch {
	case oerr != nil:
		p("Session overrides: Cannot read")
	case len(ov) == 0:
		p("Session overrides: None")
	default:
		p("Session overrides: %d tmux session(s) override global side-status options:", len(ov))
		for _, x := range ov {
			p("  session %s: %s", x.TmuxSession, strings.Join(x.Options, ", "))
		}
		p("  A session with side-status=off hides the sidebar. Overriding side-status-width or side-status-format can prevent the plugin from displaying as configured.")
		p("  The plugin does not change these overrides. To inherit the global setting, run `set -u -t <session> <option>`.")
	}
	if problems == 0 {
		p("Result: No issues found")
	} else {
		p("Result: Issues found: %d (see above)", problems)
	}
	return 0
}

// diagnoseDetection checks the global tmux options TUI client detection depends on (ADR-0006) and returns the number of problems.
// alternate-screen off hides every TUI client, so it counts as a problem; allow-set-title off only delays detection to the
// low-frequency catch-up, so it is reported without counting. Per-window and per-pane overrides are not checked.
func diagnoseDetection(tm *tmuxsrc.Client, p func(string, ...any)) int {
	problems := 0
	if v, err := tm.GlobalValue("alternate-screen"); err != nil {
		p("TUI detection: Cannot read alternate-screen")
	} else if v == "off" {
		problems++
		p("TUI detection: alternate-screen is off. TUI clients are identified by the alternate screen, so none can be detected; run `set -g alternate-screen on`.")
	}
	if v, err := tm.GlobalValue("allow-set-title"); err != nil {
		p("TUI detection: Cannot read allow-set-title")
	} else if v == "off" {
		p("TUI detection: allow-set-title is off. A newly started TUI client appears, and one exited in an existing shell disappears, only on the periodic check (every %s by default).", sidebar.DefaultBackstop)
	}
	return problems
}

// diagnosticLine keeps each status record on a single line.
func diagnosticLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// diagnoseJump prints the "Keyboard jump" line and returns the number of problems to count (an invalid setting, a failed binding, and an overridden binding each count as one).
// Read-only: it reads only the setting option, @tos_jump_trigger, and list-keys. It does not detect what the trigger key was bound to before binding.
// reported and startSetting are the settings the daemon read at startup (unknown when haveState is false): if the current setting differs from them,
// the user changed the setting and has not reloaded yet; that is "setting not yet applied", not a binding failure.
func diagnoseJump(tm *tmuxsrc.Client, running, haveState bool, startSetting string, p func(string, ...any)) int {
	set := tm.JumpTriggerSetting()
	if set.Unreadable {
		p("Keyboard jump: Cannot read the trigger key setting")
		return 0
	}
	configured := set.Key()
	shown := configured
	if !set.Set {
		shown = configured + " (default; not configured)"
	}
	actual := tm.ActualJumpTrigger()
	switch {
	case configured == "" || tm.JumpKeyInvalid(configured):
		p("Keyboard jump: Invalid setting (%s value %q is not a tmux key name). No trigger key is bound; there is no fallback to A.", tmuxsrc.JumpTriggerOption, configured)
		return 1
	case actual == "" && running && !haveState:
		p("Keyboard jump: Configured trigger key: %s; daemon status not yet available", shown)
		return 0
	case actual == "" && running && startSetting != set.String():
		p("Keyboard jump: Configured trigger key: %s; bound key: none. Setting not applied; reload the tmux configuration.", shown)
		return 0
	case actual == "" && running:
		p("Keyboard jump: Binding failed (configured trigger key: %s; bound key: none). tmux rejected the binding.", shown)
		return 1
	case actual == "":
		p("Keyboard jump: Not bound (configured trigger key: %s; daemon not running)", shown)
		return 0
	case actual != configured:
		p("Keyboard jump: Configured trigger key: %s; bound key: %s. Setting not applied; reload the tmux configuration.", shown, actual)
		return 0
	case !tm.PrefixBindingIsJump(actual):
		p("Keyboard jump: Overwritten (configured trigger key: %s). The prefix binding no longer enters jump mode; check tmux.conf and other plugins.", shown)
		return 1
	}
	p("Keyboard jump: Bound (configured trigger key: %s; bound key: %s). Press prefix, then %s, to enter jump mode.", shown, actual, actual)
	return 0
}

func serviceText(s instance.State) string {
	switch s.Service {
	case sidebar.ServiceConnected:
		return "Connected (read-only)"
	case sidebar.ServiceConnecting:
		return "Connecting"
	case sidebar.ServiceDown:
		return "Cannot connect: " + valueOr(s.ServiceReason, "unknown reason") + ". Retrying automatically; the plugin does not start or restart the OpenCode service."
	case sidebar.ServiceIdle:
		return "Standby (nothing to track; no connection or session queries)"
	}
	return "Not configured"
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func since(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}
