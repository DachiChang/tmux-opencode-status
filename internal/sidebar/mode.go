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
		err = fmt.Errorf("讀取模式設定：%w", rerr)
		notice = "無法讀取模式設定"
	case !s.Set:
		d.mode, d.haveMode = contract.ModeCwd, true
		return d.mode, true, "", nil
	case s.Value == string(contract.ModeCwd) || s.Value == string(contract.ModeGlobal):
		d.mode, d.haveMode = contract.Mode(s.Value), true
		return d.mode, true, "", nil
	default:
		err = fmt.Errorf("%s 的值 %q 無效（只接受 cwd 或 global）", ModeOption, s.Value)
		notice = "模式設定無效"
	}
	if d.haveMode {
		return d.mode, true, notice + "，未套用，仍用 " + string(d.mode), err
	}
	return "", false, notice + "，尚未開始追蹤", err
}

type tabGroup struct {
	name string
	dir  string // cwd 群組的目錄；global 群組為空（跳轉目標取全部 TUI）
	tabs []contract.Tab
}

// groupsFor 依模式把執行中 TUI 的資料轉成群組：cwd 每個 TUI 目錄一組；
// global 只有一個 GLOBAL 群組（有 TUI 才納入），兩種來源從不合併。
func (d *Daemon) groupsFor(mode contract.Mode, tuiDirs []string) []tabGroup {
	if len(tuiDirs) == 0 {
		return nil
	}
	if mode == contract.ModeGlobal {
		return []tabGroup{{name: "GLOBAL", tabs: d.tabs.Select(contract.ModeGlobal, "")}}
	}
	names := displayNames(tuiDirs)
	groups := make([]tabGroup, 0, len(tuiDirs))
	for i, dir := range tuiDirs {
		groups = append(groups, tabGroup{name: names[i], dir: dir, tabs: d.tabs.Select(contract.ModeCwd, dir)})
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

// jumpTarget 是群組的跳轉目標：cwd 群組取該目錄 TUI 中 pane 編號最小者，global 群組（dir 為空）取全部 TUI 中最小者。
// 編號以數字比較（%3 先於 %13）；無法解析成編號的 pane id 不參與，全部無法解析時回傳空字串（不加 range）。
func jumpTarget(dir string, byDir map[string][]string, all []string) string {
	ids := all
	if dir != "" {
		ids = byDir[dir]
	}
	best := -1
	for _, id := range ids {
		n, err := strconv.Atoi(strings.TrimPrefix(id, "%"))
		if err != nil || n < 0 || !strings.HasPrefix(id, "%") {
			continue
		}
		if best < 0 || n < best {
			best = n
		}
	}
	if best < 0 {
		return ""
	}
	return strconv.Itoa(best)
}
