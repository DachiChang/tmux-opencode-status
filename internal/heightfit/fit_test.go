package heightfit

import (
	"strings"
	"testing"
)

func sample() []Group {
	return []Group{
		{Name: "aicova/api", Tabs: []Tab{{Title: "a1", State: Running}, {Title: "a2", State: Attention}, {Title: "a3", State: Idle}}},
		{Name: "infra", Tabs: []Tab{{Title: "b1", State: Failed}, {Title: "b2", State: Unread}}},
		{Name: "profcoo/api", Tabs: []Tab{{Title: "c1", State: Unknown}, {Title: "c2", State: Running}}},
	}
}

func countKind(rows []Row, k RowKind) int {
	n := 0
	for _, r := range rows {
		if r.Kind == k {
			n++
		}
	}
	return n
}

func TestEnoughHeightShowsEverythingWithoutFooter(t *testing.T) {
	// title + rule + 3 headers + 2 blanks + 7 tabs = 14 rows
	rows, hidden := Fit(sample(), 14)
	if hidden != 0 || len(rows) != 14 || countKind(rows, RowMore) != 0 {
		t.Fatalf("hidden=%d rows=%d more=%d", hidden, len(rows), countKind(rows, RowMore))
	}
}

func tabTitles(rows []Row) []string {
	var out []string
	for _, r := range rows {
		if r.Kind == RowTab {
			out = append(out, r.Text)
		}
	}
	return out
}

func TestHiddenCountIsTabsNotGroupsOrLines(t *testing.T) {
	rows, hidden := Fit(sample(), 6)
	if hidden != 5 {
		t.Fatalf("hidden = %d, want 5 (a3,b1,b2,c1,c2)", hidden)
	}
	if got := tabTitles(rows); len(got) != 2 || got[0] != "a1" || got[1] != "a2" {
		t.Fatalf("visible = %v", got)
	}
	last := rows[len(rows)-1]
	if last.Kind != RowMore || last.Hidden != 5 || len(rows) != 6 {
		t.Fatalf("last = %+v, len = %d", last, len(rows))
	}
}

func TestFullyHiddenGroupsHaveNoHeaderButAreCounted(t *testing.T) {
	rows, hidden := Fit(sample(), 7)
	if hidden != 4 {
		t.Fatalf("hidden = %d, want 4", hidden)
	}
	if n := countKind(rows, RowGroup); n != 1 {
		t.Fatalf("group headers = %d, want 1", n)
	}
}

func TestCrossGroupTruncationKeepsSourceOrder(t *testing.T) {
	rows, hidden := Fit(sample(), 10)
	got := tabTitles(rows)
	want := []string{"a1", "a2", "a3", "b1"}
	if len(got) != len(want) {
		t.Fatalf("visible = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visible = %v, want %v", got, want)
		}
	}
	if hidden != 3 {
		t.Fatalf("hidden = %d, want 3", hidden)
	}
}

func TestTitleSummaryCountsHiddenTabsAndIgnoresHeight(t *testing.T) {
	small, _ := Fit(sample(), 6)
	big, _ := Fit(sample(), 40)
	const want = "●2 ?1 ◆1 ✗1 !1" // idle a3 omitted
	if small[0].Summary != want || big[0].Summary != want {
		t.Fatalf("small=%q big=%q want=%q", small[0].Summary, big[0].Summary, want)
	}
}

func TestAllIdleHasNoSummary(t *testing.T) {
	rows, _ := Fit([]Group{{Name: "x", Tabs: []Tab{{Title: "t", State: Idle}}}}, 10)
	if rows[0].Summary != "" {
		t.Fatalf("summary = %q", rows[0].Summary)
	}
}

func TestNeverOverflowsAndAccountsForEveryTab(t *testing.T) {
	for avail := 0; avail <= 20; avail++ {
		rows, hidden := Fit(sample(), avail)
		if len(rows) > avail {
			t.Fatalf("avail %d: %d rows", avail, len(rows))
		}
		if avail >= 3 && countKind(rows, RowTab)+hidden != 7 {
			t.Fatalf("avail %d: shown %d + hidden %d != 7", avail, countKind(rows, RowTab), hidden)
		}
	}
}

func TestRenderTruncatesByCellWidthWithEllipsis(t *testing.T) {
	rows := []Row{{Kind: RowTab, Text: "中文標題很長很長很長很長很長很長很長很長很長", State: Running}}
	line := Render(rows, 20)[0]
	plain := StripStyles(line)
	if w := CellWidth(plain); w != 20 && w != 19 {
		t.Fatalf("width = %d: %q", w, plain)
	}
	if !strings.HasSuffix(plain, "…") {
		t.Fatalf("no ellipsis: %q", plain)
	}
}

func TestRenderEscapesTmuxFormatCharactersInTitles(t *testing.T) {
	rows := []Row{{Kind: RowTab, Text: "x #[fg=red] #{pane_id} #(id) , }", State: Idle}}
	line := Render(rows, 36)[0]
	for _, bad := range []string{"#[fg=red]", "#{pane_id}", "#(id)"} {
		if strings.Contains(strings.ReplaceAll(line, "##", ""), bad) {
			t.Fatalf("unescaped %q in %q", bad, line)
		}
	}
}

func TestRenderRightAlignsSummaryWithinWidth(t *testing.T) {
	rows := []Row{{Kind: RowGroup, Text: "aicova/api", Summary: "●1 ?1"}}
	plain := StripStyles(Render(rows, 35)[0])
	if w := CellWidth(plain); w != 35 {
		t.Fatalf("width = %d: %q", w, plain)
	}
	if !strings.HasSuffix(plain, "●1 ?1") {
		t.Fatalf("summary not at right edge: %q", plain)
	}
}

func TestRenderFooterSaysHowManyTabsRemain(t *testing.T) {
	plain := StripStyles(Render([]Row{{Kind: RowMore, Hidden: 5}}, 35)[0])
	if !strings.Contains(plain, "還有 5 個 tab") {
		t.Fatalf("footer = %q", plain)
	}
}

func TestRenderTitleRowIsExactlyWidthWithSummaryOnTheRight(t *testing.T) {
	rows, _ := Fit(sample(), 40)
	plain := StripStyles(Render(rows, 36)[0])
	if CellWidth(plain) != 36 || !strings.HasPrefix(plain, "◆ OPENCODE") || !strings.HasSuffix(plain, "●2 ?1 ◆1 ✗1 !1") {
		t.Fatalf("title = %q (%d cells)", plain, CellWidth(plain))
	}
}

func TestPublishCoversEveryAvailableHeightWithItsOwnLayout(t *testing.T) {
	p := Publish(sample(), 36)
	// Selecting by avail must reproduce what Fit+Render give for that avail.
	for avail := 3; avail <= 30; avail++ {
		rows, _ := Fit(sample(), avail)
		want := strings.Join(Render(rows, 36), "\n")
		if got := p.Select(avail); got != want {
			t.Fatalf("avail %d:\n got %q\nwant %q", avail, got, want)
		}
	}
}

func TestPublishFormatSelectsByClientHeightAndStatusLines(t *testing.T) {
	p := Publish(sample(), 36)
	f := p.Format()
	for _, must := range []string{"#{client_height}", "#{status}", "#{@tos_sb_"} {
		if !strings.Contains(f, must) {
			t.Fatalf("format lacks %q: %s", must, f)
		}
	}
	if strings.Contains(f, "#{E:") || strings.Contains(f, "#{T:") {
		t.Fatalf("format must not re-expand option content: %s", f)
	}
}

func TestPublishOnlyEmitsDistinctLayouts(t *testing.T) {
	p := Publish(sample(), 36)
	seen := map[string]bool{}
	for _, v := range p.Options {
		if seen[v] {
			t.Fatalf("duplicate layout published")
		}
		seen[v] = true
	}
	if len(p.Options) < 2 {
		t.Fatalf("options = %d", len(p.Options))
	}
}
