package sidebar

import (
	"fmt"

	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// ThemeOptionPrefix 是色票的 tmux 全域使用者選項前綴（插件專屬，不與其他選項共用）；
// 完整選項名稱為前綴加色票名稱，例如 `set -g @opencode-status-color-project colour33`。
const ThemeOptionPrefix = "@opencode-status-color-"

// ThemeSource 讀取使用者設定的色票原始值：鍵是色票名稱（不含前綴），只含有設定的項目。
// 讀取失敗必須回報錯誤，不得當成全部未設定。
type ThemeSource interface {
	Swatches() (map[string]string, error)
}

type unsetTheme struct{}

func (unsetTheme) Swatches() (map[string]string, error) { return nil, nil }

// resolveTheme 讀取並套用色票：成功就以新主題為準（無效項退回預設，不影響其他項）；
// 讀取失敗沿用上次有效的主題，從未讀取成功則維持預設主題。回傳讀取錯誤供呼叫端記錄。
func (d *Daemon) resolveTheme() error {
	raw, err := d.deps.Theme.Swatches()
	if err != nil {
		return fmt.Errorf("read theme settings: %w", err)
	}
	d.theme, _ = render.ParseTheme(raw)
	return nil
}
