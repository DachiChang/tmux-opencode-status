// Package tabsrc 讀取並監看 OpenCode TUI 保存的 tab 清單（唯讀）。
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

// DefaultPath 是標準 latest channel 的預設資料位置。
func DefaultPath() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "opencode", "latest", "tui", "tabs.json")
}

type Source struct{ Path string }

// Read 解析 tab 清單。檔案不存在代表尚無任何 TUI 保存過 tab（全新環境），視為空清單；
// 其他讀取或格式錯誤都回報錯誤，呼叫端不得當成空清單。
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

// Watch 監看檔案所在目錄（OpenCode 以 tmp 檔 rename 更新，inode 每次都變，不能監看檔案本身），
// 目錄內任何變動就送出一次通知。目錄暫時不存在時定期重試。內容是否真的改變由呼叫端判斷。
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
			// watcher 裝好之前的變動不會有事件；就緒後補一次喚醒（目錄稍後才出現而重試成功時亦同），
			// 讓啟動空窗內的變動不必等低頻補漏。
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
