package tmuxsrc

import (
	"strings"

	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// JumpTriggerOption 是使用者設定觸發鍵的選項（插件只讀）；值只寫鍵名，一律綁在 prefix 表。
// 實際綁定成功的鍵是另一回事，記在 render.BoundTriggerOption。
// DefaultJumpTrigger 是選項未設定時的觸發鍵。
const (
	JumpTriggerOption  = "@opencode-status-jump-trigger"
	DefaultJumpTrigger = "A"
)

// JumpTrigger 是使用者對觸發鍵的設定。選項不存在（Set=false）與存在但值為空字串是兩種狀態：
// 前者用預設鍵，後者是無效設定。Unreadable 表示讀取設定本身失敗（不是任何一種設定值）。
type JumpTrigger struct {
	Value      string
	Set        bool
	Unreadable bool
}

// Key 是生效的觸發鍵名：未設定為預設 A；空字串（無效）與讀取失敗回傳空字串。
func (t JumpTrigger) Key() string {
	switch {
	case t.Unreadable:
		return ""
	case !t.Set:
		return DefaultJumpTrigger
	}
	return t.Value
}

// String 是設定的穩定文字（含未設定、空字串與讀取失敗的區別），供 start 的指紋與 diagnose 比對使用。
func (t JumpTrigger) String() string {
	switch {
	case t.Unreadable:
		return "unreadable"
	case !t.Set:
		return "unset"
	}
	return "set:" + t.Value
}

// JumpTriggerSetting 唯讀取得觸發鍵設定。以輸出長度區分未設定（空輸出）與空字串（單一換行）。
func (c *Client) JumpTriggerSetting() JumpTrigger {
	out, err := c.tmux("", "show-options", "-gqv", JumpTriggerOption)
	if err != nil {
		return JumpTrigger{Unreadable: true}
	}
	if len(out) == 0 {
		return JumpTrigger{}
	}
	return JumpTrigger{Value: strings.TrimSuffix(string(out), "\n"), Set: true}
}

// ActualJumpTrigger 讀取實際綁定的觸發鍵（@tos_jump_trigger）；沒有為空字串。
func (c *Client) ActualJumpTrigger() string {
	v, _ := c.GlobalValue(render.BoundTriggerOption)
	return v
}

// 綁定跳轉模式失敗的固定描述（寫進最近錯誤；不含原始錯誤或鍵名）。
const (
	JumpTriggerInvalidMessage = "Invalid trigger key setting; no trigger key bound"
	JumpTriggerFailedMessage  = "Trigger key binding failed; no trigger key bound"
)

// jumpTableBinding 是跳轉按鍵表所有字母（render.JumpLetters）的綁定：只在 tmux 內執行（不開 shell、不呼叫插件執行檔），
// 讀取 @tos_jump_key_<字母>（%N）切到該 pane；選項為空時不動作。
func jumpTableBinding() string {
	var b strings.Builder
	for _, l := range []byte(render.JumpLetters) {
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
// 依據 tmux cmd-list-keys.c（next-3.9 實測相同）：先檢查鍵名（無效 → "invalid key: <鍵>"），
// 之後才檢查按鍵表（"table <表> doesn't exist"），所以合法鍵名的探測一定停在後者。
// tmux 沒有不帶副作用的結構化驗證，bind-key／unbind-key 的失敗是 "unknown key"，且 bind 會真的改綁定。
func (c *Client) JumpKeyInvalid(key string) bool {
	if key == "" {
		return true
	}
	_, err := c.tmux("", "list-keys", "-T", "tos-probe-none", "--", keyArg(key))
	return err != nil && strings.Contains(err.Error(), "invalid key")
}

// BindJumpMode 在第一次發布前建立鍵盤跳轉的綁定，回傳要記入最近錯誤的固定描述（成功為空）：
//  1. 依設定（由呼叫端讀取；未設定 → A；空字串、讀取失敗或 tmux 不接受的鍵名無效）。
//  2. 上一實例留下的 @tos_jump_trigger 與新鍵不同（或新鍵無效）時先 unbind 舊鍵。
//  3. 新鍵有效時綁定 prefix <鍵> → switch-client -T opencode-status-jump，成功才記為實際綁定的鍵；
//     無效或失敗時不綁、不退回預設。
//  4. 跳轉按鍵表所有字母一律綁定（使用者的 root 別名可能用得到）。
func (c *Client) BindJumpMode(set JumpTrigger) string {
	c.jumpTrigger = ""
	var problem string
	key := set.Key()
	switch {
	case set.Unreadable:
		problem = JumpTriggerFailedMessage
	case key == "" || c.JumpKeyInvalid(key):
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
