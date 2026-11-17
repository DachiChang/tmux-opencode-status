// Package instance 管理「每個 tmux socket 一個常駐實例」所需的本機資源：
// 以 socket path 衍生的鎖檔（實例存活期間持有，行程結束即使 kill -9 也自動釋放，不會有過期鎖）、
// 診斷用狀態檔，以及序列化 start 的啟動鎖。檔案放在使用者專屬的 runtime／temp 目錄（0700），
// 不放進 tmux 或 OpenCode 的資料位置。
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

// Paths 是某個 tmux socket 對應的本機檔案。
type Paths struct {
	Dir   string
	Lock  string // 實例鎖：常駐程式存活期間持有
	State string // 診斷用狀態（只含計數與固定描述，不含憑證、prompt、對話或標題）
	Start string // 啟動鎖：序列化同一 socket 的 start（決定是否啟動／交接）
	Log   string // 常駐程式的 stderr
}

// RuntimeDir 優先使用 $XDG_RUNTIME_DIR，否則使用每位使用者專屬的 temp 目錄（macOS 的 $TMPDIR）。
func RuntimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	name := "tmux-opencode-status"
	if base == "" {
		base = os.TempDir()
		name += "-" + strconv.Itoa(os.Getuid())
	}
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("建立 runtime 目錄：%w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || !fi.IsDir() || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("runtime 目錄 %s 的擁有者或權限不安全（需為自己所有且 0700）", dir)
	}
	return dir, nil
}

// PathsFor 以 tmux 回報的 socket path 衍生檔名：不同 socket 各自獨立，
// 同一個 socket 不論以 -L 或 -S 指定都落在同一組檔案。
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

// ErrHeld 表示鎖已被其他行程持有。
var ErrHeld = errors.New("鎖已被持有")

// Lock 是一把 fcntl 記錄鎖。fcntl 鎖不會被子行程繼承，因此常駐程式啟動的 tmux 子行程不會意外持有它。
type Lock struct {
	f    *os.File
	path string
}

func setlk(f *os.File, typ int16) error {
	l := unix.Flock_t{Type: typ, Whence: 0}
	return unix.FcntlFlock(f.Fd(), unix.F_SETLK, &l)
}

// 注意：POSIX 記錄鎖在行程對「同一檔案的任何 fd」close 時就會釋放。
// 因此同一行程內不得對自己持有的鎖檔再開檔查詢（Holder 只供其他行程使用）。

// Acquire 取得獨占鎖；wait 為 0 時不等待，否則最多輪詢 wait。
// 取得後確認路徑仍指向同一個檔案：持有者結束時會刪除鎖檔，晚到的等待者因此不會鎖在已被刪除的舊檔上。
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
			_ = f.Close() // 檔案已被前任持有者刪除：重新開
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

// SetContent 把這個實例的描述（有效設定指紋與所屬 server）寫進鎖檔，供之後的 start 判斷是否需要交接。
func (l *Lock) SetContent(v string) error {
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err := l.f.WriteAt([]byte(v), 0)
	return err
}

// ReadContent 讀鎖檔內容（不需持有鎖）。
func ReadContent(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// Release 先刪除鎖檔再釋放鎖。
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = os.Remove(l.path)
	_ = l.f.Close()
	l.f = nil
}

// Holder 以 F_GETLK 查詢持有者（不取得鎖，因此不會干擾正要啟動的實例）。
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

// ErrorRecord 是一筆最近發生的問題：只含來源與固定描述，絕不放原始錯誤文字。
type ErrorRecord struct {
	At      time.Time `json:"at"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

// State 是常駐程式寫給診斷用的狀態。診斷只讀這份檔案與 tmux，不連 OpenCode。
type State struct {
	PID           int           `json:"pid"`
	ServerPID     int           `json:"serverPid"`
	Key           string        `json:"key"`
	Started       time.Time     `json:"started"`
	Mode          string        `json:"mode"`
	ModeIssue     string        `json:"modeIssue,omitempty"`
	Tracking      bool          `json:"tracking"`
	TUIs          int           `json:"tuis"`
	Tabs          int           `json:"tabs"`
	Service       string        `json:"service"`
	ServiceReason string        `json:"serviceReason,omitempty"`
	Issues        []string      `json:"issues,omitempty"`
	Recent        []ErrorRecord `json:"recent,omitempty"`
}

// WriteState 以暫存檔 + rename 原子寫入。
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
