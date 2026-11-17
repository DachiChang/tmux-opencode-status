package heightfit_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
	"github.com/DachiChang/tmux-opencode-status/internal/heightfit"
)

// 側欄內容寬度 35（36 格扣掉分隔線）；POC 另留右邊距 1，實際繪製寬度 34。
const contentWidth = 35

func lines(groups []heightfit.Group, avail int) []string {
	rows, _ := heightfit.Fit(groups, avail)
	return heightfit.Render(rows, contentWidth)
}

func plain(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = heightfit.StripStyles(l)
	}
	return out
}

func one(title string, st heightfit.State) []heightfit.Group {
	return []heightfit.Group{{Name: "api", Tabs: []heightfit.Tab{{Title: title, State: st, Session: "s1"}}}}
}

// ---- 視覺骨架：與 POC 相同的邊界、縮排與右對齊 ----

func TestLayoutMatchesThePOCGeometryAt36Cells(t *testing.T) {
	groups := []heightfit.Group{
		{Name: "opencode", Tabs: []heightfit.Tab{
			{Title: "桃園搜尋爬取與分類", State: heightfit.Running, Session: "a"},
			{Title: "protal-prod-eks202606 reloaded 很長很長很長", State: heightfit.Unread, Session: "b"},
			{Title: "opencode 2.3.0 更新內容", State: heightfit.Idle, Session: "c"},
		}},
		{Name: "asgard", Tabs: []heightfit.Tab{{Title: "InfluxDB 磁碟用量持續上升", State: heightfit.Attention, Session: "d"}}},
	}
	want := []string{
		" ◆ OPENCODE               ●1 ?1 ◆1",
		" ─────────────────────────────────",
		" opencode                    ●1 ◆1",
		"  ● 桃園搜尋爬取與分類",
		"  ◆ protal-prod-eks202606 reloade…",
		"  · opencode 2.3.0 更新內容",
		"",
		" asgard                         ?1",
		"  ? InfluxDB 磁碟用量持續上升",
	}
	got := plain(lines(groups, 30))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("與 POC 幾何不符\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i, l := range got {
		if w := cellwidth.String(l); w > 34 {
			t.Fatalf("第 %d 行寬 %d 超過 34 格：%q", i, w, l)
		}
	}
}

// ---- 顏色語意：●綠 ?黃 ◆藍 ✗紅 !灰 ·灰；未讀粗體、閒置壓暗、無底色 ----

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

func tabLine(t *testing.T, st heightfit.State, title string) string {
	t.Helper()
	ls := lines(one(title, st), 20)
	return ls[len(ls)-1]
}

func TestStateSymbolsUseTheSpecColours(t *testing.T) {
	cases := []struct {
		st     heightfit.State
		sym    string
		colour string
	}{
		{heightfit.Running, "●", "#4ADE80"},
		{heightfit.Attention, "?", "#FACC15"},
		{heightfit.Unread, "◆", "#60A5FA"},
		{heightfit.Failed, "✗", "#F87171"},
		{heightfit.Unknown, "!", "#737373"},
		{heightfit.Idle, "·", "#737373"},
	}
	for _, c := range cases {
		st := styleOf(t, tabLine(t, c.st, "x"), c.sym)
		if !strings.Contains(st, "fg="+c.colour) {
			t.Errorf("%s 的樣式 = %q，要 %s", c.sym, st, c.colour)
		}
	}
}

func TestTitleEmphasisFollowsSpec(t *testing.T) {
	bold := func(st heightfit.State) bool {
		s := styleOf(t, tabLine(t, st, "TITLE"), "TITLE")
		return strings.Contains(s, "bold") && !strings.Contains(s, "nobold")
	}
	dim := func(st heightfit.State) bool {
		return strings.Contains(styleOf(t, tabLine(t, st, "TITLE"), "TITLE"), "fg=#737373")
	}
	if !bold(heightfit.Unread) || !bold(heightfit.Failed) {
		t.Error("未讀（◆ ✗）標題應粗體")
	}
	if bold(heightfit.Running) || bold(heightfit.Idle) || bold(heightfit.Attention) {
		t.Error("只有未讀標題粗體")
	}
	if !dim(heightfit.Idle) {
		t.Error("閒置標題應壓暗")
	}
	if dim(heightfit.Running) || dim(heightfit.Unread) {
		t.Error("非閒置標題不得壓暗")
	}
}

func TestNoBackgroundColoursAnywhere(t *testing.T) {
	groups := []heightfit.Group{{Name: "api", Tabs: []heightfit.Tab{
		{Title: "a", State: heightfit.Attention, Session: "1"}, {Title: "b", State: heightfit.Unknown, Session: "2"},
		{Title: "c", State: heightfit.Failed, Session: "3"}, {Title: "d", State: heightfit.Idle, Session: "4"}}}, {Name: "empty"}}
	rows, _ := heightfit.FitView(heightfit.View{Groups: groups, Notice: "n"}, 30)
	for _, l := range heightfit.Render(rows, contentWidth) {
		if strings.Contains(l, "bg=") || strings.Contains(l, "reverse") {
			t.Fatalf("不得有底色或反白：%q", l)
		}
	}
}

func TestChromeColoursFollowThePOC(t *testing.T) {
	ls := lines(one("x", heightfit.Running), 20)
	if st := styleOf(t, ls[0], "◆"); !strings.Contains(st, "fg=#A78BFA") || !strings.Contains(st, "bold") {
		t.Errorf("◆ 應為紫色粗體：%q", st)
	}
	if st := styleOf(t, ls[0], "OPENCODE"); !strings.Contains(st, "fg=#A3A3A3") || strings.Contains(st, "bold") {
		t.Errorf("OPENCODE 應為淺灰、不粗體：%q", st)
	}
	if st := styleOf(t, ls[1], "─"); !strings.Contains(st, "fg=#404040") {
		t.Errorf("分隔線樣式：%q", st)
	}
	if st := styleOf(t, ls[2], "api"); !strings.Contains(st, "fg=#FAFAFA") || !strings.Contains(st, "bold") {
		t.Errorf("群組名稱應為亮色粗體：%q", st)
	}
	if st := styleOf(t, ls[2], "●1"); !strings.Contains(st, "fg=#4ADE80") || strings.Contains(st, "bold") {
		t.Errorf("群組摘要符號應為對應顏色、不粗體：%q", st)
	}
}

func TestSummarySymbolsAreColouredIndividually(t *testing.T) {
	groups := []heightfit.Group{{Name: "g", Tabs: []heightfit.Tab{
		{Title: "1", State: heightfit.Running, Session: "1"}, {Title: "2", State: heightfit.Attention, Session: "2"},
		{Title: "3", State: heightfit.Unread, Session: "3"}, {Title: "4", State: heightfit.Failed, Session: "4"},
		{Title: "5", State: heightfit.Unknown, Session: "5"}}}}
	ls := lines(groups, 20)
	for sym, colour := range map[string]string{"●1": "#4ADE80", "?1": "#FACC15", "◆1": "#60A5FA", "✗1": "#F87171", "!1": "#737373"} {
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
	groups := []heightfit.Group{
		{Name: "a", Tabs: []heightfit.Tab{{Title: "x", State: heightfit.Running, Session: "shared"}}},
		{Name: "b", Tabs: []heightfit.Tab{{Title: "x", State: heightfit.Running, Session: "shared"}, {Title: "y", State: heightfit.Running, Session: "other"}}},
	}
	got := plain(lines(groups, 30))
	if !strings.HasSuffix(got[0], "●2") {
		t.Errorf("全體應按 session 去重為 ●2：%q", got[0])
	}
	if !strings.HasSuffix(got[2], "●1") {
		t.Errorf("群組 a 應為 ●1：%q", got[2])
	}
	var gb string
	for _, l := range got {
		if strings.HasPrefix(l, " b") {
			gb = l
		}
	}
	if !strings.HasSuffix(gb, "●2") {
		t.Errorf("群組 b 的兩個成員都應計入 ●2：%q", gb)
	}
}

func TestGroupSummaryCountsEveryMemberTabOncePerTab(t *testing.T) {
	groups := []heightfit.Group{{Name: "a", Tabs: []heightfit.Tab{
		{Title: "x", State: heightfit.Attention, Session: "s"}, {Title: "y", State: heightfit.Attention, Session: "s"}}}}
	got := plain(lines(groups, 30))
	if !strings.HasSuffix(got[2], "?2") || !strings.HasSuffix(got[0], "?1") {
		t.Fatalf("群組按成員 ?2、全體按 session ?1：\n%s", strings.Join(got, "\n"))
	}
}

func TestAllIdleStillShowsTitleAndGroupWithoutCounts(t *testing.T) {
	got := plain(lines(one("quiet", heightfit.Idle), 20))
	want := []string{" ◆ OPENCODE", " ─────────────────────────────────", " api", "  · quiet"}
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
		for _, line := range plain(lines(one(title, heightfit.Running), 20)) {
			if w := cellwidth.String(line); w > 34 {
				t.Errorf("%q → 寬 %d 超過 34：%q", title, w, line)
			}
		}
		tab := plain(lines(one(title, heightfit.Running), 20))[3]
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
		got := plain(lines(one(title, heightfit.Idle), 20))[3]
		if got != "  · "+title {
			t.Errorf("%q 被改動：%q", title, got)
		}
	}
}

func TestLongGroupNameIsTruncatedLeavingRoomForItsSummary(t *testing.T) {
	groups := []heightfit.Group{{Name: strings.Repeat("很長的目錄名稱", 6), Tabs: []heightfit.Tab{{Title: "x", State: heightfit.Running, Session: "1"}}}}
	got := plain(lines(groups, 20))[2]
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
		groups := []heightfit.Group{{Name: title, Tabs: []heightfit.Tab{{Title: title, State: heightfit.Running, Session: "s"}}}}
		rows, _ := heightfit.FitView(heightfit.View{Groups: groups, Notice: title, Empty: title}, 30)
		ls := heightfit.Render(rows, contentWidth)
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
			if w := cellwidth.String(heightfit.StripStyles(l)); w > 34 {
				t.Errorf("%q → 寬 %d：%q", title, w, l)
			}
		}
		if got := len(ls); got != len(rows) {
			t.Errorf("%q → %d 行，預期 %d（資料產生了額外行）", title, got, len(rows))
		}
	}
}

func TestLiteralHashesSurviveInTitles(t *testing.T) {
	got := plain(lines(one("Issue #459 解決方案", heightfit.Idle), 20))[3]
	if got != "  · Issue #459 解決方案" {
		t.Fatalf("%q", got)
	}
}

// unescapedHash 以 tmux 的讀法掃描一行：## 是字面 #，#[...] 只允許是本插件的樣式標記
// （內容只含 fg= / bold / default 這類值）；其他任何 # 都代表資料可能被當成格式。
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
			if !regexp.MustCompile(`^(fg=#[0-9A-Fa-f]{6}|bold|default)(,(fg=#[0-9A-Fa-f]{6}|bold))*$`).MatchString(line[i+2 : i+j]) {
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
		tab := plain(lines(one(title, heightfit.Idle), 20))[3]
		if w := cellwidth.String(tab); w != cellwidth.String("  · abc") {
			t.Errorf("開頭 %U 使 tab 行寬變成 %d：%q", []rune(lead)[0], w, tab)
		}
		group := plain(lines([]heightfit.Group{{Name: title, Tabs: []heightfit.Tab{{Title: "x", State: heightfit.Running, Session: "1"}}}}, 20))[2]
		if w := cellwidth.String(group); w != 34 {
			t.Errorf("開頭 %U 使群組行寬變成 %d：%q", []rune(lead)[0], w, group)
		}
	}
}

func TestAbsurdlyLargeCountsNeverPushASummaryPastTheEdge(t *testing.T) {
	var tabs []heightfit.Tab
	for _, st := range []heightfit.State{heightfit.Running, heightfit.Attention, heightfit.Unread, heightfit.Failed, heightfit.Unknown} {
		for i := 0; i < 120; i++ {
			tabs = append(tabs, heightfit.Tab{Title: "t", State: st, Session: string(rune('a'+st)) + string(rune(i+0x100))})
		}
	}
	groups := []heightfit.Group{{Name: "一個很長很長的群組名稱", Tabs: tabs}}
	rows, _ := heightfit.Fit(groups, 12)
	for _, l := range plain(heightfit.Render(rows, contentWidth)) {
		if w := cellwidth.String(l); w > 34 {
			t.Errorf("寬 %d 越界：%q", w, l)
		}
	}
}

func TestLongNoticeWrapsInsteadOfOverflowing(t *testing.T) {
	p := heightfit.PublishView(heightfit.View{Notice: strings.Repeat("無法讀取資料", 8), Empty: "沒有 OpenCode TUI"}, contentWidth)
	for _, l := range plain(strings.Split(p.Select(20), "\n")) {
		if w := cellwidth.String(l); w > 34 {
			t.Errorf("寬 %d 越界：%q", w, l)
		}
	}
	if n := len(strings.Split(p.Select(20), "\n")); n < 4 {
		t.Errorf("長提示應折成多行，實際 %d 行", n)
	}
}
