package render

import "strings"

// Theme 是側欄的 13 個色票；每個欄位是 tmux 樣式中 fg=／bg= 後面的顏色值。
type Theme struct {
	Emphasis      string // OPENCODE 底塊、專案名稱前的 ▎
	Title         string // 底塊上的 OPENCODE 文字
	Split         string // 標題下分隔線
	Project       string // 專案名稱
	ActiveTab     string // 有狀態（執行中／等你處理／未讀）的 tab 標題
	InactiveTab   string // 閒置與資料異常的 tab 標題、無 tab、摘要的 …
	StateRunning  string // ●
	StateAsk      string // ?（內部狀態為 Attention）
	StateFinished string // ◆
	StateFailed   string // ✗
	StateProblem  string // !
	StateIdle     string // ·
	Notice        string // ▲ 讀取錯誤提示
}

// DefaultTheme 回傳 #14 定案（v3）的預設配色。
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

// swatches 是色票名稱與 Theme 欄位的唯一對照表，順序固定（也是無效清單的順序）；新增色票只需改這裡與 Theme。
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

// SwatchNames 是 13 個色票的名稱，順序同 swatches。
var SwatchNames = func() []string {
	names := make([]string, len(swatches))
	for i, sw := range swatches {
		names[i] = sw.name
	}
	return names
}()

// stateMarks 是狀態、符號與色票的唯一對照表；閒置不在表中，走預設列（· 與 state-idle）。
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

// summaryColor 依摘要裡的符號取色；摘要不含閒置，未知符號一律用 state-problem。
func (t Theme) summaryColor(sym string) string {
	for _, m := range stateMarks {
		if m.sym == sym {
			return *m.color(&t)
		}
	}
	return t.StateProblem
}

// ParseTheme 以預設主題為底，套用使用者設定的原始色值（鍵為色票名稱）。
// 無效的值只讓該項維持預設，並依 swatches 的順序列在回傳的無效清單；未知名稱忽略。
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

// ValidColour 判斷色值能否安全放進 tmux 樣式的 fg=／bg=：只接受 #RRGGBB、colourN／colorN（0–255）、
// 具名色（含 bright 變體）與 default；其餘一律無效，因此值裡不可能出現 ]、,、#{ 等格式字元。
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
