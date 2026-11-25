// Package instance manages the local resources needed for "one resident instance per tmux socket":
// a lock file derived from the socket path (held while the instance is alive and released
// automatically when the process exits, even after kill -9, so there are no stale locks),
// a diagnostic state file, and a startup lock that serializes start. The files live in a
// per-user runtime/temp directory (0700), not in tmux's or OpenCode's data locations.
package instance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Paths are the local files that belong to a tmux socket.
type Paths struct {
	Dir   string
	Lock  string // instance lock: held while the resident process is alive
	State string // diagnostic state (counts and fixed descriptions only; no credentials, prompts, conversations, or titles)
	Start string // startup lock: serializes start for the same socket (decides whether to start or hand over)
	Log   string // stderr of the resident process
}

// RuntimeDir prefers $XDG_RUNTIME_DIR and otherwise uses the per-user temp directory ($TMPDIR on macOS).
func RuntimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	name := "tmux-opencode-status"
	if base == "" {
		base = os.TempDir()
		name += "-" + strconv.Itoa(os.Getuid())
	}
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create runtime directory: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || !fi.IsDir() || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("unsafe ownership or permissions on runtime directory %s (must be owned by the current user with mode 0700)", dir)
	}
	return dir, nil
}

// PathsFor derives file names from the socket path reported by tmux: different sockets are
// independent, and the same socket maps to the same set of files whether it is specified
// with -L or -S.
func PathsFor(socketPath string) (Paths, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return Paths{}, err
	}
	if real, err := filepath.EvalSymlinks(socketPath); err == nil {
		socketPath = real
	}
	sum := sha256.Sum256([]byte(socketPath))
	id := hex.EncodeToString(sum[:8])
	return Paths{
		Dir:   dir,
		Lock:  filepath.Join(dir, id+".lock"),
		State: filepath.Join(dir, id+".json"),
		Start: filepath.Join(dir, id+".start"),
		Log:   filepath.Join(dir, id+".log"),
	}, nil
}

// ErrHeld means the lock is already held by another process.
var ErrHeld = errors.New("lock is already held")

// Lock is an fcntl record lock. fcntl locks are not inherited by child processes, so tmux
// child processes started by the resident process cannot accidentally hold it.
type Lock struct {
	f    *os.File
	path string
}

func setlk(f *os.File, typ int16) error {
	l := unix.Flock_t{Type: typ, Whence: 0}
	return unix.FcntlFlock(f.Fd(), unix.F_SETLK, &l)
}

// Note: a POSIX record lock is released when the process closes any fd of the same file.
// So a process must not reopen a lock file it holds in order to query it (Holder is for
// use by other processes only).

// Acquire takes the exclusive lock; with a wait of 0 it does not wait, otherwise it polls for at most wait.
// After acquiring, it confirms the path still points to the same file: the holder deletes the lock file when it exits, so a late waiter does not end up locking an old, already-deleted file.
func Acquire(path string, wait time.Duration) (*Lock, error) {
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		switch err := setlk(f, unix.F_WRLCK); {
		case err == nil:
			a, aerr := f.Stat()
			b, berr := os.Stat(path)
			if aerr == nil && berr == nil && os.SameFile(a, b) {
				return &Lock{f: f, path: path}, nil
			}
			_ = f.Close() // the file was deleted by the previous holder: reopen
			continue
		case errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EACCES):
			_ = f.Close()
			if !time.Now().Before(deadline) {
				return nil, ErrHeld
			}
			time.Sleep(20 * time.Millisecond)
		default:
			_ = f.Close()
			return nil, err
		}
	}
}

// SetContent writes this instance's description (effective configuration fingerprint and owning server) into the lock file so that a later start can decide whether a handover is needed.
func (l *Lock) SetContent(v string) error {
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err := l.f.WriteAt([]byte(v), 0)
	return err
}

// ReadContent reads the lock file's content (the lock need not be held).
func ReadContent(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// Release deletes the lock file and then releases the lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = os.Remove(l.path)
	_ = l.f.Close()
	l.f = nil
}

// Holder queries the holder with F_GETLK (it does not take the lock, so it does not disturb an instance that is starting).
func Holder(path string) (pid int, held bool) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	l := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0}
	if err := unix.FcntlFlock(f.Fd(), unix.F_GETLK, &l); err != nil {
		return 0, false
	}
	if l.Type == unix.F_UNLCK {
		return 0, false
	}
	return int(l.Pid), true
}

// ErrorRecord is a recent problem: it holds only the source and a fixed description, never raw error text.
type ErrorRecord struct {
	At      time.Time `json:"at"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

// State is the state the resident process writes for diagnostics. Diagnostics read only this file and tmux; they do not connect to OpenCode.
type State struct {
	PID           int       `json:"pid"`
	ServerPID     int       `json:"serverPid"`
	Key           string    `json:"key"`
	Started       time.Time `json:"started"`
	Mode          string    `json:"mode"`
	ModeIssue     string    `json:"modeIssue,omitempty"`
	Tracking      bool      `json:"tracking"`
	TUIs          int       `json:"tuis"`
	Tabs          int       `json:"tabs"`
	Service       string    `json:"service"`
	ServiceReason string    `json:"serviceReason,omitempty"`
	Issues        []string  `json:"issues,omitempty"`
	// JumpTrigger is the trigger key setting the resident process read at startup (tmuxsrc.JumpTrigger.String()); diagnose uses it to tell that the "setting has not been applied yet".
	JumpTrigger string        `json:"jumpTrigger,omitempty"`
	Recent      []ErrorRecord `json:"recent,omitempty"`
}

// WriteState writes atomically using a temporary file plus rename.
func WriteState(path string, s State) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ReadState(path string) (State, error) {
	var s State
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s)
}
