package heightfit

import (
	"strconv"
	"strings"
)

type State int

const (
	Idle State = iota
	Running
	Attention
	Unread
	Failed
	Unknown
)

type Tab struct {
	Title string
	State State
	// Session 是 tab 對應的 session；非空時摘要按 session 去重（同一 session
	// 出現在多個 tab 清單也只計一次）。
	Session string
}

type Group struct {
	Name string
	Tabs []Tab
}

type RowKind int

const (
	RowTitle RowKind = iota
	RowRule
	RowGroup
	RowTab
	RowBlank
	RowMore
	RowNote   // dim informational line, e.g. 沒有 OpenCode TUI / 無 tab
	RowNotice // read-error banner, drawn in warning colour
)

type Row struct {
	Kind    RowKind
	Text    string
	Summary string
	State   State
	Hidden  int
	Indent  int // RowNote 額外縮排格數
}

// View is everything the sidebar can show: the logical list plus the two
// non-list lines. Empty is shown when there are no groups at all; Notice is a
// single banner under the header (data could not be read, list may be stale).
// A Notice may carry several lines separated by "\n"; PublishView wraps a long
// notice to the sidebar width that way.
type View struct {
	Groups []Group
	Empty  string
	Notice string
}

// Fit lays the logical list out for one client. avail is that client's own
// side-status row count. Only whole tab rows are kept, in source order; when
// anything is hidden the last row is a footer carrying the number of hidden
// tabs (not groups, not text rows).
func Fit(groups []Group, avail int) ([]Row, int) {
	return FitView(View{Groups: groups}, avail)
}

// FitView is Fit for a full View. A group without tabs is kept and shows a
// "無 tab" note; that note is not a tab and is not counted as hidden.
func FitView(v View, avail int) ([]Row, int) {
	groups := v.Groups
	head := []Row{{Kind: RowTitle, Summary: Summary(groups)}, {Kind: RowRule}}
	if v.Notice != "" {
		for _, line := range strings.Split(v.Notice, "\n") {
			head = append(head, Row{Kind: RowNotice, Text: line})
		}
	}
	if len(groups) == 0 {
		rows := head
		if v.Empty != "" {
			rows = append(rows, Row{Kind: RowNote, Text: v.Empty})
		}
		return clip(rows, avail), 0
	}
	total, units := 0, 0
	for _, g := range groups {
		total += len(g.Tabs)
		units += max(len(g.Tabs), 1)
	}

	if full := layout(groups, units); len(head)+len(full) <= avail {
		return append(head, full...), 0
	}
	budget := avail - len(head) - 1 // one row reserved for the footer
	shown := 0
	for shown < units && len(layout(groups, shown+1)) <= budget {
		shown++
	}
	body := layout(groups, shown)
	visible := 0
	for _, r := range body {
		if r.Kind == RowTab {
			visible++
		}
	}
	rows := append(head, body...)
	if visible == total {
		return clip(rows, avail), 0
	}
	rows = append(rows, Row{Kind: RowMore, Hidden: total - visible})
	return clip(rows, avail), total - visible
}

func clip(rows []Row, avail int) []Row {
	if len(rows) > avail {
		rows = rows[:max(avail, 0)]
	}
	return rows
}

// layout renders the first n units (in order) with group headers and blanks.
// A unit is one tab, or the "無 tab" note of a group that has none.
func layout(groups []Group, n int) []Row {
	var rows []Row
	for _, g := range groups {
		if n == 0 {
			break
		}
		if len(rows) > 0 {
			rows = append(rows, Row{Kind: RowBlank})
		}
		rows = append(rows, Row{Kind: RowGroup, Text: g.Name, Summary: groupSummary(g)})
		if len(g.Tabs) == 0 {
			rows = append(rows, Row{Kind: RowNote, Text: "無 tab", Indent: tabIndent})
			n--
			continue
		}
		for _, t := range g.Tabs {
			if n == 0 {
				break
			}
			rows = append(rows, Row{Kind: RowTab, Text: t.Title, State: t.State})
			n--
		}
	}
	return rows
}

// Summary counts the tabs of the given groups, visible or not, in the order
// ● ? ◆ ✗ !, omitting zero counts and idle. A session shown by several tabs is
// counted once: this is the whole-sidebar rule.
func Summary(groups []Group) string { return summarize(groups, true) }

// groupSummary is the per-group rule: every member tab counts once, even when
// another group lists the same session.
func groupSummary(g Group) string { return summarize([]Group{g}, false) }

func summarize(groups []Group, dedupe bool) string {
	var c [6]int
	seen := map[string]bool{}
	for _, g := range groups {
		for _, t := range g.Tabs {
			if dedupe && t.Session != "" {
				if seen[t.Session] {
					continue
				}
				seen[t.Session] = true
			}
			c[t.State]++
		}
	}
	out := ""
	for _, e := range []struct {
		sym string
		n   int
	}{{"●", c[Running]}, {"?", c[Attention]}, {"◆", c[Unread]}, {"✗", c[Failed]}, {"!", c[Unknown]}} {
		if e.n == 0 {
			continue
		}
		if out != "" {
			out += " "
		}
		out += e.sym + itoa(e.n)
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
