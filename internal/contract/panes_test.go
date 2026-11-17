package contract_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

func loadPanes(t *testing.T) map[string]contract.Pane {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/opencode-2.3.0/panes.json")
	if err != nil {
		t.Fatal(err)
	}
	var panes []contract.Pane
	if err := json.Unmarshal(raw, &panes); err != nil {
		t.Fatal(err)
	}
	byName := map[string]contract.Pane{}
	for _, p := range panes {
		byName[p.Name] = p
	}
	return byName
}

// 真實 tmux next-3.9 + opencode v2.3.0 的 pane 事實：只有互動 TUI 是候選，
// 其餘（含 alternate screen 的 `session list`、mini、run、serve、一般 shell）都排除。
func TestClassifyPaneRecognisesInteractiveTUIOnly(t *testing.T) {
	panes := loadPanes(t)
	cases := []struct {
		name string
		want bool
	}{
		{"tui", true},
		{"tuidir", true},
		{"tuisess", true},
		{"tuispace", true},
		{"standalone", true},
		{"mini", false},
		{"run", false},
		{"serve", false},
		{"sessls", false},
		{"idle", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := panes[c.name]
			if !ok {
				t.Fatalf("fixture 缺少 pane %q", c.name)
			}
			if got := contract.IsTUICandidate(p); got != c.want {
				t.Fatalf("IsTUICandidate(%s) = %v, want %v（argv=%v alt=%v）", c.name, got, c.want, p.ForegroundArgv, p.AlternateOn)
			}
		})
	}
}
