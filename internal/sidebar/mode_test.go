package sidebar_test

import (
	"strings"
	"testing"
)

// Two directories each have their own cwd list, plus one global list (including sessions not under any TUI directory).
const bothScopes = `{"global":{"tabs":[{"sessionID":"g1","title":"global one"},{"sessionID":"g2","title":"其他目錄的工作"},{"sessionID":"g3","title":"global three"}]},
"cwd":{"/work/api":{"tabs":[{"sessionID":"c1","title":"api tab"}]},"/work/web":{"tabs":[{"sessionID":"c2","title":"web tab"}]}}}`

func TestGlobalModeListsOneSharedListUnderASingleGlobalGroupInSourceOrder(t *testing.T) {
	w := newWorld(t)
	w.mode.use("global")
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE                      !3",
		"─────────────────────────────────",
		"GLOBAL                        !3",
		"! global one",
		"! 其他目錄的工作",
		"! global three",
	})
}

func TestGlobalModeDoesNotRepeatTabsForMultipleTUIsInDifferentDirectories(t *testing.T) {
	w := newWorld(t)
	w.mode.use("global")
	w.panes.set(tui("%1", "/work/api"), tui("%2", "/work/api"), tui("%3", "/work/web"), tui("%3", "/work/web"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	got := strings.Join(w.out.screen(30), "\n")
	if strings.Count(got, "global one") != 1 || strings.Count(got, "GLOBAL") != 1 || strings.Contains(got, "api tab") || strings.Contains(got, "web tab") {
		t.Fatalf("global mode should have only one group, each tab once, with no cwd list mixed in:\n%s", got)
	}
}

func TestGlobalModeStopsIncludingTheListOnceTheLastTUIIsGone(t *testing.T) {
	w := newWorld(t)
	w.mode.use("global")
	w.panes.set(tui("%1", "/work/api"), tui("%2", "/work/web"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	w.panes.set(tui("%2", "/work/web"))
	w.sync(t)
	if got := strings.Join(w.out.screen(20), "\n"); !strings.Contains(got, "global one") {
		t.Fatalf("the shared list should be kept while a TUI remains:\n%s", got)
	}
	w.panes.set()
	w.sync(t)
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE",
		"─────────────────────────────────",
		"No OpenCode TUI running",
	})
}

func TestGlobalModeWithTUIButNoSharedTabsKeepsTheGlobalGroup(t *testing.T) {
	w := newWorld(t)
	w.mode.use("global")
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"c1","title":"cwd only"}]}}}`)
	w.sync(t)
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE",
		"─────────────────────────────────",
		"GLOBAL",
		"No tabs",
	})
}

func TestUnsetModeDefaultsToCwdAndDoesNotMixInGlobalList(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"), tui("%2", "/other"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	got := strings.Join(w.out.screen(30), "\n")
	if !strings.Contains(got, "api tab") || strings.Contains(got, "GLOBAL") || strings.Contains(got, "global one") {
		t.Fatalf("the default should be cwd:\n%s", got)
	}
}

func TestSwitchingModeBothWaysLeavesNoItemsFromTheOldSource(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"), tui("%2", "/work/web"))
	w.setTabs(t, bothScopes)
	w.sync(t)

	w.mode.use("global")
	w.sync(t)
	got := strings.Join(w.out.screen(30), "\n")
	if !strings.Contains(got, "global one") || strings.Contains(got, "api tab") || strings.Contains(got, "web tab") || strings.Contains(got, "web ") {
		t.Fatalf("cwd entries remain after switching to global:\n%s", got)
	}

	w.mode.use("cwd")
	w.sync(t)
	got = strings.Join(w.out.screen(30), "\n")
	if !strings.Contains(got, "api tab") || !strings.Contains(got, "web tab") || strings.Contains(got, "GLOBAL") || strings.Contains(got, "global one") {
		t.Fatalf("global entries remain after switching back to cwd:\n%s", got)
	}

	w.mode.unset() // removing the setting returns to the default cwd
	w.mode.use("global")
	w.mode.unset()
	w.sync(t)
	if got := strings.Join(w.out.screen(30), "\n"); strings.Contains(got, "GLOBAL") {
		t.Fatalf("removing the setting should return to the default cwd:\n%s", got)
	}
}

func TestSessionsInOtherDirectoriesStayOutOfCwdModeButAreInGlobalMode(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	if got := strings.Join(w.out.screen(30), "\n"); strings.Contains(got, "其他目錄的工作") {
		t.Fatalf("cwd mode should not include the global list:\n%s", got)
	}
	w.mode.use("global")
	w.sync(t)
	if got := strings.Join(w.out.screen(30), "\n"); !strings.Contains(got, "其他目錄的工作") {
		t.Fatalf("global mode should include the full shared list:\n%s", got)
	}
}

func TestInvalidModeOnFirstSyncDoesNotStartTrackingAndSaysSo(t *testing.T) {
	for _, bad := range []string{"Global", "all", "", " cwd", "global\n"} {
		w := newWorld(t)
		w.mode.use(bad)
		w.panes.set(tui("%1", "/work/api"))
		w.setTabs(t, bothScopes)
		if err := w.d.Sync(); err == nil {
			t.Fatalf("%q: an invalid setting must report an error", bad)
		}
		got := strings.Join(w.out.screen(20), "\n")
		if !strings.Contains(got, "▲ Invalid mode; not tracking") {
			t.Fatalf("%q: should report invalid and not start tracking:\n%s", bad, got)
		}
		for _, leak := range []string{"No OpenCode TUI running", "No tabs", "api tab", "GLOBAL", "global one"} {
			if strings.Contains(got, leak) {
				t.Fatalf("%q: should not start tracking or show %q:\n%s", bad, leak, got)
			}
		}
		if w.panes.reads != 0 {
			t.Fatalf("%q: an invalid setting should not start reading/tracking (read panes %d times)", bad, w.panes.reads)
		}
	}
}

func TestInvalidModeAfterAValidOneKeepsTheValidModeAndReportsNotApplied(t *testing.T) {
	w := newWorld(t)
	w.mode.use("global")
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, bothScopes)
	w.sync(t)

	w.mode.use("globl")
	if err := w.d.Sync(); err == nil {
		t.Fatal("an invalid setting on reload must report an error")
	}
	got := strings.Join(w.out.screen(20), "\n")
	if !strings.Contains(got, "▲ Invalid mode; using global") || !strings.Contains(got, "global one") || strings.Contains(got, "api tab") {
		t.Fatalf("should keep global and report it as not applied:\n%s", got)
	}

	w.mode.use("cwd") // recovers after the fix; the notice disappears
	w.sync(t)
	got = strings.Join(w.out.screen(20), "\n")
	if strings.Contains(got, "Invalid mode") || !strings.Contains(got, "api tab") {
		t.Fatalf("after the fix cwd should apply and the notice should disappear:\n%s", got)
	}
}

func TestUnsetAfterInvalidFirstSettingStartsTrackingWithDefaultCwd(t *testing.T) {
	w := newWorld(t)
	w.mode.use("bogus")
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, bothScopes)
	_ = w.d.Sync()
	w.mode.unset()
	w.sync(t)
	if got := strings.Join(w.out.screen(20), "\n"); !strings.Contains(got, "api tab") {
		t.Fatalf("after removing the invalid setting, tracking should start with the default cwd:\n%s", got)
	}
}

func TestUnreadableModeSettingIsNotTreatedAsUnset(t *testing.T) {
	w := newWorld(t)
	w.mode.use("global")
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	w.mode.fail(errBoom)
	if err := w.d.Sync(); err == nil {
		t.Fatal("failing to read the setting must report an error")
	}
	got := strings.Join(w.out.screen(20), "\n")
	if !strings.Contains(got, "global one") || strings.Contains(got, "api tab") {
		t.Fatalf("when the setting cannot be read, the existing mode should be kept instead of falling back to the default cwd:\n%s", got)
	}
}

func TestChangingTheModeAloneRewritesTheSidebarAndRepeatedValidSettingDoesNot(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	w.mode.use("cwd") // reload the same value: no redraw
	w.sync(t)
	if n := w.out.count(); n != 1 {
		t.Fatalf("reloading the same mode wrote %d times", n)
	}
	w.mode.use("global")
	w.sync(t)
	if n := w.out.count(); n != 2 {
		t.Fatalf("a mode change should redraw; actually wrote %d times", n)
	}
}

func TestModeNotificationAppliesWithoutWaitingForBackstop(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, bothScopes)
	r := startRun(t, w)
	r.waitPublish(t)
	w.mode.use("global")
	r.wake <- struct{}{}
	r.waitPublish(t)
	if got := strings.Join(w.out.screen(20), "\n"); !strings.Contains(got, "GLOBAL") {
		t.Fatalf("global should apply after the reload notification:\n%s", got)
	}
}
