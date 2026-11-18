package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
	"github.com/DachiChang/tmux-opencode-status/internal/fakeoc"
)

// 版面的真實終端驗證（issue #10）：隔離 tmux server 上掛多個 attached client（各自不同的
// pane 尺寸），讀回每個 client 實際繪製的畫面。

// extraClient 在外層 server 開一個新的 attached client，尺寸固定。
func (e *env) extraClient(name string, width, height int) {
	e.t.Helper()
	e.outTmux("new-session", "-d", "-s", name, "-x", fmt.Sprint(width), "-y", fmt.Sprint(height),
		fmt.Sprintf("tmux -L %s -f /dev/null attach -t main", e.in))
}

// clientScreen 是該 client 左側 36 格（含分隔線）逐行畫面，不修剪行尾，以便檢查格寬。
func (e *env) clientScreen(name string) []string {
	e.t.Helper()
	return sidebarRows(e.rawClientScreen(name))
}

func (e *env) rawClientScreen(name string) []string {
	e.t.Helper()
	var out []string
	for _, l := range strings.Split(e.outTmux("capture-pane", "-p", "-t", name), "\n") {
		// 36 格 = 35 格內容 + 1 格分隔線；以格寬切，避免寬字元被切成兩半。
		var b strings.Builder
		w := 0
		for _, c := range cellwidth.Clusters(l) {
			cw := cellwidth.String(c)
			if w+cw > 35 {
				break
			}
			b.WriteString(c)
			w += cw
		}
		out = append(out, b.String())
	}
	return out
}

func (e *env) clientWaits(name string, within time.Duration, what string, ok func([]string) bool) []string {
	e.t.Helper()
	start := time.Now()
	var last []string
	for time.Since(start) < within {
		last = e.clientScreen(name)
		if ok(last) {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("%s：%v 內未達成，%s 畫面：\n%s", what, within, name, strings.Join(last, "\n"))
	return nil
}

// sidebarRows 去掉最底下 tmux 自己的狀態列：side-status 只佔上方 client_height-1 列。
func sidebarRows(ls []string) []string {
	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
		ls = ls[:len(ls)-1]
	}
	if len(ls) > 0 && strings.HasPrefix(ls[len(ls)-1], "[main]") {
		ls = ls[:len(ls)-1]
	}
	return ls
}

func trimRight(ls []string) string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = strings.TrimRight(l, " ")
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func contains(ls []string, s string) bool { return strings.Contains(strings.Join(ls, "\n"), s) }

func TestLayoutOnRealClientsDisambiguationSummaryTruncationAndPerClientHeight(t *testing.T) {
	e := newEnv(t)
	svc := fakeoc.New(t)
	for i := 0; i < 12; i++ {
		svc.AddSession(fmt.Sprintf("ses_%02d", i), "")
	}
	svc.WriteServiceFile(e.servicePath)

	a := filepath.Join(e.dir, "aicova", "api")
	p := filepath.Join(e.dir, "profcoo", "api")
	infra := filepath.Join(e.dir, "infra")
	base := map[string]int{a: 0, p: 4, infra: 8}
	tabs := func(dir string, titles ...string) string {
		var items []string
		for i, ti := range titles {
			items = append(items, fmt.Sprintf(`{"sessionID":"ses_%02d","title":%q}`, base[dir]+i, ti))
		}
		return `"` + dir + `":{"tabs":[` + strings.Join(items, ",") + `],"unread":{}}`
	}
	e.writeTabs(`{"global":{"tabs":[],"unread":{}},"cwd":{` + strings.Join([]string{
		tabs(a, "修正登入逻辑 bug", "PR #42 #[fg=red] #{pane_id} #(touch "+e.dir+"/PWNED)", "寫單元測試 這是一個非常非常長的標題需要被截斷成省略號", "quiet"),
		tabs(p, "migrate db", "docs", "e\u0301clair 👨‍👩‍👧 family 🇹🇼", "refactor"),
		tabs(infra, "terraform plan", "k8s upgrade", "ok", "last"),
	}, ",") + `}}`)
	e.tui(a, "--server", "http://127.0.0.1:1")
	e.tui(p, "--server", "http://127.0.0.1:1")
	e.tui(infra, "--server", "http://127.0.0.1:1")

	e.extraClient("c9", 100, 9)
	e.extraClient("c14", 100, 14)
	e.extraClient("c40", 100, 40)
	e.clientWaits("c40", 6*time.Second, "服務接上，tab 回到閒置", func(s []string) bool { return contains(s, "· last") })
	svc.Start("ses_00")
	svc.Start("ses_05")
	svc.Finish("ses_01", "failed")
	svc.Finish("ses_06", "succeeded")

	full := e.clientWaits("c40", 6*time.Second, "三個目錄與狀態都出現", func(s []string) bool {
		return contains(s, "● 修正登入逻辑 bug") && contains(s, "✗ PR #42") && contains(s, "◆ ") && contains(s, "· last")
	})
	t.Logf("c40（完整）：\n%s", trimRight(full))

	whole := trimRight(full)
	if !contains(full, " ▎aicova/api") || !contains(full, " ▎profcoo/api") || !contains(full, " ▎infra") {
		t.Fatalf("目錄名稱不符：\n%s", whole)
	}
	if !(strings.Index(whole, "aicova/api") < strings.Index(whole, "infra") && strings.Index(whole, "infra") < strings.Index(whole, "profcoo/api")) {
		t.Fatalf("目錄應依完整路徑排序（aicova < infra < profcoo）：\n%s", whole)
	}
	for i, l := range full {
		if w := cellwidth.String(strings.TrimRight(l, " ")); w > 34 {
			t.Fatalf("第 %d 行可見寬 %d 越界（最右一格須保留空白）：%q", i, w, l)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(full[0], " "), "●2 ◆1 ✗1") {
		t.Fatalf("頂部摘要應為 ●2 ◆1 ✗1（閒置省略）：%q", full[0])
	}
	if _, err := os.Stat(filepath.Join(e.dir, "PWNED")); err == nil {
		t.Fatal("標題中的 #() 被執行了")
	}
	if !contains(full, "✗ PR #42 #[fg=red] #{pane_id} #…") {
		t.Fatalf("特殊字元標題應被字面畫出：\n%s", whole)
	}
	if !contains(full, "…") {
		t.Fatalf("長標題應以 … 截斷：\n%s", whole)
	}

	small := e.clientScreen("c9")
	mid := e.clientScreen("c14")
	t.Logf("c9（可用 8 列）：\n%s\n\nc14（可用 13 列）：\n%s", trimRight(small), trimRight(mid))
	summary := func(ls []string) string {
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ls[0]), "OPENCODE"))
	}
	for name, scr := range map[string][]string{"c9": small, "c14": mid} {
		if summary(scr) != summary(full) {
			t.Fatalf("%s 頂部摘要 %q 應與完整 client 相同 %q（含畫面外 tabs）", name, summary(scr), summary(full))
		}
		shown, hidden := 0, 0
		for _, l := range scr[1:] { // 第一行是 OPENCODE 底塊
			if strings.HasPrefix(l, "  ") && strings.TrimSpace(l) != "" {
				shown++
			}
			if strings.Contains(l, "… 還有") {
				fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "… 還有")), "%d", &hidden)
			}
		}
		if hidden == 0 || shown+hidden != 12 {
			t.Fatalf("%s：可見 %d + 未呈現 %d 應等於 12 個 tab 且有「… 還有 N 個 tab」：\n%s", name, shown, hidden, trimRight(scr))
		}
	}
	if contains(full, "… 還有") {
		t.Fatalf("完整 client 不應有「… 還有」：\n%s", whole)
	}

	beforeMid := trimRight(mid)
	e.outTmux("resize-window", "-t", "c9", "-y", "40")
	got := e.clientWaits("c9", 3*time.Second, "c9 放大後完整顯示", func(s []string) bool { return !contains(s, "… 還有") && contains(s, "· last") })
	t.Logf("c9 resize 為 40 列：\n%s", trimRight(got))
	if after := trimRight(e.clientScreen("c14")); after != beforeMid {
		t.Fatalf("resize c9 不得影響 c14：\n%s\n---\n%s", beforeMid, after)
	}
}

func TestEmptyAndDataProblemFramesOnARealClient(t *testing.T) {
	e := newEnv(t)
	e.extraClient("c20", 100, 20)
	got := e.clientWaits("c20", 4*time.Second, "沒有 TUI", func(s []string) bool { return contains(s, "沒有 OpenCode TUI") })
	t.Logf("沒有 TUI：\n%s", trimRight(got))
	if contains(got, "無 tab") || contains(got, "無法") {
		t.Fatalf("空清單不得混入其他提示：\n%s", trimRight(got))
	}

	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {}}))
	e.tui(dir)
	got = e.clientWaits("c20", 4*time.Second, "有 TUI 無 tab", func(s []string) bool { return contains(s, "無 tab") })
	t.Logf("有 TUI 無 tab：\n%s", trimRight(got))
	if contains(got, "沒有 OpenCode TUI") {
		t.Fatalf("有 TUI 時不得顯示沒有 TUI：\n%s", trimRight(got))
	}

	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"keep me"}}))
	e.clientWaits("c20", 4*time.Second, "有 tab", func(s []string) bool { return contains(s, "keep me") })
	e.writeTabs(`{"global":`)
	got = e.clientWaits("c20", 6*time.Second, "不可讀提示", func(s []string) bool { return contains(s, "無法讀取 tab 清單") })
	t.Logf("tab 清單不可讀：\n%s", trimRight(got))
	if !contains(got, "keep me") || contains(got, "無 tab") {
		t.Fatalf("應保留最後清單且不是空清單：\n%s", trimRight(got))
	}
	for i, l := range got {
		if w := cellwidth.String(strings.TrimRight(l, " ")); w > 34 {
			t.Fatalf("第 %d 行可見寬 %d 越界：%q", i, w, l)
		}
	}
}

// 換行、控制字元、雙向控制、tmux format 字樣與孤立組合字元，從 tab 清單一路到真實 client 畫面：
// 每個標題恰好占一行，沒有任何內容被當成格式或指令執行。
func TestHostileTitlesStayInsideTheirRowOnARealClient(t *testing.T) {
	e := newEnv(t)
	e.extraClient("c30", 100, 30)
	dir := filepath.Join(e.dir, "work", "api")
	pwn := filepath.Join(e.dir, "PWNED")
	titles := []string{
		"line1\nline2\r\nline3",
		"ctl\x01\x02\x1b[31mred\x07bell\x7f",
		"#(touch " + pwn + ")",
		"#{pane_id} #[fg=red] ##[bold] #",
		"\u202eevil\u202c \u2066iso\u2069",
		"\ufe0f\u200d\u0301lead",
		"中文 組合 e\u0301e\u0301e\u0301 👨‍👩‍👧👨‍👩‍👧👨‍👩‍👧 🇹🇼🇯🇵 long long long long long long",
	}
	e.writeTabs(e.tabsJSON(map[string][]string{dir: titles}))
	e.tui(dir)
	got := e.clientWaits("c30", 5*time.Second, "六個 tab 都出現", func(s []string) bool { return contains(s, "lead") && contains(s, "中文") })
	t.Logf("惡意標題：\n%s", trimRight(got))
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("標題中的 #() 被執行了")
	}
	var tabRows []string
	for _, l := range got[1:] { // 第一行是 OPENCODE 底塊
		if strings.HasPrefix(l, "  ") && strings.TrimSpace(l) != "" {
			tabRows = append(tabRows, l)
		}
	}
	// line1\nline2\r\nline3 的換行變成空白，仍是同一個 tab 同一行
	if len(tabRows) != len(titles) {
		t.Fatalf("六個標題應恰好占六行，實際 %d：\n%s", len(tabRows), trimRight(got))
	}
	for i, l := range got {
		if w := cellwidth.String(strings.TrimRight(l, " ")); w > 34 {
			t.Fatalf("第 %d 行可見寬 %d 越界：%q", i, w, l)
		}
	}
	if !contains(got, "line1 line2  line3") || !contains(got, "#(touch /") || !contains(got, "#{pane_id} #[fg=red] ##[bold]…") {
		t.Fatalf("特殊字元應被字面畫出：\n%s", trimRight(got))
	}
}
