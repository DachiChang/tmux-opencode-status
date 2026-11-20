package render

import "strings"

// Published is the global state shared by all clients: the normal layout, the
// jump-mode layout and the letter → pane map, each a tmux user option. When a
// layout is taller than a client, tmux truncates it from the bottom.
type Published struct {
	// Options maps option name to value: MainOption, JumpOption and one
	// JumpKeyOption(letter) per lettered group.
	Options map[string]string
}

// OptPrefix is the tmux user option prefix owned by this plugin; it must never
// collide with a user's own @options (the POC used @oc_side / @sb_*).
// Every option is named @tos_<purpose>_<name> (ADR-0004).
const OptPrefix = "@tos_"

// MainOption is the option holding the full layout.
const MainOption = OptPrefix + "layout_main"

// JumpOption is the option holding the jump-mode layout.
const JumpOption = OptPrefix + "layout_jump"

// JumpKeyPrefix prefixes the per-letter jump target options (@tos_jump_key_<a-z>);
// the value is the target pane id (%N).
const JumpKeyPrefix = OptPrefix + "jump_key_"

// JumpTriggerOption records the trigger key that is actually bound.
const JumpTriggerOption = OptPrefix + "jump_trigger"

// JumpTable is the key table jump mode switches to. It is a public promise:
// users may alias it from the root table.
const JumpTable = "opencode-status-jump"

// JumpKeyOption is the option holding the jump target of a letter.
func JumpKeyOption(letter byte) string { return JumpKeyPrefix + string(letter) }

// Publish lays out groups with the default theme.
func Publish(groups []Group, width int) Published {
	return PublishView(View{Groups: groups}, width, DefaultTheme())
}

// PublishView is Publish for a full View drawn with th.
func PublishView(v View, width int, th Theme) Published {
	if v.Notice != "" {
		var lines []string
		for _, para := range strings.Split(v.Notice, "\n") {
			for i, l := range wrap(para, max(width-2*margin-noticePrefixWidth, 1)) {
				lines = append(lines, l)
				v.noticeContinuation = append(v.noticeContinuation, i > 0)
			}
		}
		v.Notice = strings.Join(lines, "\n")
	}
	rows := FitView(v)
	opts := map[string]string{
		MainOption: strings.Join(Render(rows, width, th), "\n"),
		JumpOption: strings.Join(RenderJump(rows, width, th), "\n"),
	}
	for _, r := range rows {
		if r.Kind == RowGroup && r.Letter != 0 {
			opts[JumpKeyOption(r.Letter)] = "%" + r.JumpTarget
		}
	}
	return Published{Options: opts}
}

// Format builds side-status-format: the jump layout for a client whose key table
// is the jump table, the normal layout otherwise. The layouts are read with
// #{@name} (one expansion), so '#' escaping done by Render is the only layer and
// data containing ',' or '#{' cannot change the condition.
func (p Published) Format() string {
	return "#{?#{==:#{client_key_table}," + JumpTable + "},#{" + JumpOption + "},#{" + MainOption + "}}"
}
