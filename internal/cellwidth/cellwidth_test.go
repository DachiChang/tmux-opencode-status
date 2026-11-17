package cellwidth_test

import (
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
)

// 期望值來自 tmux next-3.9 在 attached client 上的實際繪製：以 19 格寬的側欄塞滿同一個
// 字元（群集），數出真正畫得下幾份；不是由本套件的公式推出。
func TestWidthMatchesWhatTmuxActuallyDraws(t *testing.T) {
	cases := []struct {
		name, s string
		want    int
	}{
		{"ASCII", "a", 1},
		{"CJK", "中", 2},
		{"預組字", "é", 1},
		{"組合字元", "e\u0301", 1},
		{"全形字母", "ａ", 2},
		{"半形片假名", "ｱ", 1},
		{"韓文音節", "한", 2},
		{"emoji", "😀", 2},
		{"emoji + VS16", "❤️", 2},
		{"文字符號 + VS16", "☀️", 2},
		{"ZWJ 家族 emoji", "👨‍👩‍👧", 2},
		{"膚色修飾", "👍🏽", 2},
		{"國旗", "🇹🇼", 2},
		{"零寬空格", "\u200b", 0},
		{"介面符號 ◆", "◆", 1},
		{"省略號", "…", 1},
		{"框線 ─", "─", 1},
		{"圈數字", "①", 1},
		{"CJK 擴充 B", "𠀀", 2},
		{"Hangul filler 不畫", "\u3164", 0},
		{"分解的韓文 jamo", "\u1112\u1161\u11ab", 2},
		{"兩個初聲各佔一格", "\u1100\u1100", 4},
		{"多餘的終聲被 tmux 丟棄", "\u1112\u1161\u11ab\u11ab", 2},
		{"行首的 ZWJ 不畫、之後的 emoji 照畫", "\u200d👨", 2},
		{"組合字元在行首不畫", "\u0301a", 1},
		{"三個國旗符號：兩個合併、第三個獨立", "🇹🇼🇯", 3},
		{"ZWJ 後接 ASCII 不併入（tmux 單位元組不合併）", "a\u200db", 2},
		{"ZWJ 後接非 ASCII 併入前一格", "a\u200d中", 1},
		{"ZWJ 後接窄 emoji 併入但不變寬", "❤\u200d🔥", 1},
		{"emoji ZWJ ASCII", "👨\u200db", 3},
		{"非膚色基底後接修飾符獨立成格", "😀🏽", 4},
		{"ASCII 後接修飾符獨立成格", "a🏽", 3},
	}
	for _, c := range cases {
		if got := cellwidth.String(c.s); got != c.want {
			t.Errorf("%s %q：寬度 %d，tmux 實際畫 %d", c.name, c.s, got, c.want)
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
