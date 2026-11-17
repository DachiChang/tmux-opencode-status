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

const familyPageSize = 50

// live 維護對本機共用服務的唯讀連線與狀態：連上 SSE → 以 server.connected 為界重讀
// 所有被追蹤 tab 的基準資料 → 由事件增量更新。沒有被追蹤的 tab 時不連線、不查詢 session。
//
// 資料完整性逐 tab 判定：某 tab 的基準資料尚未讀到（或連線已中斷／重連尚未重讀完），
// 其狀態就是「未知」，不沿用舊結果。
type live struct {
	dialer ocsrc.Dialer
	retry  time.Duration
	notify func()

	mu     sync.Mutex
	tr     *contract.Tracker
	tabs   []string        // 被追蹤的 tab session（排序去重）
	loaded map[string]bool // 本次連線中，基準資料已完整讀到的 tab
	down   bool            // 最近一次連線嘗試失敗或已中斷

	parent context.Context
	cancel context.CancelFunc // 非 nil 表示連線迴圈執行中
	kick   chan struct{}
}

func newLive(d ocsrc.Dialer, retry time.Duration, notify func()) *live {
	if retry == 0 {
		retry = DefaultRetry
	}
	return &live{dialer: d, retry: retry, notify: notify, tr: contract.NewTracker(), loaded: map[string]bool{}, kick: make(chan struct{}, 1)}
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
		select {
		case l.kick <- struct{}{}:
		default:
		}
	}
}

// reset 清掉事件推導的狀態；持鎖呼叫。
func (l *live) reset() {
	l.tr.Invalidate()
	l.loaded = map[string]bool{}
	l.down = false
}

// State 是 tab 目前可呈現的狀態；基準資料不完整時為 Unknown，不猜測閒置。
func (l *live) State(tab string) heightfit.State {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded[tab] || l.tr.Missing(tab) {
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
		l.mu.Lock()
		l.tr.Invalidate()
		l.loaded = map[string]bool{}
		l.down = err != nil && ctx.Err() == nil
		l.mu.Unlock()
		l.notify()
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.retry):
		}
	}
}

// session 是一條連線的生命週期：每次都重新探索服務，直到串流結束才返回。
func (l *live) session(ctx context.Context) error {
	svc, err := l.dialer.Dial(ctx)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	connected := make(chan struct{}, 1)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- svc.Stream(sctx, func(e contract.Event) error {
			l.onEvent(e)
			if e.Type == contract.EventServerConnected {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
			return nil
		})
	}()

	var retryC <-chan time.Time
	ready := false
	for {
		load := false
		select {
		case err := <-streamErr:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-connected:
			ready, load = true, true
		case <-l.kick:
			load = ready
		case <-retryC:
			load = ready
		}
		if !load {
			continue
		}
		retryC = nil
		if !l.load(svc) {
			retryC = time.After(l.retry)
		}
	}
}

func (l *live) onEvent(e contract.Event) {
	l.mu.Lock()
	l.tr.Apply(e)
	if e.Type == contract.EventServerConnected {
		l.loaded = map[string]bool{}
		l.down = false
	}
	l.mu.Unlock()
	l.notify()
}

// load 重讀所有被追蹤 tab 的完整基準資料（含所有層級後代）。單一 tab 失敗只讓該 tab
// 未知；回傳 false 表示有 tab 因暫時性錯誤尚未讀到，需要稍後重試。
func (l *live) load(svc contract.API) bool {
	l.mu.Lock()
	tabs := append([]string(nil), l.tabs...)
	l.mu.Unlock()

	merged := contract.Snapshot{Running: map[string]bool{}}
	seen := map[string]bool{}
	seenPerm, seenForm := map[string]bool{}, map[string]bool{}
	ok := map[string]bool{}
	transient := false
	for _, tab := range tabs {
		snap, err := contract.DiscoverFamily(svc, tab, familyPageSize)
		if err != nil {
			if !errors.Is(err, contract.ErrSessionNotFound) {
				transient = true
			}
			continue
		}
		ok[tab] = true
		for _, s := range snap.Sessions {
			if !seen[s.ID] {
				seen[s.ID] = true
				merged.Sessions = append(merged.Sessions, s)
			}
		}
		for _, p := range snap.Permissions {
			if !seenPerm[p.ID] {
				seenPerm[p.ID] = true
				merged.Permissions = append(merged.Permissions, p)
			}
		}
		for _, f := range snap.Forms {
			if !seenForm[f.ID] {
				seenForm[f.ID] = true
				merged.Forms = append(merged.Forms, f)
			}
		}
		for id := range snap.Running {
			merged.Running[id] = true
		}
	}
	l.mu.Lock()
	if len(ok) > 0 {
		l.tr.ApplySnapshot(merged)
	}
	l.loaded = ok
	l.mu.Unlock()
	l.notify()
	return !transient
}
