package render

import "strings"

// Theme 是側欄的 13 個色票；每個欄位是 tmux 樣式中 fg=／bg= 後面的顏色值。
type Theme struct {
	Emphasis      string // OPENCODE 底塊、專案名稱前的 ▎
	Title         string // 底塊上的 OPENCODE 文字
	Split         string // 標題下分隔線
	Project       string // 專案名稱
	ActiveTab     string // 有狀態（執行中／等你處理／未讀）的 tab 標題
	InactiveTab   string // 閒置與資料異常的 tab 標題、無 tab、還有 N 個 tab、摘要的 …
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

func (t Theme) symbol(s State) (sym, color string) {
	switch s {
	case Running:
		return "●", t.StateRunning
	case Attention:
		return "?", t.StateAsk
	case FinishedUnread:
		return "◆", t.StateFinished
	case FailedUnread:
		return "✗", t.StateFailed
	case DataProblem:
		return "!", t.StateProblem
	}
	return "·", t.StateIdle
}

func (t Theme) tabTitle(s State) string {
	if s == Idle || s == DataProblem {
		return t.InactiveTab
	}
	return t.ActiveTab
}

func (t Theme) summaryColor(sym string) string {
	switch sym {
	case "●":
		return t.StateRunning
	case "?":
		return t.StateAsk
	case "◆":
		return t.StateFinished
	case "✗":
		return t.StateFailed
	}
	return t.StateProblem
}

// SwatchNames 是 13 個色票的名稱，順序固定（也是無效清單的順序）。
var SwatchNames = []string{
	"emphasis", "title", "split", "project", "active-tab", "inactive-tab",
	"state-running", "state-ask", "state-finished", "state-failed", "state-problem", "state-idle", "notice",
}

func (t *Theme) swatch(name string) *string {
	switch name {
	case "emphasis":
		return &t.Emphasis
	case "title":
		return &t.Title
	case "split":
		return &t.Split
	case "project":
		return &t.Project
	case "active-tab":
		return &t.ActiveTab
	case "inactive-tab":
		return &t.InactiveTab
	case "state-running":
		return &t.StateRunning
	case "state-ask":
		return &t.StateAsk
	case "state-finished":
		return &t.StateFinished
	case "state-failed":
		return &t.StateFailed
	case "state-problem":
		return &t.StateProblem
	case "state-idle":
		return &t.StateIdle
	case "notice":
		return &t.Notice
	}
	return nil
}

// ParseTheme 以預設主題為底，套用使用者設定的原始色值（鍵為色票名稱）。
// 無效的值只讓該項維持預設，並依 SwatchNames 的順序列在回傳的無效清單；未知名稱忽略。
func ParseTheme(raw map[string]string) (Theme, []string) {
	th := DefaultTheme()
	var invalid []string
	for _, name := range SwatchNames {
		v, set := raw[name]
		if !set {
			continue
		}
		if !ValidColour(v) {
			invalid = append(invalid, name)
			continue
		}
		*th.swatch(name) = v
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
