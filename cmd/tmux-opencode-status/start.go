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

// 單實例與重載交接（每個 tmux socket 一個實例）：
//
//   - 實例存活期間持有 <socket 衍生>.lock 的 fcntl 鎖，鎖檔內容是該實例的有效設定指紋；
//     行程結束（含 kill -9）鎖自動釋放，沒有過期鎖。
//   - start 先取得 <socket 衍生>.start 序列化鎖，所以並行的 start 一次只有一個在判斷與啟動；
//     後到者看見已有同指紋的實例就直接返回（冪等）。
//   - 指紋不同（設定或執行檔改變）：以 SIGUSR1 要求舊實例交接——它只移除自己的 hook 就退出，
//     畫面維持原樣（不閃爍、不強制開關側欄）——鎖釋放後才啟動新實例。新舊從不同時運作。
//   - run 本身也會取鎖：即使有人繞過 start 直接執行 run，也只會有一個實例。
const (
	startLockWait  = 20 * time.Second
	handoffWait    = 10 * time.Second
	spawnWait      = 10 * time.Second
	contentWait    = 3 * time.Second
	logKeepBytes   = 64 << 10
	logFailureTail = 4
)

func startInstance(o *options) int {
	// 先驗證再動任何東西：錯誤設定不得影響既有實例。
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
	gate, err := instance.Acquire(paths.Start, startLockWait)
	if err != nil {
		fmt.Fprintln(os.Stderr, "等待其他 start 完成逾時")
		return 1
	}
	defer gate.Release()

	// 其後的 run 一律以明確 socket 路徑啟動，不依賴 $TMUX。
	o.socketName, o.socketPath = "", info.SocketPath
	if o.socketPath == "" {
		fmt.Fprintln(os.Stderr, "tmux 未回報 socket 路徑")
		return 1
	}
	trigger, triggerErr := tm.JumpTriggerSetting()
	want := o.fingerprint(info.PID, triggerFingerprint(trigger, triggerErr))

	if pid, held := instance.Holder(paths.Lock); held {
		got := waitContent(paths.Lock)
		if got == want {
			return 0 // 已有同設定的實例：重複載入什麼都不做
		}
		if err := handOff(pid, paths.Lock); err != nil {
			fmt.Fprintln(os.Stderr, "無法與既有實例交接：", err)
			return 1
		}
	}
	return spawn(o, paths)
}

// waitContent 等待鎖檔內容寫好（實例取鎖與寫入指紋之間有極短空窗）。
func waitContent(path string) string {
	deadline := time.Now().Add(contentWait)
	for {
		if c := instance.ReadContent(path); c != "" || !time.Now().Before(deadline) {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// handOff 要求持有鎖的舊實例交接並等它釋放鎖。舊實例不回應時才改用 SIGTERM。
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
	return fmt.Errorf("舊實例 %d 未在時限內結束", pid)
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

// spawn 以脫離終端的背景行程啟動 run，並等它真正持有鎖（或提早失敗）才返回。
func spawn(o *options, paths instance.Paths) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "找不到自己的執行檔：", err)
		return 1
	}
	logf, err := openLog(paths.Log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "無法開啟記錄檔：", err)
		return 1
	}
	defer logf.Close()
	offset, _ := logf.Seek(0, io.SeekEnd)
	cmd := exec.Command(exe, append([]string{"run"}, o.args()...)...)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "無法啟動常駐程式：", err)
		return 1
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	deadline := time.Now().Add(spawnWait)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			// 常駐程式提早結束：能力不足、找不到 server 等。轉述它寫的固定訊息。
			fmt.Fprintln(os.Stderr, "常駐程式啟動失敗：")
			fmt.Fprint(os.Stderr, logSince(paths.Log, offset))
			return 1
		default:
		}
		if pid, held := instance.Holder(paths.Lock); held && pid == cmd.Process.Pid && instance.ReadContent(paths.Lock) != "" {
			return 0
		}
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "常駐程式未在時限內就緒")
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
		fmt.Fprintln(os.Stderr, "找不到 tmux server：", err)
		return 1
	}
	paths, err := instance.PathsFor(info.SocketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	gate, err := instance.Acquire(paths.Start, startLockWait)
	if err != nil {
		fmt.Fprintln(os.Stderr, "等待其他 start 完成逾時")
		return 1
	}
	defer gate.Release()
	pid, held := instance.Holder(paths.Lock)
	if !held {
		fmt.Println("沒有執行中的常駐實例")
		return 0
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	if !waitReleased(paths.Lock, handoffWait) {
		fmt.Fprintf(os.Stderr, "常駐實例 %d 未在時限內結束\n", pid)
		return 1
	}
	fmt.Println("已停止常駐實例並清理本插件資源")
	return 0
}
