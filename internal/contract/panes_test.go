package contract_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

// Pane facts from real tmux next-3.9 + opencode v2.3.0: a pane hosts a TUI client when its foreground command is opencode
// and it is on the alternate screen. mini, run, serve, and a plain shell are excluded; the `session list` pager is an
// accepted false positive (ADR-0006).
func TestRecordedTmuxPaneFacts(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/opencode-2.3.0/panes.json")
	if err != nil {
		t.Fatal(err)
	}
	var panes []struct {
		contract.Pane
		ForegroundCwd string `json:"foregroundCwd"`
	}
	if err := json.Unmarshal(raw, &panes); err != nil {
		t.Fatal(err)
	}
	byName := map[string]contract.Pane{}
	for _, p := range panes {
		byName[p.Name] = p.Pane
	}

	t.Run("TUI client classification", func(t *testing.T) {
		cases := []struct {
			name string
			want bool
		}{
			{"tui", true},
			{"tuidir", true},
			{"tuisess", true},
			{"tuispace", true},
			{"standalone", true},
			{"sessls", true},
			{"mini", false},
			{"run", false},
			{"serve", false},
			{"idle", false},
			{"zsh", false},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				p, ok := byName[c.name]
				if !ok {
					t.Fatalf("fixture is missing pane %q", c.name)
				}
				if got := contract.IsTUIClient(p); got != c.want {
					t.Fatalf("IsTUIClient(%s) = %v, want %v (cmd=%q alt=%v)", c.name, got, c.want, p.PaneCurrentCommand, p.AlternateOn)
				}
			})
		}
	})

	// lsof independently recorded the foreground process's cwd, which tmux should report as pane_current_path.
	t.Run("directory identity", func(t *testing.T) {
		for _, p := range panes {
			if p.PaneCurrentPath == "" || p.PaneCurrentPath != p.ForegroundCwd {
				t.Errorf("%s: pane_current_path = %q, want a nonempty path matching the recorded foreground cwd %q", p.Name, p.PaneCurrentPath, p.ForegroundCwd)
			}
		}
	})
}

func TestIsTUIClientRequiresBothCommandAndAlternateScreen(t *testing.T) {
	cases := []struct {
		name string
		pane contract.Pane
		want bool
	}{
		{"opencode on the main screen (just launched, or a non-TUI command)", contract.Pane{PaneCurrentCommand: "opencode"}, false},
		{"shell left on the alternate screen after a killed TUI", contract.Pane{PaneCurrentCommand: "zsh", AlternateOn: true}, false},
		{"another full-screen program", contract.Pane{PaneCurrentCommand: "nvim", AlternateOn: true}, false},
		{"opencode on the alternate screen", contract.Pane{PaneCurrentCommand: "opencode", AlternateOn: true}, true},
	}
	for _, c := range cases {
		if got := contract.IsTUIClient(c.pane); got != c.want {
			t.Errorf("%s: IsTUIClient = %v, want %v", c.name, got, c.want)
		}
	}
}
