package contract

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Mode 是插件模式：由使用者明確選擇，不讀取各 TUI 的環境覆寫。
type Mode string

const (
	ModeCwd    Mode = "cwd"
	ModeGlobal Mode = "global"
)

// Tab 是 TUI 保存的一個 tab。Title 只是備援；即時標題以 session API 為準。
type Tab struct {
	SessionID string `json:"sessionID"`
	Title     string `json:"title"`
}

type tabList struct {
	Tabs []Tab `json:"tabs"`
}

// TabsFile 是 `$XDG_STATE_HOME/opencode/latest/tui/tabs.json` 的內容。
// 格式為 OpenCode 非公開持久化格式：{"global":{tabs,unread},"cwd":{"<目錄>":{tabs,unread}}}。
type TabsFile struct {
	Global tabList            `json:"global"`
	Cwd    map[string]tabList `json:"cwd"`
}

// ParseTabs 解析並嚴格驗證結構；任何不相容都回報錯誤（呼叫端必須保留最後可用清單，
// 並與「有 TUI 但無 tab」區分）。
func ParseTabs(raw []byte) (*TabsFile, error) {
	var probe struct {
		Global *json.RawMessage `json:"global"`
		Cwd    *json.RawMessage `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("tabs.json 不是有效 JSON：%w", err)
	}
	if probe.Global == nil || probe.Cwd == nil {
		return nil, errors.New("tabs.json 缺少 global 或 cwd")
	}
	var f TabsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("tabs.json 結構不相容：%w", err)
	}
	check := func(where string, l tabList) error {
		for i, t := range l.Tabs {
			if t.SessionID == "" {
				return fmt.Errorf("tabs.json %s.tabs[%d] 缺少 sessionID", where, i)
			}
		}
		return nil
	}
	if err := check("global", f.Global); err != nil {
		return nil, err
	}
	for dir, l := range f.Cwd {
		if err := check("cwd["+dir+"]", l); err != nil {
			return nil, err
		}
	}
	return &f, nil
}

// Select 依插件模式取得 tab 清單。cwd 模式以 TUI 行程的實際工作目錄做字串精確比對
// （目錄身分沿用 OpenCode，不正規化）；global 模式忽略 dir。
func (f *TabsFile) Select(mode Mode, dir string) []Tab {
	if mode == ModeGlobal {
		return f.Global.Tabs
	}
	return f.Cwd[dir].Tabs
}

// EmptyTabs 是尚未有任何 TUI 保存過 tab 的清單（tabs.json 不存在的全新環境）。
func EmptyTabs() *TabsFile { return &TabsFile{Cwd: map[string]tabList{}} }
