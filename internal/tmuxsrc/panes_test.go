package tmuxsrc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Verifies the external behavior of Panes() with an isolated tmux server (-L <socket> -f /dev/null) and real panes;
// process data (argv/cwd) comes from a controlled procReader, which produces the "unreadable" and "process exited" cases.

type fakeProc struct {
	argv     []string
	argvErr  error
	cwd      string
	cwdErr   error
	gone     bool // process has exited
	argvCall int
}

func (f *fakeProc) Argv(int) ([]string, error) { f.argvCall++; return f.argv, f.argvErr }
func (f *fakeProc) Cwd(int) (string, error)    { return f.cwd, f.cwdErr }
func (f *fakeProc) Alive(int) bool             { return !f.gone }

// opencodePane starts a pane in the isolated server whose foreground command name is opencode (a fake opencode built with go).
func opencodePane(t *testing.T) *Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(dir, "opencode")
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Hour) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Build a process named opencode with go: tmux reports the foreground command by process name (copies of system executables are killed by code signing).
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build fake opencode: %v: %s", err, out)
	}
	sock := fmt.Sprintf("tos-panes-%d-%d", os.Getpid(), time.Now().UnixNano()%100000)
	c := &Client{SocketArgs: []string{"-L", sock}, ConfigArgs: []string{"-f", "/dev/null"}}
	if _, err := c.tmux("", "new-session", "-d", "-s", "t", "-x", "80", "-y", "24", "-c", dir, "exec "+bin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = c.tmux("", "kill-server") })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := c.tmux("", "list-panes", "-a", "-F", "#{pane_current_command}")
		if strings.TrimSpace(string(out)) == "opencode" {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("tmux did not report the pane's foreground command as opencode")
	return nil
}

func TestUnreadableCandidateArgvIsAReadErrorNotADroppedPane(t *testing.T) {
	c := opencodePane(t)
	c.proc = &fakeProc{argvErr: errors.New("operation not permitted")}
	facts, err := c.Panes()
	if err == nil {
		t.Fatalf("must report an error when the process is alive but argv is unreadable, not skip the pane: %+v", facts)
	}
	if facts != nil {
		t.Fatalf("must not return partial data on error: %+v", facts)
	}
}

func TestUnreadableCandidateCwdIsAReadErrorNotAnEmptyDirectory(t *testing.T) {
	c := opencodePane(t)
	c.proc = &fakeProc{argv: []string{"opencode"}, cwdErr: errors.New("lsof 失敗")}
	if facts, err := c.Panes(); err == nil {
		t.Fatalf("must report an error when the TUI working directory is unreadable, not produce an empty directory: %+v", facts)
	}
	c.proc = &fakeProc{argv: []string{"opencode"}, cwd: "/work/api"}
	facts, err := c.Panes()
	if err != nil || len(facts) != 1 || facts[0].ForegroundCwd != "/work/api" {
		t.Fatalf("should return normally when readable: %+v %v", facts, err)
	}
}

func TestProcessThatEndedWhileReadingIsNormalDisappearance(t *testing.T) {
	c := opencodePane(t)
	c.proc = &fakeProc{argvErr: errors.New("no such process"), gone: true}
	facts, err := c.Panes()
	if err != nil {
		t.Fatalf("an exited process is a normal disappearance, not a read error: %v", err)
	}
	if len(facts) != 1 || len(facts[0].ForegroundArgv) != 0 {
		t.Fatalf("pane is still listed but has no foreground argv: %+v", facts)
	}
	c.proc = &fakeProc{argv: []string{"opencode"}, cwdErr: errors.New("no such process"), gone: true}
	facts, err = c.Panes()
	if err != nil || len(facts) != 1 || len(facts[0].ForegroundArgv) != 0 || facts[0].ForegroundCwd != "" {
		t.Fatalf("a process that exits while its cwd is being read is also a normal disappearance: %+v %v", facts, err)
	}
}
