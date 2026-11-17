package heightfit

import "strconv"

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
)

type Row struct {
	Kind    RowKind
	Text    string
	Summary string
	State   State
	Hidden  int
}

// Fit lays the logical list out for one client. avail is that client's own
// side-status row count. Only whole tab rows are kept, in source order; when
// anything is hidden the last row is a footer carrying the number of hidden
// tabs (not groups, not text rows).
func Fit(groups []Group, avail int) ([]Row, int) {
	total := 0
	for _, g := range groups {
		total += len(g.Tabs)
	}
	head := []Row{{Kind: RowTitle, Summary: Summary(groups)}, {Kind: RowRule}}

	if full := layout(groups, total); len(head)+len(full) <= avail {
		return append(head, full...), 0
	}
	budget := avail - len(head) - 1 // one row reserved for the footer
	shown := 0
	for shown < total && len(layout(groups, shown+1)) <= budget {
		shown++
	}
	rows := append(head, layout(groups, shown)...)
	rows = append(rows, Row{Kind: RowMore, Hidden: total - shown})
	if len(rows) > avail {
		rows = rows[:max(avail, 0)]
	}
	return rows, total - shown
}

// layout renders the first n tabs (in order) with group headers and blanks.
func layout(groups []Group, n int) []Row {
	var rows []Row
	for _, g := range groups {
		if n == 0 {
			break
		}
		if len(rows) > 0 {
			rows = append(rows, Row{Kind: RowBlank})
		}
		rows = append(rows, Row{Kind: RowGroup, Text: g.Name, Summary: Summary([]Group{g})})
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

// Summary counts every tab of the given groups, visible or not, in the order
// ● ? ◆ ✗ !, omitting zero counts and idle.
func Summary(groups []Group) string {
	var c [6]int
	for _, g := range groups {
		for _, t := range g.Tabs {
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
