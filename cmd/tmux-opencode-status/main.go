// tmux-opencode-status 是常駐程式：把同一 tmux server 中執行中 OpenCode TUI 的 tab 清單
// 寫進原生 side-status。嚴格唯讀：不呼叫 OpenCode 寫入 API、不啟動服務；tmux 綁定只有點擊跳轉、prefix 觸發鍵與 opencode-status-jump 按鍵表。
//
// 子指令（省略時等同 run，維持既有旗標用法）：
//
//	start     冪等啟動；TPM 的 .tmux 腳本在每次載入／重載時呼叫。已有同設定的實例就不動，
//	          設定或執行檔不同就安全交接，絕不留下第二個實例。
//	run       常駐程式本體（start 以背景方式啟動它）；必須明確指定 -L／-S。
//	stop      停止實例並清理本插件資源。
//	diagnose  唯讀診斷；不輸出憑證、prompt、對話或 session 標題。
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/ocsrc"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
	"github.com/DachiChang/tmux-opencode-status/internal/tabsrc"
	"github.com/DachiChang/tmux-opencode-status/internal/tmuxsrc"
)

const usage = "Usage: tmux-opencode-status [start|run|stop|diagnose] [-L <name> | -S <path>] [flags]"

// options 是所有子指令共用的旗標。
type options struct {
	socketName, socketPath, config string
	tabs, service                  string
	width                          int
	backstop, idle, resync         time.Duration
	serverCheck                    time.Duration
	show                           bool
}

func (o *options) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.socketName, "L", "", "tmux socket name")
	fs.StringVar(&o.socketPath, "S", "", "tmux socket path")
	fs.StringVar(&o.config, "f", "", "tmux configuration file (isolated tests only, e.g. /dev/null)")
	fs.StringVar(&o.tabs, "tabs", tabsrc.DefaultPath(), "Path to OpenCode tabs.json")
	fs.StringVar(&o.service, "service", ocsrc.DefaultPath(), "Path to OpenCode service.json (read-only; does not start or restart the service)")
	fs.IntVar(&o.width, "width", 36, "side-status-width")
	fs.DurationVar(&o.backstop, "backstop", sidebar.DefaultBackstop, "Interval for reconciling missed updates")
	fs.DurationVar(&o.idle, "idle-timeout", ocsrc.DefaultIdleTimeout, "Time without SSE data before treating the stream as disconnected (the service sends a heartbeat every 15 seconds)")
	fs.DurationVar(&o.resync, "resync", sidebar.DefaultResync, "Interval for reconciling session families (infrequent, not polling)")
	fs.DurationVar(&o.serverCheck, "server-check", defaultServerCheck, "Interval for checking whether the tmux server process is still running")
	fs.BoolVar(&o.show, "show", false, "Set side-status to left on startup, once per tmux server; does not reopen a sidebar you hid afterward")
}

// validate 在套用任何設定前先驗證；無效時 start 不得觸碰既有實例。
func (o *options) validate() error {
	bad := func(name, why string) error { return fmt.Errorf("invalid setting: %s %s", name, why) }
	switch {
	case o.width < 4:
		return bad("-width", "must be at least 4")
	case o.backstop <= 0:
		return bad("-backstop", "must be greater than 0")
	case o.idle <= 0:
		return bad("-idle-timeout", "must be greater than 0")
	case o.resync <= 0:
		return bad("-resync", "must be greater than 0")
	case o.serverCheck <= 0:
		return bad("-server-check", "must be greater than 0")
	}
	return nil
}

// client 建立指向明確 server 的 tmux 客戶端。run 必須明確指定 -L／-S；
// start／stop／diagnose 在沒有旗標時可沿用 $TMUX（TPM 在 run-shell 內執行時，它指向載入設定的那個 server）。
func (o *options) client(allowEnv bool) (*tmuxsrc.Client, error) {
	if o.socketName != "" && o.socketPath != "" {
		return nil, fmt.Errorf("-L and -S are mutually exclusive")
	}
	if o.socketName == "" && o.socketPath == "" && allowEnv {
		o.socketPath = socketFromEnv(os.Getenv("TMUX"))
	}
	if o.socketName == "" && o.socketPath == "" {
		return nil, fmt.Errorf("specify -L <name> or -S <path> explicitly; the default tmux server is not used")
	}
	tm := &tmuxsrc.Client{Width: o.width}
	if o.socketName != "" {
		tm.SocketArgs = []string{"-L", o.socketName}
	} else {
		tm.SocketArgs = []string{"-S", o.socketPath}
	}
	if o.config != "" {
		tm.ConfigArgs = []string{"-f", o.config}
	}
	return tm, nil
}

// socketFromEnv 取出 $TMUX（格式 <socket path>,<server pid>,<session>）中的 socket 路徑。
func socketFromEnv(v string) string {
	for i := 0; i < 2; i++ {
		j := strings.LastIndex(v, ",")
		if j < 0 {
			return ""
		}
		v = v[:j]
	}
	return v
}

// args 重建成 run 子行程的命令列（socket 一律明確）。
func (o *options) args() []string {
	a := []string{}
	if o.socketName != "" {
		a = append(a, "-L", o.socketName)
	} else {
		a = append(a, "-S", o.socketPath)
	}
	if o.config != "" {
		a = append(a, "-f", o.config)
	}
	a = append(a, "-tabs", o.tabs, "-service", o.service, "-width", strconv.Itoa(o.width),
		"-backstop", o.backstop.String(), "-idle-timeout", o.idle.String(), "-resync", o.resync.String(),
		"-server-check", o.serverCheck.String())
	if o.show {
		a = append(a, "-show")
	}
	return a
}

func main() {
	args := os.Args[1:]
	sub := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	var o options
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	o.bind(fs)
	_ = fs.Parse(args)
	var code int
	switch sub {
	case "run":
		code = runDaemon(&o)
	case "start":
		code = startInstance(&o)
	case "stop":
		code = stopInstance(&o)
	case "diagnose":
		code = diagnose(&o, os.Stdout)
	default:
		fmt.Fprintln(os.Stderr, usage)
		code = 2
	}
	os.Exit(code)
}
