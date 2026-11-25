// Package tmuxsrc is the adapter for a single tmux server: pane facts, sidebar output, and change notifications.
// Every command explicitly targets the socket given at construction and never uses the default server.
package tmuxsrc

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
)

// Client points to one explicit tmux server. SocketArgs is, for example, ["-L","name"] or ["-S","/path"].
type Client struct {
	SocketArgs []string
	// ConfigArgs lets isolated tests pass -f /dev/null; it is empty in production (the server is already running).
	ConfigArgs []string
	// Width is side-status-width (including the 1-column separator line).
	Width     int
	published map[string]bool
	// lastFormat is the last side-status-format this instance wrote; on stop it is removed only if the content is still the same,
	// so a value the user set later is not overwritten.
	lastFormat string
	// jumpTrigger is the trigger key this instance actually bound successfully; an empty string means nothing is bound (invalid or failed). It is written to @tos_jump_trigger on every publish.
	jumpTrigger string
	// proc is the source of foreground-process facts; nil uses the local implementation (tests substitute a controlled one).
	proc procReader
}

// procReader reads a foreground process's argv and working directory; alive reports whether the process still exists.
type procReader interface {
	Argv(pid int) ([]string, error)
	Cwd(pid int) (string, error)
	Alive(pid int) bool
}

type localProc struct{}

func (localProc) Argv(pid int) ([]string, error) { return procArgv(pid) }
func (localProc) Cwd(pid int) (string, error)    { return procCwd(pid) }
func (localProc) Alive(pid int) bool             { return ProcessAlive(pid) }

func (c *Client) procs() procReader {
	if c.proc != nil {
		return c.proc
	}
	return localProc{}
}

const (
	wakeChannel = "tos_wake"
	// titleChannel is dedicated to pane title changes: a TUI may change its title frequently, so it must be throttled separately from structural events.
	titleChannel = "tos_title"
	// titleMinGap is the minimum interval between title wake-ups: the first change after a quiet period wakes immediately (leading),
	// consecutive changes within the interval are merged by tmux into one remembered signal, and one more wake-up follows when the interval ends (trailing).
	titleMinGap = 300 * time.Millisecond
	// hookIndex is the dedicated index this plugin uses in hook arrays, so it does not overwrite the user's own hooks.
	hookIndex = 41700
)

// These hooks only "wake" the sync; the actual content is always re-read, and any hook that is missed is covered by the low-frequency catch-up.
var wakeHooks = []string{
	"pane-exited", "window-linked", "window-unlinked",
	"after-new-window", "after-split-window", "after-kill-pane",
	"session-created", "session-closed",
}

// optionHook wakes only when a mode option or any swatch option is set (including `set -gu` and a set in a reloaded config file), so the daemon's own
// writes to sidebar options do not cause feedback.
var optionHook = "after-set-option"

// titleHook is used only to trigger an earlier rescan (tmux has no structural event when a TUI starts or exits in an existing shell);
// it does not read the title content and is not a basis for identification. It does not fire when terminal titles are off; the low-frequency catch-up covers that.
var titleHook = "pane-title-changed"

func optionHookCmd() string {
	arg := "#{hook_argument_0}"
	cond := "#{||:#{==:" + arg + "," + sidebar.ModeOption + "},#{m:" + sidebar.ThemeOptionPrefix + "*," + arg + "}}"
	return `if-shell -F "` + cond + `" "wait-for -S ` + wakeChannel + `"`
}

func (c *Client) tmux(stdin string, args ...string) ([]byte, error) {
	full := append(append(append([]string{}, c.SocketArgs...), c.ConfigArgs...), args...)
	cmd := exec.Command("tmux", full...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tmux %s：%w：%s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// Panes lists the foreground-process facts of every pane on the same server; a pane listed repeatedly through a linked window is taken only once.
func (c *Client) Panes() ([]sidebar.PaneFact, error) {
	out, err := c.tmux("", "list-panes", "-a", "-F", "#{pane_id}\t#{pane_tty}\t#{pane_current_path}\t#{pane_current_command}\t#{alternate_on}")
	if err != nil {
		return nil, err
	}
	procs, err := foregroundProcs()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	facts := []sidebar.PaneFact{}
	var unresolved []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		fact := sidebar.PaneFact{PaneID: f[0], Pane: contract.Pane{
			PaneCurrentPath: f[2], PaneCurrentCommand: f[3], AlternateOn: f[4] == "1",
		}}
		if pid, ok := procs[strings.TrimPrefix(f[1], "/dev/")]; ok {
			if err := c.readForeground(&fact, pid); err != nil {
				unresolved = append(unresolved, f[0])
				continue
			}
		}
		facts = append(facts, fact)
	}
	if len(unresolved) > 0 {
		return nil, fmt.Errorf("cannot identify foreground processes for %d pane(s) (%s)", len(unresolved), strings.Join(unresolved, ", "))
	}
	return facts, nil
}

// readForeground reads the foreground process's argv and (for TUI candidates) its working directory.
//   - Process has exited: a normal disappearance, not an error; the pane currently has no identifiable foreground process, and the next scan will see the new facts.
//   - Process is alive but unreadable: only a pane whose foreground command tmux itself reports as opencode is an undeterminable candidate,
//     so report an error (the caller keeps the last usable data and shows a notice); other panes (for example processes owned by other users)
//     cannot be the user's OpenCode TUI and must not invalidate the whole sidebar.
func (c *Client) readForeground(fact *sidebar.PaneFact, pid int) error {
	pr := c.procs()
	candidate := fact.PaneCurrentCommand == "opencode"
	argv, err := pr.Argv(pid)
	if err != nil {
		if !pr.Alive(pid) || !candidate {
			return nil
		}
		return fmt.Errorf("read process argv: %w", err)
	}
	fact.ForegroundArgv = argv
	if !contract.IsTUICandidate(fact.Pane) {
		return nil
	}
	cwd, err := pr.Cwd(pid)
	if err != nil {
		if !pr.Alive(pid) {
			fact.ForegroundArgv = nil
			return nil
		}
		return fmt.Errorf("read process working directory: %w", err)
	}
	fact.ForegroundCwd = cwd
	return nil
}

// foregroundProcs returns tty name → foreground process group leader (pgid == tpgid). pane_pid is usually the shell and cannot be used.
func foregroundProcs() (map[string]int, error) {
	out, err := exec.Command("ps", "-axo", "pid=,pgid=,tpgid=,tty=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps：%w", err)
	}
	res := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 4 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		pgid, _ := strconv.Atoi(f[1])
		tpgid, _ := strconv.Atoi(f[2])
		if pid > 0 && pgid == tpgid && pid == pgid {
			res[f[3]] = pid
		}
	}
	return res, nil
}

// Publish writes the layout options and side-status-format in a single source-file (the same command queue),
// and clears unused options under the same prefix (left by a previous instance or a crash). Clients never see a half update.
func (c *Client) Publish(p render.Published) error {
	var s strings.Builder
	s.WriteString("set-option -g side-status-width " + strconv.Itoa(c.Width) + "\n")
	opts := make(map[string]string, len(p.Options)+1)
	for n, v := range p.Options {
		opts[n] = v
	}
	if c.jumpTrigger != "" {
		// The actually bound trigger key is written together with the layout: a handed-over new instance and stop both learn from here which key to unbind.
		opts[render.BoundTriggerOption] = c.jumpTrigger
	}
	names := make([]string, 0, len(opts))
	for n := range opts {
		names = append(names, n)
	}
	sort.Strings(names)
	if c.published == nil {
		// First write: existing options under this plugin's own prefix (left by a previous instance or a crash) are cleared by this write as well.
		c.published = c.existingPluginOptions()
	}
	next := map[string]bool{}
	for _, n := range names {
		next[n] = true
		s.WriteString("set-option -g " + n + " " + quote(opts[n]) + "\n")
	}
	s.WriteString("set-option -g side-status-format " + quote(p.Format()) + "\n")
	for old := range c.published {
		if !next[old] {
			s.WriteString("set-option -gu " + old + "\n")
		}
	}
	if _, err := c.tmux(s.String(), "source-file", "-"); err != nil {
		return err
	}
	c.published = next
	c.lastFormat = p.Format()
	return nil
}

// existingPluginOptions lists the internal options in the global scope that belong to this plugin's prefix (@tos_). A read failure is treated as none (a later write adds the new content).
func (c *Client) existingPluginOptions() map[string]bool {
	out, err := c.tmux("", "show-options", "-g")
	found := map[string]bool{}
	if err != nil {
		return found
	}
	for _, line := range strings.Split(string(out), "\n") {
		if name, _, _ := strings.Cut(line, " "); strings.HasPrefix(name, render.OptPrefix) {
			found[name] = true
		}
	}
	return found
}

// ClearDisplay, when the daemon exits because of an explicit stop (the server is still alive), removes the internal options (@tos_*) this plugin published,
// and, if side-status-format is still the content this instance wrote, resets it and width to the tmux defaults so no stale content is left that is no longer updated.
// It touches only options under this plugin's prefix and the global options it wrote itself; it does not touch other user options.
func (c *Client) ClearDisplay() {
	var b strings.Builder
	if cur, err := c.tmux("", "show-options", "-gqv", "side-status-format"); err == nil && c.lastFormat != "" &&
		strings.TrimSuffix(string(cur), "\n") == c.lastFormat {
		b.WriteString("set-option -gu side-status-format\nset-option -gu side-status-width\n")
	}
	for name := range c.existingPluginOptions() {
		b.WriteString("set-option -gu " + name + "\n")
	}
	_, _ = c.tmux(b.String(), "source-file", "-")
}

// JumpKey is the root key-table key taken over for sidebar click-to-jump (tmux's left mouse button press event on side-status).
const JumpKey = "MouseDown1SideStatus"

// jumpBinding runs only inside tmux (no shell, no call to the plugin executable): when the range string matches oc<N> (prefix: see render.JumpRangePrefix), it switches to pane %N
// (session, window, and pane in one step, across sessions); otherwise it keeps tmux's default switch-client -t =.
// It does not use range=pane: see ADR-0002.
const jumpBinding = `bind -T root ` + JumpKey + ` {
  if -F "#{m/r:^` + render.JumpRangePrefix + `[0-9]+$,#{mouse_status_range}}" {
    run-shell -C "switch-client -t '%#{s/^` + render.JumpRangePrefix + `//:mouse_status_range}'"
  } {
    switch-client -t =
  }
}
`

// BindJump takes over MouseDown1SideStatus. Like Publish it writes through source-file to avoid quote-escaping problems;
// writing the same content repeatedly is idempotent (the new instance writes it once more on handover).
func (c *Client) BindJump() error {
	_, err := c.tmux(jumpBinding, "source-file", "-")
	return err
}

// ResetJump writes MouseDown1SideStatus back to the tmux default on explicit stop. It does not unbind: after stop,
// side-status-format returns to the tmux default, and clicking to switch session/window in the default format depends on this default binding.
// It does not save or restore any custom binding the user had.
func (c *Client) ResetJump() {
	_, _ = c.tmux("", "bind-key", "-T", "root", JumpKey, "switch-client", "-t", "=")
}

// quote converts to a double-quoted string for a tmux config file.
func quote(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "\n", `\n`).Replace(v) + `"`
}

// Setting reads a global mode option read-only. An option that does not exist (empty output) and one that exists with an empty string (output of a single newline)
// are two different states; the value itself has only the one trailing newline added by tmux removed, with no other trimming.
func (c *Client) Setting() (sidebar.ModeSetting, error) {
	out, err := c.tmux("", "show-options", "-gqv", sidebar.ModeOption)
	if err != nil {
		return sidebar.ModeSetting{}, err
	}
	if len(out) == 0 {
		return sidebar.ModeSetting{}, nil
	}
	return sidebar.ModeSetting{Value: strings.TrimSuffix(string(out), "\n"), Set: true}, nil
}

const (
	swatchNameSep = "\x1f"
	swatchEnd     = "\x1e"
)

// Swatches reads the raw values of all swatch options read-only with a single `show-options -g`; keys are the swatch names with the prefix removed.
// Values are returned as-is (including empty strings and format characters) and validity is decided by the caller; control characters are used as separators instead of parsing tmux's escaped output,
// so values containing newlines or quotes are not misread. A read failure is reported as an error and is not treated as everything being unset.
func (c *Client) Swatches() (map[string]string, error) {
	out, err := c.tmux("", "show-options", "-g", "-F", "#{option_name}"+swatchNameSep+"#{option_value}"+swatchEnd)
	if err != nil {
		return nil, err
	}
	res := map[string]string{}
	for _, rec := range strings.Split(string(out), swatchEnd) {
		name, val, ok := strings.Cut(strings.TrimPrefix(rec, "\n"), swatchNameSep)
		if !ok {
			continue
		}
		if swatch, ok := strings.CutPrefix(name, sidebar.ThemeOptionPrefix); ok && swatch != "" {
			res[swatch] = val
		}
	}
	return res, nil
}

// MissingCapabilities lists the required capabilities this tmux lacks (the side-status options of 3.9).
// Only a missing option (invalid option) counts as a missing capability; other errors (the server temporarily unavailable) are reported as-is and are not misjudged as missing capabilities.
func (c *Client) MissingCapabilities() ([]string, error) {
	var missing []string
	for _, opt := range []string{"side-status", "side-status-width", "side-status-format"} {
		if _, err := c.tmux("", "show-options", "-gv", opt); err != nil {
			if strings.Contains(err.Error(), "invalid option") {
				missing = append(missing, opt)
				continue
			}
			return nil, err
		}
	}
	return missing, nil
}

// SessionOverrides lists the tmux sessions that override side-status options at session level (returning only the session id and option name,
// not the session name). These sessions do not follow the global settings, and the plugin never clears them.
func (c *Client) SessionOverrides() ([]SessionOverride, error) {
	out, err := c.tmux("", "list-sessions", "-F", "#{session_id}")
	if err != nil {
		return nil, err
	}
	var res []SessionOverride
	for _, id := range strings.Fields(string(out)) {
		opts, err := c.tmux("", "show-options", "-t", id)
		if err != nil {
			continue // the session disappeared between queries
		}
		o := SessionOverride{TmuxSession: id}
		for _, line := range strings.Split(string(opts), "\n") {
			name, val, _ := strings.Cut(line, " ")
			if name == "side-status" || name == "side-status-width" || name == "side-status-format" {
				if name == "side-status-format" {
					val = "(custom format)"
				}
				o.Options = append(o.Options, name+"="+val)
			}
		}
		if len(o.Options) > 0 {
			res = append(res, o)
		}
	}
	return res, nil
}

// SessionOverride is one session's side-status override.
type SessionOverride struct {
	TmuxSession string
	Options     []string
}

// GlobalValue reads the value of a global option read-only.
func (c *Client) GlobalValue(name string) (string, error) {
	out, err := c.tmux("", "show-options", "-gqv", name)
	return strings.TrimSuffix(string(out), "\n"), err
}

// ShownMarker records "this server has already had the sidebar shown by the plugin"; later reloads, handovers, or restarts no longer turn it on for the user,
// so a sidebar the user has hidden is not reopened. The option disappears when the tmux server ends.
const ShownMarker = "@opencode-status-shown"

// Show sets side-status to left, and does so only once per tmux server lifetime (used by the caller only when the user explicitly asks).
func (c *Client) Show() error {
	out, err := c.tmux("", "show-options", "-gqv", ShownMarker)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "" {
		return nil
	}
	_, err = c.tmux("", "set-option", "-g", "side-status", "left", ";", "set-option", "-g", ShownMarker, "1")
	return err
}

// ClearShownMarker is for explicit stop (so a later restart can show the sidebar again).
func (c *Client) ClearShownMarker() {
	_, _ = c.tmux("", "set-option", "-gu", ShownMarker)
}

// ServerInfo is the identity of the tmux server on this socket.
type ServerInfo struct {
	SocketPath string
	PID        int
	Version    string
}

// Server asks for the server's socket path, process, and version; reports an error when the server is not there.
func (c *Client) Server() (ServerInfo, error) {
	out, err := c.tmux("", "display-message", "-p", "#{socket_path}\t#{pid}\t#{version}")
	if err != nil {
		return ServerInfo{}, err
	}
	f := strings.Split(strings.TrimRight(string(out), "\n"), "\t")
	if len(f) != 3 {
		return ServerInfo{}, fmt.Errorf("unexpected response format from tmux display-message")
	}
	pid, perr := strconv.Atoi(f[1])
	if perr != nil || f[0] == "" {
		return ServerInfo{}, fmt.Errorf("unexpected response format from tmux display-message")
	}
	return ServerInfo{SocketPath: f[0], PID: pid, Version: f[2]}, nil
}

// ProcessAlive reports whether the process still exists. Whether the tmux server has ended is decided by its process,
// not by a single command failure: a command can fail from momentary busyness or environment problems, and only the process being absent is a definite end.
func ProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// WatchEvents installs the wake-up hooks and blocks on `tmux wait-for`; it sends one notification for each wake-up.
// tmux remembers a signal sent while nobody is waiting, so changes between two wait-for calls are not lost. The hooks are removed on exit.
func (c *Client) WatchEvents(ctx context.Context, wake chan<- struct{}) error {
	for _, h := range wakeHooks {
		if _, err := c.tmux("", "set-hook", "-g", h+"["+strconv.Itoa(hookIndex)+"]", "wait-for -S "+wakeChannel); err != nil {
			c.RemoveHooks()
			return err
		}
	}
	if _, err := c.tmux("", "set-hook", "-g", optionHook+"["+strconv.Itoa(hookIndex)+"]", optionHookCmd()); err != nil {
		c.RemoveHooks()
		return err
	}
	if _, err := c.tmux("", "set-hook", "-g", titleHook+"["+strconv.Itoa(hookIndex)+"]", "wait-for -S "+titleChannel); err != nil {
		c.RemoveHooks()
		return err
	}
	var titleLoop sync.WaitGroup
	titleLoop.Add(1)
	go func() {
		defer titleLoop.Done()
		c.waitLoop(ctx, titleChannel, titleMinGap, wake)
	}()
	defer titleLoop.Wait()
	defer c.RemoveHooks()
	// Changes that happen before the hooks are installed produce no notification; after installing, wake once more so changes in the startup gap need not wait for the low-frequency catch-up.
	select {
	case wake <- struct{}{}:
	default:
	}
	c.waitLoop(ctx, wakeChannel, 0, wake)
	return nil
}

// waitLoop blocks on `tmux wait-for <channel>` and sends one notification for each wake-up, then waits at least minGap
// before waiting again (0 means no throttling). Signals during the throttled period are remembered and merged by tmux, so they are neither lost nor accumulated.
func (c *Client) waitLoop(ctx context.Context, channel string, minGap time.Duration, wake chan<- struct{}) {
	for ctx.Err() == nil {
		args := append(append(append([]string{}, c.SocketArgs...), c.ConfigArgs...), "wait-for", channel)
		cmd := exec.CommandContext(ctx, "tmux", args...)
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return
			}
			select { // server temporarily unavailable: retry later; the low-frequency catch-up handles syncing
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		select {
		case wake <- struct{}{}:
		default:
		}
		if minGap > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(minGap):
			}
		}
	}
}

// RemoveHooks removes only the hook indexes this plugin installed and leaves the user's own hooks alone.
func (c *Client) RemoveHooks() {
	_, _ = c.tmux("", "set-hook", "-gu", optionHook+"["+strconv.Itoa(hookIndex)+"]")
	_, _ = c.tmux("", "set-hook", "-gu", titleHook+"["+strconv.Itoa(hookIndex)+"]")
	for _, h := range wakeHooks {
		_, _ = c.tmux("", "set-hook", "-gu", h+"["+strconv.Itoa(hookIndex)+"]")
	}
}
