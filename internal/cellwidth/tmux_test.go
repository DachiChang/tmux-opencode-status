package cellwidth_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
)

// Compare against a real tmux: put each sample into the side-status of an isolated server,
// let an attached client draw it, and infer how many cells the sample takes from the
// position of the rightmost marker. Only the isolated socket is touched, never the user's
// default server.
func TestWidthAgreesWithARealAttachedTmuxClient(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found")
	}
	in, out := fmt.Sprintf("tos-cw-in-%d", os.Getpid()), fmt.Sprintf("tos-cw-out-%d", os.Getpid())
	tm := func(sock string, a ...string) string {
		b, err := exec.Command("tmux", append([]string{"-L", sock, "-f", "/dev/null"}, a...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", a, err, b)
		}
		return string(b)
	}
	tm(in, "new-session", "-d", "-s", "main", "-x", "80", "-y", "10")
	tm(out, "new-session", "-d", "-s", "hold", "-x", "80", "-y", "10", fmt.Sprintf("tmux -L %s -f /dev/null attach -t main", in))
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", in, "-f", "/dev/null", "kill-server").Run()
		_ = exec.Command("tmux", "-L", out, "-f", "/dev/null", "kill-server").Run()
	})
	time.Sleep(500 * time.Millisecond)
	tm(in, "set", "-g", "side-status", "left", ";", "set", "-g", "side-status-width", "40")

	samples := []string{
		"a", "中", "é", "e\u0301", "ａ", "ｱ", "한", "😀", "❤️", "☀️", "👨‍👩‍👧", "👍🏽", "🇹🇼", "🇹🇼🇯", "🇹🇼🇯🇵",
		"\u200b", "◆", "…", "─", "①", "𠀀", "\u1112\u1161\u11ab", "\u1100\u1100", "a\u200db", "a\u200d👨", "\u200d👨",
		"\u0301a", "x\u3164", "桃園搜尋爬取與分類", "PCI-DSS 部署 kubectl", "Issue #459", "▲▼", "✓✔", "→", "⚠️", "ℹ️", "①②", "ー",
		"ｶﾞ", "가나다", "اب", "שלום", "ไทย", "तो",
	}
	for _, s := range samples {
		// Use #{l:} literals to avoid format expansion; the rightmost marker's position = the blank space left in the 40-1 cell content width
		tm(in, "set", "-g", "side-status-format", "#{l:"+strings.ReplaceAll(s, "#", "##")+"}#[align=right]|")
		time.Sleep(120 * time.Millisecond)
		line := strings.SplitN(strings.SplitN(tm(out, "capture-pane", "-p", "-t", "hold"), "\n", 2)[0], "│", 2)[0]
		trim := strings.TrimRight(line, " ")
		if !strings.HasSuffix(trim, "|") {
			t.Fatalf("%q: marker not found: %q", s, line)
		}
		body := strings.TrimSuffix(trim, "|")
		pad := len(body) - len(strings.TrimRight(body, " "))
		drawn := 39 - 1 - pad
		if got := cellwidth.String(s); got != drawn {
			t.Errorf("%q: cellwidth=%d, tmux actually draws %d", s, got, drawn)
		}
	}
}
