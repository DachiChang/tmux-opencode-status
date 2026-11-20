package render_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// 側欄內容寬度 35（36 格扣掉分隔線）；POC 另留右邊距 1，實際繪製寬度 34。
const contentWidth = 35

func lines(groups []render.Group) []string {
	rows := render.Fit(groups)
	return render.Render(rows, contentWidth, render.DefaultTheme())
}

func plain(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = render.StripStyles(l)
	}
	return out
}

func one(title string, st render.State) []render.Group {
	return []render.Group{{Name: "api", Tabs: []render.Tab{{Title: title, State: st, SessionID: "s1"}}}}
}

// ---- 視覺骨架：與 POC 相同的邊界、縮排與右對齊 ----

func TestLayoutMatchesThePOCGeometryAt36Cells(t *testing.T) {
	groups := []render.Group{
		{Name: "opencode", Tabs: []render.Tab{
			{Title: "桃園搜尋爬取與分類", State: render.Running, SessionID: "a"},
			{Title: "protal-prod-eks202606 reloaded 很長很長很長", State: render.FinishedUnread, SessionID: "b"},
			{Title: "opencode 2.3.0 更新內容", State: render.Idle, SessionID: "c"},
		}},
		{Name: "asgard", Tabs: []render.Tab{{Title: "InfluxDB 磁碟用量持續上升", State: render.Attention, SessionID: "d"}}},
	}
	want := []string{
		"  OPENCODE                ●1 ?1 ◆1",
		" ─────────────────────────────────",
		" ▎opencode                   ●1 ◆1",
		"  ● 桃園搜尋爬取與分類",
		"  ◆ protal-prod-eks202606 reloade…",
		"  · opencode 2.3.0 更新內容",
		"",
		" ▎asgard                        ?1",
		"  ? InfluxDB 磁碟用量持續上升",
	}
	got := plain(lines(groups))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("與定案幾何不符\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i, l := range got {
		if w := cellwidth.String(l); w > 34 {
			t.Fatalf("第 %d 行寬 %d 超過 34 格：%q", i, w, l)
		}
	}
}

// ---- 顏色語意：色票語意由 theme_test.go 逐項鎖定；這裡保留共用的樣式查詢工具 ----

var styleRe = regexp.MustCompile(`#\[([^\]]*)\]([^#]*)`)

// styleOf 回傳 line 中 text 第一次出現時生效的樣式。
func styleOf(t *testing.T, line, text string) string {
	t.Helper()
	for _, m := range styleRe.FindAllStringSubmatch(line, -1) {
		if strings.Contains(m[2], text) {
			return m[1]
		}
	}
	t.Fatalf("在 %q 找不到 %q 的樣式", line, text)
	return ""
}

func tabLine(t *testing.T, st render.State, title string) string {
	t.Helper()
	ls := lines(one(title, st))
	return ls[len(ls)-1]
}

func TestStateSymbolsUseTheSpecColours(t *testing.T) {
	cases := []struct {
		st     render.State
		sym    string
		colour string
	}{
		{render.Running, "●", "#4ADE80"},
		{render.Attention, "?", "#FACC15"},
		{render.FinishedUnread, "◆", "#60A5FA"},
		{render.FailedUnread, "✗", "#F87171"},
		{render.DataProblem, "!", "#A1A1AA"},
		{render.Idle, "·", "#71717A"},
	}
	for _, c := range cases {
		st := styleOf(t, tabLine(t, c.st, "x"), c.sym)
		if st != "fg="+c.colour {
			t.Errorf("%s 的樣式 = %q，要 fg=%s", c.sym, st, c.colour)
		}
	}
}

func TestTabTitlesUseActiveOrInactiveColour(t *testing.T) {
	cases := map[render.State]string{
		render.Running: "#cac5b5", render.Attention: "#cac5b5", render.FinishedUnread: "#cac5b5", render.FailedUnread: "#cac5b5",
		render.Idle: "#71717A", render.DataProblem: "#71717A",
	}
	for st, colour := range cases {
		if got := styleOf(t, tabLine(t, st, "TITLE"), "TITLE"); got != "fg="+colour {
			t.Errorf("狀態 %d 的標題樣式 = %q，要 fg=%s", st, got, colour)
		}
	}
}

func TestChromeColours(t *testing.T) {
	ls := lines(one("x", render.Running))
	if st := styleOf(t, ls[0], "OPENCODE"); st != "bg=#A78BFA,fg=#18181B" {
		t.Errorf("OPENCODE 應為紫底深字：%q", st)
	}
	if st := styleOf(t, ls[1], "─"); st != "fg=#404040" {
		t.Errorf("分隔線樣式：%q", st)
	}
	if st := styleOf(t, ls[2], "▎"); st != "fg=#A78BFA" {
		t.Errorf("▎ 應為紫色：%q", st)
	}
	if st := styleOf(t, ls[2], "api"); st != "fg=#F2EEE3" {
		t.Errorf("專案名稱樣式：%q", st)
	}
	if st := styleOf(t, ls[2], "●1"); st != "fg=#4ADE80" {
		t.Errorf("群組摘要符號應為對應顏色：%q", st)
	}
}

func TestSummarySymbolsAreColouredIndividually(t *testing.T) {
	groups := []render.Group{{Name: "g", Tabs: []render.Tab{
		{Title: "1", State: render.Running, SessionID: "1"}, {Title: "2", State: render.Attention, SessionID: "2"},
		{Title: "3", State: render.FinishedUnread, SessionID: "3"}, {Title: "4", State: render.FailedUnread, SessionID: "4"},
		{Title: "5", State: render.DataProblem, SessionID: "5"}}}}
	ls := lines(groups)
	for sym, colour := range map[string]string{"●1": "#4ADE80", "?1": "#FACC15", "◆1": "#60A5FA", "✗1": "#F87171", "!1": "#A1A1AA"} {
		if st := styleOf(t, ls[0], sym); !strings.Contains(st, "fg="+colour) {
			t.Errorf("頂部摘要 %s 樣式 = %q，要 %s", sym, st, colour)
		}
	}
	if got := plain(ls)[0]; !strings.HasSuffix(got, "●1 ?1 ◆1 ✗1 !1") {
		t.Errorf("摘要順序應為 ● ? ◆ ✗ !：%q", got)
	}
}

// ---- 摘要計數：全體按 session 去重，群組按群組成員 ----

func TestTopSummaryDedupesBySessionButGroupSummariesCountTheirMembers(t *testing.T) {
	groups := []render.Group{
		{Name: "a", Tabs: []render.Tab{{Title: "x", State: render.Running, SessionID: "shared"}}},
		{Name: "b", Tabs: []render.Tab{{Title: "x", State: render.Running, SessionID: "shared"}, {Title: "y", State: render.Running, SessionID: "other"}}},
	}
	got := plain(lines(groups))
	if !strings.HasSuffix(got[0], "●2") {
		t.Errorf("全體應按 session 去重為 ●2：%q", got[0])
	}
	if !strings.HasSuffix(got[2], "●1") {
		t.Errorf("群組 a 應為 ●1：%q", got[2])
	}
	var gb string
	for _, l := range got {
		if strings.HasPrefix(l, " ▎b") {
			gb = l
		}
	}
	if !strings.HasSuffix(gb, "●2") {
		t.Errorf("群組 b 的兩個成員都應計入 ●2：%q", gb)
	}
}

func TestGroupSummaryCountsEveryMemberTabOncePerTab(t *testing.T) {
	groups := []render.Group{{Name: "a", Tabs: []render.Tab{
		{Title: "x", State: render.Attention, SessionID: "s"}, {Title: "y", State: render.Attention, SessionID: "s"}}}}
	got := plain(lines(groups))
	if !strings.HasSuffix(got[2], "?2") || !strings.HasSuffix(got[0], "?1") {
		t.Fatalf("群組按成員 ?2、全體按 session ?1：\n%s", strings.Join(got, "\n"))
	}
}

func TestAllIdleStillShowsTitleAndGroupWithoutCounts(t *testing.T) {
	got := plain(lines(one("quiet", render.Idle)))
	want := []string{"  OPENCODE", " ─────────────────────────────────", " ▎api", "  · quiet"}
	for i := range want {
		if strings.TrimRight(got[i], " ") != want[i] {
			t.Fatalf("got %q want %q", got[i], want[i])
		}
	}
}

// ---- Unicode：依格寬截斷，不拆開字元，不越界 ----

func TestTruncationNeverSplitsAClusterAndNeverOverflows(t *testing.T) {
	titles := []string{
		strings.Repeat("中文標題", 10),
		strings.Repeat("a", 80),
		strings.Repeat("👨‍👩‍👧", 20),
		strings.Repeat("👍🏽", 20),
		strings.Repeat("🇹🇼", 20),
		strings.Repeat("e\u0301", 50),
		strings.Repeat("한", 40),
		"混合 mixed 中文 👨‍👩‍👧 e\u0301 end of a very long title that keeps going",
		strings.Repeat("a", 28) + "中" + "bcdef", // 寬字元剛好跨過邊界
		strings.Repeat("a", 29) + "中",
		"尾端 ZWJ" + strings.Repeat("x", 40) + "\u200d",
		strings.Repeat("😀\u200d", 30),
	}
	for _, title := range titles {
		for _, line := range plain(lines(one(title, render.Running))) {
			if w := cellwidth.String(line); w > 34 {
				t.Errorf("%q → 寬 %d 超過 34：%q", title, w, line)
			}
		}
		tab := plain(lines(one(title, render.Running)))[3]
		body := strings.TrimPrefix(tab, "  ● ")
		if cellwidth.String(title) > 30 {
			if !strings.HasSuffix(body, "…") {
				t.Errorf("%q 被截斷但沒有 …：%q", title, body)
			}
			// 截斷後的每個群集都必須是原標題中完整的群集
			orig := map[string]bool{}
			for _, c := range cellwidth.Clusters(title) {
				orig[c] = true
			}
			for _, c := range cellwidth.Clusters(strings.TrimSuffix(body, "…")) {
				if !orig[c] {
					t.Errorf("%q：截斷結果含被切開的群集 %q", title, c)
				}
			}
		}
	}
}

func TestShortTitlesAreNotTruncated(t *testing.T) {
	for _, title := range []string{"短", "write tests", "修正登入逻辑 bug", strings.Repeat("a", 30), strings.Repeat("中", 15)} {
		got := plain(lines(one(title, render.Idle)))[3]
		if got != "  · "+title {
			t.Errorf("%q 被改動：%q", title, got)
		}
	}
}

func TestLongGroupNameIsTruncatedLeavingRoomForItsSummary(t *testing.T) {
	groups := []render.Group{{Name: strings.Repeat("很長的目錄名稱", 6), Tabs: []render.Tab{{Title: "x", State: render.Running, SessionID: "1"}}}}
	got := plain(lines(groups))[2]
	if w := cellwidth.String(got); w != 34 {
		t.Fatalf("群組列寬 = %d，摘要應貼齊右邊界：%q", w, got)
	}
	if !strings.Contains(got, "…") || !strings.HasSuffix(got, "●1") {
		t.Fatalf("%q", got)
	}
}

// ---- 資料就是資料：格式字樣、控制字元與換行都不得越界或被執行 ----

func TestHostileTextIsRenderedLiterallyAndStaysOnOneLine(t *testing.T) {
	hostile := []string{
		"x #[fg=red] #{pane_id} #(touch /tmp/x) ,}",
		"line1\nline2\r\nline3",
		"tab\there\x00nul\x1b[31mesc\x07bell\x7f",
		"ls\u2028ps\u2029nel\u0085",
		"\u202eevil rtl\u202c \u2066iso\u2069 \u200e\u200f",
		"##[fg=red]## #[ #{ #( ]]] }}} \\ \" $HOME `x`",
		"#",
		"##",
		"\xff\xfe invalid utf8 \xc3",
	}
	for _, title := range hostile {
		groups := []render.Group{{Name: title, Tabs: []render.Tab{{Title: title, State: render.Running, SessionID: "s"}}}}
		rows := render.FitView(render.View{Groups: groups, Notice: title, Empty: title})
		ls := render.Render(rows, contentWidth, render.DefaultTheme())
		for _, l := range ls {
			if strings.ContainsAny(l, "\n\r\x00\x1b\x07\x7f\u2028\u2029\u0085") {
				t.Errorf("%q → 輸出含控制字元：%q", title, l)
			}
			if strings.ContainsAny(l, "\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069\u200e\u200f") {
				t.Errorf("%q → 輸出含雙向控制字元：%q", title, l)
			}
			if bad := unescapedHash(l); bad != "" {
				t.Errorf("%q → 資料中的 # 沒有跳脫：%q（%s）", title, l, bad)
			}
			if w := cellwidth.String(render.StripStyles(l)); w > 34 {
				t.Errorf("%q → 寬 %d：%q", title, w, l)
			}
		}
		if got := len(ls); got != len(rows) {
			t.Errorf("%q → %d 行，預期 %d（資料產生了額外行）", title, got, len(rows))
		}
	}
}

func TestLiteralHashesSurviveInTitles(t *testing.T) {
	got := plain(lines(one("Issue #459 解決方案", render.Idle)))[3]
	if got != "  · Issue #459 解決方案" {
		t.Fatalf("%q", got)
	}
}

// unescapedHash 以 tmux 的讀法掃描一行：## 是字面 #，#[...] 只允許是本插件的樣式標記
// （內容只含 fg= / bg= / default 這類值）；其他任何 # 都代表資料可能被當成格式。
func unescapedHash(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		if i+1 >= len(line) {
			return "行尾孤立的 #"
		}
		switch line[i+1] {
		case '#':
			i++
		case '[':
			j := strings.IndexByte(line[i:], ']')
			if j < 0 {
				return "未結束的 #["
			}
			if !regexp.MustCompile(`^(fg=#[0-9A-Fa-f]{6}|bg=#[0-9A-Fa-f]{6},fg=#[0-9A-Fa-f]{6}|default)$`).MatchString(line[i+2 : i+j]) {
				return "非本插件的樣式標記 " + line[i:i+j+1]
			}
			i += j
		default:
			return "# 後接 " + string(line[i+1])
		}
	}
	return ""
}

func TestLeadingInvisibleCharactersCannotWidenTheirNeighbours(t *testing.T) {
	// 開頭的 VS16／ZWJ／組合字元／孤立中聲會併進前面的空白，tmux 因此把空白畫成 2 格。
	for _, lead := range []string{"\ufe0f", "\u200d", "\u0301", "\u1161", "\u3164", "\u200b"} {
		title := lead + "abc"
		tab := plain(lines(one(title, render.Idle)))[3]
		if w := cellwidth.String(tab); w != cellwidth.String("  · abc") {
			t.Errorf("開頭 %U 使 tab 行寬變成 %d：%q", []rune(lead)[0], w, tab)
		}
		group := plain(lines([]render.Group{{Name: title, Tabs: []render.Tab{{Title: "x", State: render.Running, SessionID: "1"}}}}))[2]
		if w := cellwidth.String(group); w != 34 {
			t.Errorf("開頭 %U 使群組行寬變成 %d：%q", []rune(lead)[0], w, group)
		}
	}
}

func TestAbsurdlyLargeCountsNeverPushASummaryPastTheEdge(t *testing.T) {
	var tabs []render.Tab
	for _, st := range []render.State{render.Running, render.Attention, render.FinishedUnread, render.FailedUnread, render.DataProblem} {
		for i := 0; i < 120; i++ {
			tabs = append(tabs, render.Tab{Title: "t", State: st, SessionID: string(rune('a'+st)) + string(rune(i+0x100))})
		}
	}
	groups := []render.Group{{Name: "一個很長很長的群組名稱", Tabs: tabs}}
	rows := render.Fit(groups)
	for _, l := range plain(render.Render(rows, contentWidth, render.DefaultTheme())) {
		if w := cellwidth.String(l); w > 34 {
			t.Errorf("寬 %d 越界：%q", w, l)
		}
	}
}

func TestLongNoticeWrapsInsteadOfOverflowing(t *testing.T) {
	p := render.PublishView(render.View{Notice: strings.Repeat("無法讀取資料", 8), Empty: "沒有 OpenCode TUI"}, contentWidth, render.DefaultTheme())
	for _, l := range plain(strings.Split(p.Options[render.MainOption], "\n")) {
		if w := cellwidth.String(l); w > 34 {
			t.Errorf("寬 %d 越界：%q", w, l)
		}
	}
	if n := len(strings.Split(p.Options[render.MainOption], "\n")); n < 4 {
		t.Errorf("長提示應折成多行，實際 %d 行", n)
	}
}

// ---- 單一完整版面：高度不足時由 tmux 從底部截斷，插件不排多份、不加提示 ----

func manyGroups(n int) []render.Group {
	var groups []render.Group
	for i := 0; i < n; i++ {
		var tabs []render.Tab
		for j := 0; j < 4; j++ {
			tabs = append(tabs, render.Tab{Title: fmt.Sprintf("tab-%02d-%d", i, j), State: render.Idle, SessionID: fmt.Sprintf("s%d-%d", i, j)})
		}
		groups = append(groups, render.Group{Name: fmt.Sprintf("dir-%02d", i), Tabs: tabs})
	}
	return groups
}

func TestPublishWritesOneLayoutOptionAndAFixedFormat(t *testing.T) {
	for _, n := range []int{0, 1, 30} {
		p := render.Publish(manyGroups(n), contentWidth)
		if len(p.Options) != 1 {
			t.Fatalf("%d 個群組：應只有一個版面選項，實際 %d 個", n, len(p.Options))
		}
		if _, ok := p.Options["@tos_sb_main"]; !ok {
			t.Fatalf("%d 個群組：選項應為 @tos_sb_main：%v", n, p.Options)
		}
		if got := p.Format(); got != "#{@tos_sb_main}" {
			t.Fatalf("%d 個群組：format = %q，要固定為 #{@tos_sb_main}", n, got)
		}
	}
}

func TestLongContentIsPublishedWholeWithTheLastTabOnTheLastRow(t *testing.T) {
	groups := manyGroups(30)
	groups[29].Tabs[3].State = render.Attention
	groups[28].Tabs[0].State = render.FinishedUnread
	p := render.Publish(groups, contentWidth)
	ls := plain(strings.Split(p.Options["@tos_sb_main"], "\n"))

	// 標題 + 分隔線 + 30 個群組列 + 29 個空行 + 120 個 tab 列
	if want := 2 + 30 + 29 + 120; len(ls) != want {
		t.Fatalf("版面應完整 %d 列，實際 %d 列", want, len(ls))
	}
	if last := ls[len(ls)-1]; !strings.Contains(last, "tab-29-3") {
		t.Fatalf("最後一列應是最後一個 tab：%q", last)
	}
	if got := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ls[0]), "OPENCODE")); got != "?1 ◆1" {
		t.Fatalf("頂部摘要應計入畫面外的 tab：%q", got)
	}
}

func cellwidthOf(line string) int { return cellwidth.String(render.StripStyles(line)) }
