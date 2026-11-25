package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/fakeoc"
)

// Process-level external behavior tests: the real daemon + an isolated tmux server (-L <socket> -f /dev/null) +
// a fake `opencode` executable + a controlled tabs.json; results are observed from the attached client's actual screen.
// The user's default tmux server and OpenCode data are not involved at all.

type env struct {
	t        *testing.T
	dir      string
	in, out  string // the observed server and the outer server hosting the client
	daemon   *exec.Cmd
	tabsPath string
	// servicePath points at the test's own service.json; the user's real OpenCode service is never read.
	servicePath string
	binDir      string
	bin         string
	daemonEnv   []string
	daemonArgs  []string // extra command-line flags for the daemon (for example, a shorter silence timeout)
	daemonBin   string
	// runtimeDir isolates lock and state files (XDG_RUNTIME_DIR); tests never touch the user's real runtime directory.
	runtimeDir string
}

func (e *env) inTmux(args ...string) string {
	e.t.Helper()
	b, err := exec.Command("tmux", append([]string{"-L", e.in, "-f", "/dev/null"}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("tmux %v: %v: %s", args, err, b)
	}
	return string(b)
}

func (e *env) outTmux(args ...string) string {
	e.t.Helper()
	b, err := exec.Command("tmux", append([]string{"-L", e.out, "-f", "/dev/null"}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("tmux(out) %v: %v: %s", args, err, b)
	}
	return string(b)
}

// fakeOpencodeSrc is a fake opencode: by default it only sleeps. When FAKE_TITLE is non-empty it sets the terminal title at startup like a real TUI,
// restores the title and exits on SIGINT / SIGTERM; FAKE_CHURN_MS makes it change the title frequently and continuously.
const fakeOpencodeSrc = `package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func title(s string) {
	if os.Getenv("FAKE_TITLE") != "" {
		fmt.Fprintf(os.Stdout, "\033]0;%s\007", s)
	}
}

func main() {
	title("OpenCode")
	if ms, _ := strconv.Atoi(os.Getenv("FAKE_CHURN_MS")); ms > 0 {
		go func() {
			for i := 0; ; i++ {
				title(fmt.Sprintf("OpenCode %d", i))
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}()
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-c:
		title("shell")
	case <-time.After(time.Hour):
	}
}
`

func newEnv(t *testing.T) *env { t.Helper(); return newEnvWith(t, nil) }

// newEnvWith's daemonEnv is appended to the daemon's environment variables (used to put a tmux wrapper at the front of PATH to produce a deterministic slow startup).
func newEnvWith(t *testing.T, daemonEnv []string) *env { return newEnvFull(t, daemonEnv, nil) }

func newEnvFull(t *testing.T, daemonEnv, daemonArgs []string) *env {
	t.Helper()
	e := newBareEnv(t, daemonEnv, daemonArgs)
	e.startDaemon(e.daemonBin)
	return e
}

// newBareEnv sets up the isolated tmux server, the outer client, and the fake opencode, but does not start the daemon (the test decides how to start it).
func newBareEnv(t *testing.T, daemonEnv, daemonArgs []string) *env {
	t.Helper()
	if testing.Short() {
		t.Skip("-short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	e := &env{t: t, dir: root, in: fmt.Sprintf("tos-e2e-in-%d", os.Getpid()), out: fmt.Sprintf("tos-e2e-out-%d", os.Getpid())}
	e.runtimeDir = filepath.Join(root, "run")
	e.tabsPath = filepath.Join(root, "state", "tabs.json")
	e.servicePath = filepath.Join(root, "state", "service.json")
	e.binDir = filepath.Join(root, "bin")
	must(t, os.MkdirAll(filepath.Dir(e.tabsPath), 0o755))
	must(t, os.MkdirAll(e.binDir, 0o755))

	// Fake opencode: just a sleeping process whose argv[0] basename is opencode.
	src := filepath.Join(root, "fake", "main.go")
	must(t, os.MkdirAll(filepath.Dir(src), 0o755))
	must(t, os.WriteFile(src, []byte(fakeOpencodeSrc), 0o644))
	e.bin = filepath.Join(e.binDir, "opencode")
	if b, err := exec.Command("go", "build", "-o", e.bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build fake opencode: %v: %s", err, b)
	}
	daemonBin := filepath.Join(root, "tmux-opencode-status")
	if b, err := exec.Command("go", "build", "-o", daemonBin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v: %s", err, b)
	}
	e.daemonBin = daemonBin
	e.writeTabs(`{"global":{"tabs":[],"unread":{}},"cwd":{}}`)

	e.inTmux("new-session", "-d", "-s", "main", "-x", "100", "-y", "30", "-c", root)
	// Hosting client: attaches to the observed server from a pane on the outer server, with a fixed size.
	e.outTmux("new-session", "-d", "-s", "hold", "-x", "100", "-y", "30",
		fmt.Sprintf("tmux -L %s -f /dev/null attach -t main", e.in))
	t.Cleanup(func() {
		if e.daemon != nil && e.daemon.Process != nil {
			_ = e.daemon.Process.Kill()
			_ = e.daemon.Wait()
		}
		e.stopStartedDaemons()
		_ = exec.Command("tmux", "-L", e.in, "-f", "/dev/null", "kill-server").Run()
		_ = exec.Command("tmux", "-L", e.out, "-f", "/dev/null", "kill-server").Run()
	})
	e.daemonEnv, e.daemonArgs = daemonEnv, daemonArgs
	return e
}

func (e *env) startDaemon(bin string) {
	args := append([]string{"-L", e.in, "-f", "/dev/null", "-tabs", e.tabsPath, "-service", e.servicePath, "-show", "-backstop", "5s"}, e.daemonArgs...)
	e.daemon = exec.Command(bin, args...)
	e.daemon.Stderr = os.Stderr
	e.daemon.Env = e.cmdEnv()
	must(e.t, e.daemon.Start())
}

func (e *env) writeTabs(raw string) {
	tmp := e.tabsPath + ".tmp"
	must(e.t, os.WriteFile(tmp, []byte(raw), 0o644))
	must(e.t, os.Rename(tmp, e.tabsPath)) // OpenCode updates via tmp + rename
}

func (e *env) tabsJSON(dirs map[string][]string) string {
	cwd := map[string]any{}
	for d, titles := range dirs {
		var tabs []map[string]string
		for i, ti := range titles {
			tabs = append(tabs, map[string]string{"sessionID": fmt.Sprintf("ses_%d", i), "title": ti})
		}
		cwd[d] = map[string]any{"tabs": tabs, "unread": map[string]any{}}
	}
	b, _ := json.Marshal(map[string]any{"global": map[string]any{"tabs": []any{}, "unread": map[string]any{}}, "cwd": cwd})
	return string(b)
}

// tui starts the fake opencode in a new window with cwd set to dir.
func (e *env) tui(dir string, extra ...string) string {
	return e.tuiCmd("exec", dir, extra...)
}

// tuiUnderShell starts a TUI inside an interactive shell (with job control): when it is killed the pane still exists and tmux emits no hook.
func (e *env) tuiUnderShell(dir string) string {
	must(e.t, os.MkdirAll(dir, 0o755))
	win := strings.TrimSpace(e.inTmux("new-window", "-d", "-P", "-F", "#{window_id}", "-c", dir))
	time.Sleep(400 * time.Millisecond) // wait for the shell to be ready
	e.inTmux("send-keys", "-t", win, e.bin, "Enter")
	return win
}

func (e *env) tuiCmd(prefix, dir string, extra ...string) string {
	must(e.t, os.MkdirAll(dir, 0o755))
	return strings.TrimSpace(e.inTmux("new-window", "-d", "-P", "-F", "#{window_id}", "-c", dir, strings.TrimSpace(prefix+" "+strings.Join(append([]string{e.bin}, extra...), " "))))
}

// sidebar is the leftmost 36 cells of the client's actual screen (including the divider).
func (e *env) sidebar() string {
	e.t.Helper()
	var lines []string
	for _, l := range strings.Split(e.outTmux("capture-pane", "-p", "-t", "hold"), "\n") {
		r := []rune(l)
		if len(r) > 35 {
			r = r[:35]
		}
		lines = append(lines, strings.TrimRight(string(r), " "))
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func (e *env) eventually(within time.Duration, what string, ok func(string) bool) time.Duration {
	e.t.Helper()
	start := time.Now()
	var last string
	for time.Since(start) < within {
		last = e.sidebar()
		if ok(last) {
			return time.Since(start)
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("%s: not reached within %v, current screen:\n%s", what, within, last)
	return 0
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSidebarFollowsTUIAndTabChangesOnARealTmux(t *testing.T) {
	e := newEnv(t)
	api := filepath.Join(e.dir, "work", "api")

	e.eventually(3*time.Second, "shows no TUI after startup", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })

	// TUI appears: keep the group and show "No tabs" (there is no tab data yet)
	e.tui(api, "--server", "http://127.0.0.1:1")
	d := e.eventually(time.Second, "new TUI appears within 1 second", func(s string) bool {
		return strings.Contains(s, "api") && strings.Contains(s, "No tabs")
	})
	t.Logf("TUI appears → screen: %v", d)

	// A tab appears (tabs.json updated by rename); status is not connected yet, so a gray ! placeholder symbol is shown
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入", "write tests"}}))
	d = e.eventually(time.Second, "new tab appears within 1 second", func(s string) bool {
		return strings.Contains(s, "! 修正登入") && strings.Contains(s, "! write tests")
	})
	t.Logf("tab appears → screen: %v", d)

	// A second TUI in the same directory: not duplicated; a TUI in another directory forms its own group, sorted by full path
	e.tui(api, "-c")
	web := filepath.Join(e.dir, "work", "web")
	webWin := e.tui(web)
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入", "write tests"}, web: {"landing"}, filepath.Join(e.dir, "work", "old"): {"歷史"}}))
	e.eventually(time.Second, "two directories", func(s string) bool { return strings.Contains(s, "landing") })
	s := e.sidebar()
	if strings.Count(s, "修正登入") != 1 || strings.Contains(s, "歷史") {
		t.Fatalf("duplicate in the same directory or history directory listed:\n%s", s)
	}
	if strings.Index(s, "api") > strings.Index(s, "web") {
		t.Fatalf("directories not sorted by full path:\n%s", s)
	}

	// Close web's TUI window (a tmux hook notifies the daemon)
	e.inTmux("kill-window", "-t", webWin)
	e.eventually(time.Second, "closed TUI disappears within 1 second", func(s string) bool { return !strings.Contains(s, "landing") })

}

// Missed notification: the TUI process is forcibly killed but its pane (shell) remains, so tmux emits no hook;
// only the low-frequency catch-up can correct this within 10 seconds.
func TestMissedNotificationIsCaughtByBackstopWithin10Seconds(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	e.tuiUnderShell(dir)
	e.eventually(10*time.Second, "TUI started inside a shell (no hook) appears within 10 seconds", func(s string) bool { return strings.Contains(s, "! a") })
	must(t, exec.Command("pkill", "-KILL", "-f", e.bin).Run())
	d := e.eventually(10*time.Second, "missed notification corrected within 10 seconds", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })
	if d < time.Second {
		t.Fatalf("updated after %v, so this is not a missed-notification scenario", d)
	}
	t.Logf("missed notification → catch-up: %v", d)
}

func TestStoppingDaemonRemovesItsHooksOnly(t *testing.T) {
	e := newEnv(t)
	e.inTmux("set-hook", "-g", "pane-exited[3]", "display-message user-hook")
	time.Sleep(500 * time.Millisecond)
	if !strings.Contains(e.inTmux("show-hooks", "-gw"), "wait-for -S tos_wake") {
		t.Fatal("the daemon should have wake-up hooks while running")
	}
	must(t, e.daemon.Process.Signal(syscall.SIGTERM))
	_ = e.daemon.Wait()
	hooks := e.inTmux("show-hooks", "-gw")
	if strings.Contains(hooks, "tos_wake") || !strings.Contains(hooks, "user-hook") {
		t.Fatalf("after exit, only the plugin's own hooks should be removed:\n%s", hooks)
	}
}

func TestNonTUIOpencodeCommandsAreNotListed(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "svc")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"x"}}))
	e.tui(dir, "serve")
	e.tui(dir, "session", "list")
	time.Sleep(1500 * time.Millisecond)
	if s := e.sidebar(); !strings.Contains(s, "No OpenCode TUI running") || strings.Contains(s, "svc") {
		t.Fatalf("non-TUI listed:\n%s", s)
	}
}

func TestIncompatibleTabsFileShowsNoticeAndKeepsLastList(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"keep me"}}))
	e.tui(dir)
	e.eventually(2*time.Second, "initial display", func(s string) bool { return strings.Contains(s, "keep me") })
	e.writeTabs(`{"version":9}`)
	e.eventually(2*time.Second, "shows a notice and keeps the list when incompatible", func(s string) bool {
		return strings.Contains(s, "Cannot read tab list") && strings.Contains(s, "keep me") && !strings.Contains(s, "No tabs")
	})
}

func TestUserTmuxOptionsAndBindingsAreUntouched(t *testing.T) {
	e := newEnv(t)
	e.inTmux("set", "-g", "@oc_side", "user-value")
	e.inTmux("set", "-g", "@oc_side_bak", "user-bak")
	before := keysWithoutJump(e.inTmux("list-keys"))
	dir := filepath.Join(e.dir, "work", "api")
	e.tui(dir)
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	e.eventually(2*time.Second, "has content", func(s string) bool { return strings.Contains(s, "a") && strings.Contains(s, "api") })
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "@oc_side")); got != "user-value" {
		t.Fatalf("@oc_side was changed: %q", got)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "@oc_side_bak")); got != "user-bak" {
		t.Fatalf("@oc_side_bak was changed: %q", got)
	}
	if after := keysWithoutJump(e.inTmux("list-keys")); after != before {
		t.Fatal("keyboard bindings were changed other than MouseDown1SideStatus")
	}
	if strings.Contains(e.inTmux("show-options", "-g"), "@sb_") {
		t.Fatal("the old POC prefix @sb_ must not be used")
	}
}

func TestDaemonRefusesToRunWithoutExplicitSocket(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "d")
	if b, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	if err := exec.Command(bin).Run(); err == nil {
		t.Fatal("must not run without a socket (to avoid using the default tmux server by mistake)")
	}
}

// A TUI in another tmux server (different socket) must not appear in this server's sidebar.
func TestOtherTmuxServerPanesAreNotMixedIn(t *testing.T) {
	e := newEnv(t)
	other := e.in + "-other"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", other, "-f", "/dev/null", "kill-server").Run() })
	otherDir := filepath.Join(e.dir, "work", "elsewhere")
	mine := filepath.Join(e.dir, "work", "mine")
	must(t, os.MkdirAll(otherDir, 0o755))
	if b, err := exec.Command("tmux", "-L", other, "-f", "/dev/null", "new-session", "-d", "-s", "o", "-c", otherDir, "exec "+e.bin).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	e.writeTabs(e.tabsJSON(map[string][]string{otherDir: {"foreign"}, mine: {"local"}}))
	e.tui(mine)
	e.eventually(2*time.Second, "this server's TUI", func(s string) bool { return strings.Contains(s, "local") })
	if s := e.sidebar(); strings.Contains(s, "foreign") || strings.Contains(s, "elsewhere") {
		t.Fatalf("data from another server mixed in:\n%s", s)
	}
}

// Full process: the real daemon + isolated tmux + a fake OpenCode service (httptest + SSE).
// Observe one session's running / finished / read lifecycle from the attached client's actual screen.
func TestSessionLifecycleOnARealTmuxWithFakeOpenCodeService(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	svc := fakeoc.New(t)
	svc.AddSession("ses_0", "")
	svc.AddSession("ses_1", "")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"修正登入", "write tests"}}))
	e.tui(dir, "--server", "http://127.0.0.1:1")

	e.eventually(3*time.Second, "service not up yet: keeps ! with a notice, not idle", func(s string) bool {
		return strings.Contains(s, "! 修正登入") && strings.Contains(s, "Cannot connect to OpenCode")
	})
	svc.WriteServiceFile(e.servicePath)
	e.eventually(5*time.Second, "connects automatically once the service appears", func(s string) bool {
		return strings.Contains(s, "· 修正登入") && strings.Contains(s, "· write tests") && !strings.Contains(s, "Cannot connect")
	})

	svc.Start("ses_0")
	d := e.eventually(time.Second, "running appears within 1 second", func(s string) bool {
		return strings.Contains(s, "● 修正登入") && strings.Contains(s, " OPENCODE ") && strings.Contains(s, "●1")
	})
	t.Logf("running → screen: %v", d)
	svc.Finish("ses_0", "failed")
	svc.Start("ses_1")
	svc.Finish("ses_1", "interrupted")
	d = e.eventually(time.Second, "failed / interrupted appears within 1 second", func(s string) bool {
		return strings.Contains(s, "✗ 修正登入") && strings.Contains(s, "◆ write tests")
	})
	t.Logf("finished unread → screen: %v", d)
	t.Logf("screen:\n%s", e.sidebar())
	svc.View("ses_0")
	svc.View("ses_1")
	d = e.eventually(time.Second, "returns to idle after another client marks it read", func(s string) bool {
		return strings.Contains(s, "· 修正登入") && strings.Contains(s, "· write tests")
	})
	t.Logf("read → idle: %v", d)

	// Service interruption: remove the discovery file and disconnect, which turns tabs to !; they recover automatically when the service returns
	_ = os.Remove(e.servicePath)
	svc.Drop()
	e.eventually(3*time.Second, "! after disconnect", func(s string) bool {
		return strings.Contains(s, "! 修正登入") && strings.Contains(s, "Cannot connect to OpenCode")
	})
	svc.WriteServiceFile(e.servicePath)
	e.eventually(5*time.Second, "automatic recovery", func(s string) bool { return strings.Contains(s, "· 修正登入") })

	if w := svc.Writes(); len(w) != 0 {
		t.Fatalf("write request sent to OpenCode: %v", w)
	}
	for _, r := range svc.Requests() {
		if strings.Contains(r, "/view") || strings.Contains(r, "/reply") {
			t.Fatalf("mark-read / reply was called: %s", r)
		}
	}
}

// Waiting for approval / waiting for an answer: multiple requests, interleaved handling, permission priority, question cancellation; only GETs throughout,
// and no tmux bindings are added or changed beyond what the plugin owns.
func TestPendingApprovalsAndAnswersOnARealTmuxWithFakeOpenCodeService(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	svc := fakeoc.New(t)
	for _, id := range []string{"ses_0", "ses_1", "ses_2"} {
		svc.AddSession(id, "")
	}
	svc.SeedPermission("per_old", "ses_0") // existed before the sidebar started
	svc.WriteServiceFile(e.servicePath)
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"approve me", "answer me", "calm"}}))
	bindingsBefore := e.outTmux("list-keys")
	e.tui(dir, "--server", "http://127.0.0.1:1")

	d := e.eventually(5*time.Second, "pending approval that existed before startup", func(s string) bool {
		return strings.Contains(s, "? approve me") && strings.Contains(s, "· answer me") && strings.Contains(s, "?1")
	})
	t.Logf("initial pending approval → screen: %v", d)

	svc.AskForm("frm_1", "ses_1")
	svc.AskForm("frm_2", "ses_1")
	svc.AskPermission("per_new", "ses_0")
	e.eventually(time.Second, "multiple requests: each tab counted once", func(s string) bool {
		return strings.Contains(s, "? approve me") && strings.Contains(s, "? answer me") && strings.Contains(s, "?2") && !strings.Contains(s, "?4")
	})
	svc.ReplyPermission("per_old") // interleaved: ses_0 still has per_new
	svc.AnswerForm("frm_1")        // ses_1 still has frm_2
	e.eventually(time.Second, "still pending after interleaved handling", func(s string) bool {
		return strings.Contains(s, "? approve me") && strings.Contains(s, "? answer me")
	})
	svc.ReplyPermission("per_new")
	svc.CancelForm("frm_2")
	e.eventually(time.Second, "returns to idle after requests complete / are cancelled", func(s string) bool {
		return strings.Contains(s, "· approve me") && strings.Contains(s, "· answer me") && !strings.Contains(s, "?")
	})

	// Permission takes priority over questions and running; after a reply, do not guess idle: if still running, return to running
	svc.Start("ses_2")
	svc.AskForm("frm_3", "ses_2")
	svc.AskPermission("per_3", "ses_2")
	e.eventually(time.Second, "pending overrides running", func(s string) bool { return strings.Contains(s, "? calm") && !strings.Contains(s, "●") })
	svc.ReplyPermission("per_3")
	svc.CancelForm("frm_3")
	e.eventually(time.Second, "returns to running", func(s string) bool { return strings.Contains(s, "● calm") })

	// Color: yellow ?, with no background block
	svc.AskPermission("per_4", "ses_2")
	e.eventually(time.Second, "waiting for approval", func(s string) bool { return strings.Contains(s, "? calm") })
	var own []string
	for _, l := range strings.Split(e.inTmux("show-options", "-g"), "\n") {
		if strings.HasPrefix(l, "@tos_layout_") {
			own = append(own, l)
		}
	}
	opts := strings.Join(own, "\n")
	if opts == "" {
		t.Fatalf("sidebar output option not found:\n%s", opts)
	}
	noBlocks := strings.ReplaceAll(opts, "#[bg=#A78BFA,fg=#18181B] OPENCODE ", "")
	noBlocks = regexp.MustCompile(`#\[bg=#A78BFA,fg=#18181B\] [a-z] `).ReplaceAllString(noBlocks, "") // jump label
	if strings.Contains(noBlocks, "bg=") || strings.Contains(opts, "fg=#FACC15]!") {
		t.Fatalf("pending must not use background blocks other than the title or a yellow !:\n%s", opts)
	}
	if !strings.Contains(opts, "#[fg=#FACC15]?#[default]") {
		t.Fatalf("yellow ? not found in sidebar output:\n%s", opts)
	}

	if w := svc.Writes(); len(w) != 0 {
		t.Fatalf("write request sent to OpenCode: %v", w)
	}
	for _, r := range svc.Requests() {
		if strings.Contains(r, "reply") || strings.Contains(r, "/view") {
			t.Fatalf("reply / mark-read was called: %s", r)
		}
	}
	if after := e.outTmux("list-keys"); after != bindingsBefore {
		t.Fatal("must not add or change any tmux binding")
	}
}

// Multi-level subagent family: grandchild running and pending states roll up to the tab; the main session finishes first; descendants that existed before startup;
// unrelated families are isolated; a descendant failure does not taint the tab. Only GETs throughout, and no tmux bindings are added or changed beyond what the plugin owns.
func TestSessionFamilyRollsUpToTabsOnARealTmuxWithFakeOpenCodeService(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	svc := fakeoc.New(t)
	// A multi-level family that existed before the sidebar started (with no session-created events)
	svc.Seed("ses_R", "", "", 0, 0, false)
	svc.Seed("ses_C", "ses_R", "", 0, 0, false)
	svc.Seed("ses_G", "ses_C", "", 0, 0, false)
	svc.Seed("ses_Q", "", "", 0, 0, false)
	svc.Seed("ses_Q1", "ses_Q", "", 0, 0, false)
	svc.Seed("ses_X", "", "", 0, 0, false) // unrelated family
	svc.Seed("ses_X1", "ses_X", "", 0, 0, false)
	svc.SeedPermission("per_old", "ses_G") // grandchild's pending approval, not visible from the tab itself
	svc.WriteServiceFile(e.servicePath)
	e.writeTabs(`{"global":{"tabs":[],"unread":{}},"cwd":{"` + dir + `":{"tabs":[` +
		`{"sessionID":"ses_R","title":"root job"},{"sessionID":"ses_X","title":"other family"},{"sessionID":"ses_Q","title":"calm"}],"unread":{}}}}`)
	bindingsBefore := e.outTmux("list-keys")
	e.tui(dir, "--server", "http://127.0.0.1:1")

	d := e.eventually(5*time.Second, "grandchild pending approval that existed before startup", func(s string) bool { return strings.Contains(s, "? root job") })
	t.Logf("existing grandchild blocked → screen: %v", d)
	t.Logf("screen:\n%s", e.sidebar())
	svc.ReplyPermission("per_old")
	e.eventually(time.Second, "returns to idle after handling; descendants are not listed separately", func(s string) bool {
		return strings.Contains(s, "· root job") && !strings.Contains(s, "ses_")
	})

	svc.AddSession("ses_G2", "ses_G") // a fourth level added after startup
	svc.Start("ses_R")
	svc.Start("ses_G2")
	e.eventually(time.Second, "family running", func(s string) bool { return strings.Contains(s, "● root job") })
	svc.Finish("ses_R", "succeeded") // the main session finishes first while descendants are still running
	time.Sleep(300 * time.Millisecond)
	d = e.eventually(time.Second, "still ● after the main session finishes first", func(s string) bool { return strings.Contains(s, "● root job") })
	t.Logf("main finished first → screen: %v", d)
	svc.AskForm("frm_1", "ses_G2")
	e.eventually(time.Second, "grandchild waiting for an answer takes priority over running", func(s string) bool { return strings.Contains(s, "? root job") && !strings.Contains(s, "● root job") })
	e.eventually(time.Second, "unrelated families and other tabs are unaffected", func(s string) bool {
		return strings.Contains(s, "· other family") && strings.Contains(s, "· calm")
	})
	svc.AnswerForm("frm_1")
	svc.Finish("ses_G2", "failed") // a descendant failure is not a tab failure
	d = e.eventually(time.Second, "family finished: only the tab's own finish is unread", func(s string) bool {
		return strings.Contains(s, "◆ root job") && !strings.Contains(s, "✗")
	})
	t.Logf("family finished → screen: %v", d)
	svc.Start("ses_R") // parent woken up
	e.eventually(time.Second, "parent woken up", func(s string) bool { return strings.Contains(s, "● root job") })

	if w := svc.Writes(); len(w) != 0 {
		t.Fatalf("write request sent to OpenCode: %v", w)
	}
	for _, r := range svc.Requests() {
		if strings.Contains(r, "reply") || strings.Contains(r, "/view") {
			t.Fatalf("reply / mark-read was called: %s", r)
		}
	}
	if after := e.outTmux("list-keys"); after != bindingsBefore {
		t.Fatal("must not add or change any tmux binding")
	}
}

// ---- #8 cwd / global mode and configuration reload ----

const modeOpt = "@opencode-status-mode"

// bothScopesJSON: the global list contains sessions that are not under any TUI directory; the cwd list holds a separate copy.
func (e *env) bothScopesJSON(dirs map[string][]string, global []string) string {
	var gtabs []map[string]string
	for i, ti := range global {
		gtabs = append(gtabs, map[string]string{"sessionID": fmt.Sprintf("ses_g%d", i), "title": ti})
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(e.tabsJSON(dirs)), &m)
	m["global"] = map[string]any{"tabs": gtabs, "unread": map[string]any{}}
	b, _ := json.Marshal(m)
	return string(b)
}

// reload simulates `tmux source-file ~/.tmux.conf` reloading a configuration file that contains the mode option.
func (e *env) reload(conf string) {
	e.t.Helper()
	path := filepath.Join(e.dir, "reload.conf")
	must(e.t, os.WriteFile(path, []byte(conf), 0o644))
	e.inTmux("source-file", path)
}

// daemonCount counts daemon instances pointing at the observed server (pgrep does not count itself).
func (e *env) daemonCount() int {
	out, _ := exec.Command("pgrep", "-f", "tmux-opencode-status -L "+e.in+" ").Output()
	return len(strings.Fields(string(out)))
}

func TestReloadingTmuxConfigSwitchesBetweenCwdAndGlobalWithoutRestart(t *testing.T) {
	e := newEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	web := filepath.Join(e.dir, "work", "web")
	e.writeTabs(e.bothScopesJSON(map[string][]string{api: {"api tab"}, web: {"web tab"}}, []string{"global one", "其他目錄的工作"}))
	e.tui(api)
	e.tui(web, "-c")

	e.eventually(3*time.Second, "default cwd", func(s string) bool {
		return strings.Contains(s, "api tab") && strings.Contains(s, "web tab") && !strings.Contains(s, "GLOBAL")
	})
	pid := e.daemon.Process.Pid

	e.reload("set -g " + modeOpt + " global\n")
	d := e.eventually(time.Second, "switches to global within 1 second after reload", func(s string) bool {
		return strings.Contains(s, "GLOBAL") && strings.Contains(s, "其他目錄的工作") && strings.Count(s, "global one") == 1 &&
			!strings.Contains(s, "api tab") && !strings.Contains(s, "web tab")
	})
	t.Logf("cwd → global: %v", d)

	e.reload("set -g " + modeOpt + " cwd\n")
	e.eventually(time.Second, "switches back to cwd after reload, leaving no global remnants", func(s string) bool {
		return strings.Contains(s, "api tab") && strings.Contains(s, "web tab") && !strings.Contains(s, "GLOBAL") && !strings.Contains(s, "global one")
	})

	e.reload("set -gu " + modeOpt + "\nset -g " + modeOpt + " global\nset -gu " + modeOpt + "\n")
	e.eventually(time.Second, "removing the setting returns to the default cwd", func(s string) bool { return strings.Contains(s, "api tab") && !strings.Contains(s, "GLOBAL") })

	if e.daemon.Process.Pid != pid || e.daemonCount() != 1 {
		t.Fatalf("switching modes must not create a second instance (pid %d → %d, instance count %d)", pid, e.daemon.Process.Pid, e.daemonCount())
	}
}

func TestGlobalModeMultipleTUIsAndLastClosedOnARealTmux(t *testing.T) {
	e := newEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	web := filepath.Join(e.dir, "work", "web")
	e.reload("set -g " + modeOpt + " global\n")
	e.writeTabs(e.bothScopesJSON(map[string][]string{api: {"api tab"}}, []string{"shared a", "shared b"}))
	w1 := e.tui(api)
	w2 := e.tui(api, "-c")
	w3 := e.tui(web)
	e.eventually(2*time.Second, "multiple TUIs list only one shared list", func(s string) bool { return strings.Contains(s, "shared a") })
	s := e.sidebar()
	if strings.Count(s, "shared a") != 1 || strings.Count(s, "GLOBAL") != 1 || strings.Contains(s, "api tab") || strings.Contains(s, "web") {
		t.Fatalf("global list duplicated or mixed with cwd:\n%s", s)
	}
	e.inTmux("kill-window", "-t", w1)
	e.inTmux("kill-window", "-t", w2)
	time.Sleep(700 * time.Millisecond)
	if s := e.sidebar(); !strings.Contains(s, "shared a") {
		t.Fatalf("the shared list should be kept while a TUI remains:\n%s", s)
	}
	e.inTmux("kill-window", "-t", w3)
	e.eventually(time.Second, "empty screen after the last TUI closes", func(s string) bool {
		return strings.Contains(s, "No OpenCode TUI running") && !strings.Contains(s, "shared a") && !strings.Contains(s, "GLOBAL")
	})
}

func TestInvalidModeNeverStartsTrackingThenReloadKeepsValidMode(t *testing.T) {
	// Invalid at first: do not start tracking
	e := newEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.inTmux("set", "-g", modeOpt, "globl")
	e.writeTabs(e.bothScopesJSON(map[string][]string{api: {"api tab"}}, []string{"shared a"}))
	e.tui(api)
	e.eventually(2*time.Second, "first invalid value shows a notice and is not tracked", func(s string) bool {
		return strings.Contains(s, "▲ Invalid mode; not tracking\n")
	})
	if s := e.sidebar(); strings.Contains(s, "api tab") || strings.Contains(s, "shared a") || strings.Contains(s, "No OpenCode TUI running") {
		t.Fatalf("a first invalid setting must not start tracking:\n%s", s)
	}
	// Start tracking after it is fixed
	e.reload("set -g " + modeOpt + " global\n")
	e.eventually(time.Second, "starts tracking after changing to a valid value", func(s string) bool { return strings.Contains(s, "shared a") })
	// Invalid on reload: keep global and show that it was not applied
	e.reload("set -g " + modeOpt + " bogus\n")
	e.eventually(time.Second, "invalid value on reload keeps the original mode and reports it", func(s string) bool {
		return strings.Contains(s, "▲ Invalid mode; using global\n") && strings.Contains(s, "shared a") && !strings.Contains(s, "api tab")
	})
	e.reload("set -g " + modeOpt + " cwd\n")
	e.eventually(time.Second, "applied after the fix and the notice disappears", func(s string) bool { return strings.Contains(s, "api tab") && !strings.Contains(s, "Invalid mode") })
}

func TestModeSwitchDoesNotForceAHiddenSidebarOpen(t *testing.T) {
	e := newEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.bothScopesJSON(map[string][]string{api: {"api tab"}}, []string{"shared a"}))
	e.tui(api)
	e.eventually(3*time.Second, "displayed", func(s string) bool { return strings.Contains(s, "api tab") })
	e.inTmux("set", "-g", "side-status", "off")
	e.reload("set -g " + modeOpt + " global\n")
	time.Sleep(1200 * time.Millisecond)
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "side-status")); got != "off" {
		t.Fatalf("switching modes must not reopen the sidebar, side-status = %q", got)
	}
}

func TestModeOptionIsPluginPrefixedAndOtherOptionsStayUntouched(t *testing.T) {
	e := newEnv(t)
	e.inTmux("set", "-g", "@oc_side", "user-value")
	e.reload("set -g " + modeOpt + " global\n")
	time.Sleep(600 * time.Millisecond)
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "@oc_side")); got != "user-value" {
		t.Fatalf("@oc_side was changed: %q", got)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-gv", modeOpt)); got != "global" {
		t.Fatalf("the plugin must not rewrite the user's mode option: %q", got)
	}
}

func TestStoppingDaemonAlsoRemovesTheModeHook(t *testing.T) {
	e := newEnv(t)
	e.inTmux("set-hook", "-g", "after-set-option[3]", "display-message user-hook")
	time.Sleep(500 * time.Millisecond)
	must(t, e.daemon.Process.Signal(syscall.SIGTERM))
	_ = e.daemon.Wait()
	hooks := e.inTmux("show-hooks", "-g")
	if strings.Contains(hooks, "tos_wake") || !strings.Contains(hooks, "user-hook") {
		t.Fatalf("after exit, only the plugin's own hooks should be removed:\n%s", hooks)
	}
}

// Startup gap: a configuration change made before the hooks are installed must not be discovered only by the low-frequency catch-up (5 seconds).
// A tmux wrapper delays installing the after-set-option hook (shared by the mode and palette options; the last one installed) by 300ms, enlarging the gap deterministically without depending on -race timing;
// a configuration reload after the daemon first reads settings but before the hook is installed must still be applied within 1 second.
func TestModeChangeDuringDaemonStartupIsNotLeftToTheBackstop(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not found")
	}
	shim := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in *\" after-set-option[\"*) sleep 0.3;; esac\nexec " + realTmux + " \"$@\"\n"
	must(t, os.WriteFile(filepath.Join(shim, "tmux"), []byte(script), 0o755))

	e := newEnvWith(t, []string{"PATH=" + shim + ":" + os.Getenv("PATH")})
	api := filepath.Join(e.dir, "work", "api")
	e.inTmux("set", "-g", modeOpt, "globl")
	e.writeTabs(e.bothScopesJSON(map[string][]string{api: {"api tab"}}, []string{"shared a"}))
	e.tui(api)
	e.eventually(5*time.Second, "daemon first reads the settings", func(s string) bool { return strings.Contains(s, "Invalid mode") })
	e.reload("set -g " + modeOpt + " global\n")
	d := e.eventually(time.Second, "reload within the startup gap is applied within 1 second", func(s string) bool { return strings.Contains(s, "shared a") })
	t.Logf("reload → screen: %v", d)
}

// ---- #4 addition: pane title changes as an early-rescan wake-up signal ----

// shellWindow opens an interactive shell window (with job control) and returns the window id; commands are then run in it with shellRun.
// Uses a clean sh -i so delays from the user's zsh configuration (prompt, preexec) do not pollute timing measurements.
func (e *env) shellWindow(dir string) string {
	must(e.t, os.MkdirAll(dir, 0o755))
	win := strings.TrimSpace(e.inTmux("new-window", "-d", "-P", "-F", "#{window_id}", "-c", dir, "env PS1='$ ' sh -i"))
	time.Sleep(400 * time.Millisecond) // wait for the shell to be ready
	return win
}

func (e *env) shellRun(win, cmdline string) { e.inTmux("send-keys", "-t", win, cmdline, "Enter") }

// Starting / exiting a TUI in an existing shell: tmux emits no structural event, but the TUI changes the terminal title → reflected within 1 second.
func TestTUIStartedAndStoppedInAnExistingShellFollowsWithin1SecondWhenTitlesAreOn(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	e.eventually(3*time.Second, "startup", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })
	win := e.shellWindow(dir)

	e.shellRun(win, "FAKE_TITLE=1 "+e.bin)
	d := e.eventually(time.Second, "TUI started in a shell appears within 1 second", func(s string) bool { return strings.Contains(s, "! a") })
	t.Logf("started in a shell → screen: %v", d)

	e.inTmux("send-keys", "-t", win, "C-c")
	d = e.eventually(time.Second, "TUI exited in a shell disappears within 1 second", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })
	t.Logf("exited in a shell → screen: %v", d)
}

// Terminal titles disabled (the process does not change the title): no wake-up, and the low-frequency catch-up still reflects it correctly within 10 seconds.
func TestTUIInAnExistingShellStillFollowsViaBackstopWhenTitlesAreOff(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	e.eventually(3*time.Second, "startup", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })
	win := e.shellWindow(dir)

	e.shellRun(win, e.bin)
	e.eventually(10*time.Second, "start with titles disabled appears within 10 seconds", func(s string) bool { return strings.Contains(s, "! a") })
	e.inTmux("send-keys", "-t", win, "C-c")
	e.eventually(10*time.Second, "exit with titles disabled disappears within 10 seconds", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })
}

// Identification is based on the foreground process: a non-TUI opencode command changing the title, or any process setting the title to OpenCode, does not count as a TUI.
func TestTitleChangesNeverMakeANonTUIProcessLookLikeATUI(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "svc")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"x"}}))
	win := e.shellWindow(dir)
	e.shellRun(win, "FAKE_TITLE=1 "+e.bin+" serve")
	win2 := e.shellWindow(dir)
	e.shellRun(win2, `printf '\033]0;OpenCode\007'; sleep 60`)
	time.Sleep(1500 * time.Millisecond)
	tabRow := regexp.MustCompile(`(?m)^[ \t]*[●?◆✗!·] x[ \t]*$`)
	if s := e.sidebar(); !strings.Contains(s, "No OpenCode TUI running") || strings.Contains(s, "svc") || tabRow.MatchString(s) {
		t.Fatalf("non-TUI listed because of its title:\n%s", s)
	}
}

// tmuxLog uses a tmux wrapper at the front of PATH to record every tmux command the daemon issues.
func tmuxLogShim(t *testing.T) (path, logFile string) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not found")
	}
	shim := t.TempDir()
	logFile = filepath.Join(shim, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> " + logFile + "\nexec " + realTmux + " \"$@\"\n"
	must(t, os.WriteFile(filepath.Join(shim, "tmux"), []byte(script), 0o755))
	return shim + ":" + os.Getenv("PATH"), logFile
}

func countCalls(t *testing.T, logFile, sub string) int {
	b, _ := os.ReadFile(logFile)
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// A TUI that changes its title constantly and frequently (every 20ms): must not turn into a wake-up storm (the number of scans is capped),
// and must not write to tmux because of title changes (no write when the content is unchanged).
func TestFrequentTitleChangesDoNotCauseAWakeStormOrTmuxWrites(t *testing.T) {
	path, logFile := tmuxLogShim(t)
	e := newEnvWith(t, []string{"PATH=" + path})
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	win := e.shellWindow(dir)
	e.shellRun(win, "FAKE_TITLE=1 FAKE_CHURN_MS=20 "+e.bin)
	e.eventually(2*time.Second, "TUI appears", func(s string) bool { return strings.Contains(s, "! a") })

	time.Sleep(500 * time.Millisecond)
	scans0, writes0 := countCalls(t, logFile, "list-panes"), countCalls(t, logFile, "source-file")
	time.Sleep(3 * time.Second)
	scans, writes := countCalls(t, logFile, "list-panes")-scans0, countCalls(t, logFile, "source-file")-writes0
	t.Logf("%d scans and %d writes in 3 seconds (the title changes about every 20ms)", scans, writes)
	if scans > 14 {
		t.Fatalf("wake-up storm: %d scans in 3 seconds", scans)
	}
	if writes != 0 {
		t.Fatalf("wrote to tmux %d times although the content did not change", writes)
	}
	if s := e.sidebar(); !strings.Contains(s, "! a") {
		t.Fatalf("the screen must still be correct after throttling:\n%s", s)
	}
}

// Silent disconnects, service restarts, and discovery info changes: all injected into the fake service; the daemon recovers without being reloaded,
// and never calls any write endpoint. The product default of 45 seconds is shortened by a flag to avoid really waiting.
func TestSilentDisconnectAndServiceRestartRecoverOnARealTmuxWithFakeOpenCodeService(t *testing.T) {
	e := newEnvFull(t, nil, []string{"-idle-timeout", "600ms"})
	dir := filepath.Join(e.dir, "work", "api")
	svc := fakeoc.New(t)
	svc.SetHeartbeat(100 * time.Millisecond)
	svc.AddSession("ses_0", "")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"修正登入"}}))
	e.tui(dir, "--server", "http://127.0.0.1:1")
	svc.WriteServiceFile(e.servicePath)
	e.eventually(5*time.Second, "connects to the service", func(s string) bool { return strings.Contains(s, "· 修正登入") })
	svc.Start("ses_0")
	e.eventually(time.Second, "running", func(s string) bool { return strings.Contains(s, "● 修正登入") })

	// Silent disconnect: the connection gets no FIN and no more heartbeats; the finish event is lost
	svc.Stall()
	svc.Finish("ses_0", "succeeded")
	d := e.eventually(8*time.Second, "reconnects automatically and re-reads everything after the silence timeout (600ms) + retry interval (default 3s)", func(s string) bool { return strings.Contains(s, "◆ 修正登入") })
	t.Logf("silent disconnect → recovered: %v (timeout set to 600ms)", d)

	// Service restart: the old service disappears and a new service appears (different port, pending items gone, different data)
	next := fakeoc.New(t)
	next.SetHeartbeat(100 * time.Millisecond)
	next.Seed("ses_0", "", "failed", 900, 0, false)
	svc.Drop()
	_ = os.Remove(e.servicePath)
	e.eventually(3*time.Second, "! during the restart gap", func(s string) bool {
		return strings.Contains(s, "! 修正登入") && strings.Contains(strings.Join(strings.Fields(s), ""), "CannotconnecttoOpenCode")
	})
	next.WriteServiceFile(e.servicePath)
	e.eventually(5*time.Second, "rediscovers and syncs", func(s string) bool { return strings.Contains(s, "✗ 修正登入") })

	for _, x := range []*fakeoc.Server{svc, next} {
		if w := x.Writes(); len(w) != 0 {
			t.Fatalf("write request sent to OpenCode: %v", w)
		}
	}
}
