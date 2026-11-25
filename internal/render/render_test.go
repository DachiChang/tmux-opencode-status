package render_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// Sidebar content width is 35 (36 cells minus the divider); the POC leaves another right margin of 1, for an actual drawn width of 34.
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

// ---- Visual skeleton: same margins, indentation, and right alignment as the POC ----

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
		t.Fatalf("does not match the finalized geometry\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i, l := range got {
		if w := cellwidth.String(l); w > 34 {
			t.Fatalf("line %d width %d exceeds 34 cells: %q", i, w, l)
		}
	}
}

// ---- Colour semantics: theme_test.go pins each swatch; the shared style lookup helpers stay here ----

var styleRe = regexp.MustCompile(`#\[([^\]]*)\]([^#]*)`)

// styleOf returns the style in effect where text first appears in line.
func styleOf(t *testing.T, line, text string) string {
	t.Helper()
	for _, m := range styleRe.FindAllStringSubmatch(line, -1) {
		if strings.Contains(m[2], text) {
			return m[1]
		}
	}
	t.Fatalf("in %q, no style found for %q", line, text)
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
			t.Errorf("%s style = %q, want fg=%s", c.sym, st, c.colour)
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
			t.Errorf("title style for state %d = %q, want fg=%s", st, got, colour)
		}
	}
}

func TestChromeColours(t *testing.T) {
	ls := lines(one("x", render.Running))
	if st := styleOf(t, ls[0], "OPENCODE"); st != "bg=#A78BFA,fg=#18181B" {
		t.Errorf("OPENCODE should be dark text on a purple background: %q", st)
	}
	if st := styleOf(t, ls[1], "─"); st != "fg=#404040" {
		t.Errorf("rule style: %q", st)
	}
	if st := styleOf(t, ls[2], "▎"); st != "fg=#A78BFA" {
		t.Errorf("▎ should be purple: %q", st)
	}
	if st := styleOf(t, ls[2], "api"); st != "fg=#F2EEE3" {
		t.Errorf("project name style: %q", st)
	}
	if st := styleOf(t, ls[2], "●1"); st != "fg=#4ADE80" {
		t.Errorf("group summary symbols should have their matching colours: %q", st)
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
			t.Errorf("top summary %s style = %q, want %s", sym, st, colour)
		}
	}
	if got := plain(ls)[0]; !strings.HasSuffix(got, "●1 ?1 ◆1 ✗1 !1") {
		t.Errorf("summary order should be ● ? ◆ ✗ !: %q", got)
	}
}

// ---- Summary counts: overall deduplicated by session, groups by group members ----

func TestTopSummaryDedupesBySessionButGroupSummariesCountTheirMembers(t *testing.T) {
	groups := []render.Group{
		{Name: "a", Tabs: []render.Tab{{Title: "x", State: render.Running, SessionID: "shared"}}},
		{Name: "b", Tabs: []render.Tab{{Title: "x", State: render.Running, SessionID: "shared"}, {Title: "y", State: render.Running, SessionID: "other"}}},
	}
	got := plain(lines(groups))
	if !strings.HasSuffix(got[0], "●2") {
		t.Errorf("overall should be deduplicated by session to ●2: %q", got[0])
	}
	if !strings.HasSuffix(got[2], "●1") {
		t.Errorf("group a should be ●1: %q", got[2])
	}
	var gb string
	for _, l := range got {
		if strings.HasPrefix(l, " ▎b") {
			gb = l
		}
	}
	if !strings.HasSuffix(gb, "●2") {
		t.Errorf("both members of group b should count as ●2: %q", gb)
	}
}

func TestGroupSummaryCountsEveryMemberTabOncePerTab(t *testing.T) {
	groups := []render.Group{{Name: "a", Tabs: []render.Tab{
		{Title: "x", State: render.Attention, SessionID: "s"}, {Title: "y", State: render.Attention, SessionID: "s"}}}}
	got := plain(lines(groups))
	if !strings.HasSuffix(got[2], "?2") || !strings.HasSuffix(got[0], "?1") {
		t.Fatalf("group by members ?2, overall by session ?1:\n%s", strings.Join(got, "\n"))
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

// ---- Unicode: truncate by cell width, never split characters, never overflow ----

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
		strings.Repeat("a", 28) + "中" + "bcdef", // wide character exactly straddles the boundary
		strings.Repeat("a", 29) + "中",
		"尾端 ZWJ" + strings.Repeat("x", 40) + "\u200d",
		strings.Repeat("😀\u200d", 30),
	}
	for _, title := range titles {
		for _, line := range plain(lines(one(title, render.Running))) {
			if w := cellwidth.String(line); w > 34 {
				t.Errorf("%q → width %d exceeds 34: %q", title, w, line)
			}
		}
		tab := plain(lines(one(title, render.Running)))[3]
		body := strings.TrimPrefix(tab, "  ● ")
		if cellwidth.String(title) > 30 {
			if !strings.HasSuffix(body, "…") {
				t.Errorf("%q was truncated but has no …: %q", title, body)
			}
			// Every cluster left after truncation must be a whole cluster of the original title
			orig := map[string]bool{}
			for _, c := range cellwidth.Clusters(title) {
				orig[c] = true
			}
			for _, c := range cellwidth.Clusters(strings.TrimSuffix(body, "…")) {
				if !orig[c] {
					t.Errorf("%q: truncated result contains a split cluster %q", title, c)
				}
			}
		}
	}
}

func TestShortTitlesAreNotTruncated(t *testing.T) {
	for _, title := range []string{"短", "write tests", "修正登入逻辑 bug", strings.Repeat("a", 30), strings.Repeat("中", 15)} {
		got := plain(lines(one(title, render.Idle)))[3]
		if got != "  · "+title {
			t.Errorf("%q was modified: %q", title, got)
		}
	}
}

func TestLongGroupNameIsTruncatedLeavingRoomForItsSummary(t *testing.T) {
	groups := []render.Group{{Name: strings.Repeat("很長的目錄名稱", 6), Tabs: []render.Tab{{Title: "x", State: render.Running, SessionID: "1"}}}}
	got := plain(lines(groups))[2]
	if w := cellwidth.String(got); w != 34 {
		t.Fatalf("group row width = %d, the summary should align with the right edge: %q", w, got)
	}
	if !strings.Contains(got, "…") || !strings.HasSuffix(got, "●1") {
		t.Fatalf("%q", got)
	}
}

// ---- Data is data: format strings, control characters, and newlines must not overflow or be executed ----

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
				t.Errorf("%q → output contains control characters: %q", title, l)
			}
			if strings.ContainsAny(l, "\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069\u200e\u200f") {
				t.Errorf("%q → output contains bidirectional control characters: %q", title, l)
			}
			if bad := unescapedHash(l); bad != "" {
				t.Errorf("%q → # in the data is not escaped: %q (%s)", title, l, bad)
			}
			if w := cellwidth.String(render.StripStyles(l)); w > 34 {
				t.Errorf("%q → width %d: %q", title, w, l)
			}
		}
		if got := len(ls); got != len(rows) {
			t.Errorf("%q → %d lines, want %d (the data produced extra lines)", title, got, len(rows))
		}
	}
}

func TestLiteralHashesSurviveInTitles(t *testing.T) {
	got := plain(lines(one("Issue #459 解決方案", render.Idle)))[3]
	if got != "  · Issue #459 解決方案" {
		t.Fatalf("%q", got)
	}
}

// unescapedHash scans a line the way tmux reads it: ## is a literal #, and #[...] may only be this plugin's style markers
// (containing only values such as fg= / bg= / default); any other # means the data may be treated as a format.
func unescapedHash(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		if i+1 >= len(line) {
			return "a lone # at the end of the line"
		}
		switch line[i+1] {
		case '#':
			i++
		case '[':
			j := strings.IndexByte(line[i:], ']')
			if j < 0 {
				return "an unterminated #["
			}
			if !regexp.MustCompile(`^(fg=#[0-9A-Fa-f]{6}|bg=#[0-9A-Fa-f]{6},fg=#[0-9A-Fa-f]{6}|default)$`).MatchString(line[i+2 : i+j]) {
				return "a style marker not from this plugin: " + line[i:i+j+1]
			}
			i += j
		default:
			return "# followed by " + string(line[i+1])
		}
	}
	return ""
}

func TestLeadingInvisibleCharactersCannotWidenTheirNeighbours(t *testing.T) {
	// A leading VS16/ZWJ/combining character/stray jungseong merges into the preceding space, so tmux draws that space 2 cells wide.
	for _, lead := range []string{"\ufe0f", "\u200d", "\u0301", "\u1161", "\u3164", "\u200b"} {
		title := lead + "abc"
		tab := plain(lines(one(title, render.Idle)))[3]
		if w := cellwidth.String(tab); w != cellwidth.String("  · abc") {
			t.Errorf("leading %U makes the tab row width %d: %q", []rune(lead)[0], w, tab)
		}
		group := plain(lines([]render.Group{{Name: title, Tabs: []render.Tab{{Title: "x", State: render.Running, SessionID: "1"}}}}))[2]
		if w := cellwidth.String(group); w != 34 {
			t.Errorf("leading %U makes the group row width %d: %q", []rune(lead)[0], w, group)
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
			t.Errorf("width %d overflows: %q", w, l)
		}
	}
}

func TestLongNoticeWrapsInsteadOfOverflowing(t *testing.T) {
	p := render.PublishView(render.View{Notice: strings.Repeat("無法讀取資料", 8), Empty: "No OpenCode TUI running"}, contentWidth, render.DefaultTheme())
	for _, l := range plain(strings.Split(p.Options[render.MainOption], "\n")) {
		if w := cellwidth.String(l); w > 34 {
			t.Errorf("width %d overflows: %q", w, l)
		}
	}
	if n := len(strings.Split(p.Options[render.MainOption], "\n")); n < 4 {
		t.Errorf("a long notice should wrap into multiple lines, got %d", n)
	}
}

// ---- Single complete layout: when the height is insufficient tmux truncates from the bottom; the plugin does not lay out multiple copies or add a hint ----

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

func TestPublishWritesTheTwoLayoutsAndOneKeyPerLetteredGroupWithAFixedFormat(t *testing.T) {
	for _, n := range []int{0, 1, 30} {
		groups := manyGroups(n)
		for i := range groups {
			groups[i].JumpTarget = fmt.Sprint(i + 1)
		}
		p := render.Publish(groups, contentWidth)
		letters := min(n, 26)
		if len(p.Options) != 2+letters {
			t.Fatalf("%d groups: want two layouts plus %d letters, got %d options", n, letters, len(p.Options))
		}
		for _, name := range []string{"@tos_layout_main", "@tos_layout_jump"} {
			if _, ok := p.Options[name]; !ok {
				t.Fatalf("%d groups: missing %s: %v", n, name, p.Options)
			}
		}
		const want = "#{?#{==:#{client_key_table},opencode-status-jump},#{@tos_layout_jump},#{@tos_layout_main}}"
		if got := p.Format(); got != want {
			t.Fatalf("%d groups: format = %q, want it fixed at %q", n, got, want)
		}
	}
}

func TestLongContentIsPublishedWholeWithTheLastTabOnTheLastRow(t *testing.T) {
	groups := manyGroups(30)
	groups[29].Tabs[3].State = render.Attention
	groups[28].Tabs[0].State = render.FinishedUnread
	p := render.Publish(groups, contentWidth)
	ls := plain(strings.Split(p.Options["@tos_layout_main"], "\n"))

	// title + rule + 30 group rows + 29 blank rows + 120 tab rows
	if want := 2 + 30 + 29 + 120; len(ls) != want {
		t.Fatalf("the layout should have all %d rows, got %d", want, len(ls))
	}
	if last := ls[len(ls)-1]; !strings.Contains(last, "tab-29-3") {
		t.Fatalf("the last row should be the last tab: %q", last)
	}
	if got := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ls[0]), "OPENCODE")); got != "?1 ◆1" {
		t.Fatalf("the top summary should count off-screen tabs: %q", got)
	}
	// At any assumed height (tmux truncates from the bottom), both stateful tabs are off-screen and the summary is still the same line.
	for avail := 5; avail <= 50; avail++ {
		shown := strings.Join(ls[:avail], "\n")
		if strings.Contains(shown, "tab-28-0") || strings.Contains(shown, "tab-29-3") {
			t.Fatalf("height %d should not show the off-screen stateful tabs:\n%s", avail, shown)
		}
	}
}

func cellwidthOf(line string) int { return cellwidth.String(render.StripStyles(line)) }
