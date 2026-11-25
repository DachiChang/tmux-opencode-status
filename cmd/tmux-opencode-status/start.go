package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/instance"
)

// Single instance and reload handoff (one instance per tmux socket):
//
//   - While alive, an instance holds an fcntl lock on <socket-derived>.lock; the lock file contains the instance's effective-settings fingerprint.
//     The lock is released automatically when the process exits (including kill -9), so there are no stale locks.
//   - start first acquires the <socket-derived>.start serialization lock, so only one concurrent start at a time decides and launches;
//     a later arrival that sees an instance with the same fingerprint returns immediately (idempotent).
//   - Different fingerprint (settings or executable changed): ask the old instance to hand off with SIGUSR1; it removes only its own hooks and exits,
//     leaving the display as is (no flicker, no forced sidebar toggling). The new instance starts only after the lock is released, so old and new never run at the same time.
//   - run also takes the lock: even if someone bypasses start and runs run directly, only one instance can exist.
const (
	startLockWait  = 20 * time.Second
	handoffWait    = 10 * time.Second
	spawnWait      = 10 * time.Second
	contentWait    = 3 * time.Second
	logKeepBytes   = 64 << 10
	logFailureTail = 4
)

func startInstance(o *options) int {
	// Validate before touching anything: a bad configuration must not affect an existing instance.
	if err := o.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	tm, err := o.client(true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
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
	gate, err := instance.Acquire(paths.Start, startLockWait)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Timed out waiting for another start command")
		return 1
	}
	defer gate.Release()

	// From here on, run is always started with an explicit socket path and does not depend on $TMUX.
	o.socketName, o.socketPath = "", info.SocketPath
	if o.socketPath == "" {
		fmt.Fprintln(os.Stderr, "tmux did not report a socket path")
		return 1
	}
	want := o.fingerprint(info.PID, tm.JumpTriggerSetting())

	if pid, held := instance.Holder(paths.Lock); held {
		got := waitContent(paths.Lock)
		if got == want {
			return 0 // An instance with the same settings already exists: a repeated load does nothing
		}
		if err := handOff(pid, paths.Lock); err != nil {
			fmt.Fprintln(os.Stderr, "Cannot hand off from the existing daemon:", err)
			return 1
		}
	}
	return spawn(o, paths)
}

// waitContent waits for the lock file content to be written (there is a very short gap between an instance acquiring the lock and writing its fingerprint).
func waitContent(path string) string {
	deadline := time.Now().Add(contentWait)
	for {
		if c := instance.ReadContent(path); c != "" || !time.Now().Before(deadline) {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// handOff asks the old instance holding the lock to hand off and waits for it to release the lock. SIGTERM is used only if the old instance does not respond.
func handOff(pid int, lockPath string) error {
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil && err != syscall.ESRCH {
		return err
	}
	if waitReleased(lockPath, handoffWait/2) {
		return nil
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	if waitReleased(lockPath, handoffWait/2) {
		return nil
	}
	return fmt.Errorf("previous daemon %d did not exit in time", pid)
}

func waitReleased(lockPath string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, held := instance.Holder(lockPath); !held {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	_, held := instance.Holder(lockPath)
	return !held
}

// spawn starts run as a background process detached from the terminal, and returns only once it actually holds the lock (or has failed early).
func spawn(o *options, paths instance.Paths) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot locate the plugin binary:", err)
		return 1
	}
	logf, err := openLog(paths.Log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot open the log file:", err)
		return 1
	}
	defer logf.Close()
	offset, _ := logf.Seek(0, io.SeekEnd)
	cmd := exec.Command(exe, append([]string{"run"}, o.args()...)...)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "Cannot start the daemon:", err)
		return 1
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	deadline := time.Now().Add(spawnWait)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			// The daemon exited early: insufficient capabilities, server not found, and so on. Relay the fixed message it wrote.
			fmt.Fprintln(os.Stderr, "Daemon startup failed:")
			fmt.Fprint(os.Stderr, logSince(paths.Log, offset))
			return 1
		default:
		}
		if pid, held := instance.Holder(paths.Lock); held && pid == cmd.Process.Pid && instance.ReadContent(paths.Lock) != "" {
			return 0
		}
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "Timed out waiting for the daemon to become ready")
	return 1
}

func openLog(path string) (*os.File, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > logKeepBytes {
		_ = os.Remove(path)
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func logSince(path string, offset int64) string {
	raw, err := os.ReadFile(path)
	if err != nil || int64(len(raw)) <= offset {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(raw[offset:]), "\n"), "\n")
	if len(lines) > logFailureTail {
		lines = lines[len(lines)-logFailureTail:]
	}
	return strings.Join(lines, "\n") + "\n"
}

func stopInstance(o *options) int {
	tm, err := o.client(true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	info, err := tm.Server()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot find tmux server:", err)
		return 1
	}
	paths, err := instance.PathsFor(info.SocketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	gate, err := instance.Acquire(paths.Start, startLockWait)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Timed out waiting for another start command")
		return 1
	}
	defer gate.Release()
	pid, held := instance.Holder(paths.Lock)
	if !held {
		fmt.Println("No daemon is running")
		return 0
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	if !waitReleased(paths.Lock, handoffWait) {
		fmt.Fprintf(os.Stderr, "Daemon %d did not exit in time\n", pid)
		return 1
	}
	fmt.Println("Stopped the daemon and cleaned up plugin resources")
	return 0
}
