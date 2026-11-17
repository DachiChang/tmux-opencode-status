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

// defaultServerCheck 是確認 tmux server 行程仍在的間隔。以行程存活判斷，不以 tmux 指令成敗判斷：
// 指令可能因暫時忙碌或環境問題失敗，行程不在才是確定的結束。
const defaultServerCheck = 2 * time.Second

// 結束原因決定要清理什麼。
type exitKind int

const (
	exitStop       exitKind = iota // 明確停止：清掉 hook、版面選項、side-status-format
	exitHandoff                    // 交接給新實例：只移除 hook，畫面保持不動等新實例接手
	exitServerGone                 // server 已結束：tmux 內的資源已隨 server 消失，只清本機檔案
)

// fingerprint 描述一個實例的有效設定與執行檔；start 以它判斷「已有同設定的實例」或「需要交接」。
func (o *options) fingerprint(serverPID int) string {
	exe, _ := os.Executable()
	var stamp string
	if fi, err := os.Stat(exe); err == nil {
		stamp = fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
	}
	// 納入 tmux server 的 pid：同一個 socket 路徑上換了新的 server，舊實例（即使還沒偵測到 server 已結束）也必須交接。
	return fmt.Sprintf("v1|%d|%s|%s|%s|%s|%d|%s|%s|%s|%s|%v|%s|%s", serverPID, o.socketName, o.socketPath, o.config, o.tabs+"|"+o.service,
		o.width, o.backstop, o.idle, o.resync, o.serverCheck, o.show, exe, stamp)
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
	// 訊號處理先於取鎖安裝：start 一旦看到鎖被持有就可能送出交接訊號。
	term := make(chan os.Signal, 1)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)
	handoff := make(chan os.Signal, 1)
	signal.Notify(handoff, syscall.SIGUSR1)

	info, err := tm.Server()
	if err != nil {
		fmt.Fprintln(os.Stderr, "找不到 tmux server：", err)
		return 1
	}
	if missing, err := tm.MissingCapabilities(); err != nil {
		fmt.Fprintln(os.Stderr, "無法確認 tmux 能力：", err)
		return 1
	} else if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "tmux %s 缺少必要能力：%v（需要 tmux 3.9 的 side-status）；不啟動\n", info.Version, missing)
		return 1
	}
	paths, err := instance.PathsFor(info.SocketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lock, err := instance.Acquire(paths.Lock, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "此 tmux server 已有常駐實例，不啟動第二個")
		return 0
	}
	defer lock.Release()
	_ = lock.SetContent(o.fingerprint(info.PID))

	started := time.Now()
	state := instance.State{PID: os.Getpid(), ServerPID: info.PID, Started: started, Service: sidebar.ServiceNone}
	defer func() { _ = os.Remove(paths.State) }()
	_ = instance.WriteState(paths.State, state)
	report := func(s sidebar.Status) {
		st := instance.State{PID: state.PID, ServerPID: info.PID, Started: started, Mode: s.Mode, ModeIssue: s.ModeIssue,
			Tracking: s.Tracking, TUIs: s.TUIs, Tabs: s.Tabs, Service: s.Service, ServiceReason: s.ServiceReason, Issues: s.Issues}
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
			fmt.Fprintln(os.Stderr, "tmux 事件通知：", err)
		}
	}()
	if o.show {
		if err := tm.Show(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	tick := time.NewTicker(o.backstop)
	defer tick.Stop()
	d := sidebar.New(sidebar.Config{Width: o.width, Resync: o.resync}, sidebar.Deps{Panes: tm, Tabs: tabs, Out: tm, Mode: tm, Wake: wake,
		Ticks: tick.C, Service: ocsrc.Source{Path: o.service, IdleTimeout: o.idle}, Report: report})
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	kind = <-exited
	cancel()
	<-done
	<-events // 等 hook 清理完成再退出
	switch kind {
	case exitStop:
		tm.ClearDisplay()
		tm.ClearShownMarker()
	case exitHandoff:
		// 畫面維持原樣，由新實例第一次同步接手；hook 已由 WatchEvents 移除。
	case exitServerGone:
	}
	return 0
}
