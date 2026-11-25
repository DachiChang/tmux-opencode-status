package tmuxsrc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

func TestPaneDiscoveryReadsFactsAndListsLinkedPanesOnlyOnce(t *testing.T) {
	out := "%1\topencode\t1\t/work/dir with space\n" +
		"%1\topencode\t1\t/work/dir with space\n" + // the same pane listed again through a linked window
		"%2\topencode\t0\t/work/svc\n" +
		"%3\tzsh\t0\t/work/api\n"
	facts, err := parsePanes([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 3 {
		t.Fatalf("want 3 distinct panes, got %+v", facts)
	}
	want := contract.Pane{PaneCurrentCommand: "opencode", AlternateOn: true, PaneCurrentPath: "/work/dir with space"}
	if facts[0].PaneID != "%1" || facts[0].Pane != want {
		t.Fatalf("first pane = %+v, want %+v", facts[0], want)
	}
	if contract.IsTUIClient(facts[1].Pane) || contract.IsTUIClient(facts[2].Pane) {
		t.Fatalf("panes on the main screen must not be TUI clients: %+v", facts[1:])
	}
}

func TestTUIPaneWithoutAPathIsAReadErrorNotAnEmptyDirectory(t *testing.T) {
	facts, err := parsePanes([]byte("%1\topencode\t1\t\n%2\tzsh\t0\t/work/api\n"))
	if err == nil {
		t.Fatalf("a TUI client without a directory must be a read error, not an empty directory: %+v", facts)
	}
	if facts != nil {
		t.Fatalf("must not return partial data on error: %+v", facts)
	}
}

func TestNonTUIPaneWithoutAPathDoesNotInvalidateTheScan(t *testing.T) {
	facts, err := parsePanes([]byte("%1\topencode\t0\t\n%2\tsh\t1\t\n%3\topencode\t1\t/work/api\n"))
	if err != nil || len(facts) != 3 {
		t.Fatalf("panes that are not TUI clients may lack a path: %+v %v", facts, err)
	}
}

// fakeOpencodeSrc stays on the main screen unless its first argument is "alt", in which case it enters the alternate screen like the TUI.
const fakeOpencodeSrc = `package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "alt" {
		fmt.Print("\033[?1049h")
	}
	time.Sleep(time.Hour)
}
`

// isolatedServer builds a fake opencode (tmux reports the foreground command by process name; copies of system executables are
// killed by code signing) and returns a client for an isolated tmux server (-L <socket> -f /dev/null).
func isolatedServer(t *testing.T) (c *Client, bin, dir string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	dir, _ = filepath.EvalSymlinks(t.TempDir())
	bin = filepath.Join(dir, "opencode")
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(fakeOpencodeSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build fake opencode: %v: %s", err, out)
	}
	sock := fmt.Sprintf("tos-panes-%d-%d", os.Getpid(), time.Now().UnixNano()%100000)
	c = &Client{SocketArgs: []string{"-L", sock}, ConfigArgs: []string{"-f", "/dev/null"}}
	t.Cleanup(func() { _, _ = c.tmux("", "kill-server") })
	return c, bin, dir
}

func waitForPanes(t *testing.T, c *Client, want int, what string, ok func([]contract.Pane) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last []contract.Pane
	for time.Now().Before(deadline) {
		facts, err := c.Panes()
		if err == nil && len(facts) == want {
			last = last[:0]
			for _, f := range facts {
				last = append(last, f.Pane)
			}
			if ok(last) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: last panes %+v", what, last)
}

// Against a real tmux server: only the opencode process that entered the alternate screen is a TUI client, and the pane opened
// through a symlink reports the physical directory (the same key OpenCode stores, see ADR-0006).
func TestPanesIdentifiesTUIByAlternateScreenAndReportsThePhysicalDirectory(t *testing.T) {
	c, bin, dir := isolatedServer(t)
	real := filepath.Join(dir, "project")
	link := filepath.Join(dir, "project-link")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := c.tmux("", "new-session", "-d", "-s", "t", "-x", "80", "-y", "24", "-c", link, "exec "+bin+" alt"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.tmux("", "new-window", "-d", "-t", "t:", "-c", dir, "exec "+bin+" serve"); err != nil {
		t.Fatal(err)
	}
	waitForPanes(t, c, 2, "one TUI client in the physical directory and one opencode on the main screen", func(ps []contract.Pane) bool {
		var tui, other int
		for _, p := range ps {
			switch {
			case contract.IsTUIClient(p) && p.PaneCurrentPath == real:
				tui++
			case p.PaneCurrentCommand == "opencode" && !p.AlternateOn:
				other++
			}
		}
		return tui == 1 && other == 1
	})
}
