package sidebar_test

import (
	"strings"
	"testing"
)

// 兩個目錄各有自己的 cwd 清單，另有一份 global 清單（含不在任何 TUI 目錄下的 session）。
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
		t.Fatalf("global 模式應只有一個群組、每個 tab 一次、不混入 cwd 清單：\n%s", got)
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
		t.Fatalf("還有 TUI 時共享清單應保留：\n%s", got)
	}
	w.panes.set()
	w.sync(t)
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE",
		"─────────────────────────────────",
		"沒有 OpenCode TUI",
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
		"無 tab",
	})
}

func TestUnsetModeDefaultsToCwdAndDoesNotMixInGlobalList(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"), tui("%2", "/other"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	got := strings.Join(w.out.screen(30), "\n")
	if !strings.Contains(got, "api tab") || strings.Contains(got, "GLOBAL") || strings.Contains(got, "global one") {
		t.Fatalf("預設應為 cwd：\n%s", got)
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
		t.Fatalf("切到 global 後殘留 cwd 項目：\n%s", got)
	}

	w.mode.use("cwd")
	w.sync(t)
	got = strings.Join(w.out.screen(30), "\n")
	if !strings.Contains(got, "api tab") || !strings.Contains(got, "web tab") || strings.Contains(got, "GLOBAL") || strings.Contains(got, "global one") {
		t.Fatalf("切回 cwd 後殘留 global 項目：\n%s", got)
	}

	w.mode.unset() // 移除設定回到預設 cwd
	w.mode.use("global")
	w.mode.unset()
	w.sync(t)
	if got := strings.Join(w.out.screen(30), "\n"); strings.Contains(got, "GLOBAL") {
		t.Fatalf("移除設定應回到預設 cwd：\n%s", got)
	}
}

func TestSessionsInOtherDirectoriesStayOutOfCwdModeButAreInGlobalMode(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	if got := strings.Join(w.out.screen(30), "\n"); strings.Contains(got, "其他目錄的工作") {
		t.Fatalf("cwd 模式不應納入 global 清單：\n%s", got)
	}
	w.mode.use("global")
	w.sync(t)
	if got := strings.Join(w.out.screen(30), "\n"); !strings.Contains(got, "其他目錄的工作") {
		t.Fatalf("global 模式應納入完整共享清單：\n%s", got)
	}
}

func TestInvalidModeOnFirstSyncDoesNotStartTrackingAndSaysSo(t *testing.T) {
	for _, bad := range []string{"Global", "all", "", " cwd", "global\n"} {
		w := newWorld(t)
		w.mode.use(bad)
		w.panes.set(tui("%1", "/work/api"))
		w.setTabs(t, bothScopes)
		if err := w.d.Sync(); err == nil {
			t.Fatalf("%q：無效設定必須回報錯誤", bad)
		}
		got := strings.Join(w.out.screen(20), "\n")
		if !strings.Contains(got, "模式設定無效") || !strings.Contains(got, "尚未開始追蹤") {
			t.Fatalf("%q：應提示無效且未開始追蹤：\n%s", bad, got)
		}
		for _, leak := range []string{"沒有 OpenCode TUI", "無 tab", "api tab", "GLOBAL", "global one"} {
			if strings.Contains(got, leak) {
				t.Fatalf("%q：不應開始追蹤或顯示 %q：\n%s", bad, leak, got)
			}
		}
		if w.panes.reads != 0 {
			t.Fatalf("%q：無效設定不應開始讀取／追蹤（讀了 %d 次 pane）", bad, w.panes.reads)
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
		t.Fatal("重載時無效設定必須回報錯誤")
	}
	got := strings.Join(w.out.screen(20), "\n")
	if !strings.Contains(flat(got), flat("未套用，仍用 global")) || !strings.Contains(got, "global one") || strings.Contains(got, "api tab") {
		t.Fatalf("應保留 global 並提示未套用：\n%s", got)
	}

	w.mode.use("cwd") // 修正後恢復，提示消失
	w.sync(t)
	got = strings.Join(w.out.screen(20), "\n")
	if strings.Contains(got, "未套用") || !strings.Contains(got, "api tab") {
		t.Fatalf("修正後應套用 cwd 且提示消失：\n%s", got)
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
		t.Fatalf("移除無效設定後應以預設 cwd 開始追蹤：\n%s", got)
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
		t.Fatal("讀不到設定必須回報錯誤")
	}
	got := strings.Join(w.out.screen(20), "\n")
	if !strings.Contains(got, "global one") || strings.Contains(got, "api tab") {
		t.Fatalf("讀不到設定時應保留原模式，而不是退回預設 cwd：\n%s", got)
	}
}

func TestChangingTheModeAloneRewritesTheSidebarAndRepeatedValidSettingDoesNot(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	w.mode.use("cwd") // 重載同一個值：不重畫
	w.sync(t)
	if n := w.out.count(); n != 1 {
		t.Fatalf("同一模式重載寫入 %d 次", n)
	}
	w.mode.use("global")
	w.sync(t)
	if n := w.out.count(); n != 2 {
		t.Fatalf("模式改變應重畫，實際寫入 %d 次", n)
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
		t.Fatalf("重載通知後應套用 global：\n%s", got)
	}
}
