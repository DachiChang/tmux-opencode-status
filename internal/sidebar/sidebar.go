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
	"github.com/DachiChang/tmux-opencode-status/internal/ocsrc"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
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
	Publish(render.Published) error
}

type Deps struct {
	Panes PaneSource
	Tabs  TabSource
	Out   Output
	// Mode 讀取插件模式選項；nil 等同未設定（預設 cwd）。
	Mode ModeSource
	// Theme 讀取色票選項；nil 等同全部未設定（預設主題）。
	Theme ThemeSource

	// Wake 是事件通知（tmux hook、tab 檔變動）；Ticks 是低頻補漏。兩者皆可為 nil，
	// 時間由呼叫端注入，測試因此不需等待真實時間。
	Wake  <-chan struct{}
	Ticks <-chan time.Time

	// Service 是 OpenCode 本機共用服務的唯讀連線來源；nil 表示不接服務，所有 tab 維持未知。
	Service ocsrc.Dialer

	// Report 在可觀察狀態改變時被呼叫（診斷用）。內容只有計數與固定描述，
	// 不含憑證、prompt、對話、session 或 tab 標題，也不含原始錯誤文字。nil 表示不回報。
	Report func(Status)

	// Startup 是啟動時（Daemon 之外）發生、要記入最近錯誤的問題（固定描述，例如觸發鍵無效）。
	Startup []ErrorRecord
}

// Service 連線狀態的固定描述值。
const (
	ServiceIdle       = "idle"       // 沒有追蹤項目，刻意不連線、不查詢
	ServiceConnected  = "connected"  // 已連上並完成基準讀取
	ServiceConnecting = "connecting" // 正在連線或讀取基準資料
	ServiceDown       = "down"       // 連線失敗或已中斷，自動重試中
	ServiceNone       = "none"       // 未設定服務來源
)

// Status 是診斷用的目前狀態快照。
type Status struct {
	Mode          string // 目前生效的模式（cwd／global）；尚未開始追蹤時為空
	ModeIssue     string // 模式設定問題的固定描述
	Tracking      bool
	TUIs          int
	Tabs          int
	Service       string
	ServiceReason string
	Issues        []string // 目前持續存在的問題（固定描述）
	Recent        []ErrorRecord
}

// ErrorRecord 是一筆最近發生的問題：只有來源與固定描述。
type ErrorRecord struct {
	At      time.Time
	Source  string // tmux／tabs／mode／theme／service／output／jump
	Message string
}

const maxRecent = 8

type Config struct {
	Width    int           // side-status-width，內容寬度為 Width-1（一格是分隔線）
	Debounce time.Duration // 收到通知後等多久合併同時發生的變動
	Retry    time.Duration // 服務連線失敗後的重試間隔（預設 DefaultRetry）
	Resync   time.Duration // 家族補漏重讀週期（預設 DefaultResync）
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
	theme     render.Theme  // 最後一次讀取成功的主題；從未成功時為預設
	mode      contract.Mode // 最後一次生效的有效模式
	haveMode  bool
	havePanes bool
	last      string // 上次寫出內容的指紋
	havePub   bool
	notice    string // 最近一次 Sync 的來源讀取提示

	live     *live
	liveWake chan struct{} // 服務事件／狀態變化通知（只需重新排版，不必重讀 tmux）
	ctx      context.Context

	issues    []string // 最近一次 Sync 的來源問題（固定描述）
	modeIssue string
	recent    []ErrorRecord
	lastRecv  map[string]bool // 目前仍在持續的問題來源，避免同一個持續問題每次同步都記一筆
	lastRep   string

	lastService string
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
	if deps.Theme == nil {
		deps.Theme = unsetTheme{}
	}
	d := &Daemon{cfg: cfg, deps: deps, theme: render.DefaultTheme(), liveWake: make(chan struct{}, 1)}
	d.recent = append(d.recent, deps.Startup...)
	if deps.Service != nil {
		d.live = newLive(deps.Service, cfg.Retry, cfg.Resync, func() {
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
	d.issues = d.issues[:0]
	active := map[string]bool{}
	note := func(source, msg string) {
		d.issues = append(d.issues, msg)
		active[source] = true
		if !d.lastRecv[source] {
			d.recent = append(d.recent, ErrorRecord{At: time.Now(), Source: source, Message: msg})
			if len(d.recent) > maxRecent {
				d.recent = d.recent[len(d.recent)-maxRecent:]
			}
		}
	}
	defer func() { d.lastRecv = active; d.report() }()
	if err := d.resolveTheme(); err != nil {
		errs = append(errs, err)
		note("theme", "Cannot read theme settings; keeping the last valid colors")
	}
	_, tracking, modeNotice, modeErr := d.resolveMode()
	d.modeIssue = ""
	if modeErr != nil {
		errs = append(errs, modeErr)
		d.modeIssue = modeNotice
		note("mode", modeNotice)
	}
	if !tracking {
		// 首次就沒有有效模式：不開始追蹤（不讀 tmux／tab 清單、不顯示空狀態），只留提示。
		if err := d.publish(render.View{Notice: modeNotice}); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	if panes, err := d.deps.Panes.Panes(); err != nil {
		errs = append(errs, fmt.Errorf("read tmux panes: %w", err))
		notice = "Cannot read tmux panes"
		note("tmux", "Cannot read tmux pane list")
	} else {
		d.panes, d.havePanes = panes, true
	}
	if tabs, err := d.deps.Tabs.Read(); err != nil {
		errs = append(errs, fmt.Errorf("read tab list: %w", err))
		note("tabs", "Tab list is unreadable or has an incompatible format")
		if notice == "" {
			notice = "Cannot read tab list"
		}
	} else {
		d.tabs = tabs
	}
	d.notice = notice
	if modeNotice != "" {
		d.notice = modeNotice // 設定錯誤比資料讀取提示更需要使用者處理
	}
	if err := d.render(); err != nil {
		errs = append(errs, err)
		note("output", "Cannot write tmux sidebar options")
	}
	return errors.Join(errs...)
}

// report 把目前狀態交給 Report；只有內容改變才呼叫。
func (d *Daemon) report() {
	if d.deps.Report == nil {
		return
	}
	st := Status{Mode: "", ModeIssue: d.modeIssue, Tracking: d.haveMode, Service: ServiceNone,
		Issues: append([]string(nil), d.issues...), Recent: append([]ErrorRecord(nil), d.recent...)}
	if d.haveMode {
		st.Mode = string(d.mode)
	}
	if d.havePanes {
		for _, p := range d.panes {
			if contract.IsTUICandidate(p.Pane) {
				st.TUIs++
			}
		}
	}
	if d.haveMode && d.havePanes && d.tabs != nil {
		seen := map[string]bool{}
		for _, g := range d.view(d.mode).Groups {
			for _, t := range g.Tabs {
				seen[t.SessionID] = true
			}
		}
		st.Tabs = len(seen)
	}
	if d.live != nil {
		st.Service, st.ServiceReason = d.live.Status()
		down := st.Service == ServiceDown && st.ServiceReason != ""
		if down {
			st.Issues = append(st.Issues, "Cannot connect to OpenCode: "+st.ServiceReason)
		}
		// 服務失敗與其他來源一樣只在「剛發生」時記一筆最近錯誤，不因持續失敗而洗版。
		if down && d.lastService != st.ServiceReason {
			d.recent = append(d.recent, ErrorRecord{At: time.Now(), Source: "service", Message: st.ServiceReason})
			if len(d.recent) > maxRecent {
				d.recent = d.recent[len(d.recent)-maxRecent:]
			}
			st.Recent = append([]ErrorRecord(nil), d.recent...)
		}
		if down {
			d.lastService = st.ServiceReason
		} else if st.Service == ServiceConnected || st.Service == ServiceIdle {
			d.lastService = ""
		}
	}
	key := fmt.Sprintf("%v|%v", st.Issues, st)
	if key == d.lastRep {
		return
	}
	d.lastRep = key
	d.deps.Report(st)
}

// render 以最後可用資料與服務狀態重新排版並寫出（內容沒變就不寫）。
func (d *Daemon) render() error {
	if !d.haveMode {
		return nil // 尚未開始追蹤（模式無效）：不連線服務、不顯示狀態
	}
	view := d.view(d.mode)
	view.Notice = d.notice
	if d.live != nil && d.ctx != nil {
		var ids []string
		for _, g := range view.Groups {
			for _, t := range g.Tabs {
				ids = append(ids, t.SessionID)
			}
		}
		d.live.Track(d.ctx, ids)
		// 服務斷線與 tab 清單／tmux 讀取失敗是不同問題，各自提示一次（多行）；
		// 服務斷線的提示不依 tab 數重複。
		if len(ids) > 0 && d.live.Down() {
			if view.Notice != "" {
				view.Notice += "\n"
			}
			view.Notice += "Cannot connect to OpenCode"
		}
	}
	return d.publish(view)
}

// stateOf 是 tab 目前的狀態；未接服務或基準資料不完整時為資料異常，不猜測閒置。
func (d *Daemon) stateOf(session string) render.State {
	if d.live == nil {
		return render.DataProblem
	}
	return d.live.State(session)
}

// titleOf 是 tab 顯示的標題：有 API session.title 時以它為準（TUI 同樣如此），
// 否則退回 tabs.json 保存的標題。
func (d *Daemon) titleOf(t contract.Tab) string {
	if d.live != nil {
		if title, ok := d.live.Title(t.SessionID); ok {
			return title
		}
	}
	return t.Title
}

// view 由最後可用資料組出側欄內容。從未讀到成功資料的來源沒有任何內容可顯示，
// 此時不輸出「沒有 TUI／無 tab」，只留提示。
func (d *Daemon) view(mode contract.Mode) render.View {
	if !d.havePanes || d.tabs == nil {
		return render.View{}
	}
	dirs := map[string]bool{}
	var jumps jumpTargets
	for _, p := range d.panes {
		if contract.IsTUICandidate(p.Pane) {
			dirs[p.ForegroundCwd] = true
			jumps.add(p.ForegroundCwd, p.PaneID)
		}
	}
	var names []string
	for dir := range dirs {
		names = append(names, dir)
	}
	sort.Strings(names)
	view := render.View{Empty: "No OpenCode TUI running"}
	for _, tg := range d.groupsFor(mode, names, jumps) {
		g := render.Group{Name: tg.name, JumpTarget: tg.jump}
		for _, t := range tg.tabs {
			g.Tabs = append(g.Tabs, render.Tab{Title: d.titleOf(t), SessionID: t.SessionID, State: d.stateOf(t.SessionID)})
		}
		view.Groups = append(view.Groups, g)
	}
	return view
}

func (d *Daemon) publish(v render.View) error {
	p := render.PublishView(v, d.cfg.Width-1, d.theme)
	fp := fingerprint(p)
	if d.havePub && fp == d.last {
		return nil
	}
	if err := d.deps.Out.Publish(p); err != nil {
		return fmt.Errorf("write to tmux: %w", err)
	}
	d.last, d.havePub = fp, true
	return nil
}

func fingerprint(p render.Published) string {
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
			d.report()
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
