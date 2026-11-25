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

// Real-terminal verification of the layout (issue #10): several attached clients (each with a different
// pane size) on an isolated tmux server, reading back the screen each client actually draws.

// extraClient opens a new attached client on the outer server with a fixed size.
func (e *env) extraClient(name string, width, height int) {
	e.t.Helper()
	e.outTmux("new-session", "-d", "-s", name, "-x", fmt.Sprint(width), "-y", fmt.Sprint(height),
		fmt.Sprintf("tmux -L %s -f /dev/null attach -t main", e.in))
}

// clientScreen is the client's leftmost 36 cells (including the divider), line by line, with trailing whitespace not trimmed so cell widths can be checked.
func (e *env) clientScreen(name string) []string {
	e.t.Helper()
	return sidebarRows(e.rawClientScreen(name))
}

func (e *env) rawClientScreen(name string) []string {
	e.t.Helper()
	var out []string
	for _, l := range strings.Split(e.outTmux("capture-pane", "-p", "-t", name), "\n") {
		// 36 cells = 35 cells of content + 1 cell of divider; cut by cell width so wide characters are not split in half.
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
	e.t.Fatalf("%s: not reached within %v, %s screen:\n%s", what, within, name, strings.Join(last, "\n"))
	return nil
}

// sidebarRows drops the bottom row that is tmux's own status line: side-status occupies only the top client_height-1 rows.
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

func TestLayoutOnRealClientsDisambiguationSummarySingleLayoutAndTmuxTruncation(t *testing.T) {
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
	e.clientWaits("c40", 6*time.Second, "service connects, tabs return to idle", func(s []string) bool { return contains(s, "· last") })
	svc.Start("ses_00")
	svc.Start("ses_05")
	svc.Finish("ses_01", "failed")
	svc.Finish("ses_06", "succeeded")

	full := e.clientWaits("c40", 6*time.Second, "all three directories and statuses appear", func(s []string) bool {
		return contains(s, "● 修正登入逻辑 bug") && contains(s, "✗ PR #42") && contains(s, "◆ ") && contains(s, "· last")
	})
	t.Logf("c40 (full):\n%s", trimRight(full))

	whole := trimRight(full)
	if !contains(full, " ▎aicova/api") || !contains(full, " ▎profcoo/api") || !contains(full, " ▎infra") {
		t.Fatalf("directory names do not match:\n%s", whole)
	}
	if !(strings.Index(whole, "aicova/api") < strings.Index(whole, "infra") && strings.Index(whole, "infra") < strings.Index(whole, "profcoo/api")) {
		t.Fatalf("directories should be sorted by full path (aicova < infra < profcoo):\n%s", whole)
	}
	for i, l := range full {
		if w := cellwidth.String(strings.TrimRight(l, " ")); w > 34 {
			t.Fatalf("line %d has visible width %d, out of bounds (the rightmost cell must stay blank): %q", i, w, l)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(full[0], " "), "●2 ◆1 ✗1") {
		t.Fatalf("the top summary should be ●2 ◆1 ✗1 (idle omitted): %q", full[0])
	}
	if _, err := os.Stat(filepath.Join(e.dir, "PWNED")); err == nil {
		t.Fatal("#() in a title was executed")
	}
	if !contains(full, "✗ PR #42 #[fg=red] #{pane_id} #…") {
		t.Fatalf("titles with special characters should be drawn literally:\n%s", whole)
	}
	if !contains(full, "…") {
		t.Fatalf("a long title should be truncated with …:\n%s", whole)
	}

	small := e.clientWaits("c9", 3*time.Second, "c9 receives the latest layout", func(s []string) bool { return contains(s, "● 修正登入逻辑 bug") && contains(s, "✗ PR #42") })
	mid := e.clientWaits("c14", 3*time.Second, "c14 receives the latest layout", func(s []string) bool { return contains(s, "● 修正登入逻辑 bug") && contains(s, "✗ PR #42") })
	t.Logf("c9 (8 rows available):\n%s\n\nc14 (13 rows available):\n%s", trimRight(small), trimRight(mid))
	summary := func(ls []string) string {
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ls[0]), "OPENCODE"))
	}
	var layouts []string
	for _, l := range strings.Split(e.inTmux("show-options", "-g"), "\n") {
		if strings.HasPrefix(l, "@tos_") {
			layouts = append(layouts, l)
		}
	}
	var names []string
	for _, l := range layouts {
		n, _, _ := strings.Cut(l, " ")
		names = append(names, n)
	}
	// Globally there are only two layouts (normal and jump, independent of height) plus one letter per group and the actually bound trigger key.
	if want := "@tos_jump_key_a,@tos_jump_key_b,@tos_jump_key_c,@tos_jump_trigger,@tos_layout_jump,@tos_layout_main"; strings.Join(names, ",") != want {
		t.Fatalf("global options = %q, want %q", names, want)
	}
	const wantFormat = "#{?#{==:#{client_key_table},opencode-status-jump},#{@tos_layout_jump},#{@tos_layout_main}}"
	if got := strings.TrimSpace(e.inTmux("show-options", "-gv", "side-status-format")); got != wantFormat {
		t.Fatalf("side-status-format should be fixed to %q: %q", wantFormat, got)
	}

	for name, scr := range map[string][]string{"c9": small, "c14": mid} {
		if summary(scr) != summary(full) {
			t.Fatalf("%s top summary %q should match the full client's %q (including off-screen tabs)", name, summary(scr), summary(full))
		}
		if len(scr) >= len(full) {
			t.Fatalf("%s should be shorter than the full client (tmux truncates from the bottom): %d vs %d rows", name, len(scr), len(full))
		}
		for i, l := range scr {
			if strings.TrimRight(l, " ") != strings.TrimRight(full[i], " ") {
				t.Fatalf("%s row %d should match the full layout (truncated from the bottom only):\n%q\n%q", name, i, l, full[i])
			}
		}
		if contains(scr, "· refactor") {
			t.Fatalf("%s: the last tab should be off-screen:\n%s", name, trimRight(scr))
		}
	}
	if last := strings.Fields(strings.TrimSpace(strings.Join(full, "\n"))); last[len(last)-1] != "refactor" {
		t.Fatalf("the full client's last row should be the last tab, not a notice row:\n%s", whole)
	}

	beforeMid := trimRight(mid)
	e.outTmux("resize-window", "-t", "c9", "-y", "40")
	got := e.clientWaits("c9", 3*time.Second, "c9 shows everything after being enlarged", func(s []string) bool { return contains(s, "· last") })
	t.Logf("c9 resized to 40 rows:\n%s", trimRight(got))
	if after := trimRight(e.clientScreen("c14")); after != beforeMid {
		t.Fatalf("resizing c9 must not affect c14:\n%s\n---\n%s", beforeMid, after)
	}
}

func TestEmptyAndDataProblemFramesOnARealClient(t *testing.T) {
	e := newEnv(t)
	e.extraClient("c20", 100, 20)
	got := e.clientWaits("c20", 4*time.Second, "no TUI", func(s []string) bool { return contains(s, "No OpenCode TUI running") })
	t.Logf("no TUI:\n%s", trimRight(got))
	if contains(got, "No tabs") || contains(got, "Cannot ") {
		t.Fatalf("an empty list must not mix in other notices:\n%s", trimRight(got))
	}

	dir := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{dir: {}}))
	e.tui(dir)
	got = e.clientWaits("c20", 4*time.Second, "TUI without tabs", func(s []string) bool { return contains(s, "No tabs") })
	t.Logf("TUI without tabs:\n%s", trimRight(got))
	if contains(got, "No OpenCode TUI running") {
		t.Fatalf("the no-TUI notice must not be shown while a TUI exists:\n%s", trimRight(got))
	}

	e.writeTabs(e.tabsJSON(map[string][]string{dir: {"keep me"}}))
	e.clientWaits("c20", 4*time.Second, "has tabs", func(s []string) bool { return contains(s, "keep me") })
	e.writeTabs(`{"global":`)
	got = e.clientWaits("c20", 6*time.Second, "unreadable notice", func(s []string) bool {
		for _, row := range s {
			if strings.TrimSpace(row) == "▲ Cannot read tab list" {
				return true
			}
		}
		return false
	})
	t.Logf("tab list unreadable:\n%s", trimRight(got))
	warnings := 0
	for _, row := range got {
		switch row := strings.TrimSpace(row); {
		case row == "▲ Cannot read tab list":
			warnings++
		case row == "▲ Cannot connect to OpenCode":
			// A separate warning for the disconnected service.
		case strings.HasPrefix(row, "▲"):
			t.Fatalf("Unexpected warning row: %q", row)
		}
	}
	if warnings != 1 {
		t.Fatalf("Expected one tab-list warning, got %d:\n%s", warnings, trimRight(got))
	}
	if !contains(got, "keep me") || contains(got, "No tabs") {
		t.Fatalf("the last list should be kept and not be an empty list:\n%s", trimRight(got))
	}
	for i, l := range got {
		if w := cellwidth.String(strings.TrimRight(l, " ")); w > 34 {
			t.Fatalf("line %d has visible width %d, out of bounds: %q", i, w, l)
		}
	}
}

// Newlines, control characters, bidirectional controls, tmux format strings, and orphaned combining characters, all the way from the tab list to the real client screen:
// each title occupies exactly one line, and nothing is executed as a format or command.
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
	got := e.clientWaits("c30", 5*time.Second, "all six tabs appear", func(s []string) bool { return contains(s, "lead") && contains(s, "中文") })
	t.Logf("malicious titles:\n%s", trimRight(got))
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("#() in a title was executed")
	}
	var tabRows []string
	for _, l := range got[1:] { // the first line is the OPENCODE banner block
		if strings.HasPrefix(l, "  ") && strings.TrimSpace(l) != "" {
			tabRows = append(tabRows, l)
		}
	}
	// The newlines in line1\nline2\r\nline3 become spaces, so it is still the same tab on the same line
	if len(tabRows) != len(titles) {
		t.Fatalf("six titles should occupy exactly six lines, got %d:\n%s", len(tabRows), trimRight(got))
	}
	for i, l := range got {
		if w := cellwidth.String(strings.TrimRight(l, " ")); w > 34 {
			t.Fatalf("line %d has visible width %d, out of bounds: %q", i, w, l)
		}
	}
	if !contains(got, "line1 line2  line3") || !contains(got, "#(touch /") || !contains(got, "#{pane_id} #[fg=red] ##[bold]…") {
		t.Fatalf("special characters should be drawn literally:\n%s", trimRight(got))
	}
}
