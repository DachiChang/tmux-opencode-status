package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/instance"
	"github.com/DachiChang/tmux-opencode-status/internal/ocsrc"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
	"github.com/DachiChang/tmux-opencode-status/internal/tabsrc"
	"github.com/DachiChang/tmux-opencode-status/internal/tmuxsrc"
)

// defaultServerCheck is the interval for confirming that the tmux server process is still alive. Liveness is judged by the process, not by whether a tmux command succeeds:
// a command can fail because of transient load or environment problems, whereas a missing process is a definite exit.
const defaultServerCheck = 2 * time.Second

// The exit reason determines what to clean up.
type exitKind int

const (
	exitStop       exitKind = iota // explicit stop: remove hooks, @tos_* options, and side-status-format, unbind the trigger key and clear the jump key table, and restore the click-to-jump bindings to tmux defaults
	exitHandoff                    // hand off to a new instance: remove only the hooks; leave the display and jump bindings (including the trigger key) untouched for the new instance to take over
	exitServerGone                 // server is gone: resources inside tmux vanished with it, so clean up local files only
)

// fingerprint describes an instance's effective settings and executable; start uses it to decide between "an instance with the same settings already exists" and "a handoff is needed".
//
// trigger is the @opencode-status-jump-trigger setting (distinguishing unset, empty string, and read failure): changing the trigger key and reloading
// tmux.conf changes the fingerprint, so start hands off, and the new instance binds the new key and unbinds the old one. Changing only the option value without reloading has no effect.
func (o *options) fingerprint(serverPID int, trigger tmuxsrc.JumpTrigger) string {
	exe, _ := os.Executable()
	var stamp string
	if fi, err := os.Stat(exe); err == nil {
		stamp = fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
	}
	// Include the tmux server pid: if a new server replaces the old one on the same socket path, the old instance (even if it has not yet noticed the server exited) must hand off.
	return fmt.Sprintf("v2|%d|%s|%s|%s|%s|%d|%s|%s|%s|%s|%v|%s|%s|%q", serverPID, o.socketName, o.socketPath, o.config, o.tabs+"|"+o.service,
		o.width, o.backstop, o.idle, o.resync, o.serverCheck, o.show, exe, stamp, trigger.String())
}

func runDaemon(o *options) int {
	if err := o.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	tm, err := o.client(false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	// Install signal handling before acquiring the lock: as soon as start sees the lock held, it may send a handoff signal.
	term := make(chan os.Signal, 1)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)
	handoff := make(chan os.Signal, 1)
	signal.Notify(handoff, syscall.SIGUSR1)

	info, err := tm.Server()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot find tmux server:", err)
		return 1
	}
	if missing, err := tm.MissingCapabilities(); err != nil {
		fmt.Fprintln(os.Stderr, "Cannot verify tmux capabilities:", err)
		return 1
	} else if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "tmux %s is missing required capabilities: %v (requires tmux 3.9 side-status); not starting\n", info.Version, missing)
		return 1
	}
	paths, err := instance.PathsFor(info.SocketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lock, err := instance.Acquire(paths.Lock, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "A daemon is already running for this tmux server; not starting another")
		return 0
	}
	defer lock.Release()
	trigger := tm.JumpTriggerSetting()
	_ = lock.SetContent(o.fingerprint(info.PID, trigger))

	started := time.Now()
	state := instance.State{PID: os.Getpid(), ServerPID: info.PID, Started: started, Service: sidebar.ServiceNone, JumpTrigger: trigger.String()}
	defer func() { _ = os.Remove(paths.State) }()
	_ = instance.WriteState(paths.State, state)
	report := func(s sidebar.Status) {
		st := instance.State{PID: state.PID, ServerPID: info.PID, Started: started, Mode: s.Mode, ModeIssue: s.ModeIssue,
			Tracking: s.Tracking, TUIs: s.TUIs, Tabs: s.Tabs, Service: s.Service, ServiceReason: s.ServiceReason, Issues: s.Issues,
			JumpTrigger: trigger.String()}
		for _, r := range s.Recent {
			st.Recent = append(st.Recent, instance.ErrorRecord{At: r.At, Source: r.Source, Message: r.Message})
		}
		_ = instance.WriteState(paths.State, st)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kind := exitStop
	exited := make(chan exitKind, 1)
	go func() {
		t := time.NewTicker(o.serverCheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-term:
				exited <- exitStop
			case <-handoff:
				exited <- exitHandoff
			case <-t.C:
				if tmuxsrc.ProcessAlive(info.PID) {
					continue
				}
				exited <- exitServerGone
			}
			return
		}
	}()

	tabs := tabsrc.Source{Path: o.tabs}
	wake := make(chan struct{}, 1)
	go tabs.Watch(ctx, wake)
	events := make(chan struct{})
	go func() {
		defer close(events)
		if err := tm.WatchEvents(ctx, wake); err != nil {
			fmt.Fprintln(os.Stderr, "tmux event notification:", err)
		}
	}()
	if err := tm.BindJump(); err != nil {
		fmt.Fprintln(os.Stderr, "Click-to-jump binding:", err)
	}
	var startup []sidebar.ErrorRecord
	if msg := tm.BindJumpMode(trigger); msg != "" {
		fmt.Fprintln(os.Stderr, "Keyboard jump binding:", msg)
		startup = append(startup, sidebar.ErrorRecord{At: time.Now(), Source: "jump", Message: msg})
	}
	if o.show {
		if err := tm.Show(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	tick := time.NewTicker(o.backstop)
	defer tick.Stop()
	d := sidebar.New(sidebar.Config{Width: o.width, Resync: o.resync}, sidebar.Deps{Panes: tm, Tabs: tabs, Out: tm, Mode: tm, Theme: tm, Wake: wake,
		Ticks: tick.C, Service: ocsrc.Source{Path: o.service, IdleTimeout: o.idle}, Report: report, Startup: startup})
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	kind = <-exited
	cancel()
	<-done
	<-events // wait for hook cleanup to finish before exiting
	switch kind {
	case exitStop:
		tm.ClearJumpMode() // before ClearDisplay: the latter clears @tos_jump_trigger
		tm.ClearDisplay()
		tm.ClearShownMarker()
		tm.ResetJump()
	case exitHandoff:
		// The display stays as is, and the new instance takes over on its first sync; WatchEvents has already removed the hooks.
	case exitServerGone:
	}
	return 0
}
