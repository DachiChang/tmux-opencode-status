package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 鍵盤跳轉：隔離 tmux server 上由真實 attached client 以 `send-keys -K` 送鍵
// （-K 讓鍵走 client 的按鍵表查找，與使用者在終端機按鍵相同）。全部使用 -L 隔離 server。

const jumpTable = "opencode-status-jump"

// clients 是被觀察 server 上所有 client 的名稱（依名稱排序）。
func (e *env) clients() []string {
	e.t.Helper()
	names := strings.Fields(e.inTmux("list-clients", "-F", "#{client_name}"))
	return names
}

// client 是承載 client（唯一的 client）。
func (e *env) client() string {
	e.t.Helper()
	cs := e.clients()
	if len(cs) != 1 {
		e.t.Fatalf("預期只有一個 client：%v", cs)
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
	e.t.Fatalf("client_key_table：%v 內未變成 %q，目前 %q", within, want, last)
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
	e.t.Fatalf("prefix %s 的綁定：%v 內未變成 %q，目前 %q", key, within, want, last)
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

// jumpTableKeys 是 opencode-status-jump 表的綁定行；表不存在時為空。
func (e *env) jumpTableKeys() string {
	out, err := e.tryInTmux("list-keys", "-T", jumpTable)
	if err != nil {
		return ""
	}
	return out
}

const switchToJump = "switch-client -T " + jumpTable

// keyJumpWorld 準備兩個目錄群組（api、web）的 TUI：api 的 TUI 在另一個 session 的非 active pane，
// web 在 main session 的另一個 window。回傳 api 的 window 與 pane。
func (e *env) keyJumpWorld() (apiDir, window, tuiPane string) {
	e.t.Helper()
	apiDir = filepath.Join(e.dir, "work", "api")
	webDir := filepath.Join(e.dir, "work", "web")
	e.writeTabs(e.tabsJSON(map[string][]string{apiDir: {"修正登入", "second tab"}, webDir: {"網站"}}))
	window, tuiPane = e.jumpWorld(apiDir)
	e.tui(webDir)
	e.eventually(5*time.Second, "側欄出現兩個群組", func(s string) bool {
		return strings.Contains(s, "api") && strings.Contains(s, "web") && strings.Contains(s, "網站")
	})
	return apiDir, window, tuiPane
}

func TestPrefixTriggerShowsLettersAndALetterJumpsAcrossSessions(t *testing.T) {
	e := newEnv(t)
	_, window, tuiPane := e.keyJumpWorld()
	c := e.client()
	if got := e.keyTable(c); got != "root" {
		t.Fatalf("測試前提：client_key_table = %q", got)
	}
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)

	e.sendKeys(c, "C-b", "A")
	e.waitKeyTable(c, 3*time.Second, jumpTable)
	e.eventually(3*time.Second, "群組列出現字母標籤", func(s string) bool {
		return strings.Contains(s, " a  api") && strings.Contains(s, " b  web") && !strings.Contains(s, "▎")
	})

	e.sendKeys(c, "a")
	e.waitWhere(3*time.Second, "按 a 跳到 api 的 TUI", "work "+window+" "+tuiPane)
	e.waitKeyTable(c, 3*time.Second, "root")
	e.eventually(3*time.Second, "標籤消失、回到一般版面", func(s string) bool {
		return strings.Contains(s, "▎api") && strings.Contains(s, "▎web") && !strings.Contains(s, " a  api")
	})
}

func TestEscUnboundKeysAndLettersWithoutAGroupCancelWithoutMovingOrTyping(t *testing.T) {
	e := newEnv(t)
	e.keyJumpWorld()
	c := e.client()
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)
	// 主 pane 改跑 cat：任何送進 pane 的鍵都會在按 Enter 後寫進檔案。
	typed := filepath.Join(e.dir, "typed")
	e.inTmux("respawn-pane", "-k", "-t", "main:0", "cat > "+typed)
	start := e.where()

	for _, key := range []string{"Escape", "x", "f", "Space", "1"} { // x、f 沒有群組（只有 a、b）；其餘沒綁定
		e.sendKeys(c, "C-b", "A")
		e.waitKeyTable(c, 3*time.Second, jumpTable)
		e.sendKeys(c, key)
		e.waitKeyTable(c, 3*time.Second, "root")
		if got := e.where(); got != start {
			t.Fatalf("按 %s 後位置改變：%q → %q", key, start, got)
		}
	}
	e.inTmux("send-keys", "-t", "main:0", "Enter") // 沒送進 pane 的鍵不會在這一行之前出現
	e.eventually(3*time.Second, "pane 收到 Enter", func(string) bool {
		b, _ := os.ReadFile(typed)
		return len(b) > 0
	})
	if b, _ := os.ReadFile(typed); string(b) != "\n" {
		t.Fatalf("取消時按下的鍵不應送進 pane，pane 收到 %q", b)
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
		t.Fatalf("找不到第二個 client：%v", e.clients())
	}
	e.clientWaits("second", 3*time.Second, "第二個 client 有一般版面", func(ls []string) bool {
		return strings.Contains(strings.Join(ls, "\n"), "▎api")
	})
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)

	e.sendKeys(first, "C-b", "A")
	e.waitKeyTable(first, 3*time.Second, jumpTable)
	e.eventually(3*time.Second, "第一個 client 出現標籤", func(s string) bool { return strings.Contains(s, " a  api") })
	time.Sleep(300 * time.Millisecond)
	if got := e.keyTable(second); got != "root" {
		t.Fatalf("第二個 client 的 key table = %q", got)
	}
	joined := strings.Join(e.clientScreen("second"), "\n")
	if strings.Contains(joined, " a  api") || !strings.Contains(joined, "▎api") {
		t.Fatalf("第二個 client 的側欄不應改變：\n%s", joined)
	}
}

func TestUsersRootAliasEntersJumpModeAndTheSidebarCanBeHidden(t *testing.T) {
	e := newEnv(t)
	_, window, tuiPane := e.keyJumpWorld()
	c := e.client()
	e.inTmux("bind", "-n", "M-a", "switch-client", "-T", jumpTable)
	e.inTmux("set", "-g", "side-status", "off") // 側欄隱藏時仍可進入跳轉模式並憑記憶按字母
	e.sendKeys(c, "M-a")
	e.waitKeyTable(c, 3*time.Second, jumpTable)
	e.sendKeys(c, "a")
	e.waitWhere(3*time.Second, "root 別名進入後按 a", "work "+window+" "+tuiPane)
	e.waitKeyTable(c, 3*time.Second, "root")
}

func TestChangingTheTriggerAndReloadingHandsOffAndStopRemovesEverything(t *testing.T) {
	e := newLifecycleEnv(t)
	e.inTmux("bind", "-n", "M-z", "display-message", "mine") // 使用者自己的綁定，全程不變
	keysBefore := keysWithoutJump(e.inTmux("list-keys"))
	prefixBefore := e.inTmux("list-keys", "-T", "prefix")

	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start：%v\n%s", err, out)
	}
	pids := e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)
	e.eventually(3*time.Second, "@tos_jump_trigger 與版面一起發布", func(string) bool { return e.globalOption("@tos_jump_trigger") == "A" })
	if !strings.Contains(e.jumpTableKeys(), " a ") {
		t.Fatalf("啟動後應有跳轉按鍵表：%q", e.jumpTableKeys())
	}

	// 重載同一設定：不交接、實例不變
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("重載 start：%v\n%s", err, out)
	}
	if got := e.daemons(e.in); len(got) != 1 || got[0] != pids[0] {
		t.Fatalf("設定沒變不應交接：%v → %v", pids, got)
	}

	// 改觸發鍵後重載：交接，舊鍵消失、新鍵生效
	e.inTmux("set", "-g", "@opencode-status-jump-trigger", "M-j")
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("改鍵後 start：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "交接後")
	e.waitPrefixBinding(3*time.Second, "M-j", switchToJump)
	if got := e.prefixBinding("A"); got != "" {
		t.Fatalf("舊觸發鍵 A 應被 unbind，仍是 %q", got)
	}
	e.eventually(3*time.Second, "@tos_jump_trigger 改為 M-j", func(string) bool { return e.globalOption("@tos_jump_trigger") == "M-j" })
	if got := e.daemons(e.in); len(got) != 1 || got[0] == pids[0] {
		t.Fatalf("改鍵後應由新實例接手：%v → %v", pids, got)
	}
	c := e.client()
	e.sendKeys(c, "C-b", "M-j")
	e.waitKeyTable(c, 3*time.Second, jumpTable)
	e.sendKeys(c, "Escape")
	e.waitKeyTable(c, 3*time.Second, "root")

	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("stop：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 0, 3*time.Second, "stop")
	if got := e.prefixBinding("M-j"); got != "" {
		t.Fatalf("stop 後觸發鍵應消失：%q", got)
	}
	if got := e.jumpTableKeys(); got != "" {
		t.Fatalf("stop 後跳轉按鍵表應不存在：%q", got)
	}
	if got := e.tosOptions(); len(got) != 0 {
		t.Fatalf("stop 後不應留下 @tos_*：%v", got)
	}
	if got := e.globalOption("@opencode-status-jump-trigger"); got != "M-j" {
		t.Fatalf("使用者的設定不應被動：%q", got)
	}
	if after := keysWithoutJump(e.inTmux("list-keys")); after != keysBefore {
		t.Fatal("stop 後其他綁定被改變")
	}
	if after := e.inTmux("list-keys", "-T", "prefix"); after != prefixBefore {
		t.Fatalf("stop 後 prefix 表應與啟動前相同（插件不寫回也不留下任何東西）\n前：\n%s\n後：\n%s", prefixBefore, after)
	}
}

func TestInvalidTriggerBindsNoPrefixKeyAndDiagnoseReportsItAsAProblem(t *testing.T) {
	for name, value := range map[string]string{"空字串": "", "不認得的鍵名": "notakey", "含空白": "A B"} {
		t.Run(name, func(t *testing.T) {
			e := newLifecycleEnv(t)
			prefixBefore := e.inTmux("list-keys", "-T", "prefix")
			e.inTmux("set", "-g", "@opencode-status-jump-trigger", value)
			if out, err := e.start(e.in, "-show"); err != nil {
				t.Fatalf("start：%v\n%s", err, out)
			}
			e.waitDaemons(e.in, 1, 3*time.Second, "start")
			e.eventually(3*time.Second, "常駐程式已發布", func(string) bool { return e.globalOption("@tos_layout_main") != "" || len(e.tosOptions()) > 0 })
			time.Sleep(300 * time.Millisecond)
			if after := e.inTmux("list-keys", "-T", "prefix"); after != prefixBefore {
				t.Fatalf("無效觸發鍵不應綁任何 prefix 鍵（含 A、也不退回預設）\n前：\n%s\n後：\n%s", prefixBefore, after)
			}
			if got := e.globalOption("@tos_jump_trigger"); got != "" {
				t.Fatalf("沒有實際綁定的鍵，@tos_jump_trigger = %q", got)
			}
			out := e.diagnose(e.in)
			mustContain(t, out, "鍵盤跳轉：無效設定", "發現 1 項問題")
		})
	}
}

func TestDiagnoseReportsTheTriggerAsBoundThenOverwrittenAndOtherSettingsAsExpected(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.waitPrefixBinding(3*time.Second, "A", switchToJump)
	e.eventually(3*time.Second, "已發布", func(string) bool { return e.globalOption("@tos_jump_trigger") == "A" })
	out := e.diagnose(e.in)
	mustContain(t, out, "鍵盤跳轉：已綁定", "未設定，預設", "實際綁定的鍵 A", "結論：未發現問題")

	e.inTmux("bind", "-T", "prefix", "A", "display-message", "taken")
	out = e.diagnose(e.in)
	mustContain(t, out, "鍵盤跳轉：已被覆寫", "發現 1 項問題")

	// 改了設定但尚未重載：顯示設定與實際綁定的差異，不算問題
	e.inTmux("bind", "-T", "prefix", "A", "switch-client", "-T", jumpTable)
	e.inTmux("set", "-g", "@opencode-status-jump-trigger", "M-j")
	out = e.diagnose(e.in)
	mustContain(t, out, "鍵盤跳轉：設定的觸發鍵 M-j，實際綁定的鍵 A", "重新載入 tmux 設定後生效", "結論：未發現問題")
}

func TestDiagnoseReportsABindingFailureWhenTmuxRefusesTheTrigger(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "已發布", func(string) bool { return e.globalOption("@tos_jump_trigger") == "A" })
	// 模擬「常駐程式在執行但沒有實際綁定的鍵」：實際綁定的記錄消失，設定的鍵仍有效。
	e.inTmux("set", "-gu", "@tos_jump_trigger")
	out := e.diagnose(e.in)
	mustContain(t, out, "鍵盤跳轉：綁定失敗", "發現 1 項問題")
}
