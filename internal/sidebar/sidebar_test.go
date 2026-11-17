package sidebar_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/heightfit"
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
	pubs []heightfit.Published
	err  error
	ch   chan struct{}
}

func (f *fakeOut) Publish(p heightfit.Published) error {
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

// screen 是 side-status 內容寬度為 35 的 client、可用 avail 列時看到的純文字畫面。
func (f *fakeOut) screen(avail int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pubs) == 0 {
		return nil
	}
	return strings.Split(heightfit.StripStyles(f.pubs[len(f.pubs)-1].Select(avail)), "\n")
}

type world struct {
	panes *fakePanes
	tabs  *fakeTabs
	out   *fakeOut
	mode  *fakeMode
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
		"◆ OPENCODE                       !2",
		"───────────────────────────────────",
		"api                              !2",
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
		"◆ OPENCODE",
		"───────────────────────────────────",
		"沒有 OpenCode TUI",
	})
}

func TestTUIWithoutTabsKeepsGroupAndSaysNoTab(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{}}`)
	w.sync(t)
	assertLines(t, w.out.screen(20), []string{
		"◆ OPENCODE",
		"───────────────────────────────────",
		"api",
		"  無 tab",
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
		"◆ OPENCODE                       !1",
		"───────────────────────────────────",
		"api                              !1",
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
	groups := 0
	for _, l := range got {
		if strings.HasPrefix(l, "api") {
			groups++
		}
	}
	if groups != 3 { // /a/api、/a/api-link、/z/api 各自獨立，不合併
		t.Fatalf("群組數 = %d\n%s", groups, strings.Join(got, "\n"))
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
		"◆ OPENCODE                       !1",
		"───────────────────────────────────",
		"無法讀取 tab 清單，顯示最後可用資料",
		"api                              !1",
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
	raw := w.out.pubs[0].Select(20)
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
		sidebar.Deps{Panes: w.panes, Tabs: w.tabs, Out: w.out, Mode: w.mode, Wake: r.wake, Ticks: r.ticks})
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
