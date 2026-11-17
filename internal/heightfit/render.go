package heightfit

import (
	"strings"
	"unicode"
)

var stateStyle = map[State]struct{ sym, style string }{
	Running:   {"●", "fg=green"},
	Attention: {"?", "fg=yellow,bold"},
	Unread:    {"◆", "fg=blue"},
	Failed:    {"✗", "fg=red"},
	Unknown:   {"!", "fg=colour244"},
	Idle:      {"·", "fg=colour240"},
}

var summaryStyle = map[string]string{
	"●": "fg=green", "?": "fg=yellow", "◆": "fg=blue", "✗": "fg=red", "!": "fg=colour244",
}

// Render turns fitted rows into tmux side-status lines of at most width cells.
// Titles and names are data: every '#' is doubled so tmux draws it literally,
// control characters become spaces. Output is meant to be stored in a user
// option and read back with #{@opt} (never #{E:@opt}), so it is expanded once.
func Render(rows []Row, width int) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, renderRow(r, width))
	}
	return out
}

func renderRow(r Row, width int) string {
	switch r.Kind {
	case RowTitle:
		return styled("fg=magenta,bold", "◆ ") + styled("bold", "OPENCODE") + rightSummary(10, r.Summary, width)
	case RowRule:
		return styled("fg=colour238", strings.Repeat("─", width))
	case RowGroup:
		name := clean(r.Text)
		room := width - CellWidth(r.Summary) - 1
		if r.Summary == "" {
			room = width
		}
		name = truncate(name, room)
		return styled("fg=white,bold", esc(name)) + rightSummary(CellWidth(name), r.Summary, width)
	case RowTab:
		s := stateStyle[r.State]
		title := truncate(clean(r.Text), width-2)
		ts := "fg=colour252"
		switch r.State {
		case Idle:
			ts = "fg=colour243"
		case Unread, Failed:
			ts = "fg=colour252,bold"
		}
		return styled(s.style, s.sym) + " " + styled(ts, esc(title))
	case RowMore:
		return styled("fg=colour244", truncate("… 還有 "+itoa(r.Hidden)+" 個 tab", width))
	}
	return ""
}

// rightSummary pads so the summary ends exactly at the right edge; used is the
// number of cells already on the line.
func rightSummary(used int, summary string, width int) string {
	if summary == "" {
		return ""
	}
	pad := width - used - CellWidth(summary)
	if pad < 1 {
		pad = 1
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", pad))
	for i, part := range strings.Split(summary, " ") {
		if i > 0 {
			b.WriteString(" ")
		}
		sym := string([]rune(part)[:1])
		b.WriteString(styled(summaryStyle[sym], part))
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

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// truncate cuts s to at most max cells, ending in … when something was cut.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if CellWidth(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > max-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// CellWidth approximates tmux's terminal cell width: combining marks are 0,
// East Asian wide/fullwidth and emoji are 2, everything else 1.
func CellWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	switch {
	case r >= 0x0300 && r <= 0x036F, r == 0x200D, r >= 0xFE00 && r <= 0xFE0F:
		return 0
	case r >= 0x1100 && r <= 0x115F, r >= 0x2E80 && r <= 0xA4CF, r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF, r >= 0xFE30 && r <= 0xFE6F, r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6, r >= 0x1F300 && r <= 0x1F64F, r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

// StripStyles removes #[...] and undoes ## escaping, giving the visible text.
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
