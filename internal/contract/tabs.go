package contract

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Mode is the plugin mode: explicitly chosen by the user, without reading each TUI's environment overrides.
type Mode string

const (
	ModeCwd    Mode = "cwd"
	ModeGlobal Mode = "global"
)

// Tab is one tab persisted by the TUI. Title is only a fallback; the live title comes from the session API.
type Tab struct {
	SessionID string `json:"sessionID"`
	Title     string `json:"title"`
}

type tabList struct {
	Tabs []Tab `json:"tabs"`
}

// TabsFile is the content of `$XDG_STATE_HOME/opencode/latest/tui/tabs.json`.
// The format is OpenCode's non-public persistence format: {"global":{tabs,unread},"cwd":{"<directory>":{tabs,unread}}}.
type TabsFile struct {
	Global tabList            `json:"global"`
	Cwd    map[string]tabList `json:"cwd"`
}

// ParseTabs parses and strictly validates the structure; any incompatibility is an error (callers must keep the last usable list,
// and distinguish it from "a TUI exists but has no tabs").
func ParseTabs(raw []byte) (*TabsFile, error) {
	var probe struct {
		Global *json.RawMessage `json:"global"`
		Cwd    *json.RawMessage `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("tabs.json is not valid JSON: %w", err)
	}
	if probe.Global == nil || probe.Cwd == nil {
		return nil, errors.New("tabs.json is missing global or cwd")
	}
	var f TabsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("incompatible tabs.json structure: %w", err)
	}
	check := func(where string, l tabList) error {
		for i, t := range l.Tabs {
			if t.SessionID == "" {
				return fmt.Errorf("tabs.json %s.tabs[%d] is missing sessionID", where, i)
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

// Select gets the tab list for the plugin mode. cwd mode does an exact string comparison with the TUI process's actual working directory
// (directory identity follows OpenCode and is not normalized); global mode ignores dir.
func (f *TabsFile) Select(mode Mode, dir string) []Tab {
	if mode == ModeGlobal {
		return f.Global.Tabs
	}
	return f.Cwd[dir].Tabs
}

// EmptyTabs is the list for when no TUI has ever persisted tabs (a fresh environment where tabs.json does not exist).
func EmptyTabs() *TabsFile { return &TabsFile{Cwd: map[string]tabList{}} }
