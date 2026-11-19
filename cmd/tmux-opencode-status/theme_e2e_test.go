package main_test

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 色票覆寫（issue #16）：真實常駐程式 + 隔離 tmux server；從 attached client 以 capture-pane -e
// 讀回實際繪出的跳脫序列，確認顏色真的到了畫面上。

const colorOpt = "@opencode-status-color-"

// styledSidebar 是 client 畫面（含 SGR 跳脫序列）；顏色只在這裡看得到。
func (e *env) styledSidebar() string {
	e.t.Helper()
	return e.outTmux("capture-pane", "-p", "-e", "-t", "hold")
}

func (e *env) eventuallyStyled(within time.Duration, what string, ok func(string) bool) time.Duration {
	e.t.Helper()
	start := time.Now()
	var last string
	for time.Since(start) < within {
		last = e.styledSidebar()
		if ok(last) {
			return time.Since(start)
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("%s：%v 內未達成，目前畫面（含樣式）：\n%q", what, within, last)
	return 0
}

// projectLine 是含專案名稱的那一行（含 SGR）。
func projectLine(screen, name string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, name) {
			return l
		}
	}
	return ""
}

const colour33 = "38;5;33m" // fg=colour33 的 SGR
const defaultProject = "38;2;242;238;227m"

func themeEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"api tab"}}))
	e.tui(api)
	e.eventually(3*time.Second, "已顯示", func(s string) bool { return strings.Contains(s, "api tab") })
	return e, "api"
}

func TestSwatchOverrideAndUnsetUpdateTheSidebarImmediatelyViaTheHook(t *testing.T) {
	e, name := themeEnv(t)
	if l := projectLine(e.styledSidebar(), name); !strings.Contains(l, defaultProject) || strings.Contains(l, colour33) {
		t.Fatalf("未設定應為預設色：%q", l)
	}
	pid := e.daemon.Process.Pid

	e.inTmux("set", "-g", colorOpt+"project", "colour33")
	d := e.eventuallyStyled(time.Second, "設定後 1 秒內（遠低於 5 秒補漏）套用", func(s string) bool {
		l := projectLine(s, name)
		return strings.Contains(l, colour33) && !strings.Contains(l, defaultProject)
	})
	t.Logf("set → 畫面：%v", d)

	e.inTmux("set", "-gu", colorOpt+"project")
	d = e.eventuallyStyled(time.Second, "取消後 1 秒內回到預設", func(s string) bool {
		l := projectLine(s, name)
		return strings.Contains(l, defaultProject) && !strings.Contains(l, colour33)
	})
	t.Logf("unset → 畫面：%v", d)

	e.reload("set -g " + colorOpt + "project colour33\n")
	e.eventuallyStyled(time.Second, "重載設定檔後套用", func(s string) bool { return strings.Contains(projectLine(s, name), colour33) })

	if e.daemon.Process.Pid != pid || e.daemonCount() != 1 {
		t.Fatalf("不得重啟常駐程式（pid %d → %d，實例 %d）", pid, e.daemon.Process.Pid, e.daemonCount())
	}
}

func TestInvalidSwatchFallsBackAloneAndTheSidebarShowsNoError(t *testing.T) {
	e, name := themeEnv(t)
	// 測試環境沒有服務，先等既有的「無法連線」提示出現並穩定，再取基準畫面。
	e.eventually(5*time.Second, "服務提示出現", func(s string) bool { return strings.Contains(s, "無法連線 OpenCode 服務") })
	time.Sleep(300 * time.Millisecond)
	before := strings.Join(e.clientScreen("hold"), "\n")
	e.inTmux("set", "-g", colorOpt+"project", "#{pane_id}")
	e.inTmux("set", "-g", colorOpt+"emphasis", "colour33")
	e.eventuallyStyled(time.Second, "有效的 emphasis 照常套用", func(s string) bool {
		return strings.Contains(projectLine(s, "OPENCODE"), "48;5;33m")
	})
	s := e.styledSidebar()
	if l := projectLine(s, name); !strings.Contains(l, defaultProject) {
		t.Fatalf("無效的 project 應退回預設：%q", l)
	}
	if plain := strings.Join(e.clientScreen("hold"), "\n"); plain != before {
		t.Fatalf("無效色票不應改變側欄文字（不顯示錯誤）：\n%s\n---\n%s", before, plain)
	}
}

func TestStoppingTheDaemonKeepsTheUsersSwatchOptions(t *testing.T) {
	e, name := themeEnv(t)
	e.inTmux("set", "-g", colorOpt+"project", "colour33")
	e.inTmux("set", "-g", colorOpt+"notice", "bogus")
	e.eventuallyStyled(time.Second, "已套用", func(s string) bool { return strings.Contains(projectLine(s, name), colour33) })
	must(t, e.daemon.Process.Signal(syscall.SIGTERM))
	_ = e.daemon.Wait()
	opts := e.inTmux("show-options", "-g")
	if !strings.Contains(opts, colorOpt+"project colour33") || !strings.Contains(opts, colorOpt+"notice bogus") {
		t.Fatalf("停止後使用者的色票選項應保留：\n%s", opts)
	}
}

func TestDiagnoseListsInvalidSwatchesAndStaysQuietWhenAllAreValid(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "已顯示", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })

	baseline := e.diagnose(e.in)
	mustContain(t, baseline, "結論：未發現問題")

	e.inTmux("set", "-g", colorOpt+"project", "colour33")
	e.inTmux("set", "-g", colorOpt+"notice", "default")
	valid := e.diagnose(e.in)
	mustNotContain(t, valid, "色票", colorOpt, "已退回預設")
	mustContain(t, valid, "結論：未發現問題")

	e.inTmux("set", "-g", colorOpt+"title", "#{pane_id}")
	e.inTmux("set", "-g", colorOpt+"split", "")
	e.inTmux("set", "-g", colorOpt+"emphasis", "purple]")
	out := e.diagnose(e.in)
	t.Logf("診斷（含無效色票）：\n%s", out)
	mustContain(t, out, colorOpt+"title", colorOpt+"split", colorOpt+"emphasis", "已退回預設", "結論：發現 3 項問題")
	mustNotContain(t, out, colorOpt+"project", colorOpt+"notice", "pane_id", "purple")

	e.inTmux("set", "-gu", colorOpt+"title")
	e.inTmux("set", "-gu", colorOpt+"split")
	e.inTmux("set", "-gu", colorOpt+"emphasis")
	mustContain(t, e.diagnose(e.in), "結論：未發現問題")
}
