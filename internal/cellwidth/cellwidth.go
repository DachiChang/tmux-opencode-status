// Package cellwidth computes terminal cell widths the way tmux actually draws the sidebar.
//
// The per-code-point width table (table.go) comes from a scan of every code point in
// tmux next-3.9 (utf8proc). The cluster merging rules follow tmux's screen_write_combine:
// zero-width characters, ZWJ, VS16, flags, skin tone modifiers, and Hangul jamo merge into
// the previous cell, so 👨‍👩‍👧 takes only 2 cells rather than the 6 that tmux's #{w:} gets by
// summing code points.
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

// skinToneBase is the set of emoji that tmux allows to merge with a skin tone modifier (utf8_should_combine).
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
	first rune // first code point of the cluster (tmux uses it to detect skin tones and flags)
	last  rune
	ri    int // number of regional indicator (flag) symbols
}

type jamo int

const (
	notJamo jamo = iota
	choseong
	jungseong
	jongseong
)

// jamoClass corresponds to tmux hanguljamo_get_class (including filler characters and archaic Hangul).
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

// Standalone reports whether r takes a cell of its own when placed at the start of a string.
// Zero-width characters, ZWJ, VS16, the Hangul filler, and a jungseong/jongseong that does
// not follow a preceding jamo do not. The renderer uses it to strip such characters from
// the start of a title, so the start is always a character that can be drawn on its own,
// whether or not other cells precede it.
func Standalone(r rune) bool {
	if r == zwj || r == vs16 || r == hangulFiller {
		return false
	}
	if c := jamoClass(r); c == jungseong || c == jongseong {
		return false
	}
	return rune_(r) > 0
}

// cells splits a string into the clusters that tmux draws in the same cell (corresponding
// to screen_write_combine). Zero-width characters at the start, with no previous cell to
// merge into, are not drawn and take no cell; they stay in place as a zero-width cluster.
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
			// Only undrawn characters precede this one: it is the first real cell, and the leading undrawn characters merge into its text.
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
			// tmux does not merge single-byte characters into the previous cell (screen_write_combine: ud->size < 2)
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
				} // otherwise tmux discards it
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

// Clusters returns the clusters of s and guarantees that none is split in the middle.
func Clusters(s string) []string {
	cs := cells(s)
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.text
	}
	return out
}

// Width returns the cell width of a single cluster.
func Width(cluster string) int { return String(cluster) }

// String returns the number of cells s takes in the tmux sidebar.
func String(s string) int {
	n := 0
	for _, c := range cells(s) {
		n += c.width
	}
	return n
}
