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

// use simulates the color options set by the user (only configured entries; unset ones are absent).
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

// styled is the complete layout from the latest publish (with styles).
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
		t.Fatalf("with no theme source the default theme should be used: %q", got)
	}
}

func TestUnsetSwatchesPublishTheDefaultTheme(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use()
	w.sync(t)
	if got := w.out.styled(); !strings.Contains(got, "bg=#A78BFA,fg=#18181B") || !strings.Contains(got, "fg=#F2EEE3") {
		t.Fatalf("unset should give the default theme: %q", got)
	}
}

func TestPartialOverrideChangesOnlyTheNamedElements(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use("project", "colour33", "emphasis", "#112233")
	w.sync(t)
	got := w.out.styled()
	for _, want := range []string{"bg=#112233,fg=#18181B", "fg=#112233]▎", "fg=colour33]api", "fg=#404040]─"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %q", want, got)
		}
	}
	for _, gone := range []string{"#A78BFA", "#F2EEE3"} {
		if strings.Contains(got, gone) {
			t.Errorf("overridden default color %q should not appear: %q", gone, got)
		}
	}
}

func TestInvalidSwatchFallsBackAloneAndShowsNoError(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use("project", "#{pane_id}", "emphasis", "red", "split", "")
	w.sync(t)
	got := w.out.styled()
	if !strings.Contains(got, "fg=#F2EEE3]api") || !strings.Contains(got, "fg=#404040]─") {
		t.Errorf("an invalid color should only make that entry fall back to the default: %q", got)
	}
	if !strings.Contains(got, "bg=red,fg=#18181B") {
		t.Errorf("a valid emphasis should still apply: %q", got)
	}
	if plain := strings.Join(w.out.screen(20), "\n"); strings.Contains(plain, "色") || strings.Contains(plain, "▲") {
		t.Errorf("the sidebar should not show color errors:\n%s", plain)
	}
}

func TestChangingASwatchRepublishesAndTheSameThemeDoesNot(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use("project", "red")
	w.sync(t)
	w.sync(t)
	if n := w.out.count(); n != 1 {
		t.Fatalf("an unchanged theme should not be written again; written %d times", n)
	}
	th.use("project", "blue")
	w.sync(t)
	if n := w.out.count(); n != 2 || !strings.Contains(w.out.styled(), "fg=blue]api") {
		t.Fatalf("a theme change should be republished (written %d times): %q", w.out.count(), w.out.styled())
	}
	th.use() // unset (set -gu) returns to the default
	w.sync(t)
	if n := w.out.count(); n != 3 || !strings.Contains(w.out.styled(), "fg=#F2EEE3]api") {
		t.Fatalf("unsetting a color should return to the default and republish (written %d times): %q", w.out.count(), w.out.styled())
	}
}

func TestInvalidValueEquivalentToUnsetDoesNotRewrite(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use()
	w.sync(t)
	th.use("project", "bogus")
	w.sync(t)
	if n := w.out.count(); n != 1 {
		t.Fatalf("the theme is unchanged after an invalid value falls back to the default, so it should not be written again; written %d times", n)
	}
}

func TestUnreadableThemeKeepsTheLastValidThemeAndDoesNotBlockTracking(t *testing.T) {
	w, th := newThemeWorld(t)
	th.use("project", "red")
	w.sync(t)
	th.fail(errBoom)
	w.setTabs(t, `{"global":{"tabs":[]},"cwd":{"/work/api":{"tabs":[{"sessionID":"s1","title":"新的標題"}]}}}`)
	if err := w.d.Sync(); err == nil {
		t.Fatal("failing to read the theme must report an error")
	}
	got := w.out.styled()
	if !strings.Contains(got, "fg=red]api") || !strings.Contains(got, "新的標題") {
		t.Fatalf("a read failure should keep the last valid theme and keep tracking as usual: %q", got)
	}
}

func TestNeverReadableThemeUsesTheDefaultAndStillTracks(t *testing.T) {
	w, th := newThemeWorld(t)
	th.fail(errBoom)
	if err := w.d.Sync(); err == nil {
		t.Fatal("failing to read the theme must report an error")
	}
	got := w.out.styled()
	if !strings.Contains(got, "fg=#F2EEE3]api") || !strings.Contains(got, "修正登入") {
		t.Fatalf("with no successful read the default theme should be used and tracking should continue as usual: %q", got)
	}
}

func TestThemeIsAppliedBeforeTrackingStartsAndToEveryClientLayout(t *testing.T) {
	w, th := newThemeWorld(t)
	w.mode.use("bogus") // tracking has not started; only a notice is shown, and the notice row also uses the theme colors
	th.use("notice", "colour99")
	_ = w.d.Sync()
	if got := w.out.styled(); !strings.Contains(got, "fg=colour99]▲") {
		t.Fatalf("the notice row should apply the theme: %q", got)
	}
	w.mode.use("cwd")
	th.use("project", "red")
	w.sync(t)
	seen := 0
	for _, v := range w.out.options() {
		if strings.Contains(v, "api") {
			seen++
			if !strings.Contains(v, "fg=red]api") {
				t.Fatalf("every layout should apply the same theme: %q", v)
			}
		}
		if strings.Contains(v, "#F2EEE3") {
			t.Fatalf("layout still contains default colors: %q", v)
		}
	}
	if seen == 0 {
		t.Fatal("no layout containing groups to verify")
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
		t.Fatalf("the new colors should apply after the notification: %q", got)
	}
}
