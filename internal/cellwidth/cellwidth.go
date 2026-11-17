// Package cellwidth 以 tmux 實際繪製側欄的方式計算終端格寬。
//
// 逐碼點寬度表（table.go）取自 tmux next-3.9（utf8proc）的全碼點掃描；群集合併規則
// 對應 tmux 的 screen_write_combine：零寬字元、ZWJ、VS16、國旗、膚色修飾與 Hangul jamo
// 會併入前一格，所以 👨‍👩‍👧 只佔 2 格，而不是 tmux 的 #{w:} 逐碼點加總得到的 6。
package cellwidth

import "sort"

const maxClusterBytes = 32 // tmux UTF8_SIZE

func in(spans []span, r rune) bool {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].hi >= r })
	return i < len(spans) && spans[i].lo <= r
}

func rune_(r rune) int {
	switch {
	case r < 0x20, r >= 0x7f && r < 0xa0:
		return 0
	case r < 0xa0:
		return 1
	case in(zero, r):
		return 0
	case in(wide, r):
		return 2
	}
	return 1
}

const (
	zwj          = 0x200D
	vs16         = 0xFE0F
	hangulFiller = 0x3164
)

func isRI(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func isSkinTone(r rune) bool { return r >= 0x1F3FB && r <= 0x1F3FF }

// skinToneBase 是 tmux 允許與膚色修飾合併的 emoji（utf8_should_combine）。
func skinToneBase(r rune) bool {
	switch r {
	case 0x261D, 0x26F9, 0x270A, 0x270B, 0x270C, 0x270D, 0x1F44B, 0x1F44C, 0x1F44D, 0x1F44E, 0x1F44F, 0x1F450,
		0x1F466, 0x1F467, 0x1F468, 0x1F469, 0x1F46E, 0x1F470, 0x1F471, 0x1F472, 0x1F473, 0x1F474, 0x1F475,
		0x1F476, 0x1F477, 0x1F478, 0x1F47C, 0x1F481, 0x1F482, 0x1F483, 0x1F485, 0x1F486, 0x1F487, 0x1F4AA,
		0x1F575, 0x1F57A, 0x1F590, 0x1F595, 0x1F596, 0x1F645, 0x1F646, 0x1F647, 0x1F64B, 0x1F64C, 0x1F64D,
		0x1F64E, 0x1F64F, 0x1F6B4, 0x1F6B5, 0x1F6B6, 0x1F926, 0x1F937, 0x1F938, 0x1F939, 0x1F93D, 0x1F93E,
		0x1F9B5, 0x1F9B6, 0x1F9B8, 0x1F9B9, 0x1F9CD, 0x1F9CE, 0x1F9CF, 0x1F9D1, 0x1F9D2, 0x1F9D3, 0x1F9D4,
		0x1F9D5, 0x1F9D6, 0x1F9D7, 0x1F9D8, 0x1F9D9, 0x1F9DA, 0x1F9DB, 0x1F9DC, 0x1F9DD, 0x1F9DE, 0x1F9DF:
		return true
	}
	return false
}

type cell struct {
	text  string
	width int
	first rune // 群集的第一個碼點（tmux 用它判斷膚色、國旗）
	last  rune
	ri    int // 國旗符號數
}

type jamo int

const (
	notJamo jamo = iota
	choseong
	jungseong
	jongseong
)

// jamoClass 對應 tmux hanguljamo_get_class（含填充字元與古韓文）。
func jamoClass(r rune) jamo {
	switch {
	case r >= 0x1100 && r <= 0x115F, r >= 0xA960 && r <= 0xA97C:
		return choseong
	case r >= 0x1160 && r <= 0x11A7, r >= 0xD7B0 && r <= 0xD7C6:
		return jungseong
	case r >= 0x11A8 && r <= 0x11FF, r >= 0xD7CB && r <= 0xD7FB:
		return jongseong
	}
	return notJamo
}

// Standalone 回報 r 放在字串開頭時是否會自己佔格：零寬字元、ZWJ、VS16、Hangul filler
// 與不接在前一個 jamo 後面的中／終聲都不會，渲染端用它清掉標題開頭的這類字元，
// 讓開頭一定是能獨立繪製的字元，不論前面是否已有其他格。
func Standalone(r rune) bool {
	if r == zwj || r == vs16 || r == hangulFiller {
		return false
	}
	if c := jamoClass(r); c == jungseong || c == jongseong {
		return false
	}
	return rune_(r) > 0
}

// cells 把字串切成 tmux 會畫在同一格的群集（對應 screen_write_combine）。
// 開頭沒有前一格可併入的零寬字元不畫、不佔格，以空寬群集保留在原位。
func cells(s string) []cell {
	var out []cell
	for _, r := range s {
		w := rune_(r)
		if r == hangulFiller {
			if len(out) == 0 {
				out = append(out, cell{text: string(r)})
			} else {
				out[len(out)-1].text += string(r)
			}
			continue
		}
		if len(out) == 0 || out[len(out)-1].width == 0 {
			if w == 0 || r == zwj || r == vs16 {
				if len(out) == 0 {
					out = append(out, cell{text: string(r)})
				} else {
					out[len(out)-1].text += string(r)
				}
				continue
			}
			// 前面只有不畫的字元：這是第一個真正的格，開頭的不畫字元併進它的文字。
			var prefix string
			if len(out) == 1 && out[0].width == 0 {
				prefix, out = out[0].text, nil
			}
			out = append(out, cell{text: prefix + string(r), width: w, first: r, last: r, ri: b2i(isRI(r))})
			continue
		}
		last := &out[len(out)-1]
		if len(last.text)+len(string(r)) > maxClusterBytes {
			if w == 0 || r == zwj || r == vs16 {
				continue
			}
		} else if r < 0x80 {
			// tmux 不把單位元組字元併入前一格（screen_write_combine：ud->size < 2）
		} else {
			if r == vs16 {
				last.text += string(r)
				if last.width == 1 {
					last.width = 2
				}
				last.last = r
				continue
			}
			if r == zwj || w == 0 {
				last.text += string(r)
				last.last = r
				continue
			}
			switch jamoClass(r) {
			case choseong:
			case jungseong:
				if jamoClass(last.last) == choseong {
					last.text += string(r)
					last.last = r
				} // 否則 tmux 丟棄
				continue
			case jongseong:
				if jamoClass(last.last) == jungseong {
					last.text += string(r)
					last.last = r
				}
				continue
			default:
				switch {
				case isRI(r) && isRI(last.first) && last.ri == 1:
					last.text += string(r)
					last.last, last.ri = r, 2
					if last.width == 1 {
						last.width = 2
					}
					continue
				case isSkinTone(r) && skinToneBase(last.first):
					last.text += string(r)
					last.last = r
					if last.width == 1 {
						last.width = 2
					}
					continue
				case last.last == zwj:
					last.text += string(r)
					last.last = r
					continue
				}
			}
		}
		out = append(out, cell{text: string(r), width: w, first: r, last: r, ri: b2i(isRI(r))})
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Clusters 回傳 s 的群集，保證不會在群集中間切開。
func Clusters(s string) []string {
	cs := cells(s)
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.text
	}
	return out
}

// Width 回傳單一群集的格寬。
func Width(cluster string) int { return String(cluster) }

// String 回傳 s 在 tmux 側欄中佔的格數。
func String(s string) int {
	n := 0
	for _, c := range cells(s) {
		n += c.width
	}
	return n
}
