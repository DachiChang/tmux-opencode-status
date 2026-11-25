package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Click-to-jump: a real attached client on an isolated tmux server receives SGR mouse sequences,
// and we observe the binding lifecycle, cross-session jumps, and behavior for non-plugin ranges. All tests use an isolated -L server.

const (
	defaultJumpBinding = "switch-client -t ="
	jumpKey            = "MouseDown1SideStatus"
)

// isKeyJumpLine reports whether a list-keys line belongs to keyboard jump: the jump key table itself, or any binding that switches to it
// (the prefix trigger key or a user's root alias).
func isKeyJumpLine(l string) bool {
	return strings.Contains(l, "-T opencode-status-jump ") || strings.HasSuffix(strings.TrimSpace(l), "switch-client -T opencode-status-jump")
}

// splitJumpBinding splits list-keys output into the MouseDown1SideStatus binding body (without the prefix; found=false if absent)
// and the remaining output after removing that line and the keyboard-jump bindings; the plugin takes over only the click-to-jump binding and its own keyboard-jump bindings.
func splitJumpBinding(keys string) (binding, others string, found bool) {
	var rest []string
	for _, l := range strings.Split(keys, "\n") {
		if isKeyJumpLine(l) {
			continue
		}
		if f := strings.Fields(l); len(f) >= 4 && f[3] == jumpKey {
			binding, found = strings.TrimSpace(strings.TrimPrefix(l[strings.Index(l, jumpKey)+len(jumpKey):], " ")), true
			continue
		}
		// After the jump key table is added, list-keys re-aligns columns by the longest table name, so normalize whitespace line by line before comparing.
		rest = append(rest, strings.Join(strings.Fields(l), " "))
	}
	return binding, strings.Join(rest, "\n"), found
}

// binding is the body of the MouseDown1SideStatus binding in the root key table.
func (e *env) binding() string {
	e.t.Helper()
	b, _, found := splitJumpBinding(e.inTmux("list-keys", "-T", "root"))
	if !found {
		e.t.Fatalf("%s binding not found", jumpKey)
	}
	return b
}

// keysWithoutJump is the list-keys output with the MouseDown1SideStatus line removed.
func keysWithoutJump(keys string) string {
	_, others, _ := splitJumpBinding(keys)
	return others
}

// isPluginBinding checks the full binding body: it accepts only an oc<N> range string and run-shell -C switching to '%N',
// while everything else keeps tmux's default switch-client -t =.
func isPluginBinding(b string) bool {
	return strings.Contains(b, `m/r:^oc[0-9]+$,#{mouse_status_range}`) &&
		strings.Contains(b, `run-shell -C "switch-client -t '%#{s/^oc//:mouse_status_range}'"`) &&
		strings.Contains(b, `{ switch-client -t = }`)
}

func (e *env) pluginBound() bool { return isPluginBinding(e.binding()) }

func (e *env) waitBinding(within time.Duration, what string, ok func(string) bool) {
	e.t.Helper()
	start := time.Now()
	var last string
	for time.Since(start) < within {
		if last = e.binding(); ok(last) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("%s: not reached within %v, current binding: %s", what, within, last)
}

// click sends an SGR mouse left-button press and release to the hosting client (coordinates are 1-based; x is the column, y is the row).
func (e *env) click(x, y int) {
	e.t.Helper()
	e.outTmux("send-keys", "-t", "hold", "-l", fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x, y, x, y))
}

// where is the session / window / pane the hosting client is currently in.
func (e *env) where() string {
	e.t.Helper()
	return strings.TrimSpace(e.inTmux("list-clients", "-F", "#{client_session} #{window_id} #{pane_id}"))
}

func (e *env) waitWhere(within time.Duration, what, want string) {
	e.t.Helper()
	start := time.Now()
	var last string
	for time.Since(start) < within {
		if last = e.where(); last == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("%s: not reached within %v, client is at %q, want %q", what, within, last, want)
}

func (e *env) sidebarRow(contains string) int {
	e.t.Helper()
	for i, l := range strings.Split(e.sidebar(), "\n") {
		if strings.Contains(l, contains) {
			return i + 1
		}
	}
	e.t.Fatalf("sidebar does not contain %q:\n%s", contains, e.sidebar())
	return 0
}

// jumpWorld: the client is in the main session; another session "work" has a window with two panes,
// and the pane hosting the TUI client is not the active pane. Returns that window and the pane hosting the TUI client.
func (e *env) jumpWorld(dir string) (window, tuiPane string) {
	e.t.Helper()
	must(e.t, os.MkdirAll(dir, 0o755))
	e.inTmux("set", "-g", "mouse", "on")
	e.inTmux("new-session", "-d", "-s", "work", "-x", "100", "-y", "30", "-c", e.dir)
	e.inTmux("split-window", "-d", "-t", "work:", "-c", dir, "exec "+e.bin)
	panes := strings.Fields(e.inTmux("list-panes", "-t", "work:", "-F", "#{pane_id}"))
	window = strings.TrimSpace(e.inTmux("display-message", "-p", "-t", "work:", "#{window_id}"))
	active := strings.TrimSpace(e.inTmux("display-message", "-p", "-t", "work:", "#{pane_id}"))
	for _, p := range panes {
		if p != active {
			tuiPane = p
		}
	}
	return window, tuiPane
}

func TestJumpBindingLifecycleStartStopAndHandoff(t *testing.T) {
	e := newLifecycleEnv(t)
	if got := e.binding(); got != defaultJumpBinding {
		t.Fatalf("should be the tmux default before startup: %q", got)
	}
	keys := keysWithoutJump(e.inTmux("list-keys"))
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.waitBinding(3*time.Second, "plugin binding after startup", isPluginBinding)

	// Reload the configuration (handoff): the old instance exits, the new instance takes over, and the binding is still the plugin's
	if out, err := e.start(e.in, "-show", "-resync", "61s"); err != nil {
		t.Fatalf("handoff start: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "after handoff")
	time.Sleep(500 * time.Millisecond)
	if !e.pluginBound() {
		t.Fatalf("should still be the plugin binding after handoff: %s", e.binding())
	}

	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 0, 3*time.Second, "stop")
	if got := e.binding(); got != defaultJumpBinding {
		t.Fatalf("after stop it should be restored to the tmux default: %q", got)
	}
	if after := keysWithoutJump(e.inTmux("list-keys")); after != keys {
		t.Fatal("the plugin should touch only MouseDown1SideStatus, but other bindings were changed")
	}
}

func TestClickingAGroupRowJumpsAcrossSessionsToTheJumpTarget(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"修正登入", "second tab"}}))
	window, tuiPane := e.jumpWorld(dir)
	e.waitBinding(3*time.Second, "plugin binding", isPluginBinding)
	e.eventually(3*time.Second, "group appears in the sidebar", func(s string) bool { return strings.Contains(s, "api") && strings.Contains(s, "second tab") })
	want := "work " + window + " " + tuiPane
	if e.where() == want {
		t.Fatal("test precondition: the client must not start at the target")
	}

	e.click(5, e.sidebarRow("api"))
	e.waitWhere(3*time.Second, "click the directory row", want)
	if active := strings.TrimSpace(e.inTmux("display-message", "-p", "-t", "work:", "#{pane_id}")); active != tuiPane {
		t.Fatalf("the target pane should become the window's active pane: %s", active)
	}

	// Go back to main and click a tab row instead: same result
	e.inTmux("switch-client", "-t", "main")
	e.waitWhere(3*time.Second, "back to main", "main @0 %0")
	e.eventually(3*time.Second, "screen back on main", func(string) bool {
		return strings.Contains(e.outTmux("capture-pane", "-p", "-t", "hold"), "[main]")
	})
	time.Sleep(1500 * time.Millisecond) // avoid tmux's multi-click detection: a second click that comes too fast becomes DoubleClick1SideStatus
	e.click(5, e.sidebarRow("second tab"))
	e.waitWhere(3*time.Second, "click the tab row", want)
}

func TestClickingTheNoTabRowJumpsLikeTheGroupRow(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	window, tuiPane := e.jumpWorld(dir)
	e.waitBinding(3*time.Second, "plugin binding", isPluginBinding)
	e.eventually(3*time.Second, "\"No tabs\" appears", func(s string) bool { return strings.Contains(s, "No tabs") })
	e.click(5, e.sidebarRow("No tabs"))
	e.waitWhere(3*time.Second, "click the \"No tabs\" row", "work "+window+" "+tuiPane)
}

func TestClickingNonGroupRowsStaysPut(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"one"}}))
	e.jumpWorld(dir)
	e.waitBinding(3*time.Second, "plugin binding", isPluginBinding)
	e.eventually(3*time.Second, "group appears in the sidebar", func(s string) bool { return strings.Contains(s, "one") })
	before := e.where()
	// When the connection fails the sidebar adds a notice line, shifting the group rows; pick blank spots beyond the content (about 6 rows at most).
	const (
		titleRow       = 1
		dividerRow     = 2
		blankBelowList = 10
		bottomRow      = 25
	)
	for _, c := range []struct {
		name string
		y    int
	}{
		{"title", titleRow},
		{"divider", dividerRow},
		{"blank area below the list", blankBelowList},
		{"bottom blank area", bottomRow},
	} {
		e.click(5, c.y)
		time.Sleep(400 * time.Millisecond) // negative assertion: we can only wait a while to confirm that no jump happens
		if got := e.where(); got != before {
			t.Fatalf("clicking the %s (row %d) must not jump: %q → %q", c.name, c.y, before, got)
		}
	}
}

func TestClickingAForeignRangeBehavesLikeTmuxDefault(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"one"}}))
	window, _ := e.jumpWorld(dir)
	e.waitBinding(3*time.Second, "plugin binding", isPluginBinding)
	// main session's own side-status-format is changed to a tmux-default-style range=session list
	e.inTmux("set", "-t", "main", "side-status-format", "#{S:#[range=session|#{session_id}]#{session_name}#[norange]#[nl]}")
	// The list is sorted by session name: main, work; wait for the second row to show work before clicking
	const workRow = 2
	e.eventually(3*time.Second, "sidebar switches to the session list", func(s string) bool {
		lines := strings.Split(s, "\n")
		return len(lines) >= workRow && strings.Contains(lines[workRow-1], "work")
	})
	activePane := strings.TrimSpace(e.inTmux("display-message", "-p", "-t", "work:", "#{pane_id}"))
	wantWork := "work " + window + " " + activePane
	e.click(2, workRow)
	e.waitWhere(3*time.Second, "a non-plugin range goes through switch-client -t =", wantWork)
}
