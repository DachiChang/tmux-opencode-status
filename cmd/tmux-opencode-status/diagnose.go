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

// diagnose 是嚴格唯讀的診斷：只查 tmux（show-options／list-sessions／display-message）與本機狀態檔，
// 不連 OpenCode、不寫任何 tmux 選項、不啟動或停止任何程序。
// 輸出只有計數、模式、固定描述與 tmux session id；不含憑證、prompt、對話、
// OpenCode session／tab 標題、目錄路徑，也不含原始錯誤文字或 tmux session 名稱。
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

// diagnosticLine keeps each status record on a single line.
func diagnosticLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// diagnoseJump 輸出「鍵盤跳轉」一行並回傳要計入的問題數（無效設定、綁定失敗、已被覆寫各算一項）。
// 唯讀：只讀設定選項、@tos_jump_trigger 與 list-keys。不偵測觸發鍵在綁定前原本是什麼。
// reported 與 startSetting 是常駐程式啟動時讀到的設定（haveState 為 false 時未知）：目前設定與它不同，
// 表示使用者改了設定還沒重載，那是「設定尚未套用」，不是綁定失敗。
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
