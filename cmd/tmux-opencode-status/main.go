// tmux-opencode-status 是常駐程式：把同一 tmux server 中執行中 OpenCode TUI 的 tab 清單
// 寫進原生 side-status。嚴格唯讀：不呼叫 OpenCode 寫入 API、不啟動服務、不新增任何按鍵綁定。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/ocsrc"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
	"github.com/DachiChang/tmux-opencode-status/internal/tabsrc"
	"github.com/DachiChang/tmux-opencode-status/internal/tmuxsrc"
)

func main() {
	socketName := flag.String("L", "", "tmux socket 名稱（-L）")
	socketPath := flag.String("S", "", "tmux socket 路徑（-S）")
	config := flag.String("f", "", "tmux 設定檔（-f，僅供隔離測試，例如 /dev/null）")
	tabsPath := flag.String("tabs", tabsrc.DefaultPath(), "OpenCode tabs.json 路徑")
	servicePath := flag.String("service", ocsrc.DefaultPath(), "OpenCode 服務探索檔 service.json 路徑（唯讀；不啟動或重啟服務）")
	width := flag.Int("width", 36, "side-status-width")
	backstop := flag.Duration("backstop", sidebar.DefaultBackstop, "低頻補漏同步週期")
	show := flag.Bool("show", false, "啟動時把 side-status 設為 left")
	flag.Parse()

	if (*socketName == "") == (*socketPath == "") {
		fmt.Fprintln(os.Stderr, "必須明確指定 -L <name> 或 -S <path> 其中之一，不使用預設 tmux server")
		os.Exit(2)
	}
	tm := &tmuxsrc.Client{Width: *width}
	if *socketName != "" {
		tm.SocketArgs = []string{"-L", *socketName}
	} else {
		tm.SocketArgs = []string{"-S", *socketPath}
	}
	if *config != "" {
		tm.ConfigArgs = []string{"-f", *config}
	}
	tabs := tabsrc.Source{Path: *tabsPath}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	wake := make(chan struct{}, 1)
	go tabs.Watch(ctx, wake)
	events := make(chan struct{})
	go func() {
		defer close(events)
		if err := tm.WatchEvents(ctx, wake); err != nil {
			fmt.Fprintln(os.Stderr, "tmux 事件通知：", err)
		}
	}()
	if *show {
		if err := tm.Show(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	tick := time.NewTicker(*backstop)
	defer tick.Stop()
	d := sidebar.New(sidebar.Config{Width: *width}, sidebar.Deps{Panes: tm, Tabs: tabs, Out: tm, Mode: tm, Wake: wake, Ticks: tick.C, Service: ocsrc.Source{Path: *servicePath}})
	_ = d.Run(ctx)
	stop()
	<-events // 等 hook 清理完成再退出
}
