package sidebar

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

// ModeOption 是插件模式的 tmux 全域使用者選項（插件專屬前綴，不與其他選項共用）。
// 使用者在 tmux.conf 設定 `set -g @opencode-status-mode global`，重載設定即套用。
const ModeOption = "@opencode-status-mode"

// ModeSetting 是模式選項目前在 tmux 中的原始狀態。Set 為 false 代表選項不存在（未設定）；
// 存在但值不是 cwd／global（包含空字串）則是無效設定，兩者必須分開處理。
type ModeSetting struct {
	Value string
	Set   bool
}

// ModeSource 讀取目前的模式設定；讀取失敗必須回報錯誤，不得當成「未設定」。
type ModeSource interface {
	Setting() (ModeSetting, error)
}

type unsetMode struct{}

func (unsetMode) Setting() (ModeSetting, error) { return ModeSetting{}, nil }

// resolveMode 套用設定語意：未設定＝預設 cwd；有效值＝套用；無效或讀不到＝
// 已有有效模式就保留它並回報未套用，從未有過有效模式則不開始追蹤。
// 回傳目前生效的模式（ok=false 代表尚未開始追蹤）與要呈現的提示。
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
	jump string // 跳轉目標（pane 編號的十進位字串）；空字串表示沒有
	tabs []contract.Tab
}

// groupsFor 依模式把執行中 TUI 的資料轉成群組：cwd 每個 TUI 目錄一組；
// global 只有一個 GLOBAL 群組（有 TUI 才納入），兩種來源從不合併。
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

// displayNames 為目前納入的目錄決定顯示名稱：預設最後一段，同名者逐層補上父目錄，
// 直到彼此可以區分。只是顯示用；目錄身分與排序一律用完整路徑，不因名稱合併。
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
			return dirs[i] // 根目錄 "/"
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

// jumpTargets 收集 TUI 所在的 pane 編號，算出各群組的跳轉目標：cwd 群組取該目錄 TUI 中編號最小者，
// global 群組取全部 TUI 中最小者。編號以數字比較（%3 先於 %13）；無法解析成編號的 pane id 不參與。
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

// forDir 是該目錄群組的跳轉目標；沒有可解析的 pane 編號時回傳空字串（不加 range）。
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

// paneNumber 解析 tmux pane id（%N）：必須是 % 加純十進位數字，不接受正負號、空白或其他字元。
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
