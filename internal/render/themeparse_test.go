package render_test

import (
	"reflect"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

func TestValidColourAcceptsTheDocumentedFormats(t *testing.T) {
	for _, v := range []string{
		"#A78BFA", "#a78bfa", "#000000", "#FFFFFF",
		"colour0", "colour7", "colour123", "colour255", "color0", "color255",
		"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
		"brightblack", "brightred", "brightgreen", "brightyellow", "brightblue", "brightmagenta", "brightcyan", "brightwhite",
		"default",
	} {
		if !render.ValidColour(v) {
			t.Errorf("%q 應為合法色值", v)
		}
	}
}

func TestValidColourRejectsEverythingElseIncludingFormatCharacters(t *testing.T) {
	for _, v := range []string{
		"", " ", " red", "red ", "red\n", "\tred",
		"#", "#fff", "#12345", "#1234567", "#GGGGGG", "A78BFA", "# A78BF",
		"colour", "colour256", "colour999", "colour-1", "colour1x", "colour 1", "color", "colour1000",
		"bright", "brightpurple", "purple", "orange", "terminal", "Default ", "none",
		"red]", "red,", "a,b", "#{pane_id}", "#{x}", "red#[bg=blue", "fg=red", "red;blue", "##ff0000",
		"red\"", "red'", "$HOME", "`red`", "\\red", "red\\", "#A78BFA]#[bg=red",
	} {
		if render.ValidColour(v) {
			t.Errorf("%q 應為無效色值", v)
		}
	}
}

func TestParseThemeWithNothingSetIsTheDefaultTheme(t *testing.T) {
	for _, raw := range []map[string]string{nil, {}} {
		th, invalid := render.ParseTheme(raw)
		if th != render.DefaultTheme() || len(invalid) != 0 {
			t.Fatalf("未設定應為預設主題且無無效項：%+v %v", th, invalid)
		}
	}
}

func TestParseThemeOverridesOnlyTheNamedSwatches(t *testing.T) {
	want := render.DefaultTheme()
	want.Project = "colour33"
	want.StateRunning = "#00FF00"
	th, invalid := render.ParseTheme(map[string]string{"project": "colour33", "state-running": "#00FF00"})
	if th != want || len(invalid) != 0 {
		t.Fatalf("只應改變 project 與 state-running：%+v %v", th, invalid)
	}
}

func TestParseThemeMapsEveryNameToItsOwnSwatch(t *testing.T) {
	raw := map[string]string{
		"emphasis": "#010101", "title": "#020202", "split": "#030303", "project": "#040404",
		"active-tab": "#050505", "inactive-tab": "#060606",
		"state-running": "#070707", "state-ask": "#080808", "state-finished": "#090909", "state-failed": "#0A0A0A",
		"state-problem": "#0B0B0B", "state-idle": "#0C0C0C", "notice": "#0D0D0D",
	}
	th, invalid := render.ParseTheme(raw)
	if th != customTheme() || len(invalid) != 0 {
		t.Fatalf("13 個色票應各對應自己的欄位：%+v %v", th, invalid)
	}
}

func TestParseThemeFallsBackToDefaultOnlyForTheInvalidSwatchAndReportsIt(t *testing.T) {
	want := render.DefaultTheme()
	want.Title = "red"
	th, invalid := render.ParseTheme(map[string]string{
		"title": "red", "notice": "#{pane_id}", "project": "", "split": "nope",
	})
	if th != want {
		t.Fatalf("無效項應退回預設、其他照常套用：%+v", th)
	}
	if !reflect.DeepEqual(invalid, []string{"split", "project", "notice"}) {
		t.Fatalf("無效清單應依色票固定順序列出：%v", invalid)
	}
}

func TestParseThemeIgnoresUnknownSwatchNames(t *testing.T) {
	th, invalid := render.ParseTheme(map[string]string{"nonexistent": "red"})
	if th != render.DefaultTheme() || len(invalid) != 0 {
		t.Fatalf("未知名稱不影響主題：%+v %v", th, invalid)
	}
}
