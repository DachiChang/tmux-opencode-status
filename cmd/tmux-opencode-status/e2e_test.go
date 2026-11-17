package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 程序層級的外部行為測試：真實常駐程式 + 隔離的 tmux server（-L <socket> -f /dev/null）+
// 假的 `opencode` 執行檔 + 受控的 tabs.json；從 attached client 的實際畫面觀察結果。
// 使用者的預設 tmux server 與 OpenCode 資料完全不涉及。

type env struct {
	t         *testing.T
	dir       string
	in, out   string // 被觀察的 server 與承載 client 的外層 server
	daemon    *exec.Cmd
	tabsPath  string
	binDir    string
	bin       string
	daemonEnv []string
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

// fakeOpencodeSrc 是假 opencode：預設只睡眠。FAKE_TITLE 非空時像真的 TUI 一樣在啟動時設定終端標題，
// 收到 SIGINT／SIGTERM 時還原標題再結束；FAKE_CHURN_MS 讓它持續頻繁改標題。
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

// newEnvWith 的 daemonEnv 附加到常駐程式的環境變數（用來在 PATH 前面放 tmux 包裝，製造確定性的慢啟動）。
func newEnvWith(t *testing.T, daemonEnv []string) *env {
	t.Helper()
	if testing.Short() {
		t.Skip("-short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("沒有 tmux")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	e := &env{t: t, dir: root, in: fmt.Sprintf("tos-e2e-in-%d", os.Getpid()), out: fmt.Sprintf("tos-e2e-out-%d", os.Getpid())}
	e.tabsPath = filepath.Join(root, "state", "tabs.json")
	e.binDir = filepath.Join(root, "bin")
	must(t, os.MkdirAll(filepath.Dir(e.tabsPath), 0o755))
	must(t, os.MkdirAll(e.binDir, 0o755))

	// 假 opencode：只是睡眠的行程，argv[0] 的 basename 為 opencode。
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
	e.writeTabs(`{"global":{"tabs":[],"unread":{}},"cwd":{}}`)

	e.inTmux("new-session", "-d", "-s", "main", "-x", "100", "-y", "30", "-c", root)
	// 承載 client：外層 server 的 pane 裡 attach 到被觀察的 server，尺寸固定。
	e.outTmux("new-session", "-d", "-s", "hold", "-x", "100", "-y", "30",
		fmt.Sprintf("tmux -L %s -f /dev/null attach -t main", e.in))
	t.Cleanup(func() {
		if e.daemon != nil && e.daemon.Process != nil {
			_ = e.daemon.Process.Kill()
			_ = e.daemon.Wait()
		}
		_ = exec.Command("tmux", "-L", e.in, "-f", "/dev/null", "kill-server").Run()
		_ = exec.Command("tmux", "-L", e.out, "-f", "/dev/null", "kill-server").Run()
	})
	e.daemonEnv = daemonEnv
	e.startDaemon(daemonBin)
	return e
}

func (e *env) startDaemon(bin string) {
	e.daemon = exec.Command(bin, "-L", e.in, "-f", "/dev/null", "-tabs", e.tabsPath, "-show", "-backstop", "5s")
	e.daemon.Stderr = os.Stderr
	if e.daemonEnv != nil {
		e.daemon.Env = append(os.Environ(), e.daemonEnv...)
	}
	must(e.t, e.daemon.Start())
}

func (e *env) writeTabs(raw string) {
	tmp := e.tabsPath + ".tmp"
	must(e.t, os.WriteFile(tmp, []byte(raw), 0o644))
	must(e.t, os.Rename(tmp, e.tabsPath)) // OpenCode 以 tmp + rename 更新
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

// tui 在新 window 啟動假 opencode，cwd 為 dir。
func (e *env) tui(dir string, extra ...string) string {
	return e.tuiCmd("exec", dir, extra...)
}

// tuiUnderShell 在互動 shell（有 job control）中啟動 TUI：殺掉它時 pane 仍存在，tmux 不會發出任何 hook。
func (e *env) tuiUnderShell(dir string) string {
	must(e.t, os.MkdirAll(dir, 0o755))
	win := strings.TrimSpace(e.inTmux("new-window", "-d", "-P", "-F", "#{window_id}", "-c", dir))
	time.Sleep(400 * time.Millisecond) // 等 shell 就緒
	e.inTmux("send-keys", "-t", win, e.bin, "Enter")
	return win
}

func (e *env) tuiCmd(prefix, dir string, extra ...string) string {
	must(e.t, os.MkdirAll(dir, 0o755))
	return strings.TrimSpace(e.inTmux("new-window", "-d", "-P", "-F", "#{window_id}", "-c", dir, strings.TrimSpace(prefix+" "+strings.Join(append([]string{e.bin}, extra...), " "))))
}

// sidebar 是 client 實際畫面左側 36 格（含分隔線）。
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
	e.t.Fatalf("%s：%v 內未達成，目前畫面：\n%s", what, within, last)
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

	e.eventually(3*time.Second, "啟動後顯示沒有 TUI", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })

	// TUI 出現：保留群組並顯示「無 tab」（尚無任何 tab 資料）
	e.tui(api, "--server", "http://127.0.0.1:1")
	d := e.eventually(time.Second, "新增 TUI 於 1 秒內出現", func(s string) bool {
		return strings.Contains(s, "api") && strings.Contains(s, "無 tab")
	})
	t.Logf("TUI 出現 → 畫面：%v", d)

	// tab 出現（tabs.json rename 更新），狀態未接上，顯示灰色 ! 的位置符號
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入", "write tests"}}))
	d = e.eventually(time.Second, "新增 tab 於 1 秒內出現", func(s string) bool {
		return strings.Contains(s, "! 修正登入") && strings.Contains(s, "! write tests")
	})
	t.Logf("tab 出現 → 畫面：%v", d)

	// 第二個 TUI 在同目錄：不重複；另一目錄的 TUI 另成群組並依完整路徑排序
	e.tui(api, "-c")
	web := filepath.Join(e.dir, "work", "web")
	webWin := e.tui(web)
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入", "write tests"}, web: {"landing"}, filepath.Join(e.dir, "work", "old"): {"歷史"}}))
	e.eventually(time.Second, "兩個目錄", func(s string) bool { return strings.Contains(s, "landing") })
	s := e.sidebar()
	if strings.Count(s, "修正登入") != 1 || strings.Contains(s, "歷史") {
		t.Fatalf("同目錄重複或列出歷史目錄：\n%s", s)
	}
	if strings.Index(s, "api") > strings.Index(s, "web") {
		t.Fatalf("目錄未依完整路徑排序：\n%s", s)
	}

	// 關閉 web 的 TUI window（有 tmux hook 通知）
	e.inTmux("kill-window", "-t", webWin)
	e.eventually(time.Second, "關閉 TUI 於 1 秒內消失", func(s string) bool { return !strings.Contains(s, "landing") })

}

// 漏通知：TUI 行程被強制結束但 pane（shell）還在，tmux 不發任何 hook，
// 只能靠低頻補漏在 10 秒內修正。
func TestMissedNotificationIsCaughtByBackstopWithin10Seconds(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	e.tuiUnderShell(dir)
	e.eventually(10*time.Second, "shell 內啟動的 TUI（無 hook）於 10 秒內出現", func(s string) bool { return strings.Contains(s, "! a") })
	must(t, exec.Command("pkill", "-KILL", "-f", e.bin).Run())
	d := e.eventually(10*time.Second, "漏通知於 10 秒內修正", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })
	if d < time.Second {
		t.Fatalf("%v 就更新了，代表這不是漏通知情境", d)
	}
	t.Logf("漏通知 → 補漏：%v", d)
}

func TestStoppingDaemonRemovesItsHooksOnly(t *testing.T) {
	e := newEnv(t)
	e.inTmux("set-hook", "-g", "pane-exited[3]", "display-message user-hook")
	time.Sleep(500 * time.Millisecond)
	if !strings.Contains(e.inTmux("show-hooks", "-gw"), "wait-for -S tos_wake") {
		t.Fatal("常駐程式執行中應有喚醒 hook")
	}
	must(t, e.daemon.Process.Signal(syscall.SIGTERM))
	_ = e.daemon.Wait()
	hooks := e.inTmux("show-hooks", "-gw")
	if strings.Contains(hooks, "tos_wake") || !strings.Contains(hooks, "user-hook") {
		t.Fatalf("結束後 hook 應只移除插件自己的：\n%s", hooks)
	}
}

func TestNonTUIOpencodeCommandsAreNotListed(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "svc")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"x"}}))
	e.tui(dir, "serve")
	e.tui(dir, "session", "list")
	time.Sleep(1500 * time.Millisecond)
	if s := e.sidebar(); !strings.Contains(s, "沒有 OpenCode TUI") || strings.Contains(s, "svc") {
		t.Fatalf("非 TUI 被列入：\n%s", s)
	}
}

func TestIncompatibleTabsFileShowsNoticeAndKeepsLastList(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"keep me"}}))
	e.tui(dir)
	e.eventually(2*time.Second, "初始顯示", func(s string) bool { return strings.Contains(s, "keep me") })
	e.writeTabs(`{"version":9}`)
	e.eventually(2*time.Second, "不相容時提示並保留", func(s string) bool {
		return strings.Contains(s, "無法讀取 tab 清單") && strings.Contains(s, "keep me") && !strings.Contains(s, "無 tab")
	})
}

func TestUserTmuxOptionsAndBindingsAreUntouched(t *testing.T) {
	e := newEnv(t)
	e.inTmux("set", "-g", "@oc_side", "user-value")
	e.inTmux("set", "-g", "@oc_side_bak", "user-bak")
	before := e.inTmux("list-keys")
	dir := filepath.Join(e.dir, "work", "api")
	e.tui(dir)
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	e.eventually(2*time.Second, "有內容", func(s string) bool { return strings.Contains(s, "a") && strings.Contains(s, "api") })
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "@oc_side")); got != "user-value" {
		t.Fatalf("@oc_side 被改：%q", got)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "@oc_side_bak")); got != "user-bak" {
		t.Fatalf("@oc_side_bak 被改：%q", got)
	}
	if after := e.inTmux("list-keys"); after != before {
		t.Fatal("鍵盤綁定被改變")
	}
	if strings.Contains(e.inTmux("show-options", "-g"), "@sb_") {
		t.Fatal("不應使用舊 POC 前綴 @sb_")
	}
}

func TestDaemonRefusesToRunWithoutExplicitSocket(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "d")
	if b, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	if err := exec.Command(bin).Run(); err == nil {
		t.Fatal("未指定 socket 不得執行（避免誤用預設 tmux server）")
	}
}

// 另一個 tmux server（不同 socket）中的 TUI 不得出現在這個 server 的側欄。
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
	e.eventually(2*time.Second, "本 server 的 TUI", func(s string) bool { return strings.Contains(s, "local") })
	if s := e.sidebar(); strings.Contains(s, "foreign") || strings.Contains(s, "elsewhere") {
		t.Fatalf("混入其他 server 的資料：\n%s", s)
	}
}

// ---- #8 cwd／global 模式與設定重載 ----

const modeOpt = "@opencode-status-mode"

// bothScopesJSON：global 清單含不在任何 TUI 目錄下的 session，cwd 清單另有一份。
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

// reload 模擬 `tmux source-file ~/.tmux.conf` 重載含模式選項的設定檔。
func (e *env) reload(conf string) {
	e.t.Helper()
	path := filepath.Join(e.dir, "reload.conf")
	must(e.t, os.WriteFile(path, []byte(conf), 0o644))
	e.inTmux("source-file", path)
}

// daemonCount 數出指向被觀察 server 的常駐程式實例（pgrep 不會算到自己）。
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

	e.eventually(3*time.Second, "預設 cwd", func(s string) bool {
		return strings.Contains(s, "api tab") && strings.Contains(s, "web tab") && !strings.Contains(s, "GLOBAL")
	})
	pid := e.daemon.Process.Pid

	e.reload("set -g " + modeOpt + " global\n")
	d := e.eventually(time.Second, "重載後 1 秒內切到 global", func(s string) bool {
		return strings.Contains(s, "GLOBAL") && strings.Contains(s, "其他目錄的工作") && strings.Count(s, "global one") == 1 &&
			!strings.Contains(s, "api tab") && !strings.Contains(s, "web tab")
	})
	t.Logf("cwd → global：%v", d)

	e.reload("set -g " + modeOpt + " cwd\n")
	e.eventually(time.Second, "重載後切回 cwd，不殘留 global", func(s string) bool {
		return strings.Contains(s, "api tab") && strings.Contains(s, "web tab") && !strings.Contains(s, "GLOBAL") && !strings.Contains(s, "global one")
	})

	e.reload("set -gu " + modeOpt + "\nset -g " + modeOpt + " global\nset -gu " + modeOpt + "\n")
	e.eventually(time.Second, "移除設定回到預設 cwd", func(s string) bool { return strings.Contains(s, "api tab") && !strings.Contains(s, "GLOBAL") })

	if e.daemon.Process.Pid != pid || e.daemonCount() != 1 {
		t.Fatalf("切換模式不得產生第二個實例（pid %d → %d，實例數 %d）", pid, e.daemon.Process.Pid, e.daemonCount())
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
	e.eventually(2*time.Second, "多個 TUI 只列一份共享清單", func(s string) bool { return strings.Contains(s, "shared a") })
	s := e.sidebar()
	if strings.Count(s, "shared a") != 1 || strings.Count(s, "GLOBAL") != 1 || strings.Contains(s, "api tab") || strings.Contains(s, "web") {
		t.Fatalf("global 清單重複或混入 cwd：\n%s", s)
	}
	e.inTmux("kill-window", "-t", w1)
	e.inTmux("kill-window", "-t", w2)
	time.Sleep(700 * time.Millisecond)
	if s := e.sidebar(); !strings.Contains(s, "shared a") {
		t.Fatalf("仍有 TUI 時共享清單應保留：\n%s", s)
	}
	e.inTmux("kill-window", "-t", w3)
	e.eventually(time.Second, "最後一個 TUI 關閉後呈現空畫面", func(s string) bool {
		return strings.Contains(s, "沒有 OpenCode TUI") && !strings.Contains(s, "shared a") && !strings.Contains(s, "GLOBAL")
	})
}

func TestInvalidModeNeverStartsTrackingThenReloadKeepsValidMode(t *testing.T) {
	// 首次無效：不開始追蹤
	e := newEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.inTmux("set", "-g", modeOpt, "globl")
	e.writeTabs(e.bothScopesJSON(map[string][]string{api: {"api tab"}}, []string{"shared a"}))
	e.tui(api)
	e.eventually(2*time.Second, "首次無效值提示且不追蹤", func(s string) bool {
		return strings.Contains(s, "模式設定無效") && strings.Contains(s, "尚未開始追蹤")
	})
	if s := e.sidebar(); strings.Contains(s, "api tab") || strings.Contains(s, "shared a") || strings.Contains(s, "沒有 OpenCode TUI") {
		t.Fatalf("首次無效設定不得開始追蹤：\n%s", s)
	}
	// 修正後開始追蹤
	e.reload("set -g " + modeOpt + " global\n")
	e.eventually(time.Second, "改為有效值後開始追蹤", func(s string) bool { return strings.Contains(s, "shared a") })
	// 重載時無效：保留 global 並提示未套用
	e.reload("set -g " + modeOpt + " bogus\n")
	e.eventually(time.Second, "重載無效值保留原模式並回報", func(s string) bool {
		return strings.Contains(s, "未套用") && strings.Contains(s, "shared a") && !strings.Contains(s, "api tab")
	})
	e.reload("set -g " + modeOpt + " cwd\n")
	e.eventually(time.Second, "修正後套用且提示消失", func(s string) bool { return strings.Contains(s, "api tab") && !strings.Contains(s, "未套用") })
}

func TestModeSwitchDoesNotForceAHiddenSidebarOpen(t *testing.T) {
	e := newEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.bothScopesJSON(map[string][]string{api: {"api tab"}}, []string{"shared a"}))
	e.tui(api)
	e.eventually(3*time.Second, "已顯示", func(s string) bool { return strings.Contains(s, "api tab") })
	e.inTmux("set", "-g", "side-status", "off")
	e.reload("set -g " + modeOpt + " global\n")
	time.Sleep(1200 * time.Millisecond)
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "side-status")); got != "off" {
		t.Fatalf("切換模式不應重新打開側欄，side-status = %q", got)
	}
}

func TestModeOptionIsPluginPrefixedAndOtherOptionsStayUntouched(t *testing.T) {
	e := newEnv(t)
	e.inTmux("set", "-g", "@oc_side", "user-value")
	e.reload("set -g " + modeOpt + " global\n")
	time.Sleep(600 * time.Millisecond)
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "@oc_side")); got != "user-value" {
		t.Fatalf("@oc_side 被改：%q", got)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-gv", modeOpt)); got != "global" {
		t.Fatalf("插件不應改寫使用者設定的模式選項：%q", got)
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
		t.Fatalf("結束後只應移除插件自己的 hook：\n%s", hooks)
	}
}

// 啟動空窗：hook 還沒裝好時發生的設定變動，不能只靠低頻補漏（5 秒）才發現。
// 以 tmux 包裝讓安裝模式 hook（最後一個裝的）慢 300ms，確定性地放大空窗而不依賴 -race 的快慢；
// 在常駐程式首次讀到設定之後、hook 裝好之前重載設定，仍須在 1 秒內套用。
func TestModeChangeDuringDaemonStartupIsNotLeftToTheBackstop(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("沒有 tmux")
	}
	shim := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in *\" after-set-option[\"*) sleep 0.3;; esac\nexec " + realTmux + " \"$@\"\n"
	must(t, os.WriteFile(filepath.Join(shim, "tmux"), []byte(script), 0o755))

	e := newEnvWith(t, []string{"PATH=" + shim + ":" + os.Getenv("PATH")})
	api := filepath.Join(e.dir, "work", "api")
	e.inTmux("set", "-g", modeOpt, "globl")
	e.writeTabs(e.bothScopesJSON(map[string][]string{api: {"api tab"}}, []string{"shared a"}))
	e.tui(api)
	e.eventually(5*time.Second, "常駐程式首次讀到設定", func(s string) bool { return strings.Contains(s, "模式設定無效") })
	e.reload("set -g " + modeOpt + " global\n")
	d := e.eventually(time.Second, "啟動空窗內的重載於 1 秒內套用", func(s string) bool { return strings.Contains(s, "shared a") })
	t.Logf("重載 → 畫面：%v", d)
}

// ---- #4 追加：pane title 變更作為提早重新掃描的喚醒訊號 ----

// shellWindow 開一個互動 shell window（有 job control），回傳 window id；之後以 shellRun 在其中執行指令。
// 使用乾淨的 sh -i，避免使用者 zsh 設定（prompt、preexec）的延遲污染時間量測。
func (e *env) shellWindow(dir string) string {
	must(e.t, os.MkdirAll(dir, 0o755))
	win := strings.TrimSpace(e.inTmux("new-window", "-d", "-P", "-F", "#{window_id}", "-c", dir, "env PS1='$ ' sh -i"))
	time.Sleep(400 * time.Millisecond) // 等 shell 就緒
	return win
}

func (e *env) shellRun(win, cmdline string) { e.inTmux("send-keys", "-t", win, cmdline, "Enter") }

// 既有 shell 中啟動／結束 TUI：tmux 不發結構事件，但 TUI 會改終端標題 → 1 秒內反映。
func TestTUIStartedAndStoppedInAnExistingShellFollowsWithin1SecondWhenTitlesAreOn(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	e.eventually(3*time.Second, "啟動", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })
	win := e.shellWindow(dir)

	e.shellRun(win, "FAKE_TITLE=1 "+e.bin)
	d := e.eventually(time.Second, "shell 內啟動 TUI 於 1 秒內出現", func(s string) bool { return strings.Contains(s, "! a") })
	t.Logf("shell 內啟動 → 畫面：%v", d)

	e.inTmux("send-keys", "-t", win, "C-c")
	d = e.eventually(time.Second, "shell 內結束 TUI 於 1 秒內消失", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })
	t.Logf("shell 內結束 → 畫面：%v", d)
}

// 終端標題關閉（行程不改標題）：沒有喚醒，仍由低頻補漏在 10 秒內正確反映。
func TestTUIInAnExistingShellStillFollowsViaBackstopWhenTitlesAreOff(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	e.eventually(3*time.Second, "啟動", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })
	win := e.shellWindow(dir)

	e.shellRun(win, e.bin)
	e.eventually(10*time.Second, "標題關閉時啟動於 10 秒內出現", func(s string) bool { return strings.Contains(s, "! a") })
	e.inTmux("send-keys", "-t", win, "C-c")
	e.eventually(10*time.Second, "標題關閉時結束於 10 秒內消失", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })
}

// 辨識以前景行程為準：非 TUI 的 opencode 指令改標題、任意行程把標題設成 OpenCode，都不算 TUI。
func TestTitleChangesNeverMakeANonTUIProcessLookLikeATUI(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "svc")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"x"}}))
	win := e.shellWindow(dir)
	e.shellRun(win, "FAKE_TITLE=1 "+e.bin+" serve")
	win2 := e.shellWindow(dir)
	e.shellRun(win2, `printf '\033]0;OpenCode\007'; sleep 60`)
	time.Sleep(1500 * time.Millisecond)
	if s := e.sidebar(); !strings.Contains(s, "沒有 OpenCode TUI") || strings.Contains(s, "svc") || strings.Contains(s, "x") {
		t.Fatalf("非 TUI 因標題被列入：\n%s", s)
	}
}

// tmuxLog 以 PATH 前面的 tmux 包裝記錄常駐程式發出的每個 tmux 指令。
func tmuxLogShim(t *testing.T) (path, logFile string) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("沒有 tmux")
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

// TUI 持續頻繁改標題（20ms 一次）：不得變成喚醒風暴（掃描次數有上限），
// 也不得因標題變化而寫入 tmux（內容不變不寫）。
func TestFrequentTitleChangesDoNotCauseAWakeStormOrTmuxWrites(t *testing.T) {
	path, logFile := tmuxLogShim(t)
	e := newEnvWith(t, []string{"PATH=" + path})
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"a"}}))
	win := e.shellWindow(dir)
	e.shellRun(win, "FAKE_TITLE=1 FAKE_CHURN_MS=20 "+e.bin)
	e.eventually(2*time.Second, "TUI 出現", func(s string) bool { return strings.Contains(s, "! a") })

	time.Sleep(500 * time.Millisecond)
	scans0, writes0 := countCalls(t, logFile, "list-panes"), countCalls(t, logFile, "source-file")
	time.Sleep(3 * time.Second)
	scans, writes := countCalls(t, logFile, "list-panes")-scans0, countCalls(t, logFile, "source-file")-writes0
	t.Logf("3 秒內掃描 %d 次、寫入 %d 次（標題約每 20ms 變一次）", scans, writes)
	if scans > 14 {
		t.Fatalf("喚醒風暴：3 秒內掃描 %d 次", scans)
	}
	if writes != 0 {
		t.Fatalf("內容不變卻寫入 tmux %d 次", writes)
	}
	if s := e.sidebar(); !strings.Contains(s, "! a") {
		t.Fatalf("節流後畫面仍須正確：\n%s", s)
	}
}
