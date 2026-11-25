package cellwidth_test

import (
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
)

// The expected values come from what tmux next-3.9 actually draws on an attached client:
// fill a 19-cell-wide sidebar with the same character (cluster) and count how many copies
// really fit. They are not derived from this package's formula.
func TestWidthMatchesWhatTmuxActuallyDraws(t *testing.T) {
	cases := []struct {
		name, s string
		want    int
	}{
		{"ASCII", "a", 1},
		{"CJK", "中", 2},
		{"precomposed character", "é", 1},
		{"combining character", "e\u0301", 1},
		{"fullwidth letter", "ａ", 2},
		{"halfwidth katakana", "ｱ", 1},
		{"Hangul syllable", "한", 2},
		{"emoji", "😀", 2},
		{"emoji + VS16", "❤️", 2},
		{"text symbol + VS16", "☀️", 2},
		{"ZWJ family emoji", "👨‍👩‍👧", 2},
		{"skin tone modifier", "👍🏽", 2},
		{"flag", "🇹🇼", 2},
		{"zero-width space", "\u200b", 0},
		{"interface symbol ◆", "◆", 1},
		{"ellipsis", "…", 1},
		{"box-drawing line ─", "─", 1},
		{"circled digit", "①", 1},
		{"CJK Extension B", "𠀀", 2},
		{"Hangul filler is not drawn", "\u3164", 0},
		{"decomposed Hangul jamo", "\u1112\u1161\u11ab", 2},
		{"two leading consonants take one cell each", "\u1100\u1100", 4},
		{"extra trailing consonant is discarded by tmux", "\u1112\u1161\u11ab\u11ab", 2},
		{"leading ZWJ is not drawn, the following emoji is", "\u200d👨", 2},
		{"leading combining character is not drawn", "\u0301a", 1},
		{"three flag symbols: two merge, the third stands alone", "🇹🇼🇯", 3},
		{"ASCII after ZWJ is not merged (tmux does not merge single-byte characters)", "a\u200db", 2},
		{"non-ASCII after ZWJ merges into the previous cell", "a\u200d中", 1},
		{"narrow emoji after ZWJ merges without widening", "❤\u200d🔥", 1},
		{"emoji ZWJ ASCII", "👨\u200db", 3},
		{"modifier after a non-skin-tone base stands alone", "😀🏽", 4},
		{"modifier after ASCII stands alone", "a🏽", 3},
	}
	for _, c := range cases {
		if got := cellwidth.String(c.s); got != c.want {
			t.Errorf("%s %q: width %d, tmux actually draws %d", c.name, c.s, got, c.want)
		}
	}
}

func TestClustersNeverSplitACharacter(t *testing.T) {
	got := cellwidth.Clusters("a👨‍👩‍👧e\u0301中")
	want := []string{"a", "👨‍👩‍👧", "e\u0301", "中"}
	if len(got) != len(want) {
		t.Fatalf("clusters = %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("clusters = %q, want %q", got, want)
		}
	}
}

func TestSumOfClustersEqualsStringWidth(t *testing.T) {
	s := "修正 e\u0301 👍🏽 🇹🇼🇯🇵 ❤️ x"
	n := 0
	for _, c := range cellwidth.Clusters(s) {
		n += cellwidth.String(c)
	}
	if n != cellwidth.String(s) {
		t.Fatalf("%d != %d", n, cellwidth.String(s))
	}
}
