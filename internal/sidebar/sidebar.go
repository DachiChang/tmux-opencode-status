// Package sidebar is the core of the daemon: it assembles sidebar content from tmux (pane facts) and the tab list saved by the TUI,
// and writes to tmux only when the visible output changes. External data sources and the output are interfaces;
// tests replace them with controlled implementations, and later tickets (status, global mode, layout, lifecycle) extend them here.
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

// PaneFact identifies a tmux pane (PaneID is used to drop panes listed twice through linked windows).
type PaneFact struct {
	PaneID string
	contract.Pane
}

// PaneSource lists the panes of every session on the same tmux server. Failures must be reported as errors;
// an empty list must not be returned instead.
type PaneSource interface {
	Panes() ([]PaneFact, error)
}

// TabSource reads the tab list saved by the TUI and reports an error if the format is incompatible.
type TabSource interface {
	Read() (*contract.TabsFile, error)
}

// Output writes the laid-out sidebar to tmux.
type Output interface {
	Publish(render.Published) error
}

type Deps struct {
	Panes PaneSource
	Tabs  TabSource
	Out   Output
	// Mode reads the plugin mode option; nil is treated as unset (default cwd).
	Mode ModeSource
	// Theme reads the color options; nil is treated as all unset (default theme).
	Theme ThemeSource

	// Wake carries event notifications (tmux hooks, tab file changes); Ticks is a low-frequency backstop. Either may be nil;
	// the caller injects time, so tests never wait for real time.
	Wake  <-chan struct{}
	Ticks <-chan time.Time

	// Service is the read-only connection source for the local shared OpenCode service; nil means no service is connected and every tab stays unknown.
	Service ocsrc.Dialer

	// Report is called when observable state changes (for diagnostics). It carries only counts and fixed descriptions,
	// never credentials, prompts, conversations, sessions, or tab titles, and no raw error text. nil disables reporting.
	Report func(Status)

	// Startup holds problems found at startup (outside the Daemon) that belong in the recent errors (fixed descriptions, for example an invalid trigger key).
	Startup []ErrorRecord
}

// Fixed description values for the Service connection state.
const (
	ServiceIdle       = "idle"       // nothing tracked; deliberately not connecting or querying
	ServiceConnected  = "connected"  // connected and baseline read complete
	ServiceConnecting = "connecting" // connecting or reading baseline data
	ServiceDown       = "down"       // connection failed or dropped; retrying automatically
	ServiceNone       = "none"       // no service source configured
)

// Status is a snapshot of the current state for diagnostics.
type Status struct {
	Mode          string // the mode currently in effect (cwd/global); empty until tracking starts
	ModeIssue     string // fixed description of a mode setting problem
	Tracking      bool
	TUIs          int
	Tabs          int
	Service       string
	ServiceReason string
	Issues        []string // problems that currently persist (fixed descriptions)
	Recent        []ErrorRecord
}

// ErrorRecord is one recent problem: only a source and a fixed description.
type ErrorRecord struct {
	At      time.Time
	Source  string // tmux／tabs／mode／theme／service／output／jump
	Message string
}

const maxRecent = 8

type Config struct {
	Width    int           // side-status-width; the content width is Width-1 (one cell is the divider)
	Debounce time.Duration // how long to wait after a notification to coalesce simultaneous changes
	Retry    time.Duration // retry interval after a service connection failure (default DefaultRetry)
	Resync   time.Duration // session family backstop re-read period (default DefaultResync)
}

// Time budgets (spec acceptance targets: ordinary changes within 1 second; missed notifications and forced closes corrected within 10 seconds).
const (
	DefaultDebounce = 100 * time.Millisecond
	DefaultBackstop = 5 * time.Second
)

type Daemon struct {
	cfg  Config
	deps Deps

	panes     []PaneFact // last successfully read data; reused with a notice when a read fails
	tabs      *contract.TabsFile
	theme     render.Theme  // last successfully read theme; the default if never successful
	mode      contract.Mode // last valid mode that took effect
	haveMode  bool
	havePanes bool
	last      string // fingerprint of the last output written
	havePub   bool
	notice    string // source read notice from the latest Sync

	live     *live
	liveWake chan struct{} // service event/state change notification (re-layout only; no need to re-read tmux)
	ctx      context.Context

	issues    []string // source problems from the latest Sync (fixed descriptions)
	modeIssue string
	recent    []ErrorRecord
	lastRecv  map[string]bool // sources of problems that are still ongoing, so one persistent problem is not recorded on every sync
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

// Sync reads the external data once and updates the sidebar; it does not write when the content matches the last output.
// A failed read from any source is never treated as an empty list: the last available data is reused with a notice, and an error is returned.
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
		// No valid mode on the first run: do not start tracking (no tmux/tab list reads, no empty state), only show the notice.
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
		d.notice = modeNotice // a configuration error needs the user's attention more than a data read notice
	}
	if err := d.render(); err != nil {
		errs = append(errs, err)
		note("output", "Cannot write tmux sidebar options")
	}
	return errors.Join(errs...)
}

// report hands the current state to Report; it is called only when the content changes.
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
			if contract.IsTUIClient(p.Pane) {
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
		// Like other sources, a service failure records a recent error only when it "just happened", so continuing failures do not flood the list.
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

// render re-lays out with the last available data and service state and writes it (skipped when the content is unchanged).
func (d *Daemon) render() error {
	if !d.haveMode {
		return nil // tracking has not started (invalid mode): do not connect to the service or show status
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
		// A service disconnect and a tab list/tmux read failure are different problems, each noticed once (multiple lines);
		// the service disconnect notice is not repeated per tab.
		if len(ids) > 0 && d.live.Down() {
			if view.Notice != "" {
				view.Notice += "\n"
			}
			view.Notice += "Cannot connect to OpenCode"
		}
	}
	return d.publish(view)
}

// stateOf is the tab's current status; when no service is connected or the baseline data is incomplete it is a data problem, and idle is never guessed.
func (d *Daemon) stateOf(session string) render.State {
	if d.live == nil {
		return render.DataProblem
	}
	return d.live.State(session)
}

// titleOf is the title shown for a tab: the API session.title wins when present (as in the TUI),
// otherwise it falls back to the title saved in tabs.json.
func (d *Daemon) titleOf(t contract.Tab) string {
	if d.live != nil {
		if title, ok := d.live.Title(t.SessionID); ok {
			return title
		}
	}
	return t.Title
}

// view builds the sidebar content from the last available data. A source that has never been read successfully has nothing to show;
// in that case it does not print "no TUI / no tab" and shows only the notice.
func (d *Daemon) view(mode contract.Mode) render.View {
	if !d.havePanes || d.tabs == nil {
		return render.View{}
	}
	dirs := map[string]bool{}
	var jumps jumpTargets
	for _, p := range d.panes {
		if contract.IsTUIClient(p.Pane) {
			dir := p.Pane.PaneCurrentPath
			dirs[dir] = true
			jumps.add(dir, p.PaneID)
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

// Run syncs once, then syncs on each event notification (consecutive notifications within Debounce are coalesced) or backstop tick,
// until ctx ends. A failed sync does not stop it: a notice is already shown, and the next notification or tick retries.
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
