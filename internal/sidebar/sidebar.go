// Package sidebar 是常駐程式的核心：從 tmux（pane 事實）與 TUI 保存的 tab 清單
// 彙整出側欄內容，只在可見輸出改變時寫到 tmux。外部資料來源與輸出都是介面，
// 測試以受控實作替換，後續票（狀態、global 模式、排版、生命週期）在此擴充。
package sidebar

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/heightfit"
)

// PaneFact 是一個 tmux pane 的辨識事實（PaneID 用於去除 linked window 的重複列出）。
type PaneFact struct {
	PaneID string
	contract.Pane
}

// PaneSource 列出同一 tmux server 所有 session 的 pane。失敗必須回報錯誤，
// 不得回傳空清單代替。
type PaneSource interface {
	Panes() ([]PaneFact, error)
}

// TabSource 讀取 TUI 保存的 tab 清單；格式不相容時回報錯誤。
type TabSource interface {
	Read() (*contract.TabsFile, error)
}

// Output 把已排好版的側欄寫到 tmux。
type Output interface {
	Publish(heightfit.Published) error
}

type Deps struct {
	Panes PaneSource
	Tabs  TabSource
	Out   Output
	// Mode 讀取插件模式選項；nil 等同未設定（預設 cwd）。
	Mode ModeSource

	// Wake 是事件通知（tmux hook、tab 檔變動）；Ticks 是低頻補漏。兩者皆可為 nil，
	// 時間由呼叫端注入，測試因此不需等待真實時間。
	Wake  <-chan struct{}
	Ticks <-chan time.Time
}

type Config struct {
	Width    int           // side-status-width，內容寬度為 Width-1（一格是分隔線）
	Debounce time.Duration // 收到通知後等多久合併同時發生的變動
}

// 時間預算（spec 驗收目標：一般變動 1 秒、漏通知／強制關閉 10 秒內修正）。
const (
	DefaultDebounce = 100 * time.Millisecond
	DefaultBackstop = 5 * time.Second
)

type Daemon struct {
	cfg  Config
	deps Deps

	panes     []PaneFact // 最後一次成功讀到的資料；讀取失敗時沿用並提示
	tabs      *contract.TabsFile
	mode      contract.Mode // 最後一次生效的有效模式
	haveMode  bool
	havePanes bool
	last      string // 上次寫出內容的指紋
	havePub   bool
}

func New(cfg Config, deps Deps) *Daemon {
	if cfg.Width == 0 {
		cfg.Width = 36
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = DefaultDebounce
	}
	if deps.Mode == nil {
		deps.Mode = unsetMode{}
	}
	return &Daemon{cfg: cfg, deps: deps}
}

// Sync 讀取一次外部資料並更新側欄；內容與上次寫出的相同時不寫入。
// 任何來源讀取失敗都不會被當成空清單：沿用最後可用資料並顯示提示，同時回傳錯誤。
func (d *Daemon) Sync() error {
	var errs []error
	notice := ""
	mode, tracking, modeNotice, modeErr := d.resolveMode()
	if modeErr != nil {
		errs = append(errs, modeErr)
	}
	if !tracking {
		// 首次就沒有有效模式：不開始追蹤（不讀 tmux／tab 清單、不顯示空狀態），只留提示。
		if err := d.publish(heightfit.View{Notice: modeNotice}); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	if panes, err := d.deps.Panes.Panes(); err != nil {
		errs = append(errs, fmt.Errorf("讀取 tmux pane：%w", err))
		notice = "無法讀取 tmux，顯示最後可用資料"
	} else {
		d.panes, d.havePanes = panes, true
	}
	if tabs, err := d.deps.Tabs.Read(); err != nil {
		errs = append(errs, fmt.Errorf("讀取 tab 清單：%w", err))
		if notice == "" {
			notice = "無法讀取 tab 清單，顯示最後可用資料"
		}
	} else {
		d.tabs = tabs
	}
	view := d.view(mode)
	view.Notice = notice
	if modeNotice != "" {
		view.Notice = modeNotice // 設定錯誤比資料讀取提示更需要使用者處理
	}
	if err := d.publish(view); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// view 由最後可用資料組出側欄內容。從未讀到成功資料的來源沒有任何內容可顯示，
// 此時不輸出「沒有 TUI／無 tab」，只留提示。
func (d *Daemon) view(mode contract.Mode) heightfit.View {
	if !d.havePanes || d.tabs == nil {
		return heightfit.View{}
	}
	dirs := map[string]bool{}
	for _, p := range d.panes {
		if contract.IsTUICandidate(p.Pane) {
			dirs[p.ForegroundCwd] = true
		}
	}
	var names []string
	for dir := range dirs {
		names = append(names, dir)
	}
	sort.Strings(names)
	view := heightfit.View{Empty: "沒有 OpenCode TUI"}
	for _, tg := range d.groupsFor(mode, names) {
		g := heightfit.Group{Name: tg.name}
		for _, t := range tg.tabs {
			g.Tabs = append(g.Tabs, heightfit.Tab{Title: t.Title, State: heightfit.Unknown})
		}
		view.Groups = append(view.Groups, g)
	}
	return view
}

func (d *Daemon) publish(v heightfit.View) error {
	p := heightfit.PublishView(v, d.cfg.Width-1)
	fp := fingerprint(p)
	if d.havePub && fp == d.last {
		return nil
	}
	if err := d.deps.Out.Publish(p); err != nil {
		return fmt.Errorf("寫入 tmux：%w", err)
	}
	d.last, d.havePub = fp, true
	return nil
}

func fingerprint(p heightfit.Published) string {
	names := make([]string, 0, len(p.Options))
	for n := range p.Options {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + "\x00" + p.Options[n] + "\x00")
	}
	return b.String() + p.Format()
}

// Run 先同步一次，之後每次事件通知（合併 Debounce 內的連續通知）或補漏 tick 同步一次，
// 直到 ctx 結束。同步失敗不會中止：已經顯示提示，下一次通知或 tick 會重試。
func (d *Daemon) Run(ctx context.Context) error {
	_ = d.Sync()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-d.deps.Ticks:
		case <-d.deps.Wake:
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(d.cfg.Debounce):
			}
		}
		_ = d.Sync()
	}
}
