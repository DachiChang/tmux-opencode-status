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

// 生命週期的程序外部測試（issue #11）：真實常駐程式 + 隔離 tmux server（-L … -f /dev/null）。
// 啟動入口是 `tmux-opencode-status start`（TPM 的 .tmux 腳本在每次載入／重載時呼叫的冪等指令）。
// 鎖檔與狀態檔放在測試自己的 XDG_RUNTIME_DIR，不碰使用者真實的 runtime 目錄。

// cmdEnv 是所有被測指令的環境：沒有 $TMUX（不可誤用開發者目前所在的 tmux server），runtime 目錄隔離。
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

// common 是指向被觀察 server 的共同旗標（不含子指令）。
func (e *env) common(sock string, extra ...string) []string {
	args := []string{"-L", sock, "-f", "/dev/null", "-tabs", e.tabsPath, "-service", e.servicePath, "-backstop", "5s"}
	args = append(args, e.daemonArgs...)
	return append(args, extra...)
}

// cli 執行子指令並回傳合併輸出與結果。
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
		e.t.Fatalf("%s 逾時：%s", sub, out.String())
		return "", nil
	}
}

func (e *env) start(sock string, extra ...string) (string, error) {
	return e.cli("start", sock, extra...)
}

// daemons 列出指向 sock 的常駐程式行程（不含短命的 start／stop／diagnose）。
func (e *env) daemons(sock string) []int {
	out, _ := exec.Command("ps", "-axo", "pid=,command=").Output()
	var pids []int
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		// start 以明確的 -S <socket 路徑> 啟動 run；直接執行 run 則可能是 -L <name>。
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
	e.t.Fatalf("%s：%v 內常駐實例數應為 %d，實際 %v", what, within, want, got)
	return nil
}

// allHooks 合併全域、視窗層級的 hook（show-hooks 依層級分開列出）。
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
		t.Fatalf("start：%v\n%s", err, out)
	}
	pids := e.waitDaemons(e.in, 1, 3*time.Second, "首次 start")
	e.eventually(3*time.Second, "start 後側欄顯示", func(s string) bool { return strings.Contains(s, "! 修正登入") })

	// 重載設定檔會再次執行 start：同一個實例、同一個 pid
	for i := 0; i < 3; i++ {
		if out, err := e.start(e.in, "-show"); err != nil {
			t.Fatalf("重複 start：%v\n%s", err, out)
		}
	}
	if got := e.daemons(e.in); len(got) != 1 || got[0] != pids[0] {
		t.Fatalf("重複載入後應仍是原本的實例 %v，實際 %v", pids, got)
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
		t.Errorf("並行 start 不應失敗：%s", m)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "並行 start")
	time.Sleep(700 * time.Millisecond) // 多餘的候選行程若會殘留，此時應已退出或已經被看到
	if got := e.daemons(e.in); len(got) != 1 {
		t.Fatalf("並行啟動後應只有一個實例：%v", got)
	}
}

// 重載交接：設定（這裡以寬度為例）改變後再次 start，舊實例讓位、新實例接手，從不同時有兩個；
// 交接不強制打開已隱藏的側欄、不清除 session 層級 override、不新增或改變插件接管以外的綁定。
func TestReloadWithChangedSettingsHandsOffToExactlyOneNewInstance(t *testing.T) {
	e := newLifecycleEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入"}}))
	e.tui(api)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start：%v\n%s", err, out)
	}
	old := e.waitDaemons(e.in, 1, 3*time.Second, "首次 start")
	e.eventually(3*time.Second, "已顯示", func(s string) bool { return strings.Contains(s, "! 修正登入") })

	// 使用者隱藏側欄，並在另一個 session 自行覆寫 side-status 寬度
	e.inTmux("new-session", "-d", "-s", "other")
	e.inTmux("set-option", "-t", "other", "side-status-width", "20")
	e.inTmux("set-option", "-g", "side-status", "off")
	keys := keysWithoutJump(e.inTmux("list-keys"))

	// 設定改變（-resync）：交接
	var peak int
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { // 交接期間持續取樣：任何時刻都不得出現兩個實例
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
		t.Fatalf("交接 start：%v\n%s", err, out)
	}
	now := e.waitDaemons(e.in, 1, 3*time.Second, "交接後")
	if now[0] == old[0] {
		t.Fatalf("設定改變後應由新實例接手，pid 仍為 %d", old[0])
	}
	if peak > 1 {
		t.Fatalf("交接期間同時出現 %d 個實例", peak)
	}
	if alive(old[0]) {
		t.Fatalf("舊實例 %d 應已退出", old[0])
	}

	// 新實例持續追蹤：側欄雖隱藏，內容仍更新；之後重新顯示立即看到最新內容，不需重啟
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入", "新增的 tab"}}))
	time.Sleep(900 * time.Millisecond)
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "side-status")); got != "off" {
		t.Fatalf("重載不得重新打開使用者已隱藏的側欄，side-status = %q", got)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-t", "other", "-v", "side-status-width")); got != "20" {
		t.Fatalf("session 層級 override 被清除：%q", got)
	}
	if after := keysWithoutJump(e.inTmux("list-keys")); after != keys {
		t.Fatal("新增或改變了鍵盤綁定")
	}
	e.inTmux("set-option", "-g", "side-status", "left")
	e.eventually(2*time.Second, "重新顯示不需手動重啟", func(s string) bool { return strings.Contains(s, "! 新增的 tab") })
	if n := strings.Count(e.allHooks(), "[41700]"); n != 10 {
		t.Fatalf("交接後本插件 hook 應各只有一份（8 喚醒 + 選項（模式與色票）+ 標題 = 10），實際 %d：\n%s", n, e.allHooks())
	}
}

// 不同 socket 的實例各自獨立：互不影響、互不顯示對方的 pane，停止其中一個不影響另一個。
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
		t.Fatal("兩個 socket 不得共用實例")
	}
	e.eventually(3*time.Second, "A 只顯示自己的 pane", func(s string) bool { return strings.Contains(s, "local") })
	if s := e.sidebar(); strings.Contains(s, "foreign") {
		t.Fatalf("A 的側欄混入 B 的 pane：\n%s", s)
	}
	var bOpts string
	for i := 0; i < 60 && !strings.Contains(bOpts, "foreign"); i++ {
		b, _ := exec.Command("tmux", "-L", other, "-f", "/dev/null", "show-options", "-g").CombinedOutput()
		bOpts = grep(string(b), "tos_layout")
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(bOpts, "foreign") || strings.Contains(bOpts, "local") {
		t.Fatalf("B 的 side-status 內容應只含自己的 pane：\n%s", bOpts)
	}

	// 再次 start A 不影響 B；停止 A 不影響 B
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("stop A：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 0, 3*time.Second, "停止 A")
	if got := e.daemons(other); len(got) != 1 || got[0] != b[0] {
		t.Fatalf("停止 A 不應影響 B：%v", got)
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

// 無 TUI 時常駐程式仍存活但不連線、不查詢 session；TUI 出現後自動開始追蹤；
// 側欄隱藏期間仍追蹤，重新顯示不需手動重啟；全程沒有任何對 OpenCode 的寫入。
func TestIdleWithoutTUIsAndHiddenSidebarStillRecoverWithoutManualRestart(t *testing.T) {
	e := newLifecycleEnv(t)
	svc := fakeoc.New(t)
	svc.AddSession("ses_0", "")
	svc.WriteServiceFile(e.servicePath)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入"}}))

	// 側欄一開始就是隱藏的（start 不帶 -show）
	e.inTmux("set-option", "-g", "side-status", "off")
	if out, err := e.start(e.in); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	time.Sleep(1500 * time.Millisecond)
	if svc.SessionRequests() != 0 || svc.Connects() != 0 {
		t.Fatalf("沒有 TUI（沒有追蹤項目）時不得連線或查詢 session：連線 %d、請求 %v", svc.Connects(), svc.Requests())
	}
	if !alive(pid) {
		t.Fatal("沒有 TUI 時常駐程式必須存活")
	}

	// 側欄隱藏時出現 TUI：照樣開始追蹤
	e.tui(api, "--server", "http://127.0.0.1:1")
	deadline := time.Now().Add(5 * time.Second)
	for svc.SessionRequests() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if svc.SessionRequests() == 0 {
		t.Fatal("側欄隱藏時 TUI 出現，常駐程式應照樣開始追蹤")
	}
	svc.Start("ses_0")
	time.Sleep(700 * time.Millisecond)
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "side-status")); got != "off" {
		t.Fatalf("追蹤不得打開側欄：%q", got)
	}
	// 重新顯示：立即是最新內容
	e.inTmux("set-option", "-g", "side-status", "left")
	e.eventually(2*time.Second, "重新顯示後立即是最新狀態", func(s string) bool { return strings.Contains(s, "● 修正登入") })

	if w := svc.Writes(); len(w) != 0 {
		t.Fatalf("對 OpenCode 發出了寫入請求：%v", w)
	}
}

// 最後一個 TUI 關閉後不再追蹤（釋放服務連線、不再查詢），常駐程式仍存活。
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
	e.eventually(5*time.Second, "接上服務", func(s string) bool { return strings.Contains(s, "· 修正登入") })
	e.inTmux("kill-window", "-t", win)
	e.eventually(2*time.Second, "TUI 關閉", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })
	time.Sleep(300 * time.Millisecond)
	before := len(svc.Requests())
	time.Sleep(1500 * time.Millisecond)
	if n := len(svc.Requests()); n != before || svc.Subscribers() != 0 {
		t.Fatalf("沒有追蹤項目後不得再查詢（請求 %d → %d，SSE 訂閱 %d）", before, n, svc.Subscribers())
	}
	if !alive(pid) {
		t.Fatal("最後一個 TUI 關閉後常駐程式應仍存活")
	}
	// 新增 TUI 不需手動重啟
	e.tui(api, "-c")
	e.eventually(5*time.Second, "新增 TUI 後自動恢復", func(s string) bool { return strings.Contains(s, "· 修正登入") })
}

// tmuxShim 在 PATH 前面放一個 tmux 包裝：存在 flag 檔時所有 tmux 指令都失敗（模擬暫時性失敗）。
// reject 非空時，只有含該字串的 show-options 查詢回報 invalid option（模擬缺少能力的舊 tmux）。
func tmuxShim(t *testing.T, flag, reject string) string {
	t.Helper()
	real, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("沒有 tmux")
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
	e.t.Fatalf("%s：行程 %d 在 %v 內仍未結束", what, pid, within)
}

// server 結束：常駐程式退出，本機鎖檔與狀態檔被清掉；沒有殘留背景行程。
func TestInstanceExitsAndCleansUpWhenTheTmuxServerExits(t *testing.T) {
	e := newLifecycleEnv(t, "-server-check", "200ms")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	if files := e.runtimeFiles(); len(files) < 2 {
		t.Fatalf("執行中應有鎖檔與狀態檔：%v", files)
	}
	e.inTmux("kill-server")
	e.waitGone(pid, 5*time.Second, "server 結束後常駐程式應退出")
	if files := e.runtimeFiles(); len(files) != 0 {
		t.Fatalf("server 結束後本機資源應清乾淨：%v", files)
	}
}

// 單次或短暫的 tmux 指令失敗不是 server 結束：常駐程式存活、不清除任何東西，恢復後繼續運作。
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
	e.eventually(3*time.Second, "已顯示", func(s string) bool { return strings.Contains(s, "! 修正登入") })

	must(t, os.WriteFile(flag, nil, 0o644)) // 此後所有 tmux 指令都失敗，持續數個 server 檢查週期
	time.Sleep(2500 * time.Millisecond)
	if !alive(pid) {
		t.Fatal("tmux 指令暫時失敗不得被誤判為 server 結束")
	}
	if len(e.runtimeFiles()) < 2 {
		t.Fatalf("暫時性失敗不得清除鎖檔或狀態：%v", e.runtimeFiles())
	}
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "@user-owned")); got != "keep" || !strings.Contains(e.inTmux("show-hooks", "-g")+e.inTmux("show-hooks", "-gw"), "user-hook") {
		t.Fatal("暫時性失敗不得清除使用者的選項或 hook")
	}
	must(t, os.Remove(flag))
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"修正登入", "恢復後新增"}}))
	e.eventually(7*time.Second, "tmux 恢復後繼續運作", func(s string) bool { return strings.Contains(s, "! 恢復後新增") })
}

// 明確停止：只清本插件的資源（hook、@tos_*、自己寫的 side-status-format、鎖與狀態檔），
// 使用者的選項、hook、session override 與綁定原封不動；之後 start 可重新啟動。
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
	e.eventually(3*time.Second, "已顯示", func(s string) bool { return strings.Contains(s, "! 修正登入") })

	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("stop：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 0, 3*time.Second, "stop")
	opts := e.inTmux("show-options", "-g")
	for _, l := range strings.Split(opts, "\n") {
		if strings.HasPrefix(l, "@tos_") {
			t.Fatalf("停止後不應留下任何 @tos_*：\n%s", grep(opts, "tos_"))
		}
	}
	if strings.Contains(opts, "@opencode-status-shown") {
		t.Fatalf("停止後應移除首次打開側欄標記：\n%s", grep(opts, "opencode-status-shown"))
	}
	if !strings.Contains(opts, "@opencode-status-mode cwd") {
		t.Fatalf("停止後使用者的 @opencode-status-* 設定應保留：\n%s", opts)
	}
	if f := e.inTmux("show", "-gv", "side-status-format"); strings.Contains(f, "tos_") {
		t.Fatalf("side-status-format 應回到 tmux 預設：%s", f)
	}
	if strings.Contains(e.allHooks(), "[41700]") || !strings.Contains(e.allHooks(), "user-hook") {
		t.Fatalf("停止後只應移除插件自己的 hook：\n%s", e.allHooks())
	}
	if !strings.Contains(opts, "@oc_side user-value") || !strings.Contains(opts, "@opencode-status-mode cwd") {
		t.Fatalf("使用者選項被動到：\n%s", opts)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-t", "other", "-v", "side-status-width")); got != "20" {
		t.Fatalf("session override 被清除：%q", got)
	}
	if keysWithoutJump(e.inTmux("list-keys")) != keys {
		t.Fatal("綁定被改變")
	}
	if files := e.runtimeFiles(); len(files) != 0 {
		t.Fatalf("停止後本機資源應清乾淨：%v", files)
	}
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("停止後可重新啟動：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "重新啟動")
}

// 使用者自己設定了 side-status-format：停止時不得覆蓋或刪除它。
func TestStopDoesNotRemoveAFormatTheUserSetAfterwards(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "已顯示", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })
	e.inTmux("set", "-g", "side-status-format", "my own format")
	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := strings.TrimSpace(e.inTmux("show", "-gv", "side-status-format")); got != "my own format" {
		t.Fatalf("使用者自己的 side-status-format 被動到：%q", got)
	}
}

// 缺少必要能力（舊 tmux）：start 明確報錯、不啟動；其他暫時性錯誤不被說成缺少能力。
func TestMissingTmuxCapabilityFailsLoudlyWithoutStartingAnything(t *testing.T) {
	e := newBareEnv(t, []string{tmuxShim(t, "", "side-status-width")}, nil)
	out, err := e.start(e.in, "-show")
	if err == nil || !strings.Contains(out, "缺少必要能力") || !strings.Contains(out, "side-status-width") {
		t.Fatalf("缺少能力應明確失敗：err=%v\n%s", err, out)
	}
	time.Sleep(300 * time.Millisecond)
	if got := e.daemons(e.in); len(got) != 0 {
		t.Fatalf("缺少能力不得啟動常駐程式：%v", got)
	}
	if strings.Contains(e.inTmux("show-options", "-g"), "@tos_") {
		t.Fatal("缺少能力不得寫入任何側欄選項")
	}
}

func TestTransientTmuxErrorAtStartIsNotReportedAsMissingCapability(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "fail")
	must(t, os.WriteFile(flag, nil, 0o644))
	e := newBareEnv(t, []string{tmuxShim(t, flag, "")}, nil)
	out, err := e.start(e.in)
	if err == nil {
		t.Fatalf("tmux 不可用時 start 應失敗：%s", out)
	}
	if strings.Contains(out, "缺少必要能力") {
		t.Fatalf("暫時性錯誤不得被說成缺少能力：%s", out)
	}
	if got := e.daemons(e.in); len(got) != 0 {
		t.Fatalf("不得啟動：%v", got)
	}
}

// diagnose 取得診斷輸出。
func (e *env) diagnose(sock string) string {
	e.t.Helper()
	out, err := e.cli("diagnose", sock)
	if err != nil {
		e.t.Fatalf("diagnose：%v\n%s", err, out)
	}
	return out
}

func mustContain(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("診斷輸出缺少 %q：\n%s", s, out)
		}
	}
}

func mustNotContain(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(out, s) {
			t.Errorf("診斷輸出不應含 %q：\n%s", s, out)
		}
	}
}

// 診斷涵蓋：模式、常駐狀態、TUI／tab 數、服務連線、最近錯誤、必要能力、session override 影響；
// 並且不含憑證、prompt／對話、OpenCode tab 標題、目錄路徑與 tmux session 名稱。
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

	// 服務尚未開啟
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "服務未開時提示", func(s string) bool { return strings.Contains(s, "無法連線 OpenCode 服務") })
	time.Sleep(300 * time.Millisecond)
	out := e.diagnose(e.in)
	t.Logf("診斷（服務未開）：\n%s", out)
	mustContain(t, out, "版本", "插件模式設定：cwd", "生效模式：cwd", "常駐實例：執行中",
		"服務連線：無法連線", "找不到服務探索檔", "不會啟動或重啟", "最近錯誤", "必要能力", "side-status-format 皆可用",
		"session 層級覆寫：1 個", "side-status=off", "side-status-width=20", "不會清除或修改這些覆寫")

	// 服務出現後：已連線、TUI／tab 數
	svc.WriteServiceFile(e.servicePath)
	e.eventually(6*time.Second, "接上服務", func(s string) bool { return !strings.Contains(s, "無法連線") })
	time.Sleep(300 * time.Millisecond)
	out = e.diagnose(e.in)
	t.Logf("診斷（已連線）：\n%s", out)
	mustContain(t, out, "服務連線：已連線", "發現的 TUI：1 個", "追蹤的 tab：2 個")
	// 模式與 OpenCode 實際 scope 不一致時（global 清單是空的）：沒有追蹤項目，診斷看得出來
	e.inTmux("set", "-g", "@opencode-status-mode", "global")
	e.eventually(3*time.Second, "切到 global", func(s string) bool { return strings.Contains(s, "無 tab") })
	time.Sleep(300 * time.Millisecond)
	out = e.diagnose(e.in)
	mustContain(t, out, "生效模式：global", "追蹤的 tab：0 個", "待命", "刻意不連線")
	e.inTmux("set", "-g", "@opencode-status-mode", "cwd")
	e.eventually(5*time.Second, "切回 cwd", func(s string) bool { return strings.Contains(s, "·") })
	time.Sleep(300 * time.Millisecond)
	out = e.diagnose(e.in)
	mustContain(t, out, "生效模式：cwd", "追蹤的 tab：2 個")

	for _, forbidden := range []string{fakeoc.Password, "sk-live", "SENTINEL_", "secret-project-name", e.dir, e.servicePath, e.tabsPath, "127.0.0.1", "0.0.0.0", svc.URL()} {
		mustNotContain(t, out, forbidden)
	}
	// 服務中斷後的最近錯誤也不洩漏
	_ = os.Remove(e.servicePath)
	svc.Drop()
	e.eventually(5*time.Second, "斷線提示", func(s string) bool { return strings.Contains(s, "無法連線 OpenCode 服務") })
	time.Sleep(300 * time.Millisecond)
	out = e.diagnose(e.in)
	mustContain(t, out, "服務連線：無法連線")
	for _, forbidden := range []string{fakeoc.Password, "sk-live", "SENTINEL_", "secret-project-name", e.dir, "127.0.0.1", "Authorization", "Basic "} {
		mustNotContain(t, out, forbidden)
	}
	if w := svc.Writes(); len(w) != 0 {
		t.Fatalf("對 OpenCode 發出了寫入請求：%v", w)
	}
}

// 診斷嚴格唯讀：不改 tmux 選項／hook／綁定、不啟動或停止常駐程式、不連 OpenCode。
func TestDiagnoseIsStrictlyReadOnly(t *testing.T) {
	e := newLifecycleEnv(t)
	svc := fakeoc.New(t)
	svc.WriteServiceFile(e.servicePath)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	e.eventually(3*time.Second, "已顯示", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })
	opts, hooks, keys := e.inTmux("show-options", "-g"), e.allHooks(), e.inTmux("list-keys")
	reqs := len(svc.Requests())
	e.diagnose(e.in)
	e.diagnose(e.in)
	if e.inTmux("show-options", "-g") != opts || e.allHooks() != hooks || e.inTmux("list-keys") != keys {
		t.Fatal("診斷改變了 tmux 狀態")
	}
	if got := e.daemons(e.in); len(got) != 1 || got[0] != pid {
		t.Fatalf("診斷不得啟動或停止常駐程式：%v", got)
	}
	if len(svc.Requests()) != reqs || len(svc.Writes()) != 0 {
		t.Fatal("診斷不得連線 OpenCode")
	}
}

// 診斷能指出「為什麼沒有顯示」：常駐程式沒在跑、模式設定無效、缺少能力。
func TestDiagnoseExplainsWhyNothingIsShown(t *testing.T) {
	e := newLifecycleEnv(t)
	out := e.diagnose(e.in)
	mustContain(t, out, "常駐實例：未執行", "重新載入 tmux 設定", "發現 1 項問題")

	e.inTmux("set", "-g", "@opencode-status-mode", "globl")
	if o, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	time.Sleep(600 * time.Millisecond)
	out = e.diagnose(e.in)
	mustContain(t, out, "插件模式設定：無效值", "尚未開始追蹤")
	mustNotContain(t, out, "globl")
}

func TestDiagnoseReportsAMissingCapability(t *testing.T) {
	e := newBareEnv(t, []string{tmuxShim(t, "", "side-status-format")}, nil)
	mustContain(t, e.diagnose(e.in), "必要能力：缺少 side-status-format", "tmux 3.9")
}

// 側欄隱藏時診斷會說明常駐程式仍在追蹤。
func TestDiagnoseMentionsTheSidebarIsHiddenButStillTracking(t *testing.T) {
	e := newLifecycleEnv(t)
	e.inTmux("set-option", "-g", "side-status", "off")
	if out, err := e.start(e.in); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	mustContain(t, e.diagnose(e.in), "側欄隱藏", "不需重啟")
}

// 服務斷線與 tab 清單問題都要出現在「最近錯誤」，且只有固定描述。
func TestDiagnoseRecentErrorsIncludeServiceAndTabListProblems(t *testing.T) {
	e := newLifecycleEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"a"}}))
	e.tui(api, "--server", "http://127.0.0.1:1")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "服務未開提示", func(s string) bool { return strings.Contains(s, "無法連線 OpenCode 服務") })
	e.writeTabs(`{"version":9,"secret":"sk-live-should-not-appear"}`)
	e.eventually(3*time.Second, "tab 清單不可讀提示", func(s string) bool { return strings.Contains(s, "無法讀取 tab 清單") })
	time.Sleep(300 * time.Millisecond)
	out := e.diagnose(e.in)
	mustContain(t, out, "最近錯誤：", "[service]", "找不到服務探索檔", "[tabs]", "tab 清單不可讀")
	mustNotContain(t, out, "sk-live", "secret", "version", e.dir)
}

// tmux server 重啟（同一個 socket、新的 server）時，舊實例即使還沒偵測到也不能讓 start 誤以為「已有實例」。
func TestStartAfterTheTmuxServerRestartedTakesOverFromTheStaleInstance(t *testing.T) {
	e := newLifecycleEnv(t, "-server-check", "1h") // 舊實例刻意偵測不到
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	old := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	e.inTmux("kill-server")
	e.inTmux("new-session", "-d", "-s", "again", "-x", "100", "-y", "30")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("server 重啟後 start：%v\n%s", err, out)
	}
	now := e.waitDaemons(e.in, 1, 3*time.Second, "接手後")
	if now[0] == old || alive(old) {
		t.Fatalf("應由新實例接手並讓舊實例退出：舊 %d、新 %v", old, now)
	}
	e.inTmux("show-options", "-g")
	if !strings.Contains(e.allHooks(), "[41700]") {
		t.Fatal("新 server 上應有新實例的 hook")
	}
}

// 非法設定先驗證：start 失敗、既有實例完全不受影響（不交接、不重啟）。
func TestInvalidStartSettingsAreRejectedBeforeTouchingTheRunningInstance(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pid := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	for _, bad := range [][]string{{"-width", "0"}, {"-backstop", "0s"}, {"-width", "-3"}, {"-idle-timeout", "0s"}, {"-server-check", "-1s"}} {
		out, err := e.start(e.in, bad...)
		if err == nil || !strings.Contains(out, "無效") {
			t.Fatalf("%v 應被拒絕：err=%v\n%s", bad, err, out)
		}
	}
	if got := e.daemons(e.in); len(got) != 1 || got[0] != pid {
		t.Fatalf("非法設定不得動到既有實例：%v（原 %d）", got, pid)
	}
}

// TPM 在 run-shell 內執行時沒有 -L／-S，而是用 $TMUX 指向載入設定的那個 server；沒有 $TMUX 也沒有旗標則拒絕（不碰預設 server）。
func TestStartWithoutSocketFlagsFollowsTMUXEnvAndRefusesWithoutIt(t *testing.T) {
	e := newLifecycleEnv(t)
	sock := strings.TrimSpace(e.inTmux("display-message", "-p", "#{socket_path},#{pid},0"))
	cmd := exec.Command(e.daemonBin, "start", "-tabs", e.tabsPath, "-service", e.servicePath, "-show")
	cmd.Env = append(e.cmdEnv(), "TMUX="+sock)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("以 $TMUX 啟動：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "TMUX 啟動")

	bare := exec.Command(e.daemonBin, "start")
	bare.Env = e.cmdEnv()
	if out, err := bare.CombinedOutput(); err == nil || !strings.Contains(string(out), "必須明確指定") {
		t.Fatalf("沒有 socket 資訊時必須拒絕：%v\n%s", err, out)
	}
}

// 實例被 kill -9（沒機會清理）：鎖自動釋放，沒有過期鎖；下次 start 接手，
// 並把前一個實例留下的 @tos_* 孤兒選項（含舊名稱 @tos_sb_main）一併換成新內容。
func TestStartRecoversFromAKilledInstanceWithoutStaleLockOrOrphanOptions(t *testing.T) {
	e := newLifecycleEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}}))
	e.tui(api)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	old := e.waitDaemons(e.in, 1, 3*time.Second, "start")[0]
	e.eventually(3*time.Second, "已顯示", func(s string) bool { return strings.Contains(s, "! j") })
	for _, name := range []string{"@tos_sb_main", "@tos_sb_999", "@tos_whatever"} {
		e.inTmux("set", "-g", name, "orphan from crashed instance")
	}
	must(t, syscall.Kill(old, syscall.SIGKILL))
	e.waitGone(old, 3*time.Second, "kill -9")

	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("鎖不得殘留：%v\n%s", err, out)
	}
	now := e.waitDaemons(e.in, 1, 3*time.Second, "接手")[0]
	if now == old {
		t.Fatal("應是新的行程")
	}
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"only"}}))
	e.eventually(3*time.Second, "新實例接手顯示", func(s string) bool { return strings.Contains(s, "! only") })
	opts := e.inTmux("show-options", "-g")
	for _, name := range []string{"@tos_sb_main", "@tos_sb_999", "@tos_whatever"} {
		if strings.Contains(opts, name+" ") {
			t.Fatalf("前一個實例留下的孤兒選項 %s 應在新實例第一次寫入時清掉", name)
		}
	}
	if !strings.Contains(opts, "@tos_layout_main ") {
		t.Fatal("新實例應發布 @tos_layout_main")
	}
}
