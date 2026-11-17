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

// 以隔離 tmux server（-L <socket> -f /dev/null）與真實 pane 驗證 Panes() 的外部行為；
// 行程資料（argv／cwd）由受控的 procReader 提供，製造「讀不到」與「行程已結束」。

type fakeProc struct {
	argv     []string
	argvErr  error
	cwd      string
	cwdErr   error
	gone     bool // 行程已結束
	argvCall int
}

func (f *fakeProc) Argv(int) ([]string, error) { f.argvCall++; return f.argv, f.argvErr }
func (f *fakeProc) Cwd(int) (string, error)    { return f.cwd, f.cwdErr }
func (f *fakeProc) Alive(int) bool             { return !f.gone }

// opencodePane 在隔離 server 啟動一個前景指令名稱為 opencode 的 pane（以 go 編出的假 opencode）。
func opencodePane(t *testing.T) *Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("沒有 tmux")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(dir, "opencode")
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Hour) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 以 go 編出名為 opencode 的行程：tmux 以行程名稱回報前景指令（系統執行檔的複製會被簽章機制殺掉）。
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
	t.Fatal("tmux 沒有把 pane 的前景指令回報為 opencode")
	return nil
}

func TestUnreadableCandidateArgvIsAReadErrorNotADroppedPane(t *testing.T) {
	c := opencodePane(t)
	c.proc = &fakeProc{argvErr: errors.New("operation not permitted")}
	facts, err := c.Panes()
	if err == nil {
		t.Fatalf("行程仍在但讀不到 argv 必須回報錯誤，不能略過 pane：%+v", facts)
	}
	if facts != nil {
		t.Fatalf("錯誤時不得回傳部分資料：%+v", facts)
	}
}

func TestUnreadableCandidateCwdIsAReadErrorNotAnEmptyDirectory(t *testing.T) {
	c := opencodePane(t)
	c.proc = &fakeProc{argv: []string{"opencode"}, cwdErr: errors.New("lsof 失敗")}
	if facts, err := c.Panes(); err == nil {
		t.Fatalf("讀不到 TUI 工作目錄必須回報錯誤，不能產生空目錄：%+v", facts)
	}
	c.proc = &fakeProc{argv: []string{"opencode"}, cwd: "/work/api"}
	facts, err := c.Panes()
	if err != nil || len(facts) != 1 || facts[0].ForegroundCwd != "/work/api" {
		t.Fatalf("可讀時照常回傳：%+v %v", facts, err)
	}
}

func TestProcessThatEndedWhileReadingIsNormalDisappearance(t *testing.T) {
	c := opencodePane(t)
	c.proc = &fakeProc{argvErr: errors.New("no such process"), gone: true}
	facts, err := c.Panes()
	if err != nil {
		t.Fatalf("行程已結束是正常消失，不是讀取錯誤：%v", err)
	}
	if len(facts) != 1 || len(facts[0].ForegroundArgv) != 0 {
		t.Fatalf("pane 仍列出但沒有前景 argv：%+v", facts)
	}
	c.proc = &fakeProc{argv: []string{"opencode"}, cwdErr: errors.New("no such process"), gone: true}
	facts, err = c.Panes()
	if err != nil || len(facts) != 1 || len(facts[0].ForegroundArgv) != 0 || facts[0].ForegroundCwd != "" {
		t.Fatalf("讀 cwd 時行程結束同為正常消失：%+v %v", facts, err)
	}
}
