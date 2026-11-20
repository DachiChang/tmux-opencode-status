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

// 受控外部世界：tmux pane 事實、tab 清單與 tmux 輸出。測試只透過 sidebar 的公開
// 介面（New/Sync/Run）操作，並從「寫到 tmux 的側欄輸出」觀察結果。

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

// use 模擬使用者設定（unset 以外）或重載 tmux 設定後的選項值。
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

// options 是最近一次發布的所有版面（含樣式標記），用來驗證顏色。
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

// screen 是可用 avail 列的 client 看到的純文字畫面。排版的固定邊距（左右各 1 格）與 tab 行
// 縮排（2 格）、標題底塊的空白與群組前的 ▎ 是版面常數，由 render 與 layout e2e 測試負責；
// 這裡把它們折回，讓狀態類測試只比對內容：標題行去掉邊距與底塊空白，群組行去掉邊距與 ▎，
// tab 行去掉 2 格縮排。
func (f *fakeOut) screen(avail int) []string {
	return foldGeometry(f.rawScreen(avail))
}

// rawScreen 是不經任何折回的畫面（含邊距與縮排）。
func (f *fakeOut) rawScreen(avail int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pubs) == 0 {
		return nil
	}
	return strings.Split(render.StripStyles(f.truncatedLayoutLocked(avail)), "\n")
}

// truncatedLayoutLocked 是最近一次發布的版面只留前 avail 列：
// 模擬 tmux 對可用高度為 avail 的 client 從底部截斷。呼叫端須持有 f.mu。
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
	theme sidebar.ThemeSource // nil：不接主題來源
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
		ForegroundArgv: []string{"opencode"}, ForegroundCwd: cwd,
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
		t.Fatalf("畫面不符\n got:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(w, "\n"))
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
	w.panes.set(sidebar.PaneFact{PaneID: "%1", Pane: contract.Pane{PaneCurrentCommand: "zsh", ForegroundArgv: []string{"zsh"}, ForegroundCwd: "/work/api"}})
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"ses_1","title":"歷史目錄不應出現"}]}}}`)
	w.sync(t)
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE",
		"─────────────────────────────────",
		"沒有 OpenCode TUI",
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
		"無 tab",
	})
}

func TestOnlyRunningTUIDirectoriesAreListedAndDuplicatesCollapse(t *testing.T) {
	w := newWorld(t)
	nonTUI := func(id string, argv ...string) sidebar.PaneFact {
		return sidebar.PaneFact{PaneID: id, Pane: contract.Pane{PaneCurrentCommand: "opencode", AlternateOn: true, ForegroundArgv: argv, ForegroundCwd: "/work/api"}}
	}
	w.panes.set(
		tui("%1", "/work/api"),
		tui("%1", "/work/api"), // 同一 pane 經 linked window 重複列出
		tui("%2", "/work/api"), // 同目錄第二個 TUI
		nonTUI("%3", "opencode", "serve"),
		nonTUI("%4", "opencode", "session", "list"),
	)
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{
		"/work/api":{"tabs":[{"sessionID":"ses_1","title":"one"}]},
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
		t.Fatalf("tab 順序 = %v\n%s", tabs, strings.Join(got, "\n"))
	}
	// /a/api、/a/api-link、/z/api 各自獨立不合併；同名的 api 補父目錄，api-link 本來就唯一
	if names := strings.Join(groupLines(got), ","); names != "a/api,api-link,z/api" {
		t.Fatalf("群組 = %s\n%s", names, strings.Join(got, "\n"))
	}
}

func TestUnreadableTabListKeepsLastGoodListWithNoticeNotEmptyList(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"keep me"}]}}}`)
	w.sync(t)
	w.tabs.fail(errBoom)
	if err := w.d.Sync(); err == nil {
		t.Fatal("讀取失敗必須回報錯誤")
	}
	assertLines(t, w.out.screen(20), []string{
		"OPENCODE                      !1",
		"─────────────────────────────────",
		"▲ 無法讀取 tab 清單，顯示最後可用",
		" 資料",
		"api                           !1",
		"! keep me",
	})
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"keep me"}]}}}`)
	w.sync(t)
	if got := strings.Join(w.out.screen(20), "\n"); strings.Contains(got, "無法讀取") {
		t.Fatalf("恢復後提示應消失：\n%s", got)
	}
}

func TestReadErrorBeforeAnyGoodDataShowsNoticeNotEmptyStates(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.tabs.fail(errBoom)
	_ = w.d.Sync()
	got := strings.Join(w.out.screen(20), "\n")
	if strings.Contains(got, "沒有 OpenCode TUI") || strings.Contains(got, "無 tab") || !strings.Contains(got, "無法讀取 tab 清單") {
		t.Fatalf("錯誤被偽裝成空清單：\n%s", got)
	}
	w2 := newWorld(t)
	w2.panes.fail(errBoom)
	_ = w2.d.Sync()
	got = strings.Join(w2.out.screen(20), "\n")
	if strings.Contains(got, "沒有 OpenCode TUI") || !strings.Contains(got, "無法讀取 tmux") {
		t.Fatalf("tmux 錯誤被偽裝成沒有 TUI：\n%s", got)
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
		t.Fatalf("內容不變卻寫入 %d 次", n)
	}
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"a"},{"sessionID":"s2","title":"b"}]}}}`)
	w.sync(t)
	if n := w.out.count(); n != 2 {
		t.Fatalf("內容改變應再寫入一次，實際 %d", n)
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
			t.Fatalf("未跳脫 %q：%q", bad, raw)
		}
	}
}

// ---- 事件通知 + 低頻補漏（Run 迴圈）----
// 時間以受控 channel 注入：Wake 代表事件通知（tmux hook／tab 檔變動），Ticks 代表低頻補漏。

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
		t.Fatal("沒有在預期時間內寫出側欄")
	}
}

func (r *runner) expectQuiet(t *testing.T) {
	t.Helper()
	select {
	case <-r.out.ch:
		t.Fatal("不應該有新的寫入")
	case <-time.After(60 * time.Millisecond):
	}
}

func TestEventNotificationUpdatesWithoutWaitingForBackstop(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	r := startRun(t, w)
	r.waitPublish(t) // 啟動時的第一次同步
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"new tab"}]}}}`)
	r.wake <- struct{}{}
	r.waitPublish(t)
	if got := strings.Join(w.out.screen(20), "\n"); !strings.Contains(got, "new tab") {
		t.Fatalf("新 tab 未顯示：\n%s", got)
	}
}

func TestMissedNotificationIsCaughtByBackstopTick(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	r := startRun(t, w)
	r.waitPublish(t)
	// 強制關閉 pane：沒有任何通知
	w.panes.set()
	r.expectQuiet(t) // 沒有事件也沒有補漏時，不會自己更新
	r.ticks <- time.Now()
	r.waitPublish(t)
	if got := strings.Join(w.out.screen(20), "\n"); !strings.Contains(got, "沒有 OpenCode TUI") {
		t.Fatalf("補漏後仍顯示舊資料：\n%s", got)
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
		t.Fatalf("寫入 %d 次", n)
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
	r.waitPublish(t) // 顯示提示
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"back"}]}}}`)
	r.ticks <- time.Now()
	r.waitPublish(t)
	if got := strings.Join(w.out.screen(20), "\n"); !strings.Contains(got, "back") || strings.Contains(got, "無法讀取") {
		t.Fatalf("恢復後畫面不正確：\n%s", got)
	}
}

func TestTimingBudgetsMatchTheSpec(t *testing.T) {
	// 一般變動 1 秒、漏通知 10 秒內修正（spec 驗收目標）。
	if sidebar.DefaultDebounce >= time.Second/2 {
		t.Fatalf("debounce %v 吃掉太多 1 秒預算", sidebar.DefaultDebounce)
	}
	if sidebar.DefaultBackstop > 10*time.Second-sidebar.DefaultDebounce {
		t.Fatalf("補漏週期 %v 超過 10 秒目標", sidebar.DefaultBackstop)
	}
}

// ---- 目錄顯示名稱：basename，同名逐層補父目錄（issue #10）----

// groupLines 取出畫面上的群組標題行（分隔線之後第一行，或空行之後的行）。
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
		t.Fatalf("群組名稱 = %s", got)
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
	// 排序依完整路徑（/w/aicova/api < /w/infra < /w/profcoo/api），顯示名稱不影響排序與身分
	if got := strings.Join(groupLines(w.out.screen(30)), ","); got != "aicova/api,infra,profcoo/api" {
		t.Fatalf("群組名稱 = %s", got)
	}
}

func TestDeeperCollisionsAddLevelsUntilDistinguishable(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/a/x/api"), tui("%2", "/b/x/api"), tui("%3", "/c/y/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/a/x/api":{"tabs":[]},"/b/x/api":{"tabs":[]},"/c/y/api":{"tabs":[]}}}`)
	w.sync(t)
	if got := strings.Join(groupLines(w.out.screen(30)), ","); got != "a/x/api,b/x/api,y/api" {
		t.Fatalf("群組名稱 = %s", got)
	}
}

func TestOnlyIncludedDirectoriesAreConsideredForDisambiguation(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/a/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/a/api":{"tabs":[]},"/b/api":{"tabs":[{"sessionID":"s","title":"歷史"}]}}}`)
	w.sync(t)
	if got := strings.Join(groupLines(w.out.screen(30)), ","); got != "api" {
		t.Fatalf("沒有 TUI 的目錄不應影響消歧：%s", got)
	}
}

func TestRootAndNestedPathsStillGetDistinctNames(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/api"), tui("%2", "/x/api"), tui("%3", "/"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/api":{"tabs":[]},"/x/api":{"tabs":[]},"/":{"tabs":[]}}}`)
	w.sync(t)
	if got := strings.Join(groupLines(w.out.screen(30)), ","); got != "/,api,x/api" {
		t.Fatalf("群組名稱 = %s", got)
	}
}
