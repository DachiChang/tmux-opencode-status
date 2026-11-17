package sidebar

import (
	"fmt"
	"path/filepath"

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
	groups := make([]tabGroup, 0, len(tuiDirs))
	for _, dir := range tuiDirs {
		groups = append(groups, tabGroup{name: filepath.Base(dir), tabs: d.tabs.Select(contract.ModeCwd, dir)})
	}
	return groups
}
