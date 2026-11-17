// Package contract 以 OpenCode v2.3.0／tmux next-3.9 的實測契約為準，
// 提供後續實作可直接沿用的判斷函式；同時是契約回歸的驗證入口。
package contract

import "path/filepath"

// Pane 是一個 tmux pane 的辨識事實。ForegroundArgv／ForegroundCwd 來自該 pane tty
// 的前景行程組（pgid == tpgid），不是 pane_pid（那通常是 shell）。
type Pane struct {
	Name               string   `json:"name"`
	PaneCurrentCommand string   `json:"paneCurrentCommand"`
	AlternateOn        bool     `json:"alternateOn"`
	PaneCurrentPath    string   `json:"paneCurrentPath"`
	ForegroundArgv     []string `json:"foregroundArgv"`
	ForegroundCwd      string   `json:"foregroundCwd"`
}

// opencodeSubcommands 是 `opencode --help` 列出的子命令；出現在位置參數的第一個
// 非旗標值時代表非互動 TUI（mini 雖然互動，但不使用 TUI 的 tab 清單）。
var opencodeSubcommands = map[string]bool{
	"upgrade": true, "update": true, "uninstall": true, "acp": true, "api": true,
	"debug": true, "auth": true, "mcp": true, "plugin": true, "models": true,
	"stats": true, "mini": true, "run": true, "session": true, "service": true,
	"reload": true, "pair": true, "serve": true,
}

// 會吃下一個 argv 作為值的旗標（`opencode --help` 的 FLAGS）。
var valueFlags = map[string]bool{
	"--server": true, "--session": true, "-s": true, "--prompt": true,
	"--log-level": true, "--completions": true,
}

// IsTUICandidate 判斷 pane 的前景行程是否為 OpenCode 互動 TUI 候選：
// 可執行檔 basename 為 opencode，且 argv 沒有指向子命令，也沒有 --help／--version。
// 不使用 pane title、不掃畫面、也不以 alternate screen 當必要條件
// （`opencode session list` 的 pager 也會進 alternate screen）。
func IsTUICandidate(p Pane) bool {
	if len(p.ForegroundArgv) == 0 || filepath.Base(p.ForegroundArgv[0]) != "opencode" {
		return false
	}
	args := p.ForegroundArgv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help" || a == "-h" || a == "--version" || a == "-v" || a == "--wizard":
			return false
		case valueFlags[a]:
			i++
		case len(a) > 0 && a[0] == '-':
			// 其餘旗標（--standalone、--auto、--continue、--print-logs…）不帶值。
		default:
			return !opencodeSubcommands[a]
		}
	}
	return true
}
