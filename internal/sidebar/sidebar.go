// Package sidebar 是常駐程式的核心：從 tmux（pane 事實）與 TUI 保存的 tab 清單
// 彙整出側欄內容，只在可見輸出改變時寫到 tmux。外部資料來源與輸出都是介面，
// 測試以受控實作替換，後續票（狀態、global 模式、排版、生命週期）在此擴充。
package sidebar

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/heightfit"
	"github.com/DachiChang/tmux-opencode-status/internal/ocsrc"
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

	// Wake 是事件通知（tmux hook、tab 檔變動）；Ticks 是低頻補漏。兩者皆可為 nil，
	// 時間由呼叫端注入，測試因此不需等待真實時間。
	Wake  <-chan struct{}
	Ticks <-chan time.Time

	// Service 是 OpenCode 本機共用服務的唯讀連線來源；nil 表示不接服務，所有 tab 維持未知。
	Service ocsrc.Dialer
}

type Config struct {
	Width    int           // side-status-width，內容寬度為 Width-1（一格是分隔線）
	Debounce time.Duration // 收到通知後等多久合併同時發生的變動
	Retry    time.Duration // 服務連線失敗後的重試間隔（預設 DefaultRetry）
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
	havePanes bool
	last      string // 上次寫出內容的指紋
	havePub   bool
	notice    string // 最近一次 Sync 的來源讀取提示

	live     *live
	liveWake chan struct{} // 服務事件／狀態變化通知（只需重新排版，不必重讀 tmux）
	ctx      context.Context
}

func New(cfg Config, deps Deps) *Daemon {
	if cfg.Width == 0 {
		cfg.Width = 36
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = DefaultDebounce
	}
	d := &Daemon{cfg: cfg, deps: deps, liveWake: make(chan struct{}, 1)}
	if deps.Service != nil {
		d.live = newLive(deps.Service, cfg.Retry, func() {
			select {
			case d.liveWake <- struct{}{}:
			default:
			}
		})
	}
	return d
}

// Sync 讀取一次外部資料並更新側欄；內容與上次寫出的相同時不寫入。
// 任何來源讀取失敗都不會被當成空清單：沿用最後可用資料並顯示提示，同時回傳錯誤。
func (d *Daemon) Sync() error {
	var errs []error
	notice := ""
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
	d.notice = notice
	if err := d.render(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// render 以最後可用資料與服務狀態重新排版並寫出（內容沒變就不寫）。
func (d *Daemon) render() error {
	view := d.view()
	view.Notice = d.notice
	if d.live != nil && d.ctx != nil {
		var ids []string
		for _, g := range view.Groups {
			for _, t := range g.Tabs {
				ids = append(ids, t.Session)
			}
		}
		d.live.Track(d.ctx, ids)
		if view.Notice == "" && len(ids) > 0 && d.live.Down() {
			view.Notice = "無法連線 OpenCode 服務"
		}
	}
	return d.publish(view)
}

// stateOf 是 tab 目前的狀態；未接服務或基準資料不完整時為未知，不猜測閒置。
func (d *Daemon) stateOf(session string) heightfit.State {
	if d.live == nil {
		return heightfit.Unknown
	}
	return d.live.State(session)
}

// view 由最後可用資料組出側欄內容。從未讀到成功資料的來源沒有任何內容可顯示，
// 此時不輸出「沒有 TUI／無 tab」，只留提示。
func (d *Daemon) view() heightfit.View {
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
	for _, dir := range names {
		g := heightfit.Group{Name: filepath.Base(dir)}
		for _, t := range d.tabs.Select(contract.ModeCwd, dir) {
			g.Tabs = append(g.Tabs, heightfit.Tab{Title: t.Title, Session: t.SessionID, State: d.stateOf(t.SessionID)})
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
	d.ctx = ctx
	_ = d.Sync()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-d.liveWake:
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(d.cfg.Debounce):
			}
			_ = d.render()
			continue
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
