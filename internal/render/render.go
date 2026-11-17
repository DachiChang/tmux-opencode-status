package render

import (
	"strings"
	"unicode"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
)

// 色值取自使用者確認的 POC（@oc_side）：深色文字層次、無底色。
const (
	colPurple = "#A78BFA"
	colBrand  = "#A3A3A3"
	colRule   = "#404040"
	colGroup  = "#FAFAFA"
	colTitle  = "#D4D4D4"
	colGreen  = "#4ADE80"
	colYellow = "#FACC15"
	colBlue   = "#60A5FA"
	colRed    = "#F87171"
	colGray   = "#737373"
)

var stateStyle = map[State]struct{ sym, style string }{
	Running:   {"●", "fg=" + colGreen + ",bold"},
	Attention: {"?", "fg=" + colYellow + ",bold"},
	Unread:    {"◆", "fg=" + colBlue + ",bold"},
	Failed:    {"✗", "fg=" + colRed + ",bold"},
	Unknown:   {"!", "fg=" + colGray + ",bold"},
	Idle:      {"·", "fg=" + colGray + ",bold"},
}

var summaryStyle = map[string]string{
	"●": "fg=" + colGreen, "?": "fg=" + colYellow, "◆": "fg=" + colBlue, "✗": "fg=" + colRed, "!": "fg=" + colGray,
}

const (
	margin    = 1 // 左右各留一格，與 POC 相同
	tabIndent = 2 // tab 行縮排
)

// Render 把排好的列轉成 tmux side-status 的行。width 是側欄內容寬度（36 格扣掉分隔線）；
// 每行最多 width-margin 格，所以最右一格保持空白。
//
// 標題與名稱都是資料：每個 # 雙寫，控制字元換成空白，雙向控制字元移除，每份文字依格寬
// 以群集為單位截斷。輸出存進使用者選項並以 #{@opt} 讀取（絕不用 #{E:@opt}／#{T:@opt}），
// 因此只展開一次。
func Render(rows []Row, width int) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, renderRow(r, max(width-margin, 1)))
	}
	return out
}

func renderRow(r Row, usable int) string {
	pad := strings.Repeat(" ", margin)
	switch r.Kind {
	case RowTitle:
		const used = margin + 2 + 8
		return pad + styled("fg="+colPurple+",bold", "◆") + " " + styled("fg="+colBrand, "OPENCODE") + rightSummary(used, r.Summary, usable)
	case RowRule:
		return pad + styled("fg="+colRule, strings.Repeat("─", max(usable-margin, 0)))
	case RowGroup:
		room := usable - margin
		if r.Summary != "" {
			room -= cellwidth.String(r.Summary) + 1
		}
		name := truncate(clean(r.Text), room)
		return pad + styled("fg="+colGroup+",bold", esc(name)) + rightSummary(margin+cellwidth.String(name), r.Summary, usable)
	case RowTab:
		s := stateStyle[r.State]
		title := truncate(clean(r.Text), usable-tabIndent-2)
		ts := "fg=" + colTitle
		switch r.State {
		case Idle:
			ts = "fg=" + colGray
		case Unread, Failed:
			ts = "fg=" + colTitle + ",bold"
		}
		return strings.Repeat(" ", tabIndent) + styled(s.style, s.sym) + " " + styled(ts, esc(title))
	case RowNote:
		indent := margin + r.Indent
		return strings.Repeat(" ", indent) + styled("fg="+colGray, esc(truncate(clean(r.Text), usable-indent)))
	case RowNotice:
		return pad + styled("fg="+colBrand, esc(truncate(clean(r.Text), usable-margin)))
	case RowMore:
		return pad + styled("fg="+colGray, esc(truncate("… 還有 "+itoa(r.Hidden)+" 個 tab", usable-margin)))
	}
	return ""
}

// rightSummary 補空白讓摘要的最後一格貼齊 usable；used 是該行已占的格數。
// 空間不夠（極端大量 tab）時，保留優先順序靠前的符號，其餘以 … 收尾，永不越界。
func rightSummary(used int, summary string, usable int) string {
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
			b.WriteString(styled("fg="+colGray, part))
			continue
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

// clean 讓資料只剩一行可見字元：控制字元（含換行、ESC、NUL）與行／段落分隔符變成空白，
// 雙向控制與 BOM 直接移除（它們會改變相鄰文字的顯示順序）。開頭無法獨立繪製的字元
// （VS16、ZWJ、組合字元、孤立 Hangul 中／終聲）也移除：它們會併進前面的空白或符號，
// 使 tmux 把該格畫成 2 格而讓整行越界。
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

// truncate 把 s 截到最多 max 格；有截斷時以 … 結尾。以 tmux 實際會畫在同一格的群集為單位，
// 不會切開組合字元、ZWJ emoji、國旗或膚色修飾。
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
	// 結尾的 ZWJ 會把緊接的 … 併進前一格，使省略號看不見。
	return strings.TrimRight(b.String(), "\u200d") + "…"
}

// wrap 把 s 依格寬折成最多 width 格的行；不拆群集。
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

// CellWidth 是 tmux 實際繪製的格寬（見 cellwidth 套件）。
func CellWidth(s string) int { return cellwidth.String(s) }

// StripStyles 移除 #[...] 並還原 ## 跳脫，得到可見文字。
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
