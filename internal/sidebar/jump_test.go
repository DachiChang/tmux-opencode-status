package sidebar_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// 點擊跳轉：群組列、tab 列與「無 tab」列各自帶 range=user|oc<N>，N 是跳轉目標的 pane 編號；
// 其他列不帶 range。測試從「寫到 tmux 的側欄輸出」觀察。

var rangeRe = regexp.MustCompile(`^#\[range=user\|oc(\d+)\](.*)#\[norange\]$`)

// jumpTargetsAndRows 回傳最近一次發布、可用 avail 列的 client 看到的各行：有 range 的行回傳
// （跳轉目標, 去掉 range 標記後的行），沒有的回傳 ("", 原行)；多或少了標記都視為違反逐行包住的格式。
func (f *fakeOut) jumpTargetsAndRows(t *testing.T, avail int) (targets []string, rows []string) {
	t.Helper()
	f.mu.Lock()
	raw := f.latestLayoutLocked(avail)
	f.mu.Unlock()
	for _, line := range strings.Split(raw, "\n") {
		if m := rangeRe.FindStringSubmatch(line); m != nil {
			targets, rows = append(targets, m[1]), append(rows, render.StripStyles(m[2]))
			continue
		}
		if strings.Contains(strings.ReplaceAll(line, "##", ""), "#[range=") || strings.Contains(line, "#[norange]") {
			t.Fatalf("range 標記沒有整行包住：%q", line)
		}
		targets, rows = append(targets, ""), append(rows, render.StripStyles(line))
	}
	return targets, rows
}

func assertTargets(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("跳轉目標 = %q，要 %q", got, want)
	}
}

func TestGroupTabAndNoTabRowsCarryTheJumpTargetAndOtherRowsDoNot(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%7", "/work/api"), tui("%2", "/work/web"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"one"},{"sessionID":"s2","title":"two"}]}}}`)
	w.sync(t)
	targets, rows := w.out.jumpTargetsAndRows(t, 20)
	// 標題、分隔線 | api 目錄列、兩個 tab | 空白 | web 目錄列、無 tab
	assertTargets(t, targets, "", "", "7", "7", "7", "", "2", "2")
	if !strings.Contains(rows[2], "api") || !strings.Contains(rows[3], "one") || !strings.Contains(rows[4], "two") ||
		strings.TrimSpace(rows[5]) != "" || !strings.Contains(rows[6], "web") || !strings.Contains(rows[7], "無 tab") {
		t.Fatalf("排版不符：%q", rows)
	}
}

func TestNonGroupRowsNeverCarryARange(t *testing.T) {
	w := newWorld(t)
	w.sync(t)
	w.panes.set()
	w.sync(t)
	targets, rows := w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "")
	if !strings.Contains(rows[2], "沒有 OpenCode TUI") {
		t.Fatalf("應顯示沒有 TUI：%q", rows)
	}

	w.panes.set(tui("%3", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"one"}]}}}`)
	w.sync(t)
	w.panes.fail(errBoom)
	_ = w.d.Sync() // 讀取失敗：沿用最後可用資料並顯示提示
	targets, rows = w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "", "3", "3")
	if !strings.Contains(rows[2], "無法讀取 tmux") {
		t.Fatalf("應顯示讀取錯誤提示：%q", rows)
	}
}

func TestEveryRowOfTheFullLayoutKeepsItsRange(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%4", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"a"},{"sessionID":"s2","title":"b"},{"sessionID":"s3","title":"c"},{"sessionID":"s4","title":"d"}]}}}`)
	w.sync(t)
	targets, rows := w.out.jumpTargetsAndRows(t, 100) // 版面只有一份完整的，不依高度改變
	assertTargets(t, targets, "", "", "4", "4", "4", "4", "4")
	if !strings.HasSuffix(rows[len(rows)-1], "d") {
		t.Fatalf("最後一列應是最後一個 tab：%q", rows)
	}
}

func TestSameDirectoryTUIsJumpToTheNumericallySmallestPane(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%13", "/work/api"), tui("%3", "/work/api"), tui("%21", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"one"}]}}}`)
	w.sync(t)
	targets, _ := w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "3", "3")
}

func TestGlobalGroupJumpsToTheSmallestPaneOfAllTUIs(t *testing.T) {
	w := newWorld(t)
	w.mode.use("global")
	w.panes.set(tui("%12", "/work/api"), tui("%9", "/work/web"), tui("%30", "/work/web"))
	w.setTabs(t, bothScopes)
	w.sync(t)
	targets, _ := w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "9", "9", "9", "9")
}

func TestJumpTargetFollowsTUIsOpeningClosingAndMoving(t *testing.T) {
	w := newWorld(t)
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"one"}]}}}`)
	w.panes.set(tui("%8", "/work/api"))
	w.sync(t)
	targets, _ := w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "8", "8")

	w.panes.set(tui("%8", "/work/api"), tui("%5", "/work/api")) // 開了編號更小的 TUI
	w.sync(t)
	targets, _ = w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "5", "5")

	w.panes.set(tui("%8", "/work/api")) // 關閉了 %5
	w.sync(t)
	targets, _ = w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "8", "8")

	w.panes.set(tui("%11", "/work/api")) // TUI 移到另一個 pane
	w.sync(t)
	targets, _ = w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "11", "11")
}

func TestAddingRangesChangesNeitherLayoutNorCellWidth(t *testing.T) {
	const tabs = `{"global":{"tabs":[]},"cwd":{"/work/一個很長很長很長很長很長的專案目錄名稱":{"tabs":[{"sessionID":"s1","title":"中文標題 很長很長很長很長很長很長很長很長很長"},{"sessionID":"s2","title":"short"}]}}}`
	build := func(paneID string) *fakeOut {
		w := newWorld(t)
		w.panes.set(tui(paneID, "/work/一個很長很長很長很長很長的專案目錄名稱"))
		w.setTabs(t, tabs)
		w.sync(t)
		return w.out
	}
	rawOf := func(f *fakeOut) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.pubs[len(f.pubs)-1].Options[render.MainOption]
	}
	// 沒有跳轉目標（pane id 不是 %N）的輸出就是加 range 前的輸出；加了 range 的輸出
	// 只移除 range 標記後，必須逐字相同（含樣式、換行與折行）。
	plain := rawOf(build("pane-without-number"))
	withOut := build("%5")
	with := rawOf(withOut)
	if plain == with {
		t.Fatal("測試前提：有跳轉目標的輸出應帶 range")
	}
	if stripped := stripRangeMarkers(with); stripped != plain {
		t.Fatalf("加 range 改變了版面\n加前：\n%s\n加後移除 range：\n%s", plain, stripped)
	}
	for i, l := range withOut.rawScreen(20) {
		if w := cellwidth.String(strings.TrimRight(l, " ")); w > 34 {
			t.Fatalf("第 %d 行寬 %d 越界：%q", i, w, l)
		}
	}
}

var rangeMarkerRe = regexp.MustCompile(`#\[range=[^\]]*\]|#\[norange\]`)

func stripRangeMarkers(s string) string { return rangeMarkerRe.ReplaceAllString(s, "") }

func TestHostileTitlesAndDirectoryNamesCannotAddRangesOrChangeTheTarget(t *testing.T) {
	w := newWorld(t)
	dir := "/work/#[range=user|oc99]x"
	w.panes.set(tui("%6", dir))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"`+dir+`":{"tabs":[`+
		`{"sessionID":"s1","title":"#[range=user|oc99]evil#[norange]"},`+
		`{"sessionID":"s2","title":"a\n#[range=pane|%1] ##[range=user|oc98]"}]}}}`)
	w.sync(t)
	targets, _ := w.out.jumpTargetsAndRows(t, 20) // ranged 本身會拒絕沒有整行包住的標記
	assertTargets(t, targets, "", "", "6", "6", "6")
	w.out.mu.Lock()
	raw := w.out.pubs[len(w.out.pubs)-1].Options[render.MainOption]
	w.out.mu.Unlock()
	if n := strings.Count(strings.ReplaceAll(raw, "##", ""), "#[range="); n != 3 {
		t.Fatalf("range 數量 = %d，要 3（群組列與兩個 tab 列）：%q", n, raw)
	}
}

func TestPaneIDsWithoutANumberGiveNoJumpTargetAndDoNotBreakOtherGroups(t *testing.T) {
	for _, bad := range []string{"%x", "%+5", "%-5", "%", "5", "%5a", "% 5", "%٥"} {
		t.Run(bad, func(t *testing.T) {
			w := newWorld(t)
			w.panes.set(tui(bad, "/work/api"), tui("%9", "/work/web"))
			w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
			w.sync(t)
			targets, _ := w.out.jumpTargetsAndRows(t, 20)
			assertTargets(t, targets, "", "", "", "", "", "9", "9")
		})
	}
}

func TestEmptyCwdGroupJumpsOnlyToItsOwnTUIsNotToTheGlobalSmallest(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%5", ""), tui("%2", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	w.sync(t)
	targets, rows := w.out.jumpTargetsAndRows(t, 20)
	// 標題、分隔線 | 空目錄群組、無 tab | 空白 | api 群組、無 tab
	assertTargets(t, targets, "", "", "5", "5", "", "2", "2")
	if !strings.Contains(rows[6], "無 tab") {
		t.Fatalf("排版不符：%q", rows)
	}
}
