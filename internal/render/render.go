package render

import (
	"strings"
	"unicode"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
)

const (
	margin            = 1  // one cell of margin on each side, same as the POC
	tabIndent         = 2  // indent of tab rows
	barWidth          = 1  // ▎ before the project name
	noticePrefixWidth = 2  // cell width of the "▲ " before a notice
	titleBlock        = 10 // the " OPENCODE " block
	tagWidth          = 3  // jump label " a "
)

// Render turns laid-out rows into tmux side-status lines. width is the sidebar content width (36 cells minus the divider);
// each line has at most width-margin cells, so the rightmost cell stays blank.
//
// Titles and names are data: every # is doubled, control characters become spaces, bidirectional
// control characters are removed, and each piece of text is truncated by cell width in whole
// clusters. The output is stored in a user option and read with #{@opt} (never #{E:@opt} or
// #{T:@opt}), so it is expanded only once.
func Render(rows []Row, width int, th Theme) []string { return render(rows, width, th, false) }

// RenderJump is the jump mode layout: its row count, ranges, and non-group rows are identical to Render's; only group rows
// that were assigned a letter replace the leading ▎ with a jump label.
func RenderJump(rows []Row, width int, th Theme) []string { return render(rows, width, th, true) }

func render(rows []Row, width int, th Theme, jump bool) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, jumpRange(r.JumpTarget, renderRow(r, max(width-margin, 1), th, jump)))
	}
	return out
}

// JumpRangePrefix is the fixed prefix before the pane number in a click range string (the range string is oc<N>).
// The writer (jumpRange) and tmux's MouseDown1SideStatus binding share this constant.
const JumpRangePrefix = "oc"

// jumpRange wraps a whole line in a click range: tmux's MouseDown1SideStatus binding extracts the pane number from the range string oc<N>.
// Each line starts and ends its own range and does not rely on a range continuing across newlines; the markers take no cell width.
// The argument is only the fixed prefix plus decimal digits and is unrelated to user data, so it needs no escaping.
func jumpRange(target, line string) string {
	if target == "" {
		return line
	}
	return "#[range=user|" + JumpRangePrefix + target + "]" + line + "#[norange]"
}

func renderRow(r Row, usable int, th Theme, jump bool) string {
	pad := strings.Repeat(" ", margin)
	switch r.Kind {
	case RowTitle:
		const used = margin + titleBlock
		return pad + styled("bg="+th.Emphasis+",fg="+th.Title, " OPENCODE ") + rightSummary(used, r.Summary, usable, th)
	case RowRule:
		return pad + styled("fg="+th.Split, strings.Repeat("─", max(usable-margin, 0)))
	case RowGroup:
		lead, leadWidth := pad+"#[fg="+th.Emphasis+"]▎", margin+barWidth
		if jump && strings.IndexByte(JumpLetters, r.Letter) >= 0 {
			// The label (space + letter + space) plus 1 blank cell replaces ▎ and the margin before it; the letter only comes from a–z and is not data.
			lead, leadWidth = styled("bg="+th.Emphasis+",fg="+th.Title, " "+string(r.Letter)+" ")+" ", tagWidth+1
		}
		room := usable - leadWidth
		if r.Summary != "" {
			room -= cellwidth.String(r.Summary) + 1
		}
		name := truncate(clean(r.Text), room)
		return lead + styled("fg="+th.Project, esc(name)) + rightSummary(leadWidth+cellwidth.String(name), r.Summary, usable, th)
	case RowTab:
		sym, color := th.symbol(r.State)
		title := truncate(clean(r.Text), usable-tabIndent-2)
		return strings.Repeat(" ", tabIndent) + styled("fg="+color, sym) + " " + styled("fg="+th.tabTitle(r.State), esc(title))
	case RowNote:
		indent := margin + r.Indent
		return strings.Repeat(" ", indent) + styled("fg="+th.InactiveTab, esc(truncate(clean(r.Text), usable-indent)))
	case RowNotice:
		prefix, indent, room := "▲ ", margin, usable-margin-noticePrefixWidth
		if r.Indent > 0 {
			prefix, indent = "", margin+r.Indent
			room = usable - indent
		}
		text := esc(truncate(clean(r.Text), room))
		return strings.Repeat(" ", indent) + styled("fg="+th.Notice, prefix+text)
	}
	return ""
}

// rightSummary pads with spaces so the summary's last cell lines up with usable; used is the number of cells the line already occupies.
// When there is not enough space (an extreme number of tabs), it keeps the higher-priority symbols and ends the rest with …, and never overflows.
func rightSummary(used int, summary string, usable int, th Theme) string {
	if summary == "" {
		return ""
	}
	parts := strings.Split(summary, " ")
	room := max(usable-used-1, 0)
	for cellwidth.String(strings.Join(parts, " ")) > room && len(parts) > 0 {
		parts = parts[:len(parts)-1]
		if len(parts) == 0 {
			return ""
		}
		if cellwidth.String(strings.Join(parts, " ")+" …") <= room {
			parts = append(parts, "…")
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	text := strings.Join(parts, " ")
	gap := max(usable-used-cellwidth.String(text), 1)
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", gap))
	for i, part := range parts {
		if i > 0 {
			b.WriteString(" ")
		}
		if part == "…" {
			b.WriteString(styled("fg="+th.InactiveTab, part))
			continue
		}
		sym := string([]rune(part)[:1])
		b.WriteString(styled("fg="+th.summaryColor(sym), part))
	}
	return b.String()
}

func styled(style, text string) string {
	if text == "" {
		return ""
	}
	return "#[" + style + "]" + text + "#[default]"
}

func esc(s string) string { return strings.ReplaceAll(s, "#", "##") }

// clean reduces data to a single line of visible characters: control characters (including newline, ESC, and NUL) and
// line/paragraph separators become spaces, and bidirectional controls and the BOM are removed outright (they change the display
// order of adjacent text). Leading characters that cannot be drawn on their own (VS16, ZWJ, combining characters, a stray
// Hangul jungseong/jongseong) are also removed: they would merge into the preceding space or symbol,
// making tmux draw that cell 2 cells wide and overflow the line.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), r == 0x2028, r == 0x2029:
			return ' '
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C, r == 0xFEFF:
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "\uFFFD"))
	return strings.TrimLeftFunc(s, func(r rune) bool { return !cellwidth.Standalone(r) })
}

// truncate cuts s to at most max cells and ends with … when it truncates. It works in the clusters tmux actually draws in one cell,
// so it never splits combining characters, ZWJ emoji, flags, or skin tone modifiers.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if cellwidth.String(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, c := range cellwidth.Clusters(s) {
		cw := cellwidth.String(c)
		if w+cw > max-1 {
			break
		}
		b.WriteString(c)
		w += cw
	}
	// A trailing ZWJ would merge the following … into the previous cell and make the ellipsis invisible.
	return strings.TrimRight(b.String(), "\u200d") + "…"
}

// wrap folds s into lines of at most width cells by cell width; it does not split clusters.
func wrap(s string, width int) []string {
	var lines []string
	var cur strings.Builder
	w := 0
	for _, c := range cellwidth.Clusters(s) {
		cw := cellwidth.String(c)
		if w+cw > width && cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			w = 0
		}
		cur.WriteString(c)
		w += cw
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// CellWidth is the cell width tmux actually draws (see the cellwidth package).
func CellWidth(s string) int { return cellwidth.String(s) }

// StripStyles removes #[...] and undoes ## escaping to get the visible text.
func StripStyles(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && i+1 < len(s) {
			if s[i+1] == '[' {
				j := strings.IndexByte(s[i:], ']')
				if j < 0 {
					break
				}
				i += j
				continue
			}
			if s[i+1] == '#' {
				b.WriteByte('#')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
