package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Keyboard jump: a real attached client on an isolated tmux server sends keys with `send-keys -K`
// (-K makes keys go through the client's key table lookup, the same as a user pressing keys in the terminal). All tests use an isolated -L server.

const jumpTable = "opencode-status-jump"

// clients is the names of all clients on the observed server (sorted by name).
func (e *env) clients() []string {
	e.t.Helper()
	names := strings.Fields(e.inTmux("list-clients", "-F", "#{client_name}"))
	return names
}

// client is the hosting client (the only client).
func (e *env) client() string {
	e.t.Helper()
	cs := e.clients()
	if len(cs) != 1 {
		e.t.Fatalf("expected exactly one client: %v", cs)
	}
	return cs[0]
}

func (e *env) keyTable(client string) string {
	e.t.Helper()
	return strings.TrimSpace(e.inTmux("display-message", "-c", client, "-p", "#{client_key_table}"))
}

func (e *env) sendKeys(client string, keys ...string) {
	e.t.Helper()
	e.inTmux(append([]string{"send-keys", "-K", "-c", client}, keys...)...)
}

func (e *env) waitKeyTable(client string, within time.Duration, want string) {
	e.t.Helper()
	start := time.Now()
	var last string
	for time.Since(start) < within {
		if last = e.keyTable(client); last == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("after waiting %v, client_key_table is not %q (currently %q)", within, want, last)
}

func (e *env) prefixBinding(key string) string {
	out, err := e.tryInTmux("list-keys", "-F", "#{key_command}", "-T", "prefix", "--", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (e *env) tryInTmux(args ...string) (string, error) {
	b, err := exec.Command("tmux", append([]string{"-L", e.in, "-f", "/dev/null"}, args...)...).CombinedOutput()
	return string(b), err
}

func (e *env) waitPrefixBinding(within time.Duration, key, want string) {
	e.t.Helper()
	start := time.Now()
	var last string
	for time.Since(start) < within {
		if last = e.prefixBinding(key); last == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("prefix %s binding: after waiting %v, it is not %q (currently %q)", key, within, want, last)
}

func (e *env) tosOptions() []string {
	var names []string
	for _, l := range strings.Split(e.inTmux("show-options", "-g"), "\n") {
		if name, _, _ := strings.Cut(l, " "); strings.HasPrefix(name, "@tos_") {
			names = append(names, name)
		}
	}
	return names
}

func (e *env) globalOption(name string) string {
	return strings.TrimSpace(e.inTmux("show-options", "-gqv", name))
}

// jumpTableKeys is the binding lines of the opencode-status-jump table; empty when the table does not exist.
func (e *env) jumpTableKeys() string {
	out, err := e.tryInTmux("list-keys", "-T", jumpTable)
	if err != nil {
		return ""
	}
	return out
}

const switchToJump = "switch-client -T " + jumpTable

// keyJumpWorld prepares TUIs for two directory groups (api, web): api's TUI is in a non-active pane of another session,
// and web is in another window of the main session. Returns api's window and pane.
func (e *env) keyJumpWorld() (apiDir, window, tuiPane string) {
	e.t.Helper()
	apiDir = filepath.Join(e.dir, "work", "api")
	webDir := filepath.Join(e.dir, "work", "web")
	e.writeTabs(e.tabsJSON(map[string][]string{apiDir: {"修正登入", "second tab"}, webDir: {"網站"}}))
	window, tuiPane = e.jumpWorld(apiDir)
	e.tui(webDir)
	e.eventually(5*time.Second, "two groups appear in the sidebar", func(s string) bool {
		return strings.Contains(s, "api") && strings.Contains(s, "web") && strings.Contains(s, "網站")
	})
	return apiDir, window, tuiPane
}

func TestPrefixTriggerShowsLettersAndALetterJumpsAcrossSessions(t *testing.T) {
	e := newEnv(t)
	_, window, tuiPane := e.keyJumpWorld()
	c := e.client()
	if got := e.keyTable(c); got != "root" {
		t.Fatalf("test precondition: client_key_table = %q", got)
	}
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)

	e.sendKeys(c, "C-b", "A")
	e.waitKeyTable(c, 3*time.Second, jumpTable)
	e.eventually(3*time.Second, "letter labels appear on group rows", func(s string) bool {
		return strings.Contains(s, " a  api") && strings.Contains(s, " b  web") && !strings.Contains(s, "▎")
	})

	e.sendKeys(c, "a")
	e.waitWhere(3*time.Second, "pressing a jumps to the api TUI", "work "+window+" "+tuiPane)
	e.waitKeyTable(c, 3*time.Second, "root")
	e.eventually(3*time.Second, "labels disappear and the normal layout returns", func(s string) bool {
		return strings.Contains(s, "▎api") && strings.Contains(s, "▎web") && !strings.Contains(s, " a  api")
	})
}

func TestEscUnboundKeysAndLettersWithoutAGroupCancelWithoutMovingOrTyping(t *testing.T) {
	e := newEnv(t)
	e.keyJumpWorld()
	c := e.client()
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)
	// Run cat in the main pane: any key sent into the pane is written to the file after Enter is pressed.
	typed := filepath.Join(e.dir, "typed")
	e.inTmux("respawn-pane", "-k", "-t", "main:0", "cat > "+typed)
	start := e.where()

	for _, key := range []string{"Escape", "x", "f", "Space", "1"} { // x and f have no group (only a and b exist); the rest are unbound
		e.sendKeys(c, "C-b", "A")
		e.waitKeyTable(c, 3*time.Second, jumpTable)
		e.sendKeys(c, key)
		e.waitKeyTable(c, 3*time.Second, "root")
		if got := e.where(); got != start {
			t.Fatalf("position changed after pressing %s: %q → %q", key, start, got)
		}
	}
	e.inTmux("send-keys", "-t", "main:0", "Enter") // keys that were not sent into the pane will not appear before this line
	e.eventually(3*time.Second, "pane receives Enter", func(string) bool {
		b, _ := os.ReadFile(typed)
		return len(b) > 0
	})
	if b, _ := os.ReadFile(typed); string(b) != "\n" {
		t.Fatalf("keys pressed while cancelling must not be sent into the pane, but the pane received %q", b)
	}
}

func TestOnlyTheClientThatPressedTheTriggerShowsLabels(t *testing.T) {
	e := newEnv(t)
	e.keyJumpWorld()
	first := e.client()
	e.extraClient("second", 100, 30)
	var second string
	for start := time.Now(); second == "" && time.Since(start) < 3*time.Second; time.Sleep(50 * time.Millisecond) {
		for _, c := range e.clients() {
			if c != first {
				second = c
			}
		}
	}
	if second == "" {
		t.Fatalf("second client not found: %v", e.clients())
	}
	e.clientWaits("second", 3*time.Second, "the second client has the normal layout", func(ls []string) bool {
		return strings.Contains(strings.Join(ls, "\n"), "▎api")
	})
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)

	e.sendKeys(first, "C-b", "A")
	e.waitKeyTable(first, 3*time.Second, jumpTable)
	e.eventually(3*time.Second, "labels appear on the first client", func(s string) bool { return strings.Contains(s, " a  api") })
	time.Sleep(300 * time.Millisecond)
	if got := e.keyTable(second); got != "root" {
		t.Fatalf("second client's key table = %q", got)
	}
	joined := strings.Join(e.clientScreen("second"), "\n")
	if strings.Contains(joined, " a  api") || !strings.Contains(joined, "▎api") {
		t.Fatalf("the second client's sidebar must not change:\n%s", joined)
	}
}

func TestUsersRootAliasEntersJumpModeAndTheSidebarCanBeHidden(t *testing.T) {
	e := newEnv(t)
	_, window, tuiPane := e.keyJumpWorld()
	c := e.client()
	e.inTmux("bind", "-n", "M-a", "switch-client", "-T", jumpTable)
	e.inTmux("set", "-g", "side-status", "off") // jump mode can still be entered while the sidebar is hidden, pressing letters from memory
	e.sendKeys(c, "M-a")
	e.waitKeyTable(c, 3*time.Second, jumpTable)
	e.sendKeys(c, "a")
	e.waitWhere(3*time.Second, "press a after entering via the root alias", "work "+window+" "+tuiPane)
	e.waitKeyTable(c, 3*time.Second, "root")
}

func TestChangingTheTriggerAndReloadingHandsOffAndStopRemovesEverything(t *testing.T) {
	e := newLifecycleEnv(t)
	e.inTmux("bind", "-n", "M-z", "display-message", "mine") // the user's own binding, unchanged throughout
	keysBefore := keysWithoutJump(e.inTmux("list-keys"))
	prefixBefore := e.inTmux("list-keys", "-T", "prefix")

	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	pids := e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)
	e.eventually(3*time.Second, "@tos_jump_trigger is published together with the layout", func(string) bool { return e.globalOption("@tos_jump_trigger") == "A" })
	if !strings.Contains(e.jumpTableKeys(), " a ") {
		t.Fatalf("the jump key table should exist after startup: %q", e.jumpTableKeys())
	}

	// Reload the same configuration: no handoff, the instance is unchanged
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("reload start: %v\n%s", err, out)
	}
	if got := e.daemons(e.in); len(got) != 1 || got[0] != pids[0] {
		t.Fatalf("no handoff expected when the setting is unchanged: %v → %v", pids, got)
	}

	// Reload after changing the trigger key: handoff, the old key disappears and the new key takes effect
	e.inTmux("set", "-g", "@opencode-status-jump-trigger", "M-j")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start after changing the key: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "after handoff")
	e.waitPrefixBinding(3*time.Second, "M-j", switchToJump)
	if got := e.prefixBinding("A"); got != "" {
		t.Fatalf("the old trigger key A should be unbound, but it is still %q", got)
	}
	e.eventually(3*time.Second, "@tos_jump_trigger changes to M-j", func(string) bool { return e.globalOption("@tos_jump_trigger") == "M-j" })
	if got := e.daemons(e.in); len(got) != 1 || got[0] == pids[0] {
		t.Fatalf("the new instance should take over after changing the key: %v → %v", pids, got)
	}
	c := e.client()
	e.sendKeys(c, "C-b", "M-j")
	e.waitKeyTable(c, 3*time.Second, jumpTable)
	e.sendKeys(c, "Escape")
	e.waitKeyTable(c, 3*time.Second, "root")

	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 0, 3*time.Second, "stop")
	if got := e.prefixBinding("M-j"); got != "" {
		t.Fatalf("the trigger key should be gone after stop: %q", got)
	}
	if got := e.jumpTableKeys(); got != "" {
		t.Fatalf("the jump key table should not exist after stop: %q", got)
	}
	if got := e.tosOptions(); len(got) != 0 {
		t.Fatalf("no @tos_* should remain after stop: %v", got)
	}
	if got := e.globalOption("@opencode-status-jump-trigger"); got != "M-j" {
		t.Fatalf("the user's setting must not be touched: %q", got)
	}
	if after := keysWithoutJump(e.inTmux("list-keys")); after != keysBefore {
		t.Fatal("other bindings were changed after stop")
	}
	if after := e.inTmux("list-keys", "-T", "prefix"); after != prefixBefore {
		t.Fatalf("after stop the prefix table should match its state before startup (the plugin neither writes anything back nor leaves anything behind)\nbefore:\n%s\nafter:\n%s", prefixBefore, after)
	}
}

func TestInvalidTriggerBindsNoPrefixKeyAndDiagnoseReportsItAsAProblem(t *testing.T) {
	for name, value := range map[string]string{"empty string": "", "unrecognized key name": "notakey", "contains whitespace": "A B"} {
		t.Run(name, func(t *testing.T) {
			e := newLifecycleEnv(t)
			prefixBefore := e.inTmux("list-keys", "-T", "prefix")
			e.inTmux("set", "-g", "@opencode-status-jump-trigger", value)
			if out, err := e.start(e.in, "-show"); err != nil {
				t.Fatalf("start: %v\n%s", err, out)
			}
			e.waitDaemons(e.in, 1, 3*time.Second, "start")
			e.eventually(3*time.Second, "daemon has published", func(string) bool { return e.globalOption("@tos_layout_main") != "" || len(e.tosOptions()) > 0 })
			time.Sleep(300 * time.Millisecond)
			if after := e.inTmux("list-keys", "-T", "prefix"); after != prefixBefore {
				t.Fatalf("an invalid trigger key must not bind any prefix key (including A, and must not fall back to the default)\nbefore:\n%s\nafter:\n%s", prefixBefore, after)
			}
			if got := e.globalOption("@tos_jump_trigger"); got != "" {
				t.Fatalf("no key is actually bound, @tos_jump_trigger = %q", got)
			}
			out := e.diagnose(e.in)
			mustContain(t, out, "Keyboard jump: Invalid setting", "Issues found: 1")
		})
	}
}

func TestDiagnoseReportsTheTriggerAsBoundThenOverwrittenAndOtherSettingsAsExpected(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)
	e.eventually(3*time.Second, "published", func(string) bool { return e.globalOption("@tos_jump_trigger") == "A" })
	out := e.diagnose(e.in)
	mustContain(t, out, "Keyboard jump: Bound", "A (default; not configured)", "bound key: A", "Result: No issues found")

	e.inTmux("bind", "-T", "prefix", "A", "display-message", "taken")
	out = e.diagnose(e.in)
	mustContain(t, out, "Keyboard jump: Overwritten", "Issues found: 1")

	// The setting was changed but not yet reloaded: show the difference between the setting and the actual binding; this is not counted as a problem
	e.inTmux("bind", "-T", "prefix", "A", "switch-client", "-T", jumpTable)
	e.inTmux("set", "-g", "@opencode-status-jump-trigger", "M-j")
	out = e.diagnose(e.in)
	mustContain(t, out, "Keyboard jump: Configured trigger key: M-j; bound key: A", "reload the tmux configuration", "Result: No issues found")
}

func TestDiagnoseReportsABindingFailureWhenTmuxRefusesTheTrigger(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "published", func(string) bool { return e.globalOption("@tos_jump_trigger") == "A" })
	// Simulate "the daemon is running but no key is actually bound": the record of the actual binding disappears while the configured key is still valid.
	e.inTmux("set", "-gu", "@tos_jump_trigger")
	out := e.diagnose(e.in)
	mustContain(t, out, "Keyboard jump: Binding failed", "Issues found: 1")
}

func TestDiagnoseSaysNotYetAppliedWhenTheTriggerWasInvalidAtStartAndFixedWithoutReloading(t *testing.T) {
	e := newLifecycleEnv(t)
	e.inTmux("set", "-g", "@opencode-status-jump-trigger", "")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "daemon has published", func(string) bool { return len(e.tosOptions()) > 0 })
	mustContain(t, e.diagnose(e.in), "Keyboard jump: Invalid setting", "Issues found: 1")

	// The user changed it to a valid value but has not reloaded: the setting is not yet applied, which is not a binding failure and is not counted as a problem
	e.inTmux("set", "-g", "@opencode-status-jump-trigger", "M-j")
	out := e.diagnose(e.in)
	mustContain(t, out, "Keyboard jump: Configured trigger key: M-j", "Setting not applied", "Result: No issues found")
	if strings.Contains(out, "Binding failed") {
		t.Fatalf("a binding failure must not be reported:\n%s", out)
	}
}
