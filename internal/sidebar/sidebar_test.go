package sidebar_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
)

// Controlled external world: tmux pane facts, the tab list, and tmux output. Tests operate only through sidebar's public
// interface (New/Sync/Run) and observe results from the "sidebar output written to tmux".

type fakePanes struct {
	mu    sync.Mutex
	panes []sidebar.PaneFact
	err   error
	reads int
}

func (f *fakePanes) Panes() ([]sidebar.PaneFact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.panes, f.err
}

func (f *fakePanes) set(p ...sidebar.PaneFact) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.panes, f.err = p, nil
}

func (f *fakePanes) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

type fakeTabs struct {
	mu   sync.Mutex
	file *contract.TabsFile
	err  error
}

func (f *fakeTabs) Read() (*contract.TabsFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.file, f.err
}

func (f *fakeTabs) set(file *contract.TabsFile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.file, f.err = file, nil
}

func (f *fakeTabs) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

type fakeMode struct {
	mu  sync.Mutex
	set sidebar.ModeSetting
	err error
	n   int
}

func (f *fakeMode) Setting() (sidebar.ModeSetting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return f.set, f.err
}

// use simulates the option value after the user sets it (other than unset) or reloads the tmux configuration.
func (f *fakeMode) use(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set, f.err = sidebar.ModeSetting{Value: v, Set: true}, nil
}

func (f *fakeMode) unset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set, f.err = sidebar.ModeSetting{}, nil
}

func (f *fakeMode) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

type fakeOut struct {
	mu   sync.Mutex
	pubs []render.Published
	err  error
	ch   chan struct{}
}

func (f *fakeOut) Publish(p render.Published) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.pubs = append(f.pubs, p)
	if f.ch != nil {
		select {
		case f.ch <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeOut) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pubs)
}

// options holds every layout from the latest publish (including style markers), used to verify colors.
func (f *fakeOut) options() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pubs) == 0 {
		return nil
	}
	var out []string
	for _, v := range f.pubs[len(f.pubs)-1].Options {
		out = append(out, v)
	}
	return out
}

// screen is the plain-text screen seen by a client with avail usable rows. The fixed layout margins (1 cell on each side) and the tab row
// indent (2 cells), the whitespace of the title block, and the ▎ before groups are layout constants covered by the render and layout e2e tests;
// here they are folded away so status tests compare content only: the title row drops the margins and block whitespace, group rows drop the margins and ▎,
// and tab rows drop the 2-cell indent.
func (f *fakeOut) screen(avail int) []string {
	return foldGeometry(f.rawScreen(avail))
}

// rawScreen is the screen without any folding (including margins and indent).
func (f *fakeOut) rawScreen(avail int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pubs) == 0 {
		return nil
	}
	return strings.Split(render.StripStyles(f.truncatedLayoutLocked(avail)), "\n")
}

// truncatedLayoutLocked is the latest published layout keeping only the first avail rows:
// it simulates tmux truncating from the bottom for a client whose usable height is avail. The caller must hold f.mu.
func (f *fakeOut) truncatedLayoutLocked(avail int) string {
	rows := strings.Split(f.pubs[len(f.pubs)-1].Options[render.MainOption], "\n")
	return strings.Join(rows[:min(avail, len(rows))], "\n")
}

func foldGeometry(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "  "):
			out[i] = strings.TrimPrefix(l, "  ")
		case strings.HasPrefix(l, " ▎"):
			out[i] = strings.TrimPrefix(l, " ▎")
		case strings.HasPrefix(l, " "):
			out[i] = strings.TrimPrefix(l, " ")
		default:
			out[i] = l
		}
	}
	return out
}

type world struct {
	panes *fakePanes
	tabs  *fakeTabs
	out   *fakeOut
	mode  *fakeMode
	theme sidebar.ThemeSource // nil: no theme source
	d     *sidebar.Daemon
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{panes: &fakePanes{}, tabs: &fakeTabs{file: &contract.TabsFile{}}, out: &fakeOut{}, mode: &fakeMode{}}
	w.d = sidebar.New(sidebar.Config{Width: 36}, sidebar.Deps{Panes: w.panes, Tabs: w.tabs, Out: w.out, Mode: w.mode})
	return w
}

func tui(id, cwd string) sidebar.PaneFact {
	return sidebar.PaneFact{PaneID: id, Pane: contract.Pane{
		PaneCurrentCommand: "opencode", AlternateOn: true, PaneCurrentPath: cwd,
	}}
}

func TestShowsRunningTUIDirectoryWithItsTabsAsUnknown(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.tabs.set(mustTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"ses_1","title":"修正登入"},{"sessionID":"ses_2","title":"write tests"}]}}}`))
	if err := w.d.Sync(); err != nil {
		t.Fatal(err)
	}
	got := w.out.screen(20)
	want := []string{
		"OPENCODE                      !2",
		"─────────────────────────────────",
		"api                           !2",
		"! 修正登入",
		"! write tests",
	}
	assertLines(t, got, want)
}

func mustTabs(t *testing.T, raw string) *contract.TabsFile {
	t.Helper()
	f, err := contract.ParseTabs([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	trim := func(in []string) []string {
		out := make([]string, len(in))
		for i, l := range in {
			out[i] = strings.TrimRight(l, " ")
		}
		return out
	}
	g, w := trim(got), trim(want)
	if strings.Join(g, "\n") != strings.Join(w, "\n") {
		t.Fatalf("screen mismatch\n got:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(w, "\n"))
	}
}

var errBoom = errors.New("boom")

func (w *world) sync(t *testing.T) {
	t.Helper()
	if err := w.d.Sync(); err != nil {
		t.Fatal(err)
	}
}

func (w *world) setTabs(t *testing.T, raw string) { t.Helper(); w.tabs.set(mustTabs(t, raw)) }

func TestNoTUIShowsEmptyFrameNotAnErrorOrBlank(t *testing.T) {
	w := newWorld(t)
	w.panes.set(sidebar.PaneFact{PaneID: "%1", Pane: contract.Pane{PaneCurrentCommand: "zsh", PaneCurrentPath: "/work/api"}})
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"ses_1","title":"歷史目錄不應出現"}]}}}`)
	w.sync(t)
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE",
		"─────────────────────────────────",
		"No OpenCode TUI running",
	})
}

func TestTUIWithoutTabsKeepsGroupAndSaysNoTab(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	w.sync(t)
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE",
		"─────────────────────────────────",
		"api",
		"No tabs",
	})
}

func TestOnlyRunningTUIDirectoriesAreListedAndDuplicatesCollapse(t *testing.T) {
	w := newWorld(t)
	pane := func(id, cmd string, alt bool, cwd string) sidebar.PaneFact {
		return sidebar.PaneFact{PaneID: id, Pane: contract.Pane{PaneCurrentCommand: cmd, AlternateOn: alt, PaneCurrentPath: cwd}}
	}
	w.panes.set(
		tui("%1", "/work/api"),
		tui("%1", "/work/api"),                     // the same pane listed twice through a linked window
		tui("%2", "/work/api"),                     // a second TUI in the same directory
		pane("%3", "opencode", false, "/work/svc"), // serve / run / mini stay on the main screen
		pane("%4", "zsh", true, "/work/shell"),     // a shell left on the alternate screen
		pane("%5", "nvim", true, "/work/editor"),   // another full-screen program
	)
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{
		"/work/api":{"tabs":[{"sessionID":"ses_1","title":"one"}]},
		"/work/svc":{"tabs":[{"sessionID":"ses_7","title":"svc"}]},
		"/work/shell":{"tabs":[{"sessionID":"ses_8","title":"shell"}]},
		"/work/editor":{"tabs":[{"sessionID":"ses_6","title":"editor"}]},
		"/work/old":{"tabs":[{"sessionID":"ses_9","title":"history"}]}}}`)
	w.sync(t)
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE                      !1",
		"─────────────────────────────────",
		"api                           !1",
		"! one",
	})
}

func TestDirectoriesSortByFullPathAndKeepExactIdentity(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/z/api"), tui("%2", "/a/api"), tui("%3", "/a/api-link"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{
		"/z/api":{"tabs":[{"sessionID":"s1","title":"z-1"},{"sessionID":"s2","title":"z-2"}]},
		"/a/api":{"tabs":[{"sessionID":"s3","title":"a-1"}]},
		"/a/api-link":{"tabs":[{"sessionID":"s4","title":"link-1"}]}}}`)
	w.sync(t)
	got := w.out.screen(30)
	var tabs []string
	for _, l := range got {
		if strings.HasPrefix(l, "! ") {
			tabs = append(tabs, strings.TrimPrefix(l, "! "))
		}
	}
	if strings.Join(tabs, ",") != "a-1,link-1,z-1,z-2" {
		t.Fatalf("tab order = %v\n%s", tabs, strings.Join(got, "\n"))
	}
	// /a/api, /a/api-link, and /z/api stay independent and are not merged; the duplicate api names get parent directories, while api-link was already unique
	if names := strings.Join(groupLines(got), ","); names != "a/api,api-link,z/api" {
		t.Fatalf("groups = %s\n%s", names, strings.Join(got, "\n"))
	}
}

func TestUnreadableTabListKeepsLastGoodListWithNoticeNotEmptyList(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"keep me"}]}}}`)
	w.sync(t)
	w.tabs.fail(errBoom)
	if err := w.d.Sync(); err == nil {
		t.Fatal("a read failure must report an error")
	}
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE                      !1",
		"─────────────────────────────────",
		"▲ Cannot read tab list",
		"api                           !1",
		"! keep me",
	})
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"keep me"}]}}}`)
	w.sync(t)
	if got := strings.Join(w.out.screen(20), "\n"); strings.Contains(got, "Cannot read") {
		t.Fatalf("the notice should disappear after recovery:\n%s", got)
	}
}

func TestReadErrorBeforeAnyGoodDataShowsNoticeNotEmptyStates(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.tabs.fail(errBoom)
	_ = w.d.Sync()
	got := strings.Join(w.out.screen(20), "\n")
	if strings.Contains(got, "No OpenCode TUI running") || strings.Contains(got, "No tabs") || !strings.Contains(got, "Cannot read tab list") {
		t.Fatalf("the error was disguised as an empty list:\n%s", got)
	}
	w2 := newWorld(t)
	w2.panes.fail(errBoom)
	_ = w2.d.Sync()
	got = strings.Join(w2.out.screen(20), "\n")
	if strings.Contains(got, "No OpenCode TUI running") || !strings.Contains(got, "Cannot read tmux") {
		t.Fatalf("the tmux error was disguised as no TUI:\n%s", got)
	}
}

func TestUnchangedContentIsNotRewritten(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"a"}]}}}`)
	w.sync(t)
	w.sync(t)
	w.sync(t)
	if n := w.out.count(); n != 1 {
		t.Fatalf("content was unchanged but written %d times", n)
	}
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"a"},{"sessionID":"s2","title":"b"}]}}}`)
	w.sync(t)
	if n := w.out.count(); n != 2 {
		t.Fatalf("a content change should write once more; actual %d", n)
	}
}

func TestTitlesAreDataNotTmuxFormat(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/#{pane_id}"))
	w.setTabs(t, "{\"global\":{\"tabs\":[]},\"cwd\":{\"/work/#{pane_id}\":{\"tabs\":[{\"sessionID\":\"s1\",\"title\":\"x #[fg=red] #(id)\\nline2\"}]}}}")
	w.sync(t)
	w.out.mu.Lock()
	raw := w.out.pubs[0].Options[render.MainOption]
	w.out.mu.Unlock()
	for _, bad := range []string{"#[fg=red]", "#(id)", "#{pane_id}"} {
		if strings.Contains(strings.ReplaceAll(raw, "##", ""), bad) {
			t.Fatalf("%q not escaped: %q", bad, raw)
		}
	}
}

// ---- Event notifications + low-frequency backstop (Run loop) ----
// Time is injected through controlled channels: Wake stands for event notifications (tmux hooks/tab file changes) and Ticks for the low-frequency backstop.

type runner struct {
	*world
	wake   chan struct{}
	ticks  chan time.Time
	done   chan error
	cancel context.CancelFunc
}

func startRun(t *testing.T, w *world) *runner {
	t.Helper()
	r := &runner{world: w, wake: make(chan struct{}, 1), ticks: make(chan time.Time), done: make(chan error, 1)}
	w.out.ch = make(chan struct{}, 16)
	w.d = sidebar.New(sidebar.Config{Width: 36, Debounce: time.Millisecond},
		sidebar.Deps{Panes: w.panes, Tabs: w.tabs, Out: w.out, Mode: w.mode, Theme: w.theme, Wake: r.wake, Ticks: r.ticks})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { r.done <- w.d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-r.done })
	return r
}

func (r *runner) waitPublish(t *testing.T) {
	t.Helper()
	select {
	case <-r.out.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("the sidebar was not written within the expected time")
	}
}

func (r *runner) expectQuiet(t *testing.T) {
	t.Helper()
	select {
	case <-r.out.ch:
		t.Fatal("there should be no new write")
	case <-time.After(60 * time.Millisecond):
	}
}

func TestEventNotificationUpdatesWithoutWaitingForBackstop(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	r := startRun(t, w)
	r.waitPublish(t) // the first sync at startup
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"new tab"}]}}}`)
	r.wake <- struct{}{}
	r.waitPublish(t)
	if got := strings.Join(w.out.screen(20), "\n"); !strings.Contains(got, "new tab") {
		t.Fatalf("the new tab is not shown:\n%s", got)
	}
}

func TestMissedNotificationIsCaughtByBackstopTick(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	r := startRun(t, w)
	r.waitPublish(t)
	// force-close the pane: no notification at all
	w.panes.set()
	r.expectQuiet(t) // with no event and no backstop, it does not update by itself
	r.ticks <- time.Now()
	r.waitPublish(t)
	if got := strings.Join(w.out.screen(20), "\n"); !strings.Contains(got, "No OpenCode TUI running") {
		t.Fatalf("old data still shown after the backstop:\n%s", got)
	}
}

func TestUnchangedWakeAndTickDoNotRewrite(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	r := startRun(t, w)
	r.waitPublish(t)
	r.wake <- struct{}{}
	r.ticks <- time.Now()
	r.expectQuiet(t)
	if n := w.out.count(); n != 1 {
		t.Fatalf("written %d times", n)
	}
}

func TestSyncFailuresInRunAreRetriedNotFatal(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	r := startRun(t, w)
	r.waitPublish(t)
	w.tabs.fail(errBoom)
	r.ticks <- time.Now()
	r.waitPublish(t) // the notice is shown
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"back"}]}}}`)
	r.ticks <- time.Now()
	r.waitPublish(t)
	if got := strings.Join(w.out.screen(20), "\n"); !strings.Contains(got, "back") || strings.Contains(got, "Cannot read") {
		t.Fatalf("the screen is wrong after recovery:\n%s", got)
	}
}

func TestTimingBudgetsMatchTheSpec(t *testing.T) {
	// Ordinary changes within 1 second and missed notifications corrected within 10 seconds (spec acceptance targets).
	if sidebar.DefaultDebounce >= time.Second/2 {
		t.Fatalf("debounce %v consumes too much of the 1-second budget", sidebar.DefaultDebounce)
	}
	if sidebar.DefaultBackstop > 10*time.Second-sidebar.DefaultDebounce {
		t.Fatalf("backstop period %v exceeds the 10-second target", sidebar.DefaultBackstop)
	}
}

// ---- Directory display names: basename, adding parent directories level by level for duplicates (issue #10) ----

// groupLines extracts the group title rows on screen (the first row after the divider, or the row after a blank row).
func groupLines(screen []string) []string {
	var out []string
	for i := 2; i < len(screen); i++ {
		if screen[i] == "" || (i > 2 && screen[i-1] != "") {
			continue
		}
		out = append(out, strings.Fields(screen[i])[0])
	}
	return out
}

func TestUniqueBasenamesStayShort(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/home/me/aicova/api"), tui("%2", "/home/me/profcoo/web"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/home/me/aicova/api":{"tabs":[]},"/home/me/profcoo/web":{"tabs":[]}}}`)
	w.sync(t)
	if got := strings.Join(groupLines(w.out.screen(20)), ","); got != "api,web" {
		t.Fatalf("group names = %s", got)
	}
}

func TestSameBasenameGetsParentPrefixOnlyWhereNeededAndKeepsPathOrder(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/w/profcoo/api"), tui("%2", "/w/aicova/api"), tui("%3", "/w/infra"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{
		"/w/profcoo/api":{"tabs":[{"sessionID":"s1","title":"p"}]},
		"/w/aicova/api":{"tabs":[{"sessionID":"s2","title":"a"}]},
		"/w/infra":{"tabs":[]}}}`)
	w.sync(t)
	// Ordering uses the full path (/w/aicova/api < /w/infra < /w/profcoo/api); display names affect neither ordering nor identity
	if got := strings.Join(groupLines(w.out.screen(30)), ","); got != "aicova/api,infra,profcoo/api" {
		t.Fatalf("group names = %s", got)
	}
}

func TestDeeperCollisionsAddLevelsUntilDistinguishable(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/a/x/api"), tui("%2", "/b/x/api"), tui("%3", "/c/y/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/a/x/api":{"tabs":[]},"/b/x/api":{"tabs":[]},"/c/y/api":{"tabs":[]}}}`)
	w.sync(t)
	if got := strings.Join(groupLines(w.out.screen(30)), ","); got != "a/x/api,b/x/api,y/api" {
		t.Fatalf("group names = %s", got)
	}
}

func TestOnlyIncludedDirectoriesAreConsideredForDisambiguation(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/a/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/a/api":{"tabs":[]},"/b/api":{"tabs":[{"sessionID":"s","title":"歷史"}]}}}`)
	w.sync(t)
	if got := strings.Join(groupLines(w.out.screen(30)), ","); got != "api" {
		t.Fatalf("a directory without a TUI should not affect disambiguation: %s", got)
	}
}

func TestRootAndNestedPathsStillGetDistinctNames(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/api"), tui("%2", "/x/api"), tui("%3", "/"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/api":{"tabs":[]},"/x/api":{"tabs":[]},"/":{"tabs":[]}}}`)
	w.sync(t)
	if got := strings.Join(groupLines(w.out.screen(30)), ","); got != "/,api,x/api" {
		t.Fatalf("group names = %s", got)
	}
}
