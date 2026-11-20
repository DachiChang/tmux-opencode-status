package render

import "strings"

// Published is the global state shared by all clients: one tmux user option
// holding the full layout. When the content is taller than a client, tmux
// truncates it from the bottom.
type Published struct {
	// Options maps option name to layout; it always holds exactly MainOption.
	Options map[string]string
}

// OptPrefix is the tmux user option prefix owned by this plugin; it must never
// collide with a user's own @options (the POC used @oc_side / @sb_*).
const OptPrefix = "@tos_sb_"

// MainOption is the option holding the full layout.
const MainOption = OptPrefix + "main"

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
	layout := strings.Join(Render(FitView(v), width, th), "\n")
	return Published{Options: map[string]string{MainOption: layout}}
}

// Format builds side-status-format. The option value is read with #{@name}
// (one expansion), so '#' escaping done by Render is the only layer.
func (p Published) Format() string {
	return "#{" + MainOption + "}"
}
