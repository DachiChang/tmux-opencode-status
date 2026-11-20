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
	"syscall"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
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
	// lastFormat 是本實例最後寫入的 side-status-format；停止時只有內容仍相同才移除，
	// 不覆蓋使用者之後自己設定的值。
	lastFormat string
	// jumpTrigger 是本實例實際綁定成功的觸發鍵；空字串表示沒有綁（無效或失敗）。每次發布一起寫入 @tos_jump_trigger。
	jumpTrigger string
	// proc 是讀取前景行程事實的來源；nil 使用本機實作（測試以受控實作替換）。
	proc procReader
}

// procReader 讀取前景行程的 argv 與工作目錄；alive 回報行程是否仍存在。
type procReader interface {
	Argv(pid int) ([]string, error)
	Cwd(pid int) (string, error)
	Alive(pid int) bool
}

type localProc struct{}

func (localProc) Argv(pid int) ([]string, error) { return procArgv(pid) }
func (localProc) Cwd(pid int) (string, error)    { return procCwd(pid) }
func (localProc) Alive(pid int) bool             { return ProcessAlive(pid) }

func (c *Client) procs() procReader {
	if c.proc != nil {
		return c.proc
	}
	return localProc{}
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

// optionHook 在模式選項或任何色票選項被設定（含 `set -gu` 與重載設定檔中的 set）時才喚醒，避免常駐程式自己
// 寫入側欄選項造成回授。
var optionHook = "after-set-option"

// titleHook 只用來提早重新掃描（在既有 shell 啟動／結束 TUI 時 tmux 沒有結構事件）；
// 不讀取標題內容，也不是辨識依據。終端標題關閉時不會觸發，由低頻補漏負責。
var titleHook = "pane-title-changed"

func optionHookCmd() string {
	arg := "#{hook_argument_0}"
	cond := "#{||:#{==:" + arg + "," + sidebar.ModeOption + "},#{m:" + sidebar.ThemeOptionPrefix + "*," + arg + "}}"
	return `if-shell -F "` + cond + `" "wait-for -S ` + wakeChannel + `"`
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
	var unresolved []string
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
			if err := c.readForeground(&fact, pid); err != nil {
				unresolved = append(unresolved, f[0])
				continue
			}
		}
		facts = append(facts, fact)
	}
	if len(unresolved) > 0 {
		return nil, fmt.Errorf("無法判定 %d 個 pane 的前景行程（%s）", len(unresolved), strings.Join(unresolved, "、"))
	}
	return facts, nil
}

// readForeground 讀取前景行程的 argv 與（TUI 候選的）工作目錄。
//   - 行程已結束：正常消失，不是錯誤；pane 此刻沒有可辨識的前景行程，下一次掃描就是新的事實。
//   - 行程仍在卻讀不到：只有 tmux 自己報告前景指令為 opencode 的 pane 才是無法判定的候選，
//     回報錯誤（呼叫端保留最後可用資料並提示）；其他 pane（例如屬於其他使用者的行程）
//     不可能是使用者的 OpenCode TUI，不因此讓整個側欄失效。
func (c *Client) readForeground(fact *sidebar.PaneFact, pid int) error {
	pr := c.procs()
	candidate := fact.PaneCurrentCommand == "opencode"
	argv, err := pr.Argv(pid)
	if err != nil {
		if !pr.Alive(pid) || !candidate {
			return nil
		}
		return fmt.Errorf("讀取行程 argv：%w", err)
	}
	fact.ForegroundArgv = argv
	if !contract.IsTUICandidate(fact.Pane) {
		return nil
	}
	cwd, err := pr.Cwd(pid)
	if err != nil {
		if !pr.Alive(pid) {
			fact.ForegroundArgv = nil
			return nil
		}
		return fmt.Errorf("讀取行程工作目錄：%w", err)
	}
	fact.ForegroundCwd = cwd
	return nil
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

// Publish 以單一 source-file（同一 command queue）寫入版面選項與 side-status-format，
// 並清掉同前綴下不再使用的選項（前一個實例或崩潰留下的）。client 永遠不會看到半更新。
func (c *Client) Publish(p render.Published) error {
	var s strings.Builder
	s.WriteString("set-option -g side-status-width " + strconv.Itoa(c.Width) + "\n")
	opts := make(map[string]string, len(p.Options)+1)
	for n, v := range p.Options {
		opts[n] = v
	}
	if c.jumpTrigger != "" {
		// 實際綁定的觸發鍵與版面一起寫入：交接的新實例與 stop 都從這裡知道要 unbind 哪個鍵。
		opts[render.JumpTriggerOption] = c.jumpTrigger
	}
	names := make([]string, 0, len(opts))
	for n := range opts {
		names = append(names, n)
	}
	sort.Strings(names)
	if c.published == nil {
		// 第一次寫入：本插件專屬前綴下既有的選項（前一個實例或崩潰留下的）一併由這次寫入清掉。
		c.published = c.existingPluginOptions()
	}
	next := map[string]bool{}
	for _, n := range names {
		next[n] = true
		s.WriteString("set-option -g " + n + " " + quote(opts[n]) + "\n")
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
	c.lastFormat = p.Format()
	return nil
}

// existingPluginOptions 列出目前全域中屬於本插件前綴（@tos_）的內部選項。讀取失敗視為沒有（之後的寫入會補上新內容）。
func (c *Client) existingPluginOptions() map[string]bool {
	out, err := c.tmux("", "show-options", "-g")
	found := map[string]bool{}
	if err != nil {
		return found
	}
	for _, line := range strings.Split(string(out), "\n") {
		if name, _, _ := strings.Cut(line, " "); strings.HasPrefix(name, render.OptPrefix) {
			found[name] = true
		}
	}
	return found
}

// ClearDisplay 在常駐程式因明確停止而退出（server 仍在）時，移除本插件發布的內部選項（@tos_*），
// 並在 side-status-format 仍是本實例寫入的內容時把它與 width 重設為 tmux 預設，避免留下不再更新的過期內容。
// 只動本插件前綴的選項與它自己寫入的全域選項；不碰其他使用者選項。
func (c *Client) ClearDisplay() {
	var b strings.Builder
	if cur, err := c.tmux("", "show-options", "-gqv", "side-status-format"); err == nil && c.lastFormat != "" &&
		strings.TrimSuffix(string(cur), "\n") == c.lastFormat {
		b.WriteString("set-option -gu side-status-format\nset-option -gu side-status-width\n")
	}
	for name := range c.existingPluginOptions() {
		b.WriteString("set-option -gu " + name + "\n")
	}
	_, _ = c.tmux(b.String(), "source-file", "-")
}

// JumpKey 是側欄點擊跳轉接管的 root 鍵表按鍵（tmux 對 side-status 的滑鼠左鍵按下事件）。
const JumpKey = "MouseDown1SideStatus"

// jumpBinding 只在 tmux 內執行（不開 shell、不呼叫插件執行檔）：range 字串符合 oc<N>（前綴見 render.JumpRangePrefix）時切到 pane %N
// （session、window、pane 一次到位，可跨 session），否則維持 tmux 預設的 switch-client -t =。
// 不用 range=pane：見 ADR-0002。
const jumpBinding = `bind -T root ` + JumpKey + ` {
  if -F "#{m/r:^` + render.JumpRangePrefix + `[0-9]+$,#{mouse_status_range}}" {
    run-shell -C "switch-client -t '%#{s/^` + render.JumpRangePrefix + `//:mouse_status_range}'"
  } {
    switch-client -t =
  }
}
`

// BindJump 接管 MouseDown1SideStatus。與 Publish 相同以 source-file 寫入，避免引號跳脫問題；
// 重複寫入同一內容是冪等的（交接時由新實例再寫一次）。
func (c *Client) BindJump() error {
	_, err := c.tmux(jumpBinding, "source-file", "-")
	return err
}

// ResetJump 在明確停止時把 MouseDown1SideStatus 寫回 tmux 預設。不 unbind：停止後
// side-status-format 回到 tmux 預設，預設格式的 session／window 點擊切換依賴這條預設綁定。
// 不保存或還原使用者原有的自訂綁定。
func (c *Client) ResetJump() {
	_, _ = c.tmux("", "bind-key", "-T", "root", JumpKey, "switch-client", "-t", "=")
}

// JumpTriggerOption 是使用者設定觸發鍵的選項（插件只讀）；值只寫鍵名，一律綁在 prefix 表。
// DefaultJumpTrigger 是選項未設定時的觸發鍵。
const (
	JumpTriggerOption  = "@opencode-status-jump-trigger"
	DefaultJumpTrigger = "A"
)

// JumpTrigger 是使用者對觸發鍵的設定。選項不存在（Set=false）與存在但值為空字串是兩種狀態：
// 前者用預設鍵，後者是無效設定。
type JumpTrigger struct {
	Value string
	Set   bool
}

// Key 是生效的觸發鍵名：未設定為預設 A；空字串（無效）回傳空字串。
func (t JumpTrigger) Key() string {
	if !t.Set {
		return DefaultJumpTrigger
	}
	return t.Value
}

// String 是設定的穩定文字（含未設定與空字串的區別），供 start 的指紋使用。
func (t JumpTrigger) String() string {
	if !t.Set {
		return "unset"
	}
	return "set:" + t.Value
}

// JumpTriggerSetting 唯讀取得觸發鍵設定。以輸出長度區分未設定（空輸出）與空字串（單一換行）。
func (c *Client) JumpTriggerSetting() (JumpTrigger, error) {
	out, err := c.tmux("", "show-options", "-gqv", JumpTriggerOption)
	if err != nil {
		return JumpTrigger{}, err
	}
	if len(out) == 0 {
		return JumpTrigger{}, nil
	}
	return JumpTrigger{Value: strings.TrimSuffix(string(out), "\n"), Set: true}, nil
}

// ActualJumpTrigger 讀取實際綁定的觸發鍵（@tos_jump_trigger）；沒有為空字串。
func (c *Client) ActualJumpTrigger() string {
	v, _ := c.GlobalValue(render.JumpTriggerOption)
	return v
}

// 綁定跳轉模式失敗的固定描述（寫進最近錯誤；不含原始錯誤或鍵名）。
const (
	JumpTriggerInvalidMessage = "觸發鍵設定無效，未綁定任何觸發鍵"
	JumpTriggerFailedMessage  = "觸發鍵綁定失敗，未綁定任何觸發鍵"
)

// jumpTableBinding 是跳轉按鍵表 a–z 的綁定：只在 tmux 內執行（不開 shell、不呼叫插件執行檔），
// 讀取 @tos_jump_key_<字母>（%N）切到該 pane；選項為空時不動作。
func jumpTableBinding() string {
	var b strings.Builder
	for l := byte('a'); l <= 'z'; l++ {
		opt := render.JumpKeyOption(l)
		b.WriteString("bind-key -T " + render.JumpTable + " " + string(l) + " if-shell -F \"#{" + opt + "}\" { run-shell -C \"switch-client -t '#{" + opt + "}'\" }\n")
	}
	return b.String()
}

// keyQuote 把鍵名轉成設定檔的雙引號字串：除了 quote 的跳脫，開頭的 ~ 會被 tmux 展開成家目錄，要跳脫。
func keyQuote(k string) string {
	return strings.Replace(quote(k), `"~`, `"\~`, 1)
}

// keyArg 是以 exec 引數傳給 tmux 指令的鍵名：結尾的 ; 會被當成指令分隔符，要跳脫。
func keyArg(k string) string {
	if strings.HasSuffix(k, ";") && !strings.HasSuffix(k, `\;`) {
		return strings.TrimSuffix(k, ";") + `\;`
	}
	return k
}

// JumpKeyInvalid 回報 tmux 是否認為這個鍵名無效（唯讀：查一個不存在的按鍵表）。
func (c *Client) JumpKeyInvalid(key string) bool {
	if key == "" {
		return true
	}
	_, err := c.tmux("", "list-keys", "-T", "tos-probe-none", "--", keyArg(key))
	return err != nil && strings.Contains(err.Error(), "invalid key")
}

// BindJumpMode 在第一次發布前建立鍵盤跳轉的綁定，回傳要記入最近錯誤的固定描述（成功為空）：
//  1. 依設定（由呼叫端讀取；未設定 → A；空字串或 tmux 不接受的鍵名無效）。
//  2. 上一實例留下的 @tos_jump_trigger 與新鍵不同（或新鍵無效）時先 unbind 舊鍵。
//  3. 新鍵有效時綁定 prefix <鍵> → switch-client -T opencode-status-jump，成功才記為實際綁定的鍵；
//     無效或失敗時不綁、不退回預設。
//  4. 跳轉按鍵表 a–z 一律綁定（使用者的 root 別名可能用得到）。
func (c *Client) BindJumpMode(set JumpTrigger, readErr error) string {
	c.jumpTrigger = ""
	var problem string
	err := readErr
	key := set.Key()
	if err != nil {
		key, problem = "", JumpTriggerFailedMessage
	} else if key == "" || c.JumpKeyInvalid(key) {
		key, problem = "", JumpTriggerInvalidMessage
	}
	if old := c.ActualJumpTrigger(); old != "" && old != key {
		c.unbindPrefix(old)
	}
	if key != "" {
		cmd := "bind-key -T prefix -- " + keyQuote(key) + " switch-client -T " + render.JumpTable + "\n"
		if _, err := c.tmux(cmd, "source-file", "-"); err != nil {
			problem = JumpTriggerFailedMessage
		} else {
			c.jumpTrigger = key
		}
	}
	if _, err := c.tmux(jumpTableBinding(), "source-file", "-"); err != nil && problem == "" {
		problem = JumpTriggerFailedMessage
	}
	return problem
}

func (c *Client) unbindPrefix(key string) {
	_, _ = c.tmux("unbind-key -T prefix -- "+keyQuote(key)+"\n", "source-file", "-")
}

// ClearJumpMode 在明確停止時移除觸發鍵綁定並清空跳轉按鍵表；不寫回、不還原任何綁定。
// 必須先於 ClearDisplay：後者會清掉 @tos_jump_trigger，之後就不知道要 unbind 哪個鍵。
func (c *Client) ClearJumpMode() {
	if key := c.ActualJumpTrigger(); key != "" {
		c.unbindPrefix(key)
	}
	_, _ = c.tmux("", "unbind-key", "-a", "-T", render.JumpTable) // 表不存在時 tmux 回錯，忽略
}

// PrefixBindingIsJump 回報 prefix 表的 key 是否仍是切到跳轉按鍵表的綁定（唯讀）。
func (c *Client) PrefixBindingIsJump(key string) bool {
	out, err := c.tmux("", "list-keys", "-F", "#{key_command}", "-T", "prefix", "--", keyArg(key))
	return err == nil && strings.TrimSpace(string(out)) == "switch-client -T "+render.JumpTable
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

const (
	swatchNameSep = "\x1f"
	swatchEnd     = "\x1e"
)

// Swatches 以一次 `show-options -g` 唯讀取得所有色票選項的原始值，鍵為去掉前綴的色票名稱。
// 值原樣回傳（含空字串與格式字元），是否有效由呼叫端判斷；用控制字元分隔而不解析 tmux 的跳脫輸出，
// 因此含換行或引號的值也不會被誤讀。讀取失敗回報錯誤，不當成全部未設定。
func (c *Client) Swatches() (map[string]string, error) {
	out, err := c.tmux("", "show-options", "-g", "-F", "#{option_name}"+swatchNameSep+"#{option_value}"+swatchEnd)
	if err != nil {
		return nil, err
	}
	res := map[string]string{}
	for _, rec := range strings.Split(string(out), swatchEnd) {
		name, val, ok := strings.Cut(strings.TrimPrefix(rec, "\n"), swatchNameSep)
		if !ok {
			continue
		}
		if swatch, ok := strings.CutPrefix(name, sidebar.ThemeOptionPrefix); ok && swatch != "" {
			res[swatch] = val
		}
	}
	return res, nil
}

// MissingCapabilities 列出此 tmux 缺少的必要能力（3.9 的 side-status 相關選項）。
// 選項不存在（invalid option）才算缺少；其他錯誤（server 暫時不可用）原樣回報，不誤判成缺少能力。
func (c *Client) MissingCapabilities() ([]string, error) {
	var missing []string
	for _, opt := range []string{"side-status", "side-status-width", "side-status-format"} {
		if _, err := c.tmux("", "show-options", "-gv", opt); err != nil {
			if strings.Contains(err.Error(), "invalid option") {
				missing = append(missing, opt)
				continue
			}
			return nil, err
		}
	}
	return missing, nil
}

// SessionOverrides 列出在 session 層級覆寫了 side-status 相關選項的 tmux session（只回 session id 與選項名稱，
// 不回 session 名稱）。這些 session 不會跟隨全域設定，插件也絕不清除它們。
func (c *Client) SessionOverrides() ([]SessionOverride, error) {
	out, err := c.tmux("", "list-sessions", "-F", "#{session_id}")
	if err != nil {
		return nil, err
	}
	var res []SessionOverride
	for _, id := range strings.Fields(string(out)) {
		opts, err := c.tmux("", "show-options", "-t", id)
		if err != nil {
			continue // session 在查詢之間消失
		}
		o := SessionOverride{TmuxSession: id}
		for _, line := range strings.Split(string(opts), "\n") {
			name, val, _ := strings.Cut(line, " ")
			if name == "side-status" || name == "side-status-width" || name == "side-status-format" {
				if name == "side-status-format" {
					val = "（自訂格式）"
				}
				o.Options = append(o.Options, name+"="+val)
			}
		}
		if len(o.Options) > 0 {
			res = append(res, o)
		}
	}
	return res, nil
}

// SessionOverride 是一個 session 的 side-status 覆寫。
type SessionOverride struct {
	TmuxSession string
	Options     []string
}

// GlobalValue 唯讀取得全域選項的值。
func (c *Client) GlobalValue(name string) (string, error) {
	out, err := c.tmux("", "show-options", "-gqv", name)
	return strings.TrimSuffix(string(out), "\n"), err
}

// ShownMarker 記錄「本 server 已由插件顯示過側欄」；之後的重載、交接或重啟都不再替使用者打開，
// 以免把使用者已經隱藏的側欄重新打開。選項隨 tmux server 結束而消失。
const ShownMarker = "@opencode-status-shown"

// Show 把 side-status 設為 left，且每個 tmux server 生命週期只做一次（僅在使用者明確要求時由呼叫端使用）。
func (c *Client) Show() error {
	out, err := c.tmux("", "show-options", "-gqv", ShownMarker)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "" {
		return nil
	}
	_, err = c.tmux("", "set-option", "-g", "side-status", "left", ";", "set-option", "-g", ShownMarker, "1")
	return err
}

// ClearShownMarker 供明確停止時移除（讓之後重新啟動可再次顯示）。
func (c *Client) ClearShownMarker() {
	_, _ = c.tmux("", "set-option", "-gu", ShownMarker)
}

// ServerInfo 是這個 socket 上 tmux server 的身分。
type ServerInfo struct {
	SocketPath string
	PID        int
	Version    string
}

// Server 詢問 server 的 socket 路徑、行程與版本；server 不在時回報錯誤。
func (c *Client) Server() (ServerInfo, error) {
	out, err := c.tmux("", "display-message", "-p", "#{socket_path}\t#{pid}\t#{version}")
	if err != nil {
		return ServerInfo{}, err
	}
	f := strings.Split(strings.TrimRight(string(out), "\n"), "\t")
	if len(f) != 3 {
		return ServerInfo{}, fmt.Errorf("tmux display-message 回應格式不符")
	}
	pid, perr := strconv.Atoi(f[1])
	if perr != nil || f[0] == "" {
		return ServerInfo{}, fmt.Errorf("tmux display-message 回應格式不符")
	}
	return ServerInfo{SocketPath: f[0], PID: pid, Version: f[2]}, nil
}

// ProcessAlive 回報行程是否仍存在。tmux server 結束與否以它的行程為準，
// 而不是以單次指令失敗判斷：指令可能因一時忙碌或環境問題失敗，行程不在才是確定的結束。
func ProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
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
	if _, err := c.tmux("", "set-hook", "-g", optionHook+"["+strconv.Itoa(hookIndex)+"]", optionHookCmd()); err != nil {
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
	_, _ = c.tmux("", "set-hook", "-gu", optionHook+"["+strconv.Itoa(hookIndex)+"]")
	_, _ = c.tmux("", "set-hook", "-gu", titleHook+"["+strconv.Itoa(hookIndex)+"]")
	for _, h := range wakeHooks {
		_, _ = c.tmux("", "set-hook", "-gu", h+"["+strconv.Itoa(hookIndex)+"]")
	}
}
