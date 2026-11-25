package sidebar

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

// ModeOption is the plugin mode's tmux global user option (plugin-specific prefix, not shared with other options).
// Users set `set -g @opencode-status-mode global` in tmux.conf, and reloading the configuration applies it.
const ModeOption = "@opencode-status-mode"

// ModeSetting is the raw state of the mode option in tmux. Set false means the option does not exist (unset);
// an option that exists but whose value is not cwd/global (including the empty string) is an invalid setting. The two must be handled separately.
type ModeSetting struct {
	Value string
	Set   bool
}

// ModeSource reads the current mode setting; a read failure must be reported as an error and must not be treated as "unset".
type ModeSource interface {
	Setting() (ModeSetting, error)
}

type unsetMode struct{}

func (unsetMode) Setting() (ModeSetting, error) { return ModeSetting{}, nil }

// resolveMode applies the setting semantics: unset = default cwd; valid value = apply it; invalid or unreadable =
// keep the existing valid mode and report it as not applied; if there has never been a valid mode, do not start tracking.
// It returns the mode in effect (ok=false means tracking has not started) and the notice to show.
func (d *Daemon) resolveMode() (mode contract.Mode, ok bool, notice string, err error) {
	s, rerr := d.deps.Mode.Setting()
	switch {
	case rerr != nil:
		err = fmt.Errorf("read plugin mode: %w", rerr)
		notice = "Cannot read mode"
	case !s.Set:
		d.mode, d.haveMode = contract.ModeCwd, true
		return d.mode, true, "", nil
	case s.Value == string(contract.ModeCwd) || s.Value == string(contract.ModeGlobal):
		d.mode, d.haveMode = contract.Mode(s.Value), true
		return d.mode, true, "", nil
	default:
		err = fmt.Errorf("invalid value %q for %s (expected cwd or global)", s.Value, ModeOption)
		notice = "Invalid mode"
	}
	if d.haveMode {
		return d.mode, true, notice + "; using " + string(d.mode), err
	}
	return "", false, notice + "; not tracking", err
}

type tabGroup struct {
	name string
	jump string // jump target (decimal string of the pane number); empty string means none
	tabs []contract.Tab
}

// groupsFor turns the running TUI data into groups by mode: cwd has one group per TUI directory;
// global has a single GLOBAL group (included only when a TUI exists), and the two sources are never merged.
func (d *Daemon) groupsFor(mode contract.Mode, tuiDirs []string, jumps jumpTargets) []tabGroup {
	if len(tuiDirs) == 0 {
		return nil
	}
	if mode == contract.ModeGlobal {
		return []tabGroup{{name: "GLOBAL", jump: jumps.global(), tabs: d.tabs.Select(contract.ModeGlobal, "")}}
	}
	names := displayNames(tuiDirs)
	groups := make([]tabGroup, 0, len(tuiDirs))
	for i, dir := range tuiDirs {
		groups = append(groups, tabGroup{name: names[i], jump: jumps.forDir(dir), tabs: d.tabs.Select(contract.ModeCwd, dir)})
	}
	return groups
}

// displayNames decides display names for the directories currently included: the last path segment by default, with parent directories added level by level for duplicates
// until they are distinguishable. This is for display only; directory identity and ordering always use the full path and are never merged by name.
func displayNames(dirs []string) []string {
	segs := make([][]string, len(dirs))
	depth := make([]int, len(dirs))
	for i, dir := range dirs {
		for _, p := range strings.Split(dir, "/") {
			if p != "" {
				segs[i] = append(segs[i], p)
			}
		}
		depth[i] = 1
	}
	name := func(i int) string {
		if len(segs[i]) == 0 {
			return dirs[i] // root directory "/"
		}
		return strings.Join(segs[i][max(len(segs[i])-depth[i], 0):], "/")
	}
	for {
		count := map[string]int{}
		for i := range dirs {
			count[name(i)]++
		}
		grew := false
		for i := range dirs {
			if count[name(i)] > 1 && depth[i] < len(segs[i]) {
				depth[i]++
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	out := make([]string, len(dirs))
	for i := range dirs {
		out[i] = name(i)
	}
	return out
}

// jumpTargets collects the pane numbers hosting TUIs and computes each group's jump target: a cwd group takes the lowest-numbered TUI in that directory,
// and the global group takes the lowest among all TUIs. Numbers are compared numerically (%3 before %13); pane ids that cannot be parsed as numbers do not participate.
type jumpTargets struct {
	byDir map[string]uint64
	all   uint64
	any   bool
}

func (j *jumpTargets) add(dir, paneID string) {
	n, ok := paneNumber(paneID)
	if !ok {
		return
	}
	if j.byDir == nil {
		j.byDir = map[string]uint64{}
	}
	if cur, seen := j.byDir[dir]; !seen || n < cur {
		j.byDir[dir] = n
	}
	if !j.any || n < j.all {
		j.all, j.any = n, true
	}
}

// forDir is the jump target of that directory group; it returns an empty string (no range added) when there is no parsable pane number.
func (j jumpTargets) forDir(dir string) string {
	if n, ok := j.byDir[dir]; ok {
		return strconv.FormatUint(n, 10)
	}
	return ""
}

func (j jumpTargets) global() string {
	if j.any {
		return strconv.FormatUint(j.all, 10)
	}
	return ""
}

// paneNumber parses a tmux pane id (%N): it must be % followed by plain decimal digits, and signs, whitespace, or other characters are not accepted.
func paneNumber(id string) (uint64, bool) {
	digits, ok := strings.CutPrefix(id, "%")
	if !ok || digits == "" {
		return 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	return n, err == nil
}
