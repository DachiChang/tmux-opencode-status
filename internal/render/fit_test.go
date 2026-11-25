package render

import (
	"strings"
	"testing"
)

func sample() []Group {
	return []Group{
		{Name: "aicova/api", Tabs: []Tab{{Title: "a1", State: Running}, {Title: "a2", State: Attention}, {Title: "a3", State: Idle}}},
		{Name: "infra", Tabs: []Tab{{Title: "b1", State: FailedUnread}, {Title: "b2", State: FinishedUnread}}},
		{Name: "profcoo/api", Tabs: []Tab{{Title: "c1", State: DataProblem}, {Title: "c2", State: Running}}},
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

func TestFitShowsEveryRowInOrder(t *testing.T) {
	// title + rule + 3 headers + 2 blanks + 7 tabs = 14 rows
	rows := Fit(sample())
	if len(rows) != 14 || countKind(rows, RowTab) != 7 || countKind(rows, RowGroup) != 3 {
		t.Fatalf("rows=%d tabs=%d groups=%d", len(rows), countKind(rows, RowTab), countKind(rows, RowGroup))
	}
	want := []string{"a1", "a2", "a3", "b1", "b2", "c1", "c2"}
	got := tabTitles(rows)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tabs = %v, want %v", got, want)
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

func TestTitleSummaryCountsEveryTab(t *testing.T) {
	rows := Fit(sample())
	const want = "●2 ?1 ◆1 ✗1 !1" // idle a3 omitted
	if rows[0].Summary != want {
		t.Fatalf("summary=%q want=%q", rows[0].Summary, want)
	}
}

func TestAllIdleHasNoSummary(t *testing.T) {
	rows := Fit([]Group{{Name: "x", Tabs: []Tab{{Title: "t", State: Idle}}}})
	if rows[0].Summary != "" {
		t.Fatalf("summary = %q", rows[0].Summary)
	}
}

func TestRenderTruncatesByCellWidthWithEllipsis(t *testing.T) {
	rows := []Row{{Kind: RowTab, Text: "中文標題很長很長很長很長很長很長很長很長很長", State: Running}}
	line := Render(rows, 20, DefaultTheme())[0]
	plain := StripStyles(line)
	if w := CellWidth(plain); w > 19 {
		t.Fatalf("width = %d: %q", w, plain)
	}
	if !strings.HasSuffix(plain, "…") {
		t.Fatalf("no ellipsis: %q", plain)
	}
}

func TestRenderEscapesTmuxFormatCharactersInTitles(t *testing.T) {
	rows := []Row{{Kind: RowTab, Text: "x #[fg=red] #{pane_id} #(id) , }", State: Idle}}
	line := Render(rows, 36, DefaultTheme())[0]
	for _, bad := range []string{"#[fg=red]", "#{pane_id}", "#(id)"} {
		if strings.Contains(strings.ReplaceAll(line, "##", ""), bad) {
			t.Fatalf("unescaped %q in %q", bad, line)
		}
	}
}

func TestRenderRightAlignsSummaryWithinWidth(t *testing.T) {
	rows := []Row{{Kind: RowGroup, Text: "aicova/api", Summary: "●1 ?1"}}
	plain := StripStyles(Render(rows, 35, DefaultTheme())[0])
	if w := CellWidth(plain); w != 34 {
		t.Fatalf("width = %d: %q", w, plain)
	}
	if !strings.HasSuffix(plain, "●1 ?1") {
		t.Fatalf("summary not at right edge: %q", plain)
	}
}

func TestRenderTitleRowIsExactlyWidthWithSummaryOnTheRight(t *testing.T) {
	rows := Fit(sample())
	plain := StripStyles(Render(rows, 36, DefaultTheme())[0])
	if CellWidth(plain) != 35 || !strings.HasPrefix(plain, "  OPENCODE ") || !strings.HasSuffix(plain, "●2 ?1 ◆1 ✗1 !1") {
		t.Fatalf("title = %q (%d cells)", plain, CellWidth(plain))
	}
}

func TestNoticeLinesPrecedeTheListAndNothingIsDropped(t *testing.T) {
	rows := FitView(View{Groups: sample(), Notice: "一\n二\n三\n四\n五"})
	if rows[0].Kind != RowTitle || rows[1].Kind != RowRule {
		t.Fatalf("should start with the title and rule: %+v", rows[:2])
	}
	if n := countKind(rows, RowNotice); n != 5 {
		t.Fatalf("Notice should have 5 lines, got %d", n)
	}
	if got := len(tabTitles(rows)); got != 7 {
		t.Fatalf("all tabs should be kept, got %d", got)
	}
}
