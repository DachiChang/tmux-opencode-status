package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 點擊跳轉（issue #18）：隔離 tmux server 上由真實 attached client 收到 SGR 滑鼠序列，
// 觀察綁定生命週期、跨 session 跳轉與非插件 range 的行為。全部使用 -L 隔離 server。

const (
	defaultJumpBinding = "switch-client -t ="
	jumpKey            = "MouseDown1SideStatus"
)

// binding 是 root 鍵表中 MouseDown1SideStatus 的綁定內容（不含前綴）。
func (e *env) binding() string {
	e.t.Helper()
	for _, l := range strings.Split(e.inTmux("list-keys", "-T", "root"), "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && f[3] == jumpKey {
			return strings.TrimSpace(strings.TrimPrefix(l[strings.Index(l, jumpKey)+len(jumpKey):], " "))
		}
	}
	e.t.Fatalf("找不到 %s 綁定", jumpKey)
	return ""
}

// keysWithoutJump 是 list-keys 去掉 MouseDown1SideStatus 那一行；插件只接管那一個綁定。
func keysWithoutJump(keys string) string {
	var out []string
	for _, l := range strings.Split(keys, "\n") {
		if f := strings.Fields(l); len(f) >= 4 && f[3] == jumpKey {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func (e *env) pluginBound() bool {
	b := e.binding()
	return strings.Contains(b, "mouse_status_range") && strings.Contains(b, "switch-client")
}

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
	e.t.Fatalf("%s：%v 內未達成，目前綁定：%s", what, within, last)
}

// click 對承載 client 送出 SGR 滑鼠左鍵按下與放開（座標從 1 起算，x 為欄、y 為列）。
func (e *env) click(x, y int) {
	e.t.Helper()
	e.outTmux("send-keys", "-t", "hold", "-l", fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x, y, x, y))
}

// where 是承載 client 目前所在的 session／window／pane。
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
	e.t.Fatalf("%s：%v 內未達成，client 在 %q，要 %q", what, within, last, want)
}

func (e *env) sidebarRow(contains string) int {
	e.t.Helper()
	for i, l := range strings.Split(e.sidebar(), "\n") {
		if strings.Contains(l, contains) {
			return i + 1
		}
	}
	e.t.Fatalf("側欄找不到 %q：\n%s", contains, e.sidebar())
	return 0
}

// jumpWorld：client 在 main session；另一個 session "work" 的某 window 有兩個 pane，
// TUI 在非 active 的那個。回傳該 window 與 TUI pane。
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
		t.Fatalf("啟動前應是 tmux 預設：%q", got)
	}
	keys := keysWithoutJump(e.inTmux("list-keys"))
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("start：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.waitBinding(3*time.Second, "啟動後為插件綁定", func(b string) bool { return strings.Contains(b, "mouse_status_range") })
	if !e.pluginBound() {
		t.Fatalf("不是插件綁定：%s", e.binding())
	}

	// 重載設定（交接）：舊實例退出、新實例接手，綁定仍是插件的
	if out, err := e.start(e.in, "-show", "-resync", "61s"); err != nil {
		t.Fatalf("交接 start：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "交接後")
	time.Sleep(500 * time.Millisecond)
	if !e.pluginBound() {
		t.Fatalf("交接後應仍是插件綁定：%s", e.binding())
	}

	if out, err := e.cli("stop", e.in); err != nil {
		t.Fatalf("stop：%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 0, 3*time.Second, "stop")
	if got := e.binding(); got != defaultJumpBinding {
		t.Fatalf("stop 後應寫回 tmux 預設：%q", got)
	}
	if after := keysWithoutJump(e.inTmux("list-keys")); after != keys {
		t.Fatal("插件只應動 MouseDown1SideStatus，其他綁定被改變")
	}
}

func TestClickingAGroupRowJumpsAcrossSessionsToTheNonActiveTUIPane(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"修正登入", "second tab"}}))
	window, tuiPane := e.jumpWorld(dir)
	e.waitBinding(3*time.Second, "插件綁定", func(b string) bool { return strings.Contains(b, "mouse_status_range") })
	e.eventually(3*time.Second, "側欄出現群組", func(s string) bool { return strings.Contains(s, "api") && strings.Contains(s, "second tab") })
	want := "work " + window + " " + tuiPane
	if e.where() == want {
		t.Fatal("測試前提：client 一開始不該在目標")
	}

	e.click(5, e.sidebarRow("api"))
	e.waitWhere(3*time.Second, "點目錄列", want)
	if active := strings.TrimSpace(e.inTmux("display-message", "-p", "-t", "work:", "#{pane_id}")); active != tuiPane {
		t.Fatalf("目標 pane 應成為 window 的 active pane：%s", active)
	}

	// 回到 main，改點 tab 列：結果相同
	e.inTmux("switch-client", "-t", "main")
	e.waitWhere(3*time.Second, "回到 main", "main @0 %0")
	e.eventually(3*time.Second, "畫面回到 main", func(string) bool {
		return strings.Contains(e.outTmux("capture-pane", "-p", "-t", "hold"), "[main]")
	})
	time.Sleep(1500 * time.Millisecond) // 避開 tmux 的連點判定：太快的第二次點擊會變成 DoubleClick1SideStatus
	e.click(5, e.sidebarRow("second tab"))
	e.waitWhere(3*time.Second, "點 tab 列", want)
}

func TestClickingTheNoTabRowJumpsLikeTheGroupRow(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	window, tuiPane := e.jumpWorld(dir)
	e.waitBinding(3*time.Second, "插件綁定", func(b string) bool { return strings.Contains(b, "mouse_status_range") })
	e.eventually(3*time.Second, "出現無 tab", func(s string) bool { return strings.Contains(s, "無 tab") })
	e.click(5, e.sidebarRow("無 tab"))
	e.waitWhere(3*time.Second, "點無 tab 列", "work "+window+" "+tuiPane)
}

func TestClickingNonGroupRowsStaysPut(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"one"}}))
	e.jumpWorld(dir)
	e.waitBinding(3*time.Second, "插件綁定", func(b string) bool { return strings.Contains(b, "mouse_status_range") })
	e.eventually(3*time.Second, "側欄出現群組", func(s string) bool { return strings.Contains(s, "one") })
	before := e.where()
	for _, y := range []int{1, 2, 3 + 5, 20} { // 標題、分隔線、群組後的空白處、更下方的空白處
		if y == 3+5 || y == 20 {
			y = max(y, e.sidebarRow("one")+2)
		}
		e.click(5, y)
		time.Sleep(400 * time.Millisecond)
		if got := e.where(); got != before {
			t.Fatalf("點第 %d 列不應跳轉：%q → %q", y, before, got)
		}
	}
}

func TestClickingAForeignRangeBehavesLikeTmuxDefault(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"one"}}))
	e.jumpWorld(dir)
	e.waitBinding(3*time.Second, "插件綁定", func(b string) bool { return strings.Contains(b, "mouse_status_range") })
	// main session 自己的 side-status-format 改成 tmux 預設風格的 range=session 列表
	e.inTmux("set", "-t", "main", "side-status-format", "#{S:#[range=session|#{session_id}]#{session_name}#[norange]#[nl]}")
	time.Sleep(300 * time.Millisecond)
	// 列表依 session 名稱排序：main、work；點第二列
	e.click(2, 2)
	e.waitWhere(3*time.Second, "非插件 range 走 switch-client -t =", "work "+strings.TrimSpace(e.inTmux("display-message", "-p", "-t", "work:", "#{window_id}"))+" "+strings.TrimSpace(e.inTmux("display-message", "-p", "-t", "work:", "#{pane_id}")))
}
