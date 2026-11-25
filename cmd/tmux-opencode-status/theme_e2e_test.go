package main_test

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Palette overrides (issue #16): the real daemon + an isolated tmux server; from the attached client, read back the escape sequences
// actually drawn with capture-pane -e to confirm that the colors really reach the screen.

const colorOpt = "@opencode-status-color-"

// styledSidebar is the client screen (including SGR escape sequences); colors are visible only here.
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
	e.t.Fatalf("%s: not reached within %v, current screen (with styles):\n%q", what, within, last)
	return 0
}

// projectLine is the line that contains the project name (including SGR).
func projectLine(screen, name string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, name) {
			return l
		}
	}
	return ""
}

const colour33 = "38;5;33m" // SGR for fg=colour33
const defaultProject = "38;2;242;238;227m"

func themeEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	api := filepath.Join(e.dir, "work", "api")
	e.writeTabs(e.tabsJSON(map[string][]string{api: {"api tab"}}))
	e.tui(api)
	e.eventually(3*time.Second, "displayed", func(s string) bool { return strings.Contains(s, "api tab") })
	return e, "api"
}

func TestSwatchOverrideAndUnsetUpdateTheSidebarImmediatelyViaTheHook(t *testing.T) {
	e, name := themeEnv(t)
	if l := projectLine(e.styledSidebar(), name); !strings.Contains(l, defaultProject) || strings.Contains(l, colour33) {
		t.Fatalf("an unset palette should use the default color: %q", l)
	}
	pid := e.daemon.Process.Pid

	e.inTmux("set", "-g", colorOpt+"project", "colour33")
	d := e.eventuallyStyled(time.Second, "applied within 1 second of setting (well under the 5-second catch-up)", func(s string) bool {
		l := projectLine(s, name)
		return strings.Contains(l, colour33) && !strings.Contains(l, defaultProject)
	})
	t.Logf("set → screen: %v", d)

	e.inTmux("set", "-gu", colorOpt+"project")
	d = e.eventuallyStyled(time.Second, "returns to the default within 1 second of unsetting", func(s string) bool {
		l := projectLine(s, name)
		return strings.Contains(l, defaultProject) && !strings.Contains(l, colour33)
	})
	t.Logf("unset → screen: %v", d)

	e.reload("set -g " + colorOpt + "project colour33\n")
	e.eventuallyStyled(time.Second, "applied after reloading the configuration file", func(s string) bool { return strings.Contains(projectLine(s, name), colour33) })

	if e.daemon.Process.Pid != pid || e.daemonCount() != 1 {
		t.Fatalf("the daemon must not restart (pid %d → %d, instances %d)", pid, e.daemon.Process.Pid, e.daemonCount())
	}
}

func TestInvalidSwatchFallsBackAloneAndTheSidebarShowsNoError(t *testing.T) {
	e, name := themeEnv(t)
	// The test environment has no service, so first wait for the existing "cannot connect" notice to appear and settle, then take the baseline screen.
	e.eventually(5*time.Second, "service notice appears", func(s string) bool { return strings.Contains(s, "Cannot connect to OpenCode") })
	time.Sleep(300 * time.Millisecond)
	before := strings.Join(e.clientScreen("hold"), "\n")
	e.inTmux("set", "-g", colorOpt+"project", "#{pane_id}")
	e.inTmux("set", "-g", colorOpt+"emphasis", "colour33")
	e.eventuallyStyled(time.Second, "a valid emphasis is still applied", func(s string) bool {
		return strings.Contains(projectLine(s, "OPENCODE"), "48;5;33m")
	})
	s := e.styledSidebar()
	if l := projectLine(s, name); !strings.Contains(l, defaultProject) {
		t.Fatalf("an invalid project should fall back to the default: %q", l)
	}
	if plain := strings.Join(e.clientScreen("hold"), "\n"); plain != before {
		t.Fatalf("an invalid palette must not change the sidebar text (no error shown):\n%s\n---\n%s", before, plain)
	}
}

func TestStoppingTheDaemonKeepsTheUsersSwatchOptions(t *testing.T) {
	e, name := themeEnv(t)
	e.inTmux("set", "-g", colorOpt+"project", "colour33")
	e.inTmux("set", "-g", colorOpt+"notice", "bogus")
	e.eventuallyStyled(time.Second, "applied", func(s string) bool { return strings.Contains(projectLine(s, name), colour33) })
	must(t, e.daemon.Process.Signal(syscall.SIGTERM))
	_ = e.daemon.Wait()
	opts := e.inTmux("show-options", "-g")
	if !strings.Contains(opts, colorOpt+"project colour33") || !strings.Contains(opts, colorOpt+"notice bogus") {
		t.Fatalf("the user's palette options should be kept after stop:\n%s", opts)
	}
}

func TestDiagnoseListsInvalidSwatchesAndStaysQuietWhenAllAreValid(t *testing.T) {
	e := newLifecycleEnv(t)
	if out, err := e.start(e.in, "-show"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.waitDaemons(e.in, 1, 3*time.Second, "start")
	e.eventually(3*time.Second, "displayed", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })

	baseline := e.diagnose(e.in)
	mustContain(t, baseline, "Result: No issues found")

	e.inTmux("set", "-g", colorOpt+"project", "colour33")
	e.inTmux("set", "-g", colorOpt+"notice", "default")
	valid := e.diagnose(e.in)
	mustNotContain(t, valid, "Theme settings", colorOpt, "using its default")
	mustContain(t, valid, "Result: No issues found")

	e.inTmux("set", "-g", colorOpt+"title", "#{pane_id}")
	e.inTmux("set", "-g", colorOpt+"split", "")
	e.inTmux("set", "-g", colorOpt+"emphasis", "purple]")
	out := e.diagnose(e.in)
	t.Logf("diagnostics (with an invalid palette):\n%s", out)
	mustContain(t, out, colorOpt+"title", colorOpt+"split", colorOpt+"emphasis", "using its default", "Result: Issues found: 3")
	mustNotContain(t, out, colorOpt+"project", colorOpt+"notice", "pane_id", "purple")

	e.inTmux("set", "-gu", colorOpt+"title")
	e.inTmux("set", "-gu", colorOpt+"split")
	e.inTmux("set", "-gu", colorOpt+"emphasis")
	mustContain(t, e.diagnose(e.in), "Result: No issues found")
}
