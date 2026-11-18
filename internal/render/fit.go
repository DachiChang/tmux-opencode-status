// Package render 把側欄的邏輯內容（群組、tab、狀態、提示）依各 client 的可用高度排版、
// 依終端格寬渲染成 tmux 樣式字串，並發布為全域使用者選項與 side-status-format。

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
	// SessionID 是 tab 對應的 session；非空時摘要按 session 去重（同一 session
	// 出現在多個 tab 清單也只計一次）。
	SessionID string
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
	Indent  int // RowNote 額外縮排格數；RowNotice 為折行續行的縮排
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

	// noticeContinuation 標記 Notice 各行是否為前一行折行的續行（PublishView 折行時填入）；
	// 續行不畫 ▲，縮排與文字對齊。
	noticeContinuation []bool
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
	var notice []string
	if v.Notice != "" {
		notice = strings.Split(v.Notice, "\n")
	}
	head := func(noticeLines int) []Row {
		h := []Row{{Kind: RowTitle, Summary: Summary(groups)}, {Kind: RowRule}}
		for i, line := range notice[:noticeLines] {
			row := Row{Kind: RowNotice, Text: line}
			if i < len(v.noticeContinuation) && v.noticeContinuation[i] {
				row.Indent = noticePrefixWidth
			}
			h = append(h, row)
		}
		return h
	}
	if len(groups) == 0 {
		rows := head(len(notice))
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

	if full := layout(groups, units); len(head(len(notice)))+len(full) <= avail {
		return append(head(len(notice)), full...), 0
	}
	// Something has to be hidden, so the summary row, the rule and the footer are
	// not negotiable: notice lines are the ones that give way.
	lines := min(len(notice), max(avail-3, 0))
	h := head(lines)
	budget := avail - len(h) - 1 // one row reserved for the footer
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
	rows := append(h, body...)
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
			rows = append(rows, Row{Kind: RowNote, Text: "無 tab", Indent: tabIndent - margin})
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
		out += e.sym + itoa(e.n)
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
