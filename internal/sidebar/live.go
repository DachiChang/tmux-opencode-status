package sidebar

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/heightfit"
	"github.com/DachiChang/tmux-opencode-status/internal/ocsrc"
)

// DefaultRetry 是服務連線失敗（尚未開啟、重啟、斷線）後的重試間隔；不啟動或重啟服務。
const DefaultRetry = 3 * time.Second

// DefaultResync 是家族補漏重讀的週期。事件負責即時更新；這個低頻重讀只補「事件單獨無法
// 歸屬」的缺口（例如 session.created 缺 parentID 的後代），不是輪詢，也不會讓畫面閃成未知。
const DefaultResync = 60 * time.Second

const (
	familyPageSize = 50
	maxParallel    = 4 // 同時進行中的 tab 讀取數；單一 tab 慢不會拖住其他 tab，也不會一次灌爆服務
)

// Health 是 tab 的資料健康度。「狀態未知」與「session 不存在」在內部保持區別
// （重試與恢復的方式不同），畫面共用灰色 !。
type health int

const (
	healthOK      health = iota
	healthUnknown        // 資料尚未讀到或讀取失敗（暫時性）：持續重試
	healthMissing        // OpenCode 明確回報 session 不存在：不輪詢，出現（建立事件）或補漏重讀才恢復
)

// live 維護對本機共用服務的唯讀連線與狀態：連上 SSE → 以 server.connected 為界逐 tab
// 重讀基準資料 → 由事件增量更新。沒有被追蹤的 tab 時不連線、不查詢 session。
//
// 一致性規則：
//   - 資料完整性逐 tab 判定：某 tab 的基準資料尚未讀到、讀取失敗或連線已中斷，只有該 tab
//     是「未知」；整體服務失效才讓所有 tab 失效。
//   - 每條連線有世代號；舊連線的事件、舊 tab 讀取結果都不會套用到新連線（結果經由該連線
//     專屬的通道回到連線迴圈，連線結束後就沒有人接收）。
//   - 讀取開始前先記下事件序號；序號之後收到的事件比該次讀取新，不被舊回應覆蓋。
type live struct {
	dialer ocsrc.Dialer
	retry  time.Duration
	resync time.Duration
	notify func()

	mu     sync.Mutex
	tr     *contract.Tracker
	tabs   []string // 被追蹤的 tab session（排序去重）
	gen    uint64   // 連線世代；每次連線開始或狀態重置都遞增
	ready  bool     // 本世代已收到 server.connected
	loaded map[string]bool
	jobs   map[string]*tabJob
	tokens uint64
	down   bool // 最近一次連線嘗試失敗或已中斷

	parent context.Context
	cancel context.CancelFunc // 非 nil 表示連線迴圈執行中
	kick   chan struct{}
}

// tabJob 是單一 tab 的讀取排程。
type tabJob struct {
	token    uint64 // 進行中讀取的識別；與回來的結果不符就丟棄
	inflight bool
	force    bool // 已載入也要重讀（補漏）
	missing  bool // 最近一次讀取明確回報 session 不存在
	fails    int
	retryAt  time.Time
}

type loadResult struct {
	tab   string
	token uint64
	since uint64
	snap  contract.Snapshot
	err   error
}

func newLive(d ocsrc.Dialer, retry, resync time.Duration, notify func()) *live {
	if retry == 0 {
		retry = DefaultRetry
	}
	if resync == 0 {
		resync = DefaultResync
	}
	return &live{dialer: d, retry: retry, resync: resync, notify: notify, tr: contract.NewTracker(),
		loaded: map[string]bool{}, jobs: map[string]*tabJob{}, kick: make(chan struct{}, 1)}
}

func (l *live) poke() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// Track 設定要追蹤的 tab session。清單為空時關閉連線（不查詢任何 session 資料）。
func (l *live) Track(ctx context.Context, ids []string) {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	uniq := make([]string, 0, len(set))
	for id := range set {
		uniq = append(uniq, id)
	}
	sort.Strings(uniq)

	l.mu.Lock()
	defer l.mu.Unlock()
	same := len(uniq) == len(l.tabs)
	for i := 0; same && i < len(uniq); i++ {
		same = uniq[i] == l.tabs[i]
	}
	l.tabs = uniq
	switch {
	case len(uniq) == 0:
		if l.cancel != nil {
			l.cancel()
			l.cancel = nil
			l.reset()
		}
	case l.cancel == nil:
		c, cancel := context.WithCancel(ctx)
		l.cancel, l.parent = cancel, c
		go l.loop(c)
	case !same:
		l.poke()
	}
}

// reset 清掉事件推導的狀態並讓舊世代失效；持鎖呼叫。
func (l *live) reset() {
	l.gen++
	l.ready = false
	l.tr.Invalidate()
	l.loaded = map[string]bool{}
	l.jobs = map[string]*tabJob{}
	l.down = false
}

func (l *live) health(tab string) health {
	if l.tr.Missing(tab) {
		return healthMissing
	}
	if j := l.jobs[tab]; j != nil && j.missing && !l.loaded[tab] {
		return healthMissing
	}
	if !l.loaded[tab] {
		return healthUnknown
	}
	return healthOK
}

// State 是 tab 目前可呈現的狀態；基準資料不完整時為 Unknown，不猜測閒置。
func (l *live) State(tab string) heightfit.State {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.health(tab) != healthOK {
		return heightfit.Unknown
	}
	switch l.tr.TabStatus(tab) {
	case contract.WaitingApproval, contract.WaitingAnswer:
		return heightfit.Attention
	case contract.Running:
		return heightfit.Running
	case contract.FailedUnread:
		return heightfit.Failed
	case contract.FinishedUnread:
		return heightfit.Unread
	}
	return heightfit.Idle
}

// Down 表示服務目前不可連線（已嘗試且失敗，或連線已中斷）。
func (l *live) Down() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.down
}

func (l *live) loop(ctx context.Context) {
	for ctx.Err() == nil {
		err := l.session(ctx)
		if ctx.Err() != nil {
			return // 追蹤已停止（Track 空清單已重置狀態）；不得動到之後可能啟動的新迴圈
		}
		l.mu.Lock()
		l.reset()
		l.down = err != nil
		l.mu.Unlock()
		l.notify()
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.retry):
		}
	}
}

// session 是一條連線的生命週期：每次都重新探索服務，直到串流結束或無聲逾時才返回。
func (l *live) session(ctx context.Context) error {
	svc, err := l.dialer.Dial(ctx)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	l.mu.Lock()
	l.gen++
	gen := l.gen
	l.ready = false
	l.loaded = map[string]bool{}
	l.jobs = map[string]*tabJob{}
	l.mu.Unlock()

	streamErr := make(chan error, 1)
	go func() {
		streamErr <- svc.Stream(sctx, func(e contract.Event) error {
			l.onEvent(gen, e)
			return nil
		})
	}()

	results := make(chan loadResult)
	var resyncAt time.Time
	for {
		wait := l.schedule(sctx, svc, results, &resyncAt)
		var wake <-chan time.Time
		var timer *time.Timer
		if wait >= 0 {
			timer = time.NewTimer(wait)
			wake = timer.C
		}
		select {
		case err := <-streamErr:
			if timer != nil {
				timer.Stop()
			}
			return err
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return ctx.Err()
		case r := <-results:
			l.apply(gen, r)
		case <-l.kick:
		case <-wake:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (l *live) onEvent(gen uint64, e contract.Event) {
	l.mu.Lock()
	if gen != l.gen {
		l.mu.Unlock()
		return // 舊連線的遲到事件
	}
	l.tr.Apply(e)
	switch {
	case e.Type == contract.EventServerConnected:
		l.ready = true
		l.loaded = map[string]bool{}
		l.jobs = map[string]*tabJob{} // 進行中的讀取一律作廢，全部重讀
		l.down = false
		l.poke()
	case e.Type == "session.execution.interrupted" && e.Reason == "shutdown":
		// 事件本身不當成結果（Tracker 忽略）；實際結局以重讀為準。
		for _, j := range l.jobs {
			j.force, j.retryAt = true, time.Time{}
		}
		l.poke()
	case e.Type == "session.created":
		// 原本不存在的 tab session 現在建立了：立即重讀，不等補漏週期。
		if j := l.jobs[e.SessionID]; j != nil && !l.loaded[e.SessionID] && !j.inflight {
			j.force, j.fails, j.retryAt = true, 0, time.Time{}
			l.poke()
		}
	}
	l.mu.Unlock()
	l.notify()
}

// schedule 依目前追蹤的 tab 啟動需要的讀取，回傳距離下一次需要喚醒的時間（-1 = 只等事件）。
// 每個 tab 獨立排程：只重讀需要重讀的 tab，單一 tab 失敗不連帶其他 tab。
func (l *live) schedule(ctx context.Context, svc contract.API, results chan<- loadResult, resyncAt *time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.ready {
		return -1
	}
	now := time.Now()
	tracked := map[string]bool{}
	for _, t := range l.tabs {
		tracked[t] = true
		if l.jobs[t] == nil {
			l.jobs[t] = &tabJob{}
		}
	}
	for t := range l.jobs {
		if !tracked[t] {
			delete(l.jobs, t) // 已移除的 tab：進行中的讀取結果因找不到 job 而丟棄
			delete(l.loaded, t)
		}
	}
	if resyncAt.IsZero() {
		*resyncAt = now.Add(l.resync)
	}
	if !now.Before(*resyncAt) {
		for _, j := range l.jobs {
			j.force = true
		}
		*resyncAt = now.Add(l.resync)
	}
	inflight := 0
	for _, j := range l.jobs {
		if j.inflight {
			inflight++
		}
	}
	wait := time.Duration(-1)
	soonest := func(d time.Duration) {
		if d < 0 {
			d = 0
		}
		if wait < 0 || d < wait {
			wait = d
		}
	}
	soonest(resyncAt.Sub(now))
	for _, tab := range l.tabs {
		j := l.jobs[tab]
		if j.inflight || (l.loaded[tab] && !j.force) || (j.missing && !j.force) {
			continue
		}
		if now.Before(j.retryAt) {
			soonest(j.retryAt.Sub(now))
			continue
		}
		if inflight >= maxParallel {
			continue // 有 tab 讀完會再排程
		}
		inflight++
		l.tokens++
		j.token, j.inflight, j.force = l.tokens, true, false
		since := l.tr.Mark()
		go func(tab string, token uint64) {
			snap, err := contract.DiscoverFamily(svc, tab, familyPageSize)
			select {
			case results <- loadResult{tab: tab, token: token, since: since, snap: snap, err: err}:
			case <-ctx.Done():
			}
		}(tab, j.token)
	}
	return wait
}

// apply 套用一個 tab 的讀取結果。找不到 job（tab 已移除）或識別不符（讀取已作廢）的結果丟棄。
func (l *live) apply(gen uint64, r loadResult) {
	l.mu.Lock()
	j := l.jobs[r.tab]
	if gen != l.gen || j == nil || j.token != r.token {
		l.mu.Unlock()
		return
	}
	j.inflight = false
	switch {
	case r.err == nil:
		l.tr.ApplyFamily(r.tab, r.snap, r.since)
		l.loaded[r.tab] = true
		j.fails, j.missing = 0, false
	case errors.Is(r.err, contract.ErrSessionNotFound):
		l.loaded[r.tab] = false
		j.fails, j.missing = 0, true
	default:
		// 暫時性失敗：只有這個 tab 變未知；退避重試（上限 4 倍重試間隔），不高頻重打。
		l.loaded[r.tab] = false
		j.missing = false
		j.fails++
		d := l.retry
		for i := 1; i < j.fails && d < 4*l.retry; i++ {
			d *= 2
		}
		j.retryAt = time.Now().Add(min(d, 4*l.retry))
	}
	l.mu.Unlock()
	l.notify()
	l.poke()
}
