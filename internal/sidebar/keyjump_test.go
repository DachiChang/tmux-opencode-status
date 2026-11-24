package sidebar_test

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// 鍵盤跳轉：每次發布一般版面、跳轉版面與 @tos_jump_key_<字母>。測試從「寫到 tmux 的選項」觀察：
// 選項名稱是對外承諾（ADR-0004、ADR-0005），所以直接寫字面值。

const (
	mainOpt = "@tos_layout_main"
	jumpOpt = "@tos_layout_jump"
	keyOpt  = "@tos_jump_key_"
)

func (f *fakeOut) latest() render.Published {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pubs[len(f.pubs)-1]
}

func (f *fakeOut) layout(opt string) []string {
	return strings.Split(f.latest().Options[opt], "\n")
}

// jumpKeys 是最近一次發布中所有 @tos_jump_key_<字母> 的 字母→值。
func (f *fakeOut) jumpKeys() map[string]string {
	keys := map[string]string{}
	for name, v := range f.latest().Options {
		if l, ok := strings.CutPrefix(name, keyOpt); ok {
			keys[l] = v
		}
	}
	return keys
}

func keysString(m map[string]string) string {
	var parts []string
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func plainRows(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = render.StripStyles(rangeMarkerRe.ReplaceAllString(l, ""))
	}
	return out
}

func rowWithSummary(prefix, summary string) string {
	return prefix + strings.Repeat(" ", 34-cellwidth.String(prefix)-cellwidth.String(summary)) + summary
}

func TestJumpLayoutReplacesTheBarOfLetteredGroupsWithATagAndKeepsEverythingElse(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%7", "/work/api"), tui("%2", "/work/web"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"one"},{"sessionID":"s2","title":"two"}]}}}`)
	w.sync(t)

	wantMain := []string{
		rowWithSummary("  OPENCODE", "!2"),
		" ─────────────────────────────────",
		rowWithSummary(" ▎api", "!2"),
		"  ! one",
		"  ! two",
		"",
		" ▎web",
		"  No tabs",
	}
	wantJump := append([]string(nil), wantMain...)
	wantJump[2] = rowWithSummary(" a  api", "!2")
	wantJump[6] = " b  web"
	assertRaw := func(got []string, want []string) {
		t.Helper()
		g := plainRows(got)
		for i := range g {
			g[i] = strings.TrimRight(g[i], " ")
		}
		for i := range want {
			want[i] = strings.TrimRight(want[i], " ")
		}
		if strings.Join(g, "\n") != strings.Join(want, "\n") {
			t.Fatalf("版面不符\n got:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
		}
	}
	assertRaw(w.out.layout(mainOpt), wantMain)
	assertRaw(w.out.layout(jumpOpt), wantJump)

	// 跳轉版面的標籤是 emphasis 底、title 字，後面接一般的空白；range 與一般版面逐列相同。
	jump := w.out.layout(jumpOpt)
	if !strings.Contains(jump[2], "#[bg=#A78BFA,fg=#18181B] a #[default] ") {
		t.Fatalf("標籤樣式不符：%q", jump[2])
	}
	mainRows := w.out.layout(mainOpt)
	rangeStart := regexp.MustCompile(`^#\[range=user\|oc\d+\]`)
	for i := range mainRows {
		prefix := rangeStart.FindString
		if prefix(mainRows[i]) != prefix(jump[i]) || strings.HasSuffix(mainRows[i], "#[norange]") != strings.HasSuffix(jump[i], "#[norange]") {
			t.Fatalf("第 %d 列 range 不同\n main: %q\n jump: %q", i, mainRows[i], jump[i])
		}
	}
	for _, i := range []int{0, 1, 3, 4, 5, 7} {
		if mainRows[i] != jump[i] {
			t.Fatalf("非群組列應逐字相同（第 %d 列）\n main: %q\n jump: %q", i, mainRows[i], jump[i])
		}
	}
}

func TestMainLayoutStillStartsEveryGroupWithTheBarExactlyAsBefore(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%7", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	w.sync(t)
	want := "#[range=user|oc7] #[fg=#A78BFA]▎#[fg=#F2EEE3]api#[default]#[norange]"
	if got := w.out.layout(mainOpt)[2]; got != want {
		t.Fatalf("一般版面群組列 = %q，要 %q", got, want)
	}
}

func TestKeysMapLettersToTheJumpTargetsInGroupOrder(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%13", "/work/api"), tui("%3", "/work/api"), tui("%2", "/work/web"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	w.sync(t)
	if got := keysString(w.out.jumpKeys()); got != "a=%3,b=%2" {
		t.Fatalf("字母對應 = %s", got)
	}
}

func TestLettersSkipGroupsWithoutAJumpTarget(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%x", "/work/a"), tui("%9", "/work/b"), tui("%4", "/work/c"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	w.sync(t)
	if got := keysString(w.out.jumpKeys()); got != "a=%9,b=%4" {
		t.Fatalf("字母對應 = %s", got)
	}
	rows := plainRows(w.out.layout(jumpOpt))
	// 標題、分隔線 | a（無跳轉目標）、無 tab | 空白 | b、無 tab | 空白 | c、無 tab
	if rows[2] != " ▎a" || rows[5] != " a  b" || rows[8] != " b  c" {
		t.Fatalf("標籤不符：%q", rows)
	}
}

func manyTUIs(w *world, n int) {
	for i := 0; i < n; i++ {
		w.panes.panes = append(w.panes.panes, tui(fmt.Sprintf("%%%d", i+1), fmt.Sprintf("/work/d%02d", i)))
	}
}

func TestOnlyTheFirst26GroupsGetALetterAndTheRestKeepTheBar(t *testing.T) {
	w := newWorld(t)
	manyTUIs(w, 28)
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	w.sync(t)
	keys := w.out.jumpKeys()
	if len(keys) != 26 || keys["a"] != "%1" || keys["z"] != "%26" {
		t.Fatalf("應剛好 26 個字母：%s", keysString(keys))
	}
	rows := plainRows(w.out.layout(jumpOpt))
	var groups []string
	for _, r := range rows {
		if strings.Contains(r, "d") && !strings.Contains(r, "No tabs") && !strings.Contains(r, "OPENCODE") {
			groups = append(groups, r)
		}
	}
	if len(groups) != 28 || groups[25] != " z  d25" || groups[26] != " ▎d26" || groups[27] != " ▎d27" {
		t.Fatalf("群組列不符：%q", groups)
	}
}

func TestKeysFollowTUIsOpeningClosingAndMovingAndExtraLettersDisappear(t *testing.T) {
	w := newWorld(t)
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	w.panes.set(tui("%1", "/work/a"), tui("%2", "/work/b"), tui("%3", "/work/c"))
	w.sync(t)
	if got := keysString(w.out.jumpKeys()); got != "a=%1,b=%2,c=%3" {
		t.Fatalf("初始 = %s", got)
	}
	w.panes.set(tui("%1", "/work/a"), tui("%3", "/work/c")) // 中間的 TUI 關閉
	w.sync(t)
	if got := keysString(w.out.jumpKeys()); got != "a=%1,b=%3" {
		t.Fatalf("關閉後 = %s", got)
	}
	w.panes.set(tui("%8", "/work/a"), tui("%3", "/work/c")) // 第一個移到別的 pane
	w.sync(t)
	if got := keysString(w.out.jumpKeys()); got != "a=%8,b=%3" {
		t.Fatalf("移動後 = %s", got)
	}
	w.panes.set()
	w.sync(t)
	if got := keysString(w.out.jumpKeys()); got != "" {
		t.Fatalf("沒有 TUI 時不應有任何字母：%s", got)
	}
}

func TestGlobalGroupGetsLetterA(t *testing.T) {
	w := newWorld(t)
	w.mode.use("global")
	w.panes.set(tui("%12", "/work/api"), tui("%9", "/work/web"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	if got := keysString(w.out.jumpKeys()); got != "a=%9" {
		t.Fatalf("字母對應 = %s", got)
	}
	if rows := plainRows(w.out.layout(jumpOpt)); !strings.HasPrefix(rows[2], " a  GLOBAL") {
		t.Fatalf("群組列：%q", rows[2])
	}
}

func TestJumpLayoutRetruncatesLongNamesAndKeepsTheSummaryWithinTheWidth(t *testing.T) {
	long := "/work/一個很長很長很長很長很長很長很長很長的專案目錄名稱"
	w := newWorld(t)
	w.panes.set(tui("%5", long))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"`+long+`":{"tabs":[{"sessionID":"s1","title":"x"},{"sessionID":"s2","title":"y"},{"sessionID":"s3","title":"z"}]}}}`)
	w.sync(t)
	for _, opt := range []string{mainOpt, jumpOpt} {
		row := plainRows(w.out.layout(opt))[2]
		if cellwidth.String(row) > 34 {
			t.Fatalf("%s 群組列寬 %d 超過 34：%q", opt, cellwidth.String(row), row)
		}
		if !strings.HasSuffix(row, "!3") {
			t.Fatalf("%s 群組列摘要被擠掉：%q", opt, row)
		}
	}
	jump := plainRows(w.out.layout(jumpOpt))[2]
	if !strings.HasPrefix(jump, " a  一個") || !strings.Contains(jump, "…") {
		t.Fatalf("跳轉版名稱應重新截斷：%q", jump)
	}
	if cellwidth.String(jump) != 34 {
		t.Fatalf("摘要應貼齊右側：寬 %d：%q", cellwidth.String(jump), jump)
	}
}

func TestHostileTitlesAndNamesDoNotChangeLettersTargetsOrTheConditionalFormat(t *testing.T) {
	dir := "/work/#{@tos_jump_key_a},#[range=user|oc99]x"
	w := newWorld(t)
	w.panes.set(tui("%6", dir), tui("%7", "/work/z"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"`+dir+`":{"tabs":[`+
		`{"sessionID":"s1","title":"#[range=user|oc99]evil#[norange],}"},`+
		`{"sessionID":"s2","title":"a\n#{client_key_table} ##[range=user|oc98]"}]}}}`)
	w.sync(t)
	if got := keysString(w.out.jumpKeys()); got != "a=%6,b=%7" {
		t.Fatalf("字母對應 = %s", got)
	}
	raw := w.out.latest().Options[jumpOpt]
	if n := strings.Count(strings.ReplaceAll(raw, "##", ""), "#[range="); n != 5 {
		t.Fatalf("range 數量 = %d，要 5（群組列、兩個 tab 列、第二群組的列與無 tab 列）：%q", n, raw)
	}
	want := "#{?#{==:#{client_key_table},opencode-status-jump},#{@tos_layout_jump},#{@tos_layout_main}}"
	if got := w.out.latest().Format(); got != want {
		t.Fatalf("side-status-format = %q", got)
	}
}

func TestEveryPublishedOptionIsInternalAndNamedByPurpose(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/a"), tui("%2", "/work/b"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	w.sync(t)
	valid := regexp.MustCompile(`^@tos_(layout_(main|jump)|jump_key_[a-z])$`)
	for name := range w.out.latest().Options {
		if !valid.MatchString(name) {
			t.Fatalf("發布了不符命名的選項 %q", name)
		}
	}
	w.panes.set()
	w.sync(t)
	opts := w.out.latest().Options
	if len(opts) != 2 || opts[mainOpt] == "" || opts[jumpOpt] == "" {
		t.Fatalf("沒有群組時只發布兩份版面：%v", opts)
	}
}
