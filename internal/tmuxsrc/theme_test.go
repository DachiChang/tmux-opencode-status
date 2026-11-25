package tmuxsrc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func isolated(t *testing.T) *Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := fmt.Sprintf("tos-theme-%d-%d", os.Getpid(), time.Now().UnixNano()%100000)
	c := &Client{SocketArgs: []string{"-L", sock}, ConfigArgs: []string{"-f", "/dev/null"}}
	if _, err := c.tmux("", "new-session", "-d", "-s", "t", "-x", "80", "-y", "24"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = c.tmux("", "kill-server") })
	return c
}

func TestSwatchesReadsOnlyThePluginPrefixedOptionsWithRawValues(t *testing.T) {
	c := isolated(t)
	set := func(name, v string) {
		t.Helper()
		if _, err := c.tmux("", "set-option", "-g", name, v); err != nil {
			t.Fatal(err)
		}
	}
	set("@opencode-status-color-project", "colour33")
	set("@opencode-status-color-emphasis", "#112233")
	set("@opencode-status-color-notice", "")
	set("@opencode-status-color-split", "a]b,c #{pane_id} \"q\" \\z")
	set("@opencode-status-color-title", "red\n@opencode-status-color-bogus red")
	set("@opencode-status-color-state-idle", " red ")
	set("@opencode-status-mode", "global")
	set("@opencode-status-colour-x", "red")
	set("@other", "red")
	got, err := c.Swatches()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"project":    "colour33",
		"emphasis":   "#112233",
		"notice":     "",
		"split":      "a]b,c #{pane_id} \"q\" \\z",
		"title":      "red\n@opencode-status-color-bogus red",
		"state-idle": " red ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raw values do not match the prefix filter\n got: %q\nwant: %q", got, want)
	}
}

func TestSwatchesWithNoneSetIsEmptyAndUnsetIsGone(t *testing.T) {
	c := isolated(t)
	got, err := c.Swatches()
	if err != nil || len(got) != 0 {
		t.Fatalf("unset should be empty: %v %v", got, err)
	}
	_, _ = c.tmux("", "set-option", "-g", "@opencode-status-color-project", "red")
	_, _ = c.tmux("", "set-option", "-gu", "@opencode-status-color-project")
	if got, err := c.Swatches(); err != nil || len(got) != 0 {
		t.Fatalf("should be empty after unsetting: %v %v", got, err)
	}
}

func TestSwatchesReportsAnUnreachableServerAsAnError(t *testing.T) {
	c := isolated(t)
	_, _ = c.tmux("", "kill-server")
	if got, err := c.Swatches(); err == nil {
		t.Fatalf("must report an error when the server is gone instead of treating everything as unset: %v", got)
	}
}

func TestSwatchesIsReadOnly(t *testing.T) {
	c := isolated(t)
	_, _ = c.tmux("", "set-option", "-g", "@opencode-status-color-project", "red")
	before, _ := c.tmux("", "show-options", "-g")
	if _, err := c.Swatches(); err != nil {
		t.Fatal(err)
	}
	after, _ := c.tmux("", "show-options", "-g")
	if string(before) != string(after) {
		t.Fatal("reading must not change any option")
	}
}

// Installs the same after-set-option hook command as production, then checks whether each option change leaves a wake-up signal
// (tmux remembers a signal sent while nobody is waiting, so a later wait-for returns immediately).
func TestAfterSetOptionHookWakesForModeAndEverySwatchButNotOtherOptions(t *testing.T) {
	c := isolated(t)
	if _, err := c.tmux("", "set-hook", "-g", optionHook+"[41700]", optionHookCmd()); err != nil {
		t.Fatal(err)
	}
	woken := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		args := append(append(append([]string{}, c.SocketArgs...), c.ConfigArgs...), "wait-for", wakeChannel)
		return exec.CommandContext(ctx, "tmux", args...).Run() == nil
	}
	for _, name := range []string{"@opencode-status-mode", "@opencode-status-color-project", "@opencode-status-color-state-ask", "@opencode-status-color-whatever"} {
		if _, err := c.tmux("", "set-option", "-g", name, "x"); err != nil {
			t.Fatal(err)
		}
		if !woken() {
			t.Errorf("setting %s should wake", name)
		}
		if _, err := c.tmux("", "set-option", "-gu", name); err != nil {
			t.Fatal(err)
		}
		if !woken() {
			t.Errorf("unsetting %s should wake", name)
		}
	}
	for _, name := range []string{"@other", "@opencode-status-colour-x", "@tos_layout_5", "@opencode-status-shown", "status-left"} {
		if _, err := c.tmux("", "set-option", "-g", name, "x"); err != nil {
			t.Fatal(err)
		}
		if woken() {
			t.Errorf("setting %s should not wake (avoids feedback from the daemon's own writes)", name)
		}
	}
}
