// Package tmuxsrc 是對單一 tmux server 的轉接：pane 事實、側欄輸出與變動通知。
// 每個指令都明確指向建構時給定的 socket，絕不使用預設 server。
package tmuxsrc

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/heightfit"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
)

// Client 指向一個明確的 tmux server。SocketArgs 例如 ["-L","name"] 或 ["-S","/path"]。
type Client struct {
	SocketArgs []string
	// ConfigArgs 讓隔離測試可附帶 -f /dev/null；正式使用為空（server 已在執行）。
	ConfigArgs []string
	// Width 是 side-status-width（含 1 格分隔線）。
	Width     int
	published map[string]bool
}

const (
	wakeChannel = "tos_wake"
	// titleChannel 專用於 pane title 變更：TUI 可能頻繁改標題，需要與結構事件分開節流。
	titleChannel = "tos_title"
	// titleMinGap 是標題喚醒的最小間隔：靜止後的第一次變更立即喚醒（leading），
	// 間隔內的連續變更由 tmux 合併為一次記住的 signal，間隔結束後補一次（trailing）。
	titleMinGap = 300 * time.Millisecond
	// hookIndex 是本插件在 hook 陣列使用的專屬索引，避免覆蓋使用者自己的 hook。
	hookIndex = 41700
)

// 這些 hook 只用來「喚醒」同步；實際內容一律重新讀取，漏掉任何一個都由低頻補漏補上。
var wakeHooks = []string{
	"pane-exited", "window-linked", "window-unlinked",
	"after-new-window", "after-split-window", "after-kill-pane",
	"session-created", "session-closed",
}

// 模式選項被設定（含 `set -gu` 與重載設定檔中的 set）時才喚醒，避免常駐程式自己
// 寫入側欄選項造成回授。
var modeHook = "after-set-option"

// titleHook 只用來提早重新掃描（在既有 shell 啟動／結束 TUI 時 tmux 沒有結構事件）；
// 不讀取標題內容，也不是辨識依據。終端標題關閉時不會觸發，由低頻補漏負責。
var titleHook = "pane-title-changed"

func modeHookCmd() string {
	return `if-shell -F "#{==:#{hook_argument_0},` + sidebar.ModeOption + `}" "wait-for -S ` + wakeChannel + `"`
}

func (c *Client) tmux(stdin string, args ...string) ([]byte, error) {
	full := append(append(append([]string{}, c.SocketArgs...), c.ConfigArgs...), args...)
	cmd := exec.Command("tmux", full...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tmux %s：%w：%s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// Panes 列出同一 server 所有 pane 的前景行程事實；同一 pane 經 linked window 重複列出時只取一次。
func (c *Client) Panes() ([]sidebar.PaneFact, error) {
	out, err := c.tmux("", "list-panes", "-a", "-F", "#{pane_id}\t#{pane_tty}\t#{pane_current_path}\t#{pane_current_command}\t#{alternate_on}")
	if err != nil {
		return nil, err
	}
	procs, err := foregroundProcs()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	facts := []sidebar.PaneFact{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		fact := sidebar.PaneFact{PaneID: f[0], Pane: contract.Pane{
			PaneCurrentPath: f[2], PaneCurrentCommand: f[3], AlternateOn: f[4] == "1",
		}}
		if pid, ok := procs[strings.TrimPrefix(f[1], "/dev/")]; ok {
			if argv, err := procArgv(pid); err == nil {
				fact.ForegroundArgv = argv
				if contract.IsTUICandidate(fact.Pane) {
					fact.ForegroundCwd = procCwd(pid)
				}
			}
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// foregroundProcs 回傳 tty 名稱 → 前景行程組組長（pgid == tpgid）。pane_pid 通常是 shell，不能用。
func foregroundProcs() (map[string]int, error) {
	out, err := exec.Command("ps", "-axo", "pid=,pgid=,tpgid=,tty=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps：%w", err)
	}
	res := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 4 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		pgid, _ := strconv.Atoi(f[1])
		tpgid, _ := strconv.Atoi(f[2])
		if pid > 0 && pgid == tpgid && pid == pgid {
			res[f[3]] = pid
		}
	}
	return res, nil
}

// Publish 以單一 source-file（同一 command queue）寫入所有版面選項與 side-status-format，
// 並清掉不再使用的版面選項。client 永遠不會看到半更新。
func (c *Client) Publish(p heightfit.Published) error {
	var s strings.Builder
	s.WriteString("set-option -g side-status-width " + strconv.Itoa(c.Width) + "\n")
	names := make([]string, 0, len(p.Options))
	for n := range p.Options {
		names = append(names, n)
	}
	sort.Strings(names)
	next := map[string]bool{}
	for _, n := range names {
		next[n] = true
		s.WriteString("set-option -g " + n + " " + quote(p.Options[n]) + "\n")
	}
	s.WriteString("set-option -g side-status-format " + quote(p.Format()) + "\n")
	for old := range c.published {
		if !next[old] {
			s.WriteString("set-option -gu " + old + "\n")
		}
	}
	if _, err := c.tmux(s.String(), "source-file", "-"); err != nil {
		return err
	}
	c.published = next
	return nil
}

// quote 轉成 tmux 設定檔的雙引號字串。
func quote(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "\n", `\n`).Replace(v) + `"`
}

// Setting 唯讀取得全域模式選項。選項不存在（輸出為空）與存在但值為空字串（輸出為單一換行）
// 是兩種狀態；值本身只去掉 tmux 加上的一個結尾換行，不做任何修剪。
func (c *Client) Setting() (sidebar.ModeSetting, error) {
	out, err := c.tmux("", "show-options", "-gqv", sidebar.ModeOption)
	if err != nil {
		return sidebar.ModeSetting{}, err
	}
	if len(out) == 0 {
		return sidebar.ModeSetting{}, nil
	}
	return sidebar.ModeSetting{Value: strings.TrimSuffix(string(out), "\n"), Set: true}, nil
}

// Show 把 side-status 設為 left（僅在使用者明確要求時由呼叫端使用）。
func (c *Client) Show() error {
	_, err := c.tmux("", "set-option", "-g", "side-status", "left")
	return err
}

// WatchEvents 安裝喚醒用 hook，並以 `tmux wait-for` 阻塞等待；每次被喚醒送出一次通知。
// tmux 會記住沒有等待者時的 signal，因此兩次 wait-for 之間的變動不會遺失。結束時移除 hook。
func (c *Client) WatchEvents(ctx context.Context, wake chan<- struct{}) error {
	for _, h := range wakeHooks {
		if _, err := c.tmux("", "set-hook", "-g", h+"["+strconv.Itoa(hookIndex)+"]", "wait-for -S "+wakeChannel); err != nil {
			c.RemoveHooks()
			return err
		}
	}
	if _, err := c.tmux("", "set-hook", "-g", modeHook+"["+strconv.Itoa(hookIndex)+"]", modeHookCmd()); err != nil {
		c.RemoveHooks()
		return err
	}
	if _, err := c.tmux("", "set-hook", "-g", titleHook+"["+strconv.Itoa(hookIndex)+"]", "wait-for -S "+titleChannel); err != nil {
		c.RemoveHooks()
		return err
	}
	var titleLoop sync.WaitGroup
	titleLoop.Add(1)
	go func() {
		defer titleLoop.Done()
		c.waitLoop(ctx, titleChannel, titleMinGap, wake)
	}()
	defer titleLoop.Wait()
	defer c.RemoveHooks()
	// hook 裝好之前發生的變動不會有通知；裝好後補一次喚醒，讓啟動空窗內的變動不必等低頻補漏。
	select {
	case wake <- struct{}{}:
	default:
	}
	c.waitLoop(ctx, wakeChannel, 0, wake)
	return nil
}

// waitLoop 以 `tmux wait-for <channel>` 阻塞等待，每次被喚醒送出一次通知，之後至少間隔 minGap
// 才再次等待（0 表示不節流）。節流期間的 signal 由 tmux 記住並合併，不會遺失也不會累積。
func (c *Client) waitLoop(ctx context.Context, channel string, minGap time.Duration, wake chan<- struct{}) {
	for ctx.Err() == nil {
		args := append(append(append([]string{}, c.SocketArgs...), c.ConfigArgs...), "wait-for", channel)
		cmd := exec.CommandContext(ctx, "tmux", args...)
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return
			}
			select { // server 暫時不可用：稍後重試，同步由低頻補漏負責
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		select {
		case wake <- struct{}{}:
		default:
		}
		if minGap > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(minGap):
			}
		}
	}
}

// RemoveHooks 只移除本插件安裝的 hook 索引，不動使用者自己的 hook。
func (c *Client) RemoveHooks() {
	_, _ = c.tmux("", "set-hook", "-gu", modeHook+"["+strconv.Itoa(hookIndex)+"]")
	_, _ = c.tmux("", "set-hook", "-gu", titleHook+"["+strconv.Itoa(hookIndex)+"]")
	for _, h := range wakeHooks {
		_, _ = c.tmux("", "set-hook", "-gu", h+"["+strconv.Itoa(hookIndex)+"]")
	}
}
