package render

import "strings"

// Theme holds the sidebar's 13 colour swatches; each field is the colour value that follows fg=/bg= in a tmux style.
type Theme struct {
	Emphasis      string // OPENCODE block, ▎ before the project name
	Title         string // OPENCODE text on the block
	Split         string // rule under the title
	Project       string // project name
	ActiveTab     string // titles of tabs with a state (running / needs your action / unread)
	InactiveTab   string // titles of idle and data-problem tabs, "No tabs", and the … in summaries
	StateRunning  string // ●
	StateAsk      string // ? (internal state is Attention)
	StateFinished string // ◆
	StateFailed   string // ✗
	StateProblem  string // !
	StateIdle     string // ·
	Notice        string // ▲ read-error notice
}

// DefaultTheme returns the default colours finalized in #14 (v3).
func DefaultTheme() Theme {
	return Theme{
		Emphasis:      "#A78BFA",
		Title:         "#18181B",
		Split:         "#404040",
		Project:       "#F2EEE3",
		ActiveTab:     "#cac5b5",
		InactiveTab:   "#71717A",
		StateRunning:  "#4ADE80",
		StateAsk:      "#FACC15",
		StateFinished: "#60A5FA",
		StateFailed:   "#F87171",
		StateProblem:  "#A1A1AA",
		StateIdle:     "#71717A",
		Notice:        "#FACC15",
	}
}

// swatches is the single mapping between swatch names and Theme fields, in a fixed order (also the order of the invalid list); adding a swatch only requires changing this and Theme.
var swatches = []struct {
	name  string
	field func(*Theme) *string
}{
	{"emphasis", func(t *Theme) *string { return &t.Emphasis }},
	{"title", func(t *Theme) *string { return &t.Title }},
	{"split", func(t *Theme) *string { return &t.Split }},
	{"project", func(t *Theme) *string { return &t.Project }},
	{"active-tab", func(t *Theme) *string { return &t.ActiveTab }},
	{"inactive-tab", func(t *Theme) *string { return &t.InactiveTab }},
	{"state-running", func(t *Theme) *string { return &t.StateRunning }},
	{"state-ask", func(t *Theme) *string { return &t.StateAsk }},
	{"state-finished", func(t *Theme) *string { return &t.StateFinished }},
	{"state-failed", func(t *Theme) *string { return &t.StateFailed }},
	{"state-problem", func(t *Theme) *string { return &t.StateProblem }},
	{"state-idle", func(t *Theme) *string { return &t.StateIdle }},
	{"notice", func(t *Theme) *string { return &t.Notice }},
}

// SwatchNames are the names of the 13 swatches, in the same order as swatches.
var SwatchNames = func() []string {
	names := make([]string, len(swatches))
	for i, sw := range swatches {
		names[i] = sw.name
	}
	return names
}()

// stateMarks is the single mapping between states, symbols, and swatches; idle is not in the table and takes the default row (· and state-idle).
var stateMarks = []struct {
	state State
	sym   string
	color func(*Theme) *string
}{
	{Running, "●", func(t *Theme) *string { return &t.StateRunning }},
	{Attention, "?", func(t *Theme) *string { return &t.StateAsk }},
	{FinishedUnread, "◆", func(t *Theme) *string { return &t.StateFinished }},
	{FailedUnread, "✗", func(t *Theme) *string { return &t.StateFailed }},
	{DataProblem, "!", func(t *Theme) *string { return &t.StateProblem }},
}

func (t Theme) symbol(s State) (sym, color string) {
	for _, m := range stateMarks {
		if m.state == s {
			return m.sym, *m.color(&t)
		}
	}
	return "·", t.StateIdle
}

func (t Theme) tabTitle(s State) string {
	if s == Idle || s == DataProblem {
		return t.InactiveTab
	}
	return t.ActiveTab
}

// summaryColor picks the colour from the symbol in a summary; summaries never contain idle, and any unknown symbol uses state-problem.
func (t Theme) summaryColor(sym string) string {
	for _, m := range stateMarks {
		if m.sym == sym {
			return *m.color(&t)
		}
	}
	return t.StateProblem
}

// ParseTheme starts from the default theme and applies the user's raw colour values (keyed by swatch name).
// An invalid value only leaves that entry at its default and is listed, in swatches order, in the returned invalid list; unknown names are ignored.
func ParseTheme(raw map[string]string) (Theme, []string) {
	th := DefaultTheme()
	var invalid []string
	for _, sw := range swatches {
		v, set := raw[sw.name]
		if !set {
			continue
		}
		if !ValidColour(v) {
			invalid = append(invalid, sw.name)
			continue
		}
		*sw.field(&th) = v
	}
	return th, invalid
}

var colourNames = map[string]bool{"default": true}

func init() {
	for _, c := range []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"} {
		colourNames[c] = true
		colourNames["bright"+c] = true
	}
}

// ValidColour reports whether a colour value can safely go into a tmux style's fg=/bg=: only #RRGGBB, colourN/colorN (0–255),
// named colours (including bright variants), and default are accepted; everything else is invalid, so the value can never contain format characters such as ], a comma, or #{.
func ValidColour(v string) bool {
	if colourNames[v] {
		return true
	}
	if len(v) == 7 && v[0] == '#' {
		for _, c := range []byte(v[1:]) {
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
		return true
	}
	n, ok := strings.CutPrefix(v, "colour")
	if !ok {
		n, ok = strings.CutPrefix(v, "color")
	}
	if !ok || n == "" || len(n) > 3 {
		return false
	}
	num := 0
	for _, c := range []byte(n) {
		if c < '0' || c > '9' {
			return false
		}
		num = num*10 + int(c-'0')
	}
	return num <= 255
}
