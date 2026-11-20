// Package render 把側欄的邏輯內容（群組、tab、狀態、提示）排成一份完整版面、
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
	// JumpTarget 是點擊這個群組時要跳去的 pane 編號（`%N` 的 N，十進位數字）；
	// 空字串表示沒有跳轉目標，該群組的列不加 range。
	JumpTarget string
}

type RowKind int

const (
	RowTitle RowKind = iota
	RowRule
	RowGroup
	RowTab
	RowBlank
	RowNote   // dim informational line, e.g. 沒有 OpenCode TUI / 無 tab
	RowNotice // read-error banner, drawn in warning colour
)

type Row struct {
	Kind    RowKind
	Text    string
	Summary string
	State   State
	Indent  int // RowNote 額外縮排格數；RowNotice 為折行續行的縮排
	// JumpTarget 是所屬群組的跳轉目標（群組列、tab 列與群組內的「無 tab」列才會有）；空字串表示不加 range。
	JumpTarget string
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

// Fit lays the logical list out in full: every group and every tab, in source
// order. Cutting to a client's height is left to tmux, which truncates from the
// bottom.
func Fit(groups []Group) []Row {
	return FitView(View{Groups: groups})
}

// FitView is Fit for a full View. A group without tabs is kept and shows a
// "無 tab" note.
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
func layout(groups []Group) []Row {
	var rows []Row
	for _, g := range groups {
		if len(rows) > 0 {
			rows = append(rows, Row{Kind: RowBlank})
		}
		rows = append(rows, Row{Kind: RowGroup, Text: g.Name, Summary: groupSummary(g), JumpTarget: g.JumpTarget})
		if len(g.Tabs) == 0 {
			rows = append(rows, Row{Kind: RowNote, Text: "無 tab", Indent: tabIndent - margin, JumpTarget: g.JumpTarget})
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
		out += e.sym + itoa(e.n)
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
