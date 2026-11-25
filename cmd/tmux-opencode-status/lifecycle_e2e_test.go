package main_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/fakeoc"
)

// Process-external lifecycle tests (issue #11): the real daemon + an isolated tmux server (-L … -f /dev/null).
// The entry point is `tmux-opencode-status start` (the idempotent command TPM's .tmux script calls on every load / reload).
// Lock and state files live in the test's own XDG_RUNTIME_DIR and never touch the user's real runtime directory.

// cmdEnv is the environment for every command under test: no $TMUX (so the developer's current tmux server cannot be used by mistake) and an isolated runtime directory.
func (e *env) cmdEnv() []string {
	must(e.t, os.MkdirAll(e.runtimeDir, 0o700))
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") || strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "XDG_RUNTIME_DIR="+e.runtimeDir)
	return append(out, e.daemonEnv...)
}

// common holds the flags shared by all commands that point at the observed server (without the subcommand).
func (e *env) common(sock string, extra ...string) []string {
	args := []string{"-L", sock, "-f", "/dev/null", "-tabs", e.tabsPath, "-service", e.servicePath, "-backstop", "5s"}
	args = append(args, e.daemonArgs...)
	return append(args, extra...)
}

// cli runs a subcommand and returns the combined output and the result.
func (e *env) cli(sub, sock string, extra ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command(e.daemonBin, append([]string{sub}, e.common(sock, extra...)...)...)
	cmd.Env = e.cmdEnv()
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	done := make(chan error, 1)
	must(e.t, cmd.Start())
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		e.t.Fatalf("%s timed out: %s", sub, out.String())
		return "", nil
	}
}

func (e *env) start(sock string, extra ...string) (string, error) {
	return e.cli("start", sock, extra...)
}

// daemons lists the daemon processes pointing at sock (excluding the short-lived start / stop / diagnose).
func (e *env) daemons(sock string) []int {
	out, _ := exec.Command("ps", "-axo", "pid=,command=").Output()
	var pids []int
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		// start launches run with an explicit -S <socket path>; running run directly may use -L <name>.
		if !strings.Contains(l, e.daemonBin) || !(strings.Contains(l, " -L "+sock+" ") || strings.Contains(l, "/"+sock+" ")) {
			continue
		}
		rest := strings.TrimPrefix(l[strings.Index(l, " "):], " ")
		rest = strings.TrimSpace(strings.TrimPrefix(rest, e.daemonBin))
		if strings.HasPrefix(rest, "start ") || strings.HasPrefix(rest, "stop ") || strings.HasPrefix(rest, "diagnose ") {
			continue
		}
		pid, _ := strconv.Atoi(strings.Fields(l)[0])
		pids = append(pids, pid)
	}
	return pids
}

func (e *env) stopStartedDaemons() {
	out, _ := exec.Command("ps", "-axo", "pid=,command=").Output()
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, e.daemonBin) && !strings.Contains(l, "ps -axo") {
			pid, _ := strconv.Atoi(strings.Fields(l)[0])
			if pid > 0 && pid != os.Getpid() {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
}

func (e *env) waitDaemons(sock string, want int, within time.Duration, what string) []int {
	e.t.Helper()
	start := time.Now()
	var got []int
	for time.Since(start) < within {
		got = e.daemons(sock)
		if len(got) == want {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("%s: the daemon instance count within %v should be %d, got %v", what, within, want, got)
	return nil
}

// allHooks merges global and window-level hooks (show-hooks lists them separately by level).
func (e *env) allHooks() string {
	return e.inTmux("show-hooks", "-g") + e.inTmux("show-hooks", "-gw")
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func (e *env) runtimeFiles() []string {
	var out []string
	_ = filepath.Walk(e.runtimeDir, func(p string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() && !strings.HasSuffix(p, ".log") {
			out = append(out, strings.TrimPrefix(p, e.runtimeDir))
		}
		return nil
	})
	return out
}

func newLifecycleEnv(t *testing.T, args ...string) *env {
	t.Helper()
	return newBareEnv(t, nil, args)
}

func TestStartIsIdempotentAndReloadsNeverLeaveASecondInstance(t *testing.T) {
	e := newLifecycleEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入"}}))
	e.tui(api)

	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	pids := e.waitDaemons(e.in, 1, 3*time.Second, "first start")
	e.eventually(3*time.Second, "sidebar shows after start", func(s string) bool { return strings.Contains(s, "! 修正登入") })

	// Reloading the configuration file runs start again: the same instance, the same pid
	for i := 0; i < 3; i++ {
		if out, err := e.start(e.in, "-show"); err != nil {
			t.Fatalf("repeated start: %v\n%s", err, out)
		}
	}
	if got := e.daemons(e.in); len(got) != 1 || got[0] != pids[0] {
		t.Fatalf("the original instance %v should remain after repeated loads, got %v", pids, got)
	}
}

func TestConcurrentStartsLeaveExactlyOneInstance(t *testing.T) {
	e := newLifecycleEnv(t)
	var wg sync.WaitGroup
	errs := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := e.start(e.in, "-show"); err != nil {
				errs <- fmt.Sprintf("%v: %s", err, out)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for m := range errs {
		t.Errorf("concurrent start must not fail: %s", m)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "concurrent start")
	time.Sleep(700 * time.Millisecond) // any surplus candidate process that would linger should have exited or been seen by now
	if got := e.daemons(e.in); len(got) != 1 {
		t.Fatalf("there should be exactly one instance after concurrent startup: %v", got)
	}
}

// Reload handoff: after a setting (width here) changes and start runs again, the old instance yields and the new one takes over, and there are never two at once;
// the handoff does not force-open a hidden sidebar, does not clear session-level overrides, and does not add or change bindings beyond what the plugin owns.
func TestReloadWithChangedSettingsHandsOffToExactlyOneNewInstance(t *testing.T) {
	e := newLifecycleEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入"}}))
	e.tui(api)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	old := e.waitDaemons(e.in, 1, 3*time.Second, "first start")
	e.eventually(3*time.Second, "displayed", func(s string) bool { return strings.Contains(s, "! 修正登入") })

	// The user hides the sidebar and overrides the side-status width in another session
	e.inTmux("new-session", "-d", "-s", "other")
	e.inTmux("set-option", "-t", "other", "side-status-width", "20")
	e.inTmux("set-option", "-g", "side-status", "off")
	keys := keysWithoutJump(e.inTmux("list-keys"))

	// Setting changed (-resync): handoff
	var peak int
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { // sample continuously during the handoff: two instances must never appear at any moment
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if n := len(e.daemons(e.in)); n > peak {
				peak = n
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	out, err := e.start(e.in, "-show", "-resync", "61s")
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("handoff start: %v\n%s", err, out)
	}
	now := e.waitDaemons(e.in, 1, 3*time.Second, "after handoff")
	if now[0] == old[0] {
		t.Fatalf("the new instance should take over after the setting changes, but the pid is still %d", old[0])
	}
	if peak > 1 {
		t.Fatalf("%d instances appeared at the same time during the handoff", peak)
	}
	if alive(old[0]) {
		t.Fatalf("the old instance %d should have exited", old[0])
	}

	// The new instance keeps tracking: content is updated even though the sidebar is hidden; showing it again immediately shows the latest content without a restart
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入", "新增的 tab"}}))
	time.Sleep(900 * time.Millisecond)
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "side-status")); got != "off" {
		t.Fatalf("a reload must not reopen the sidebar the user hid, side-status = %q", got)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-t", "other", "-v", "side-status-width")); got != "20" {
		t.Fatalf("the session-level override was cleared: %q", got)
	}
	if after := keysWithoutJump(e.inTmux("list-keys")); after != keys {
		t.Fatal("keyboard bindings were added or changed")
	}
	e.inTmux("set-option", "-g", "side-status", "left")
	e.eventually(2*time.Second, "showing again needs no manual restart", func(s string) bool { return strings.Contains(s, "! 新增的 tab") })
	if n := strings.Count(e.allHooks(), "[41700]"); n != 10 {
		t.Fatalf("after the handoff the plugin should have exactly one copy of each hook (8 wake-up + options (mode and palette) + title = 10), got %d:\n%s", n, e.allHooks())
	}
}

// Instances on different sockets are independent: they do not affect each other or show each other's panes, and stopping one does not affect the other.
func TestTwoSocketsRunIndependentInstances(t *testing.T) {
	e := newLifecycleEnv(t)
	other := e.in + "-b"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", other, "-f", "/dev/null", "kill-server").Run() })
	mine := filepath.Join(e.dir, "work", "mine")
	theirs := filepath.Join(e.dir, "work", "theirs")
	must(t, os.MkdirAll(theirs, 0o755))
	if b, err := exec.Command("tmux", "-L", other, "-f", "/dev/null", "new-session", "-d", "-s", "o", "-c", theirs, "exec "+e.bin).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	e.writeTabs(e.tabsJSON(map[string][]string{mine: {"local"}, theirs: {"foreign"}}))
	e.tui(mine)

	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := e.start(other, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	a := e.waitDaemons(e.in, 1, 3*time.Second, "socket A")
	b := e.waitDaemons(other, 1, 3*time.Second, "socket B")
	if a[0] == b[0] {
		t.Fatal("two sockets must not share an instance")
	}
	e.eventually(3*time.Second, "A shows only its own panes", func(s string) bool { return strings.Contains(s, "local") })
	if s := e.sidebar(); strings.Contains(s, "foreign") {
		t.Fatalf("A's sidebar contains B's panes:\n%s", s)
	}
	var bOpts string
	for i := 0; i < 60 && !strings.Contains(bOpts, "foreign"); i++ {
		b, _ := exec.Command("tmux", "-L", other, "-f", "/dev/null", "show-options", "-g").CombinedOutput()
		bOpts = grep(string(b), "tos_layout")
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(bOpts, "foreign") || strings.Contains(bOpts, "local") {
		t.Fatalf("B's side-status content should contain only its own panes:\n%s", bOpts)
	}

	// Starting A again does not affect B; stopping A does not affect B
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("stop A: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 0, 3*time.Second, "stop A")
	if got := e.daemons(other); len(got) != 1 || got[0] != b[0] {
		t.Fatalf("stopping A must not affect B: %v", got)
	}
}

func grep(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// With no TUI the daemon stays alive but does not connect or query sessions; once a TUI appears it starts tracking automatically;
// it keeps tracking while the sidebar is hidden and needs no manual restart when shown again; there are no writes to OpenCode at all.
func TestIdleWithoutTUIsAndHiddenSidebarStillRecoverWithoutManualRestart(t *testing.T) {
	e := newLifecycleEnv(t)
	svc := fakeoc.New(t)
	svc.AddSession("ses_0", "")
	svc.WriteServiceFile(e.servicePath)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入"}}))

	// The sidebar is hidden from the start (start is run without -show)
	e.inTmux("set-option", "-g", "side-status", "off")
	if out, err := e.start(e.in); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	time.Sleep(1500 * time.Millisecond)
	if svc.SessionRequests() != 0 || svc.Connects() != 0 {
		t.Fatalf("with no TUI (nothing to track) it must not connect or query sessions: connections %d, requests %v", svc.Connects(), svc.Requests())
	}
	if !alive(pid) {
		t.Fatal("the daemon must stay alive when there is no TUI")
	}

	// A TUI appears while the sidebar is hidden: tracking starts all the same
	e.tui(api, "--server", "http://127.0.0.1:1")
	deadline := time.Now().Add(5 * time.Second)
	for svc.SessionRequests() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if svc.SessionRequests() == 0 {
		t.Fatal("a TUI appeared while the sidebar was hidden, so the daemon should have started tracking anyway")
	}
	svc.Start("ses_0")
	time.Sleep(700 * time.Millisecond)
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "side-status")); got != "off" {
		t.Fatalf("tracking must not open the sidebar: %q", got)
	}
	// Show again: the content is immediately current
	e.inTmux("set-option", "-g", "side-status", "left")
	e.eventually(2*time.Second, "immediately current after showing again", func(s string) bool { return strings.Contains(s, "● 修正登入") })

	if w := svc.Writes(); len(w) != 0 {
		t.Fatalf("write request sent to OpenCode: %v", w)
	}
}

// After the last TUI closes it stops tracking (releases the service connection and stops querying), and the daemon stays alive.
func TestLastTUIClosingStopsQueriesButKeepsTheInstanceAlive(t *testing.T) {
	e := newLifecycleEnv(t)
	svc := fakeoc.New(t)
	svc.AddSession("ses_0", "")
	svc.WriteServiceFile(e.servicePath)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入"}}))
	win := e.tui(api, "--server", "http://127.0.0.1:1")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	e.eventually(5*time.Second, "connects to the service", func(s string) bool { return strings.Contains(s, "· 修正登入") })
	e.inTmux("kill-window", "-t", win)
	e.eventually(2*time.Second, "TUI closed", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })
	time.Sleep(300 * time.Millisecond)
	before := len(svc.Requests())
	time.Sleep(1500 * time.Millisecond)
	if n := len(svc.Requests()); n != before || svc.Subscribers() != 0 {
		t.Fatalf("must not query once nothing is tracked (requests %d → %d, SSE subscribers %d)", before, n, svc.Subscribers())
	}
	if !alive(pid) {
		t.Fatal("the daemon should stay alive after the last TUI closes")
	}
	// Adding a TUI needs no manual restart
	e.tui(api, "-c")
	e.eventually(5*time.Second, "recovers automatically after a TUI is added", func(s string) bool { return strings.Contains(s, "· 修正登入") })
}

// tmuxShim puts a tmux wrapper at the front of PATH: while the flag file exists, every tmux command fails (simulating a transient failure).
// When reject is non-empty, only show-options queries containing that string report an invalid option (simulating an older tmux that lacks the capability).
func tmuxShim(t *testing.T, flag, reject string) string {
	t.Helper()
	real, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not found")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n"
	if flag != "" {
		script += "if [ -e '" + flag + "' ]; then echo 'tmux: transient failure' >&2; exit 1; fi\n"
	}
	if reject != "" {
		script += "case \" $* \" in *\" show-options \"*\" " + reject + "\"*) echo \"invalid option: " + reject + "\" >&2; exit 1;; esac\n"
	}
	script += "exec " + real + " \"$@\"\n"
	must(t, os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755))
	return "PATH=" + dir + ":" + os.Getenv("PATH")
}

func (e *env) waitGone(pid int, within time.Duration, what string) {
	e.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("%s: process %d has not exited within %v", what, pid, within)
}

// Server exit: the daemon exits and the local lock and state files are removed; no background process is left behind.
func TestInstanceExitsAndCleansUpWhenTheTmuxServerExits(t *testing.T) {
	e := newLifecycleEnv(t, "-server-check", "200ms")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	if files := e.runtimeFiles(); len(files) < 2 {
		t.Fatalf("lock and state files should exist while running: %v", files)
	}
	e.inTmux("kill-server")
	e.waitGone(pid, 5*time.Second, "the daemon should exit after the server exits")
	if files := e.runtimeFiles(); len(files) != 0 {
		t.Fatalf("local resources should be cleaned up after the server exits: %v", files)
	}
}

// A single or brief tmux command failure is not a server exit: the daemon stays alive, clears nothing, and keeps working after recovery.
func TestTransientTmuxFailureIsNotMistakenForServerExit(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "fail")
	e := newBareEnv(t, []string{tmuxShim(t, flag, "")}, []string{"-server-check", "200ms"})
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入"}}))
	e.tui(api)
	e.inTmux("set", "-g", "@user-owned", "keep")
	e.inTmux("set-hook", "-g", "pane-exited[3]", "display-message user-hook")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	e.eventually(3*time.Second, "displayed", func(s string) bool { return strings.Contains(s, "! 修正登入") })

	must(t, os.WriteFile(flag, nil, 0o644)) // from now on every tmux command fails, for several server-check cycles
	time.Sleep(2500 * time.Millisecond)
	if !alive(pid) {
		t.Fatal("a transient tmux command failure must not be mistaken for a server exit")
	}
	if len(e.runtimeFiles()) < 2 {
		t.Fatalf("a transient failure must not remove lock or state files: %v", e.runtimeFiles())
	}
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "@user-owned")); got != "keep" || !strings.Contains(e.inTmux("show-hooks", "-g")+e.inTmux("show-hooks", "-gw"), "user-hook") {
		t.Fatal("a transient failure must not remove the user's options or hooks")
	}
	must(t, os.Remove(flag))
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入", "恢復後新增"}}))
	e.eventually(7*time.Second, "keeps working after tmux recovers", func(s string) bool { return strings.Contains(s, "! 恢復後新增") })
}

// Explicit stop: cleans up only this plugin's resources (hooks, @tos_*, the side-status-format it wrote, lock and state files);
// the user's options, hooks, session overrides, and bindings are left untouched; start can run again afterward.
func TestStopCleansOnlyThePluginsOwnResources(t *testing.T) {
	e := newLifecycleEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入"}}))
	e.tui(api)
	e.inTmux("set", "-g", "@oc_side", "user-value")
	e.inTmux("set", "-g", "@opencode-status-mode", "cwd")
	e.inTmux("set-hook", "-g", "pane-exited[3]", "display-message user-hook")
	e.inTmux("new-session", "-d", "-s", "other")
	e.inTmux("set-option", "-t", "other", "side-status-width", "20")
	keys := keysWithoutJump(e.inTmux("list-keys"))
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "displayed", func(s string) bool { return strings.Contains(s, "! 修正登入") })

	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 0, 3*time.Second, "stop")
	opts := e.inTmux("show-options", "-g")
	for _, l := range strings.Split(opts, "\n") {
		if strings.HasPrefix(l, "@tos_") {
			t.Fatalf("no @tos_* should remain after stop:\n%s", grep(opts, "tos_"))
		}
	}
	if strings.Contains(opts, "@opencode-status-shown") {
		t.Fatalf("the first-open sidebar marker should be removed after stop:\n%s", grep(opts, "opencode-status-shown"))
	}
	if !strings.Contains(opts, "@opencode-status-mode cwd") {
		t.Fatalf("the user's @opencode-status-* settings should be kept after stop:\n%s", opts)
	}
	if f := e.inTmux("show", "-gv", "side-status-format"); strings.Contains(f, "tos_") {
		t.Fatalf("side-status-format should return to the tmux default: %s", f)
	}
	if strings.Contains(e.allHooks(), "[41700]") || !strings.Contains(e.allHooks(), "user-hook") {
		t.Fatalf("after stop only the plugin's own hooks should be removed:\n%s", e.allHooks())
	}
	if !strings.Contains(opts, "@oc_side user-value") || !strings.Contains(opts, "@opencode-status-mode cwd") {
		t.Fatalf("the user's options were touched:\n%s", opts)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-t", "other", "-v", "side-status-width")); got != "20" {
		t.Fatalf("the session override was cleared: %q", got)
	}
	if keysWithoutJump(e.inTmux("list-keys")) != keys {
		t.Fatal("bindings were changed")
	}
	if files := e.runtimeFiles(); len(files) != 0 {
		t.Fatalf("local resources should be cleaned up after stop: %v", files)
	}
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("it should be restartable after stop: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "restart")
}

// The user set side-status-format themselves: stop must not overwrite or delete it.
func TestStopDoesNotRemoveAFormatTheUserSetAfterwards(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "displayed", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })
	e.inTmux("set", "-g", "side-status-format", "my own format")
	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "side-status-format")); got != "my own format" {
		t.Fatalf("the user's own side-status-format was touched: %q", got)
	}
}

// Missing a required capability (older tmux): start reports an explicit error and does not launch; other transient errors are not described as a missing capability.
func TestMissingTmuxCapabilityFailsLoudlyWithoutStartingAnything(t *testing.T) {
	e := newBareEnv(t, []string{tmuxShim(t, "", "side-status-width")}, nil)
	out, err := e.start(e.in, "-show")
	if err == nil || !strings.Contains(out, "missing required capabilities") || !strings.Contains(out, "side-status-width") {
		t.Fatalf("a missing capability should fail explicitly: err=%v\n%s", err, out)
	}
	time.Sleep(300 * time.Millisecond)
	if got := e.daemons(e.in); len(got) != 0 {
		t.Fatalf("a missing capability must not start the daemon: %v", got)
	}
	if strings.Contains(e.inTmux("show-options", "-g"), "@tos_") {
		t.Fatal("a missing capability must not write any sidebar option")
	}
}

func TestTransientTmuxErrorAtStartIsNotReportedAsMissingCapability(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "fail")
	must(t, os.WriteFile(flag, nil, 0o644))
	e := newBareEnv(t, []string{tmuxShim(t, flag, "")}, nil)
	out, err := e.start(e.in)
	if err == nil {
		t.Fatalf("start should fail when tmux is unavailable: %s", out)
	}
	if strings.Contains(out, "missing required capabilities") {
		t.Fatalf("a transient error must not be described as a missing capability: %s", out)
	}
	if got := e.daemons(e.in); len(got) != 0 {
		t.Fatalf("must not start: %v", got)
	}
}

// diagnose gets the diagnostic output.
func (e *env) diagnose(sock string) string {
	e.t.Helper()
	out, err := e.cli("diagnose", sock)
	if err != nil {
		e.t.Fatalf("diagnose: %v\n%s", err, out)
	}
	return out
}

func mustContain(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("diagnostic output is missing %q:\n%s", s, out)
		}
	}
}

func mustNotContain(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(out, s) {
			t.Errorf("diagnostic output must not contain %q:\n%s", s, out)
		}
	}
}

// Diagnostics cover: mode, daemon state, TUI / tab counts, service connection, recent errors, required capabilities, and the effect of session overrides;
// and they contain no credentials, prompts / conversations, OpenCode tab titles, directory paths, or tmux session names.
func TestDiagnoseReportsTheDecidedItemsAndLeaksNothingSensitive(t *testing.T) {
	e := newLifecycleEnv(t)
	svc := fakeoc.New(t)
	svc.AddSession("ses_0", "")
	svc.AddSession("ses_1", "")
	api := filepath.Join(e.dir, "work", "secret-project-name")
	const title1, title2 = "SENTINEL_TITLE_修正 token sk-live-12345", "SENTINEL_PROMPT_請幫我刪掉 production 資料庫"
	e.writeTabs(e.tabsJSON(map[string][]string{api: {title1, title2}}))
	e.inTmux("new-session", "-d", "-s", "SENTINEL_TMUX_SESSION")
	e.inTmux("new-session", "-d", "-s", "SENTINEL_OTHER_SESSION")
	e.inTmux("set-option", "-t", "SENTINEL_OTHER_SESSION", "side-status", "off")
	e.inTmux("set-option", "-t", "SENTINEL_OTHER_SESSION", "side-status-width", "20")
	e.inTmux("set", "-g", "@opencode-status-mode", "cwd")
	e.tui(api, "--server", "http://127.0.0.1:1")

	// Service not up yet
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "notice when the service is down", func(s string) bool { return strings.Contains(s, "Cannot connect to OpenCode") })
	time.Sleep(300 * time.Millisecond)
	out := e.diagnose(e.in)
	t.Logf("diagnostics (service down):\n%s", out)
	mustContain(t, out, "version", "Configured plugin mode: cwd", "Active mode: cwd", "Daemon: Running",
		"Service connection: Cannot connect", "service discovery file not found", "does not start or restart", "Recent errors", "Required capabilities", "side-status-format are available",
		"Session overrides: 1", "side-status=off", "side-status-width=20", "does not change these overrides")

	// After the service appears: connected, TUI / tab counts
	svc.WriteServiceFile(e.servicePath)
	e.eventually(6*time.Second, "connects to the service", func(s string) bool { return !strings.Contains(s, "Cannot connect") })
	time.Sleep(300 * time.Millisecond)
	out = e.diagnose(e.in)
	t.Logf("diagnostics (connected):\n%s", out)
	mustContain(t, out, "Service connection: Connected", "TUI clients found: 1", "tabs tracked: 2")
	// When the mode disagrees with OpenCode's actual scope (the global list is empty): nothing is tracked, and diagnostics make that visible
	e.inTmux("set", "-g", "@opencode-status-mode", "global")
	e.eventually(3*time.Second, "switches to global", func(s string) bool { return strings.Contains(s, "No tabs") })
	time.Sleep(300 * time.Millisecond)
	out = e.diagnose(e.in)
	mustContain(t, out, "Active mode: global", "tabs tracked: 0", "Standby", "no connection")
	e.inTmux("set", "-g", "@opencode-status-mode", "cwd")
	e.eventually(5*time.Second, "switches back to cwd", func(s string) bool { return strings.Contains(s, "·") })
	time.Sleep(300 * time.Millisecond)
	out = e.diagnose(e.in)
	mustContain(t, out, "Active mode: cwd", "tabs tracked: 2")

	for _, forbidden := range []string{fakeoc.Password, "sk-live", "SENTINEL_", "secret-project-name", e.dir, e.servicePath, e.tabsPath, "127.0.0.1", "0.0.0.0", svc.URL()} {
		mustNotContain(t, out, forbidden)
	}
	// Recent errors after a service interruption do not leak either
	_ = os.Remove(e.servicePath)
	svc.Drop()
	e.eventually(5*time.Second, "disconnect notice", func(s string) bool { return strings.Contains(s, "Cannot connect to OpenCode") })
	time.Sleep(300 * time.Millisecond)
	out = e.diagnose(e.in)
	mustContain(t, out, "Service connection: Cannot connect")
	for _, forbidden := range []string{fakeoc.Password, "sk-live", "SENTINEL_", "secret-project-name", e.dir, "127.0.0.1", "Authorization", "Basic "} {
		mustNotContain(t, out, forbidden)
	}
	if w := svc.Writes(); len(w) != 0 {
		t.Fatalf("write request sent to OpenCode: %v", w)
	}
}

// Diagnostics are strictly read-only: they do not change tmux options / hooks / bindings, start or stop the daemon, or connect to OpenCode.
func TestDiagnoseIsStrictlyReadOnly(t *testing.T) {
	e := newLifecycleEnv(t)
	svc := fakeoc.New(t)
	svc.WriteServiceFile(e.servicePath)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	e.eventually(3*time.Second, "displayed", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })
	opts, hooks, keys := e.inTmux("show-options", "-g"), e.allHooks(), e.inTmux("list-keys")
	reqs := len(svc.Requests())
	e.diagnose(e.in)
	e.diagnose(e.in)
	if e.inTmux("show-options", "-g") != opts || e.allHooks() != hooks || e.inTmux("list-keys") != keys {
		t.Fatal("diagnostics changed the tmux state")
	}
	if got := e.daemons(e.in); len(got) != 1 || got[0] != pid {
		t.Fatalf("diagnostics must not start or stop the daemon: %v", got)
	}
	if len(svc.Requests()) != reqs || len(svc.Writes()) != 0 {
		t.Fatal("diagnostics must not connect to OpenCode")
	}
}

// Diagnostics can say why nothing is shown: the daemon is not running, the mode setting is invalid, or a capability is missing.
func TestDiagnoseExplainsWhyNothingIsShown(t *testing.T) {
	e := newLifecycleEnv(t)
	out := e.diagnose(e.in)
	mustContain(t, out, "Daemon: Not running", "reload the tmux configuration", "Issues found: 1")

	e.inTmux("set", "-g", "@opencode-status-mode", "globl")
	if o, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	time.Sleep(600 * time.Millisecond)
	out = e.diagnose(e.in)
	mustContain(t, out, "Configured plugin mode: Invalid value", "Tracking not started",
		"Current issue: Invalid mode; not tracking\n", "[mode] Invalid mode; not tracking\n")
	mustNotContain(t, out, "globl")
}

func TestDiagnoseReportsAMissingCapability(t *testing.T) {
	e := newBareEnv(t, []string{tmuxShim(t, "", "side-status-format")}, nil)
	mustContain(t, e.diagnose(e.in), "Required capabilities: Missing side-status-format", "tmux 3.9")
}

// When the sidebar is hidden, diagnostics explain that the daemon is still tracking.
func TestDiagnoseMentionsTheSidebarIsHiddenButStillTracking(t *testing.T) {
	e := newLifecycleEnv(t)
	e.inTmux("set-option", "-g", "side-status", "off")
	if out, err := e.start(e.in); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	mustContain(t, e.diagnose(e.in), "sidebar hidden", "no restart")
}

// Service disconnects and tab list problems must both appear under "Recent errors", with fixed descriptions only.
func TestDiagnoseRecentErrorsIncludeServiceAndTabListProblems(t *testing.T) {
	e := newLifecycleEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"a"}}))
	e.tui(api, "--server", "http://127.0.0.1:1")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "service-down notice", func(s string) bool { return strings.Contains(s, "Cannot connect to OpenCode") })
	e.writeTabs(`{"version":9,"secret":"sk-live-should-not-appear"}`)
	e.eventually(3*time.Second, "unreadable tab list notice", func(s string) bool { return strings.Contains(s, "Cannot read tab list") })
	time.Sleep(300 * time.Millisecond)
	out := e.diagnose(e.in)
	mustContain(t, out, "Recent errors:", "[service]", "service discovery file not found", "[tabs]", "Tab list is unreadable")
	mustNotContain(t, out, "sk-live", "secret", `"version"`, e.dir)
}

// When the tmux server restarts (same socket, new server), the old instance must not make start wrongly believe "an instance already exists", even if it has not noticed yet.
func TestStartAfterTheTmuxServerRestartedTakesOverFromTheStaleInstance(t *testing.T) {
	e := newLifecycleEnv(t, "-server-check", "1h") // the old instance deliberately fails to notice
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	old := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	e.inTmux("kill-server")
	e.inTmux("new-session", "-d", "-s", "again", "-x", "100", "-y", "30")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start after the server restart: %v\n%s", err, out)
	}
	now := e.waitDaemons(e.in, 1, 3*time.Second, "after takeover")
	if now[0] == old || alive(old) {
		t.Fatalf("the new instance should take over and the old one should exit: old %d, new %v", old, now)
	}
	e.inTmux("show-options", "-g")
	if !strings.Contains(e.allHooks(), "[41700]") {
		t.Fatal("the new server should have the new instance's hooks")
	}
}

// Invalid settings are validated first: start fails and the existing instance is completely unaffected (no handoff, no restart).
func TestInvalidStartSettingsAreRejectedBeforeTouchingTheRunningInstance(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	for _, bad := range [][]string{{"-width", "0"}, {"-backstop", "0s"}, {"-width", "-3"}, {"-idle-timeout", "0s"}, {"-server-check", "-1s"}} {
		out, err := e.start(e.in, bad...)
		if err == nil || !strings.Contains(out, "invalid setting") {
			t.Fatalf("%v should be rejected: err=%v\n%s", bad, err, out)
		}
	}
	if got := e.daemons(e.in); len(got) != 1 || got[0] != pid {
		t.Fatalf("an invalid setting must not touch the existing instance: %v (was %d)", got, pid)
	}
}

// When TPM runs inside run-shell there is no -L / -S; it uses $TMUX to point at the server that loaded the configuration; with neither $TMUX nor flags the command is refused (the default server is never touched).
func TestStartWithoutSocketFlagsFollowsTMUXEnvAndRefusesWithoutIt(t *testing.T) {
	e := newLifecycleEnv(t)
	sock := strings.TrimSpace(e.inTmux("display-message", "-p", "#{socket_path},#{pid},0"))
	cmd := exec.Command(e.daemonBin, "start", "-tabs", e.tabsPath, "-service", e.servicePath, "-show")
	cmd.Env = append(e.cmdEnv(), "TMUX="+sock)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start via $TMUX: %v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start via TMUX")

	bare := exec.Command(e.daemonBin, "start")
	bare.Env = e.cmdEnv()
	if out, err := bare.CombinedOutput(); err == nil || !strings.Contains(string(out), "specify -L <name> or -S <path> explicitly") {
		t.Fatalf("it must refuse when there is no socket information: %v\n%s", err, out)
	}
}

// The instance is killed with kill -9 (no chance to clean up): the lock is released automatically and there is no stale lock; the next start takes over,
// and replaces the orphaned @tos_* options left by the previous instance (including the old name @tos_sb_main) with new content.
func TestStartRecoversFromAKilledInstanceWithoutStaleLockOrOrphanOptions(t *testing.T) {
	e := newLifecycleEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}}))
	e.tui(api)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	old := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	e.eventually(3*time.Second, "displayed", func(s string) bool { return strings.Contains(s, "! j") })
	for _, name := range []string{"@tos_sb_main", "@tos_sb_999", "@tos_whatever"} {
		e.inTmux("set", "-g", name, "orphan from crashed instance")
	}
	must(t, syscall.Kill(old, syscall.SIGKILL))
	e.waitGone(old, 3*time.Second, "kill -9")

	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("the lock must not linger: %v\n%s", err, out)
	}
	now := e.waitDaemons(e.in, 1, 3*time.Second, "takeover")[0]
	if now == old {
		t.Fatal("should be a new process")
	}
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"only"}}))
	e.eventually(3*time.Second, "new instance takes over the display", func(s string) bool { return strings.Contains(s, "! only") })
	opts := e.inTmux("show-options", "-g")
	for _, name := range []string{"@tos_sb_main", "@tos_sb_999", "@tos_whatever"} {
		if strings.Contains(opts, name+" ") {
			t.Fatalf("the orphaned option %s left by the previous instance should be cleared on the new instance's first write", name)
		}
	}
	if !strings.Contains(opts, "@tos_layout_main ") {
		t.Fatal("the new instance should publish @tos_layout_main")
	}
}
