package sidebar

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/ocsrc"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// DefaultRetry is the retry interval after a service connection fails (not yet started, restarting, or disconnected); it never starts or restarts the service.
const DefaultRetry = 3 * time.Second

// DefaultResync is the period of the session family backstop re-read. Events handle live updates; this low-frequency re-read only fills gaps where
// events alone cannot attribute a session (for example a descendant whose session.created lacks parentID). It is not polling and never flashes the screen to unknown.
const DefaultResync = 60 * time.Second

const (
	familyPageSize = 50
	maxParallel    = 4 // number of tab reads in flight at once; one slow tab does not hold up the others, and the service is not flooded all at once
)

// live maintains the read-only connection and state for the local shared service: connect SSE → re-read the baseline tab by tab, with server.connected as the boundary,
// → update incrementally from events. With no tracked tabs it neither connects nor queries sessions.
//
// Consistency rules:
//   - Data completeness is judged per tab: if a tab's baseline has not been read, its read failed, or the connection dropped, only that tab
//     is "unknown"; only a failure of the whole service invalidates every tab.
//   - Each connection has a generation number; events and tab read results from an old connection are never applied to a new one (results return to the connection loop through a
//     channel dedicated to that connection, and nobody receives them once it ends).
//   - Before a read starts, the event sequence number is recorded; events received after that sequence are newer than the read and are not overwritten by the old response.
type live struct {
	dialer ocsrc.Dialer
	retry  time.Duration
	resync time.Duration
	notify func()

	mu     sync.Mutex
	tr     *contract.Tracker
	tabs   []string // tracked tab sessions (sorted, deduplicated)
	gen    uint64   // connection generation; incremented on every connection start or state reset
	ready  bool     // server.connected received in this generation
	loaded map[string]bool
	jobs   map[string]*tabJob
	tokens uint64
	down   bool   // latest connection attempt failed or dropped
	reason string // fixed description of the latest failure (diagnostics; no raw error text)

	parent context.Context
	cancel context.CancelFunc // non-nil means the connection loop is running
	kick   chan struct{}
}

// tabJob is the read schedule for a single tab.
type tabJob struct {
	token    uint64 // identifier of the read in flight; a returning result that does not match is discarded
	inflight bool
	force    bool // re-read even if already loaded (backstop)
	missing  bool // the latest read explicitly reported that the session does not exist
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

// Track sets the tab sessions to track. With an empty list it closes the connection (and queries no session data).
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

// reset clears event-derived state and invalidates the old generation; call it with the lock held.
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

// State is the status currently presentable for a tab; when the baseline data is incomplete it is a data problem, and idle is never guessed.
func (l *live) State(tab string) render.State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return displayState(l.health(tab), l.tr.TabStatus(tab))
}

// Title is the tab's live title (API session.title); ok is false until it is obtained.
func (l *live) Title(tab string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tr.Title(tab)
}

// Down reports that the service is currently unreachable (a connection was attempted and failed, or the connection dropped).
func (l *live) Down() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.down
}

// Status reports the fixed description of the service connection: idle when nothing is tracked (deliberately not connecting),
// connected once the baseline read is complete, and down with a fixed reason otherwise.
func (l *live) Status() (state, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.cancel == nil:
		return ServiceIdle, ""
	case l.down:
		return ServiceDown, l.reason
	case l.ready:
		return ServiceConnected, ""
	}
	return ServiceConnecting, ""
}

func (l *live) loop(ctx context.Context) {
	for ctx.Err() == nil {
		err := l.runConnection(ctx)
		if ctx.Err() != nil {
			return // tracking has stopped (Track with an empty list already reset the state); must not touch a new loop that may start later
		}
		l.mu.Lock()
		l.reset()
		l.down = err != nil
		l.reason = ocsrc.Reason(err)
		l.mu.Unlock()
		l.notify()
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.retry):
		}
	}
}

// runConnection is the lifecycle of one connection: it rediscovers the service every time and returns only when the stream ends or goes silent past the timeout.
func (l *live) runConnection(ctx context.Context) error {
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
		return // late event from an old connection
	}
	l.tr.Apply(e)
	switch {
	case e.Type == contract.EventServerConnected:
		l.ready = true
		l.loaded = map[string]bool{}
		l.jobs = map[string]*tabJob{} // all reads in flight are invalidated and everything is re-read
		l.down = false
		l.poke()
	case e.Type == "session.execution.interrupted" && e.Reason == "shutdown":
		// the event itself is not treated as a result (Tracker ignores it); the actual outcome is decided by the re-read.
		for _, j := range l.jobs {
			j.force, j.retryAt = true, time.Time{}
		}
		l.poke()
	case e.Type == "session.created":
		// a tab session that did not exist before has now been created: re-read immediately without waiting for the backstop period.
		if j := l.jobs[e.SessionID]; j != nil && !l.loaded[e.SessionID] && !j.inflight {
			j.force, j.fails, j.retryAt = true, 0, time.Time{}
			l.poke()
		}
	}
	l.mu.Unlock()
	l.notify()
}

// schedule starts the reads needed for the currently tracked tabs and returns the time until the next wake-up is needed (-1 = wait for events only).
// Each tab is scheduled independently: only tabs that need a re-read are re-read, and one tab's failure does not affect the others.
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
			delete(l.jobs, t) // removed tab: the result of a read in flight is discarded because its job cannot be found
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
			continue // scheduling happens again when a tab finishes reading
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

// apply applies one tab's read result. Results whose job cannot be found (tab removed) or whose identifier does not match (read invalidated) are discarded.
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
		// transient failure: only this tab becomes unknown; retry with backoff (capped at 4x the retry interval) rather than hammering at high frequency.
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
