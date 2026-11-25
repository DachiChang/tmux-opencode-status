package tmuxsrc

import (
	"strings"

	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// JumpTriggerOption is the option where the user configures the trigger key (read-only for the plugin); the value is only a key name, and it is always bound in the prefix table.
// The key that was actually bound successfully is a separate matter, recorded in render.BoundTriggerOption.
// DefaultJumpTrigger is the trigger key used when the option is unset.
const (
	JumpTriggerOption  = "@opencode-status-jump-trigger"
	DefaultJumpTrigger = "A"
)

// JumpTrigger is the user's trigger key setting. An option that does not exist (Set=false) and one that exists with an empty string are two different states:
// the former uses the default key, the latter is an invalid setting. Unreadable means reading the setting itself failed (it is not any kind of setting value).
type JumpTrigger struct {
	Value      string
	Set        bool
	Unreadable bool
}

// Key is the effective trigger key name: the default A when unset; an empty string (invalid) or a read failure returns an empty string.
func (t JumpTrigger) Key() string {
	switch {
	case t.Unreadable:
		return ""
	case !t.Set:
		return DefaultJumpTrigger
	}
	return t.Value
}

// String is a stable text form of the setting (distinguishing unset, empty string, and read failure), used by start's fingerprint and diagnose comparisons.
func (t JumpTrigger) String() string {
	switch {
	case t.Unreadable:
		return "unreadable"
	case !t.Set:
		return "unset"
	}
	return "set:" + t.Value
}

// JumpTriggerSetting reads the trigger key setting read-only. Output length distinguishes unset (empty output) from an empty string (a single newline).
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

// ActualJumpTrigger reads the trigger key that is actually bound (@tos_jump_trigger); empty string if there is none.
func (c *Client) ActualJumpTrigger() string {
	v, _ := c.GlobalValue(render.BoundTriggerOption)
	return v
}

// Fixed description of a failure to bind jump mode (written into the last error; contains neither the raw error nor the key name).
const (
	JumpTriggerInvalidMessage = "Invalid trigger key setting; no trigger key bound"
	JumpTriggerFailedMessage  = "Trigger key binding failed; no trigger key bound"
)

// jumpTableBinding is the binding for every letter of the jump key table (render.JumpLetters): it runs only inside tmux (no shell, no call to the plugin executable),
// reads @tos_jump_key_<letter> (%N) and switches to that pane; it does nothing when the option is empty.
func jumpTableBinding() string {
	var b strings.Builder
	for _, l := range []byte(render.JumpLetters) {
		opt := render.JumpKeyOption(l)
		b.WriteString("bind-key -T " + render.JumpTable + " " + string(l) + " if-shell -F \"#{" + opt + "}\" { run-shell -C \"switch-client -t '#{" + opt + "}'\" }\n")
	}
	return b.String()
}

// keyQuote converts a key name into a double-quoted string for the config file: besides quote's escaping, a leading ~ would be expanded by tmux to the home directory, so it must be escaped.
func keyQuote(k string) string {
	return strings.Replace(quote(k), `"~`, `"\~`, 1)
}

// keyArg is the key name passed to a tmux command as an exec argument: a trailing ; would be treated as a command separator, so it must be escaped.
func keyArg(k string) string {
	if strings.HasSuffix(k, ";") && !strings.HasSuffix(k, `\;`) {
		return strings.TrimSuffix(k, ";") + `\;`
	}
	return k
}

// JumpKeyInvalid reports whether tmux considers this key name invalid (read-only: it queries a key table that does not exist).
// Per tmux cmd-list-keys.c (confirmed identical on next-3.9): the key name is checked first (invalid → "invalid key: <key>"),
// and only then the key table ("table <table> doesn't exist"), so probing with a valid key name always stops at the latter.
// tmux has no side-effect-free structured validation: bind-key/unbind-key fail with "unknown key", and bind really does change the binding.
func (c *Client) JumpKeyInvalid(key string) bool {
	if key == "" {
		return true
	}
	_, err := c.tmux("", "list-keys", "-T", "tos-probe-none", "--", keyArg(key))
	return err != nil && strings.Contains(err.Error(), "invalid key")
}

// BindJumpMode creates the keyboard-jump bindings before the first publish and returns the fixed description to record as the last error (empty on success):
//  1. Per the setting (read by the caller; unset → A; an empty string, a read failure, or a key name tmux does not accept is invalid).
//  2. When the @tos_jump_trigger left by the previous instance differs from the new key (or the new key is invalid), unbind the old key first.
//  3. When the new key is valid, bind prefix <key> → switch-client -T opencode-status-jump, and record it as the actually bound key only on success;
//     when it is invalid or fails, do not bind and do not fall back to the default.
//  4. Always bind every letter of the jump key table (the user's root aliases may use them).
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

// ClearJumpMode removes the trigger key binding and clears the jump key table on explicit stop; it does not write back or restore any binding.
// It must run before ClearDisplay: the latter clears @tos_jump_trigger, after which we would no longer know which key to unbind.
func (c *Client) ClearJumpMode() {
	if key := c.ActualJumpTrigger(); key != "" {
		c.unbindPrefix(key)
	}
	_, _ = c.tmux("", "unbind-key", "-a", "-T", render.JumpTable) // when the table does not exist tmux returns an error, which is ignored
}

// PrefixBindingIsJump reports whether a key in the prefix table is still a binding that switches to the jump key table (read-only).
func (c *Client) PrefixBindingIsJump(key string) bool {
	out, err := c.tmux("", "list-keys", "-F", "#{key_command}", "-T", "prefix", "--", keyArg(key))
	return err == nil && strings.TrimSpace(string(out)) == "switch-client -T "+render.JumpTable
}
