package render

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
