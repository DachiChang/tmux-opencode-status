package render_test

import (
	"strings"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/cellwidth"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
)

// 定案預覽（v3）：#14 Further Notes 所述情境的逐字輸出，作為獨立的真值來源。
// 預覽原稿在提示列後有一行手寫空白，Fit 不會產生，故不列入。
var v3Preview = []string{
	` #[bg=#A78BFA,fg=#18181B] OPENCODE #[default]         #[fg=#4ADE80]●1#[default] #[fg=#FACC15]?1#[default] #[fg=#60A5FA]◆1#[default] #[fg=#F87171]✗1#[default] #[fg=#A1A1AA]!1#[default]`,
	` #[fg=#404040]─────────────────────────────────#[default]`,
	` #[fg=#FACC15]▲ 讀取 OpenCode 狀態失敗，重試中#[default]`,
	` #[fg=#A78BFA]▎#[fg=#F2EEE3]tmux-opencode-status#[default]       #[fg=#4ADE80]●1#[default] #[fg=#FACC15]?1#[default]`,
	`  #[fg=#4ADE80]●#[default] #[fg=#cac5b5]實作 render 配色#[default]`,
	`  #[fg=#FACC15]?#[default] #[fg=#cac5b5]等待權限確認#[default]`,
	`  #[fg=#71717A]·#[default] #[fg=#71717A]查看 active sessions#[default]`,
	``,
	` #[fg=#A78BFA]▎#[fg=#F2EEE3]opencode#[default]                   #[fg=#60A5FA]◆1#[default] #[fg=#F87171]✗1#[default]`,
	`  #[fg=#60A5FA]◆#[default] #[fg=#cac5b5]spec ##1 完成#[default]`,
	`  #[fg=#F87171]✗#[default] #[fg=#cac5b5]go test 失敗#[default]`,
	`  #[fg=#A1A1AA]!#[default] #[fg=#71717A]狀態讀取異常#[default]`,
	``,
	` #[fg=#A78BFA]▎#[fg=#F2EEE3]dotfiles#[default]                      #[fg=#A1A1AA]!1#[default]`,
	`  #[fg=#A1A1AA]!#[default] #[fg=#71717A]資料異常（無法解析）#[default]`,
	``,
	` #[fg=#A78BFA]▎#[fg=#F2EEE3]empty-project#[default]`,
	`  #[fg=#71717A]No tabs#[default]`,
}

// v3Preview36 是同一份預覽在 36 欄（Render width=36，每行最多 35 格）的逐字輸出：
// 分隔線多一格、右對齊的摘要多一格空白，其餘不變；同樣手寫，不由程式產生。
var v3Preview36 = []string{
	` #[bg=#A78BFA,fg=#18181B] OPENCODE #[default]          #[fg=#4ADE80]●1#[default] #[fg=#FACC15]?1#[default] #[fg=#60A5FA]◆1#[default] #[fg=#F87171]✗1#[default] #[fg=#A1A1AA]!1#[default]`,
	` #[fg=#404040]──────────────────────────────────#[default]`,
	` #[fg=#FACC15]▲ 讀取 OpenCode 狀態失敗，重試中#[default]`,
	` #[fg=#A78BFA]▎#[fg=#F2EEE3]tmux-opencode-status#[default]        #[fg=#4ADE80]●1#[default] #[fg=#FACC15]?1#[default]`,
	`  #[fg=#4ADE80]●#[default] #[fg=#cac5b5]實作 render 配色#[default]`,
	`  #[fg=#FACC15]?#[default] #[fg=#cac5b5]等待權限確認#[default]`,
	`  #[fg=#71717A]·#[default] #[fg=#71717A]查看 active sessions#[default]`,
	``,
	` #[fg=#A78BFA]▎#[fg=#F2EEE3]opencode#[default]                    #[fg=#60A5FA]◆1#[default] #[fg=#F87171]✗1#[default]`,
	`  #[fg=#60A5FA]◆#[default] #[fg=#cac5b5]spec ##1 完成#[default]`,
	`  #[fg=#F87171]✗#[default] #[fg=#cac5b5]go test 失敗#[default]`,
	`  #[fg=#A1A1AA]!#[default] #[fg=#71717A]狀態讀取異常#[default]`,
	``,
	` #[fg=#A78BFA]▎#[fg=#F2EEE3]dotfiles#[default]                       #[fg=#A1A1AA]!1#[default]`,
	`  #[fg=#A1A1AA]!#[default] #[fg=#71717A]資料異常（無法解析）#[default]`,
	``,
	` #[fg=#A78BFA]▎#[fg=#F2EEE3]empty-project#[default]`,
	`  #[fg=#71717A]No tabs#[default]`,
}

func v3Rows() []render.Row {
	tab := func(title string, st render.State) render.Row {
		return render.Row{Kind: render.RowTab, Text: title, State: st}
	}
	group := func(name, summary string) render.Row {
		return render.Row{Kind: render.RowGroup, Text: name, Summary: summary}
	}
	blank := render.Row{Kind: render.RowBlank}
	return []render.Row{
		{Kind: render.RowTitle, Summary: "●1 ?1 ◆1 ✗1 !1"},
		{Kind: render.RowRule},
		{Kind: render.RowNotice, Text: "讀取 OpenCode 狀態失敗，重試中"},
		group("tmux-opencode-status", "●1 ?1"),
		tab("實作 render 配色", render.Running), tab("等待權限確認", render.Attention), tab("查看 active sessions", render.Idle),
		blank,
		group("opencode", "◆1 ✗1"),
		tab("spec #1 完成", render.FinishedUnread), tab("go test 失敗", render.FailedUnread), tab("狀態讀取異常", render.DataProblem),
		blank,
		group("dotfiles", "!1"),
		tab("資料異常（無法解析）", render.DataProblem),
		blank,
		group("empty-project", ""),
		{Kind: render.RowNote, Text: "No tabs", Indent: 1},
	}
}

func TestDefaultThemeRendersTheApprovedV3PreviewVerbatim(t *testing.T) {
	got := render.Render(v3Rows(), contentWidth, render.DefaultTheme())
	if strings.Join(got, "\n") != strings.Join(v3Preview, "\n") {
		t.Fatalf("與定案預覽不符\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(v3Preview, "\n"))
	}
	for i, l := range got {
		if w := cellwidthOf(l); w > 34 {
			t.Errorf("第 %d 行可見寬度 %d 超過 34：%q", i, w, l)
		}
	}
}

func TestDefaultThemeRendersTheApprovedV3PreviewVerbatimAt36Columns(t *testing.T) {
	got := render.Render(v3Rows(), 36, render.DefaultTheme())
	if strings.Join(got, "\n") != strings.Join(v3Preview36, "\n") {
		t.Fatalf("36 欄與預覽不符\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(v3Preview36, "\n"))
	}
	for i, l := range got {
		if w := cellwidthOf(l); w > 35 {
			t.Errorf("第 %d 行可見寬度 %d 超過 35：%q", i, w, l)
		}
	}
}

func TestPublishViewWithDefaultThemeMatchesConvenienceFunction(t *testing.T) {
	groups := []render.Group{{Name: "api", Tabs: []render.Tab{{Title: "x", State: render.Running, SessionID: "1"}}}}
	a := render.Publish(groups, contentWidth)
	b := render.PublishView(render.View{Groups: groups}, contentWidth, render.DefaultTheme())
	if a.Options[render.MainOption] != b.Options[render.MainOption] {
		t.Fatalf("便利函式應等同預設主題：\n%q\n%q", a.Options[render.MainOption], b.Options[render.MainOption])
	}
}

// customTheme 的 13 個色票互不相同，才能指認每個色票出現在哪個元素上。
func customTheme() render.Theme {
	return render.Theme{
		Emphasis: "#010101", Title: "#020202", Split: "#030303", Project: "#040404",
		ActiveTab: "#050505", InactiveTab: "#060606",
		StateRunning: "#070707", StateAsk: "#080808", StateFinished: "#090909", StateFailed: "#0A0A0A",
		StateProblem: "#0B0B0B", StateIdle: "#0C0C0C", Notice: "#0D0D0D",
	}
}

func TestEveryThemeColourAppearsOnItsElement(t *testing.T) {
	th := customTheme()
	rows := v3Rows()
	rows[0].Summary = "●1 ?1 ◆1 ✗1 !1"
	ls := render.Render(rows, contentWidth, th)
	cases := []struct {
		line  int
		text  string
		style string
		why   string
	}{
		{0, "OPENCODE", "bg=#010101,fg=#020202", "emphasis 底塊與 title 文字"},
		{0, "●1", "fg=#070707", "摘要 state-running"},
		{0, "?1", "fg=#080808", "摘要 state-ask"},
		{0, "◆1", "fg=#090909", "摘要 state-finished"},
		{0, "✗1", "fg=#0A0A0A", "摘要 state-failed"},
		{0, "!1", "fg=#0B0B0B", "摘要 state-problem"},
		{1, "─", "fg=#030303", "split"},
		{2, "▲", "fg=#0D0D0D", "notice"},
		{3, "▎", "fg=#010101", "emphasis ▎"},
		{3, "tmux-opencode-status", "fg=#040404", "project"},
		{4, "●", "fg=#070707", "running 符號"},
		{4, "實作", "fg=#050505", "active-tab"},
		{5, "?", "fg=#080808", "ask 符號"},
		{6, "·", "fg=#0C0C0C", "state-idle 符號"},
		{6, "查看", "fg=#060606", "inactive-tab（閒置）"},
		{9, "◆", "fg=#090909", "finished 符號"},
		{9, "spec", "fg=#050505", "active-tab（結束未讀）"},
		{10, "✗", "fg=#0A0A0A", "failed 符號"},
		{10, "go test", "fg=#050505", "active-tab（失敗未讀）"},
		{11, "!", "fg=#0B0B0B", "problem 符號"},
		{11, "狀態讀取", "fg=#060606", "inactive-tab（資料異常）"},
		{17, "No tabs", "fg=#060606", "inactive-tab（無 tab）"},
	}
	for _, c := range cases {
		if got := styleOf(t, ls[c.line], c.text); got != c.style {
			t.Errorf("%s：第 %d 行 %q 的樣式 = %q，要 %q", c.why, c.line, c.text, got, c.style)
		}
	}
}

func TestSummaryEllipsisUsesInactiveTabColour(t *testing.T) {
	th := customTheme()
	rows := []render.Row{{Kind: render.RowTitle, Summary: "●1 ?1 ◆1234567890123456789"}}
	l := render.Render(rows, contentWidth, th)[0]
	if !strings.Contains(render.StripStyles(l), "…") {
		t.Fatalf("測試情境應放不下摘要：%q", l)
	}
	if got := styleOf(t, l, "…"); got != "fg=#060606" {
		t.Errorf("… 樣式 = %q，要 inactive-tab", got)
	}
}

func TestNothingIsEverBold(t *testing.T) {
	var all []string
	all = append(all, render.Render(v3Rows(), contentWidth, render.DefaultTheme())...)
	groups := []render.Group{{Name: "g", Tabs: []render.Tab{
		{Title: "a", State: render.FinishedUnread, SessionID: "1"}, {Title: "b", State: render.FailedUnread, SessionID: "2"},
		{Title: "c", State: render.Idle, SessionID: "3"}}}}
	p := render.PublishView(render.View{Groups: groups, Notice: strings.Repeat("長", 80), Empty: "x"}, contentWidth, render.DefaultTheme())
	all = append(all, p.Options[render.MainOption])
	for _, l := range all {
		if strings.Contains(l, "bold") {
			t.Fatalf("不得有 bold：%q", l)
		}
	}
}

func TestBarAndTriangleAreOneCellWide(t *testing.T) {
	for _, s := range []string{"▎", "▲"} {
		if w := cellwidth.String(s); w != 1 {
			t.Errorf("%q 寬 %d，要 1", s, w)
		}
	}
}

func TestEveryLineStaysWithinWidthMinusMargin(t *testing.T) {
	long := strings.Repeat("很長的名稱", 20)
	hostile := "x #[fg=red] #{pane_id} ## \u202e" + strings.Repeat("a", 60)
	var tabs []render.Tab
	for _, st := range []render.State{render.Running, render.Attention, render.FinishedUnread, render.FailedUnread, render.DataProblem} {
		for i := 0; i < 120; i++ {
			tabs = append(tabs, render.Tab{Title: long, State: st, SessionID: string(rune('a'+st)) + string(rune(i+0x100))})
		}
	}
	for _, width := range []int{20, 35} {
		for _, name := range []string{long, hostile} {
			v := render.View{
				Groups: []render.Group{{Name: name, Tabs: tabs}, {Name: name}},
				Notice: long + "\n" + hostile, Empty: long,
			}
			p := render.PublishView(v, width, render.DefaultTheme())
			for _, l := range strings.Split(p.Options[render.MainOption], "\n") {
				if w := cellwidthOf(l); w > width-1 {
					t.Fatalf("width=%d：行寬 %d 超過 %d：%q", width, w, width-1, render.StripStyles(l))
				}
			}
		}
	}
}

func TestLongNoticeWrapsReservingTheTriangleAndAlignsContinuationLines(t *testing.T) {
	text := strings.Repeat("無法讀取資料", 8)
	p := render.PublishView(render.View{Notice: text}, contentWidth, render.DefaultTheme())
	ls := strings.Split(p.Options[render.MainOption], "\n")[2:]
	if len(ls) < 2 {
		t.Fatalf("應折成多行：%q", ls)
	}
	var joined string
	for i, l := range ls {
		pl := render.StripStyles(l)
		if w := cellwidthOf(l); w > 34 {
			t.Errorf("第 %d 行寬 %d 越界：%q", i, w, pl)
		}
		switch {
		case i == 0 && !strings.HasPrefix(pl, " ▲ "):
			t.Errorf("首行應以 ▲ 開頭：%q", pl)
		case i > 0 && (!strings.HasPrefix(pl, "   ") || strings.HasPrefix(pl, "    ")):
			t.Errorf("續行應縮排 3 格與文字對齊：%q", pl)
		}
		if i > 0 && strings.Contains(pl, "▲") {
			t.Errorf("續行不應重複 ▲：%q", pl)
		}
		joined += strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(pl), "▲"))
	}
	if joined != text {
		t.Errorf("折行後文字遺失：%q", joined)
	}
	// 首行文字預留 ▲ 的 2 格：content 34 格 - 左邊距 1 - ▲ 與空白 2 = 31 格可放文字（寬字元每個 2 格 → 15 個字）
	if first := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(render.StripStyles(ls[0])), "▲")); cellwidth.String(first) > 31 {
		t.Errorf("首行文字 %d 格，超過 31：%q", cellwidth.String(first), first)
	}
}
