package sidebar_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// Click to jump: group rows, tab rows, and "no tab" rows each carry range=user|oc<N>, where N is the jump target's pane number;
// other rows carry no range. Tests observe the "sidebar output written to tmux".

var rangeRe = regexp.MustCompile(`^#\[range=user\|oc(\d+)\](.*)#\[norange\]$`)

// jumpTargetsAndRows returns the rows seen by a client with avail usable rows in the latest publish: a row with a range returns
// (jump target, row with the range markers stripped), and a row without one returns ("", original row); extra or missing markers violate the wrap-every-row format.
func (f *fakeOut) jumpTargetsAndRows(t *testing.T, avail int) (targets []string, rows []string) {
	t.Helper()
	f.mu.Lock()
	raw := f.truncatedLayoutLocked(avail)
	f.mu.Unlock()
	for _, line := range strings.Split(raw, "\n") {
		if m := rangeRe.FindStringSubmatch(line); m != nil {
			targets, rows = append(targets, m[1]), append(rows, render.StripStyles(m[2]))
			continue
		}
		if strings.Contains(strings.ReplaceAll(line, "##", ""), "#[range=") || strings.Contains(line, "#[norange]") {
			t.Fatalf("range markers do not wrap the whole row: %q", line)
		}
		targets, rows = append(targets, ""), append(rows, render.StripStyles(line))
	}
	return targets, rows
}

func assertTargets(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("jump target = %q, want %q", got, want)
	}
}

func TestGroupTabAndNoTabRowsCarryTheJumpTargetAndOtherRowsDoNot(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%7", "/work/api"), tui("%2", "/work/web"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"one"},{"sessionID":"s2","title":"two"}]}}}`)
	w.sync(t)
	targets, rows := w.out.jumpTargetsAndRows(t, 20)
	// title, divider | api directory row, two tabs | blank | web directory row, no tab
	assertTargets(t, targets, "", "", "7", "7", "7", "", "2", "2")
	if !strings.Contains(rows[2], "api") || !strings.Contains(rows[3], "one") || !strings.Contains(rows[4], "two") ||
		strings.TrimSpace(rows[5]) != "" || !strings.Contains(rows[6], "web") || !strings.Contains(rows[7], "No tabs") {
		t.Fatalf("layout mismatch: %q", rows)
	}
}

func TestNonGroupRowsNeverCarryARange(t *testing.T) {
	w := newWorld(t)
	w.sync(t)
	w.panes.set()
	w.sync(t)
	targets, rows := w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "")
	if !strings.Contains(rows[2], "No OpenCode TUI running") {
		t.Fatalf("should show no TUI: %q", rows)
	}

	w.panes.set(tui("%3", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"one"}]}}}`)
	w.sync(t)
	w.panes.fail(errBoom)
	_ = w.d.Sync() // read failed: reuse the last available data and show a notice
	targets, rows = w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "", "3", "3")
	if !strings.Contains(rows[2], "Cannot read tmux panes") {
		t.Fatalf("should show a read error notice: %q", rows)
	}
}

func TestEveryRowOfTheFullLayoutKeepsItsRange(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%4", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"a"},{"sessionID":"s2","title":"b"},{"sessionID":"s3","title":"c"},{"sessionID":"s4","title":"d"}]}}}`)
	w.sync(t)
	targets, rows := w.out.jumpTargetsAndRows(t, 100) // there is only one complete layout, regardless of height
	assertTargets(t, targets, "", "", "4", "4", "4", "4", "4")
	if !strings.HasSuffix(rows[len(rows)-1], "d") {
		t.Fatalf("the last row should be the last tab: %q", rows)
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

	w.panes.set(tui("%8", "/work/api"), tui("%5", "/work/api")) // a TUI with a lower number was opened
	w.sync(t)
	targets, _ = w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "5", "5")

	w.panes.set(tui("%8", "/work/api")) // %5 was closed
	w.sync(t)
	targets, _ = w.out.jumpTargetsAndRows(t, 20)
	assertTargets(t, targets, "", "", "8", "8")

	w.panes.set(tui("%11", "/work/api")) // the TUI moved to another pane
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
	// Output with no jump target (pane id is not %N) is the output from before ranges were added; output with ranges,
	// once only the range markers are removed, must be identical character for character (including styles, newlines, and wrapping).
	plain := rawOf(build("pane-without-number"))
	withOut := build("%5")
	with := rawOf(withOut)
	if plain == with {
		t.Fatal("test precondition: output with a jump target should carry ranges")
	}
	if stripped := stripRangeMarkers(with); stripped != plain {
		t.Fatalf("adding ranges changed the layout\nbefore:\n%s\nafter, with ranges removed:\n%s", plain, stripped)
	}
	for i, l := range withOut.rawScreen(20) {
		if w := cellwidth.String(strings.TrimRight(l, " ")); w > 34 {
			t.Fatalf("row %d has width %d, out of bounds: %q", i, w, l)
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
	targets, _ := w.out.jumpTargetsAndRows(t, 20) // ranged itself rejects markers that do not wrap the whole row
	assertTargets(t, targets, "", "", "6", "6", "6")
	w.out.mu.Lock()
	raw := w.out.pubs[len(w.out.pubs)-1].Options[render.MainOption]
	w.out.mu.Unlock()
	if n := strings.Count(strings.ReplaceAll(raw, "##", ""), "#[range="); n != 3 {
		t.Fatalf("range count = %d, want 3 (the group row and two tab rows): %q", n, raw)
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
	// title, divider | empty-directory group, no tab | blank | api group, no tab
	assertTargets(t, targets, "", "", "5", "5", "", "2", "2")
	if !strings.Contains(rows[6], "No tabs") {
		t.Fatalf("layout mismatch: %q", rows)
	}
}
