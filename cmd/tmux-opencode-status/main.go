// tmux-opencode-status is a daemon: it writes the tab list of the running OpenCode TUIs on the same tmux server
// into the native side-status. Strictly read-only: it does not call OpenCode write APIs or start services; its only tmux bindings are click-to-jump, the prefix trigger key, and the opencode-status-jump key table.
//
// Subcommands (when omitted, equivalent to run, preserving the existing flag usage):
//
//	start     Idempotent start; called by TPM's .tmux script on every load / reload. An instance with the same settings is left alone;
//	          different settings or executable trigger a safe handoff, and a second instance is never left behind.
//	run       The daemon itself (start launches it in the background); -L / -S must be given explicitly.
//	stop      Stop the instance and clean up this plugin's resources.
//	diagnose  Read-only diagnostics; does not print credentials, prompts, conversations, or session titles.
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

// options holds the flags shared by all subcommands.
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

// validate checks settings before any are applied; when they are invalid, start must not touch an existing instance.
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

// client creates a tmux client pointing at an explicit server. run must specify -L / -S explicitly;
// start / stop / diagnose may fall back to $TMUX when no flag is given (when TPM runs inside run-shell, it points to the server that loaded the configuration).
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

// socketFromEnv extracts the socket path from $TMUX (format <socket path>,<server pid>,<session>).
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

// args rebuilds the command line for the run child process (the socket is always explicit).
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
