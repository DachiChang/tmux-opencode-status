// Package render lays out the sidebar's logical content (groups, tabs, states, notices) as
// one complete layout, renders it into tmux-styled strings by terminal cell width, and
// publishes it as global user options and side-status-format.

package render

import (
	"strconv"
	"strings"
)

type State int

const (
	Idle State = iota
	Running
	Attention
	FinishedUnread
	FailedUnread
	DataProblem
)

type Tab struct {
	Title string
	State State
	// SessionID is the session the tab corresponds to; when non-empty, the summary
	// deduplicates by session (a session that appears in several tab lists is counted once).
	SessionID string
}

type Group struct {
	Name string
	Tabs []Tab
	// JumpTarget is the pane number to jump to when this group is clicked (the N in `%N`,
	// as decimal digits); an empty string means there is no jump target, and the group's
	// rows get no range.
	JumpTarget string
}

type RowKind int

const (
	RowTitle RowKind = iota
	RowRule
	RowGroup
	RowTab
	RowBlank
	RowNote   // dim informational line, e.g. No OpenCode TUI running / No tabs
	RowNotice // read-error banner, drawn in warning colour
)

type Row struct {
	Kind    RowKind
	Text    string
	Summary string
	State   State
	Indent  int // extra indent cells for RowNote; for RowNotice, the indent of wrapped continuation lines
	// JumpTarget is the jump target of the owning group (only group rows, tab rows, and a group's "No tabs" row have one); an empty string means no range is added.
	JumpTarget string
	// Letter is the jump letter ('a'–'z') assigned to a group row, or 0 for none; only group rows have one.
	Letter byte
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

	// noticeContinuation marks whether each Notice line is a continuation of the previous
	// wrapped line (filled in by PublishView when wrapping); continuation lines do not draw
	// ▲, and their indent aligns with the text.
	noticeContinuation []bool
}

// Fit lays the logical list out in full: every group and every tab, in source
// order. Cutting to a client's height is left to tmux, which truncates from the
// bottom.
func Fit(groups []Group) []Row {
	return FitView(View{Groups: groups})
}

// FitView is Fit for a full View. A group without tabs is kept and shows a
// "No tabs" note.
func FitView(v View) []Row {
	groups := v.Groups
	rows := []Row{{Kind: RowTitle, Summary: Summary(groups)}, {Kind: RowRule}}
	if v.Notice != "" {
		for i, line := range strings.Split(v.Notice, "\n") {
			row := Row{Kind: RowNotice, Text: line}
			if i < len(v.noticeContinuation) && v.noticeContinuation[i] {
				row.Indent = noticePrefixWidth
			}
			rows = append(rows, row)
		}
	}
	if len(groups) == 0 {
		if v.Empty != "" {
			rows = append(rows, Row{Kind: RowNote, Text: v.Empty})
		}
		return rows
	}
	return append(rows, layout(groups)...)
}

// layout renders every group (in order) with group headers and blanks.
// Jump letters are assigned in group order, only to groups that have a jump target, until a–z run out.
func layout(groups []Group) []Row {
	var rows []Row
	next := 0
	for _, g := range groups {
		if len(rows) > 0 {
			rows = append(rows, Row{Kind: RowBlank})
		}
		head := Row{Kind: RowGroup, Text: g.Name, Summary: groupSummary(g), JumpTarget: g.JumpTarget}
		if g.JumpTarget != "" && next < len(JumpLetters) {
			head.Letter = JumpLetters[next]
			next++
		}
		rows = append(rows, head)
		if len(g.Tabs) == 0 {
			rows = append(rows, Row{Kind: RowNote, Text: "No tabs", Indent: tabIndent - margin, JumpTarget: g.JumpTarget})
			continue
		}
		for _, t := range g.Tabs {
			rows = append(rows, Row{Kind: RowTab, Text: t.Title, State: t.State, JumpTarget: g.JumpTarget})
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
			if dedupe && t.SessionID != "" {
				if seen[t.SessionID] {
					continue
				}
				seen[t.SessionID] = true
			}
			c[t.State]++
		}
	}
	out := ""
	for _, e := range []struct {
		sym string
		n   int
	}{{"●", c[Running]}, {"?", c[Attention]}, {"◆", c[FinishedUnread]}, {"✗", c[FailedUnread]}, {"!", c[DataProblem]}} {
		if e.n == 0 {
			continue
		}
		if out != "" {
			out += " "
		}
		out += e.sym + strconv.Itoa(e.n)
	}
	return out
}
