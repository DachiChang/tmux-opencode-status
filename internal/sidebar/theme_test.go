package sidebar_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/render"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
)

type fakeTheme struct {
	mu  sync.Mutex
	raw map[string]string
	err error
	n   int
}

func (f *fakeTheme) Swatches() (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for k, v := range f.raw {
		out[k] = v
	}
	return out, nil
}

// use 模擬使用者設定的色票（只含有設定的項目，未設定的不在其中）。
func (f *fakeTheme) use(kv ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raw, f.err = map[string]string{}, nil
	for i := 0; i+1 < len(kv); i += 2 {
		f.raw[kv[i]] = kv[i+1]
	}
}

func (f *fakeTheme) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func newThemeWorld(t *testing.T) (*world, *fakeTheme) {
	t.Helper()
	w := newWorld(t)
	th := &fakeTheme{}
	w.theme = th
	w.d = sidebar.New(sidebar.Config{Width: 36}, sidebar.Deps{Panes: w.panes, Tabs: w.tabs, Out: w.out, Mode: w.mode, Theme: th})
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"修正登入"}]}}}`)
	return w, th
}

// styled 是最近一次發布的完整版面（帶樣式）。
func (f *fakeOut) styled() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pubs[len(f.pubs)-1].Options[render.MainOption]
}

func TestNilThemeSourceMeansAllSwatchesUnset(t *testing.T) {
	w := newWorld(t)
	w.panes.set(tui("%1", "/work/api"))
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"x"}]}}}`)
	w.sync(t)
	if got := w.out.styled(); !strings.Contains(got, "#[bg=#A78BFA,fg=#18181B]") {
		t.Fatalf("未接主題來源應使用預設主題：%q", got)
	}
}

func TestUnsetSwatchesPublishTheDefaultTheme(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use()
	w.sync(t)
	if got := w.out.styled(); !strings.Contains(got, "bg=#A78BFA,fg=#18181B") || !strings.Contains(got, "fg=#F2EEE3") {
		t.Fatalf("未設定應為預設主題：%q", got)
	}
}

func TestPartialOverrideChangesOnlyTheNamedElements(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use("project", "colour33", "emphasis", "#112233")
	w.sync(t)
	got := w.out.styled()
	for _, want := range []string{"bg=#112233,fg=#18181B", "fg=#112233]▎", "fg=colour33]api", "fg=#404040]─"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q：%q", want, got)
		}
	}
	for _, gone := range []string{"#A78BFA", "#F2EEE3"} {
		if strings.Contains(got, gone) {
			t.Errorf("被覆寫的預設色 %q 不應出現：%q", gone, got)
		}
	}
}

func TestInvalidSwatchFallsBackAloneAndShowsNoError(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use("project", "#{pane_id}", "emphasis", "red", "split", "")
	w.sync(t)
	got := w.out.styled()
	if !strings.Contains(got, "fg=#F2EEE3]api") || !strings.Contains(got, "fg=#404040]─") {
		t.Errorf("無效色票應只讓該項退回預設：%q", got)
	}
	if !strings.Contains(got, "bg=red,fg=#18181B") {
		t.Errorf("有效的 emphasis 應照常套用：%q", got)
	}
	if plain := strings.Join(w.out.screen(20), "\n"); strings.Contains(plain, "色") || strings.Contains(plain, "▲") {
		t.Errorf("側欄不應顯示色票錯誤：\n%s", plain)
	}
}

func TestChangingASwatchRepublishesAndTheSameThemeDoesNot(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use("project", "red")
	w.sync(t)
	w.sync(t)
	if n := w.out.count(); n != 1 {
		t.Fatalf("主題相同不應重複寫入，寫入 %d 次", n)
	}
	th.use("project", "blue")
	w.sync(t)
	if n := w.out.count(); n != 2 || !strings.Contains(w.out.styled(), "fg=blue]api") {
		t.Fatalf("主題改變應重新發布（寫入 %d 次）：%q", w.out.count(), w.out.styled())
	}
	th.use() // 取消（set -gu）回到預設
	w.sync(t)
	if n := w.out.count(); n != 3 || !strings.Contains(w.out.styled(), "fg=#F2EEE3]api") {
		t.Fatalf("取消色票應回到預設並重新發布（寫入 %d 次）：%q", w.out.count(), w.out.styled())
	}
}

func TestInvalidValueEquivalentToUnsetDoesNotRewrite(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use()
	w.sync(t)
	th.use("project", "bogus")
	w.sync(t)
	if n := w.out.count(); n != 1 {
		t.Fatalf("無效值退回預設後主題沒變，不應重複寫入，寫入 %d 次", n)
	}
}

func TestUnreadableThemeKeepsTheLastValidThemeAndDoesNotBlockTracking(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use("project", "red")
	w.sync(t)
	th.fail(errBoom)
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"新的標題"}]}}}`)
	if err := w.d.Sync(); err == nil {
		t.Fatal("讀不到主題必須回報錯誤")
	}
	got := w.out.styled()
	if !strings.Contains(got, "fg=red]api") || !strings.Contains(got, "新的標題") {
		t.Fatalf("讀取失敗應沿用上次有效主題並照常追蹤：%q", got)
	}
}

func TestNeverReadableThemeUsesTheDefaultAndStillTracks(t *testing.T) {
	w, th := newThemeWorld(t)
	th.fail(errBoom)
	if err := w.d.Sync(); err == nil {
		t.Fatal("讀不到主題必須回報錯誤")
	}
	got := w.out.styled()
	if !strings.Contains(got, "fg=#F2EEE3]api") || !strings.Contains(got, "修正登入") {
		t.Fatalf("從未讀取成功應使用預設主題並照常追蹤：%q", got)
	}
}

func TestThemeIsAppliedBeforeTrackingStartsAndToEveryClientLayout(t *testing.T) {
	w, th := newThemeWorld(t)
	w.mode.use("bogus") // 尚未開始追蹤，只顯示提示，提示列也用主題色
	th.use("notice", "colour99")
	_ = w.d.Sync()
	if got := w.out.styled(); !strings.Contains(got, "fg=colour99]▲") {
		t.Fatalf("提示列應套用主題：%q", got)
	}
	w.mode.use("cwd")
	th.use("project", "red")
	w.sync(t)
	seen := 0
	for _, v := range w.out.options() {
		if strings.Contains(v, "api") {
			seen++
			if !strings.Contains(v, "fg=red]api") {
				t.Fatalf("每個版面都應套用同一主題：%q", v)
			}
		}
		if strings.Contains(v, "#F2EEE3") {
			t.Fatalf("版面殘留預設色：%q", v)
		}
	}
	if seen == 0 {
		t.Fatal("沒有含群組的版面可驗證")
	}
}

func TestThemeNotificationAppliesWithoutWaitingForBackstop(t *testing.T) {
	w, th := newThemeWorld(t)
	r := startRun(t, w)
	r.waitPublish(t)
	th.use("project", "red")
	r.wake <- struct{}{}
	r.waitPublish(t)
	if got := w.out.styled(); !strings.Contains(got, "fg=red]api") {
		t.Fatalf("通知後應套用新色票：%q", got)
	}
}
