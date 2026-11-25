// Package tabsrc reads and watches the tab list persisted by the OpenCode TUI (read-only).
package tabsrc

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

// DefaultPath is the default data location for the standard latest channel.
func DefaultPath() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "opencode", "latest", "tui", "tabs.json")
}

type Source struct{ Path string }

// Read parses the tab list. A missing file means no TUI has ever persisted tabs (fresh
// environment) and is treated as an empty list;
// any other read or format error is returned, and callers must not treat it as an empty list.
func (s Source) Read() (*contract.TabsFile, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return contract.EmptyTabs(), nil
	}
	if err != nil {
		return nil, err
	}
	return contract.ParseTabs(raw)
}

// Watch watches the directory containing the file (OpenCode updates it by renaming a tmp
// file, so the inode changes every time and the file itself cannot be watched) and sends one
// notification on any change in the directory. It retries periodically while the directory
// is missing. Callers decide whether the content actually changed.
func (s Source) Watch(ctx context.Context, wake chan<- struct{}) {
	dir := filepath.Dir(s.Path)
	for ctx.Err() == nil {
		w, err := fsnotify.NewWatcher()
		if err == nil {
			err = w.Add(dir)
		}
		if err != nil {
			if w != nil {
				w.Close()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
		func() {
			defer w.Close()
			// Changes before the watcher was installed produce no events; send one wake once ready
			// (including when a retry succeeds after the directory appears later),
			// so changes during the startup gap need not wait for the low-frequency catch-up.
			select {
			case wake <- struct{}{}:
			default:
			}
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-w.Events:
					if !ok {
						return
					}
					select {
					case wake <- struct{}{}:
					default:
					}
				case <-w.Errors:
				}
			}
		}()
	}
}
