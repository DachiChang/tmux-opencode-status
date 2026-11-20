package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/instance"
	"github.com/DachiChang/tmux-opencode-status/internal/render"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
	"github.com/DachiChang/tmux-opencode-status/internal/tmuxsrc"
)

// diagnose 是嚴格唯讀的診斷：只查 tmux（show-options／list-sessions／display-message）與本機狀態檔，
// 不連 OpenCode、不寫任何 tmux 選項、不啟動或停止任何程序。
// 輸出只有計數、模式、固定描述與 tmux session id；不含憑證、prompt、對話、
// OpenCode session／tab 標題、目錄路徑，也不含原始錯誤文字或 tmux session 名稱。
func diagnose(o *options, w io.Writer) int {
	tm, err := o.client(true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }
	info, err := tm.Server()
	if err != nil {
		p("tmux server：無法連線（server 不存在或暫時無回應）")
		return 1
	}
	p("tmux server：pid %d，版本 %s", info.PID, info.Version)

	problems := 0
	missing, merr := tm.MissingCapabilities()
	switch {
	case merr != nil:
		p("必要能力：無法確認（tmux 暫時無回應）")
	case len(missing) > 0:
		problems++
		p("必要能力：缺少 %s——需要 tmux 3.9 的原生 side-status；插件不會改用其他方式顯示", strings.Join(missing, "、"))
	default:
		p("必要能力：side-status、side-status-width、side-status-format 皆可用")
	}

	if v, err := tm.GlobalValue(sidebar.ModeOption); err != nil {
		p("插件模式設定：無法讀取")
	} else if v == "" {
		p("插件模式設定：未設定（預設 cwd）")
	} else if v == "cwd" || v == "global" {
		p("插件模式設定：%s", v)
	} else {
		problems++
		p("插件模式設定：無效值（只接受 cwd 或 global）；有效模式前不會開始追蹤，已有有效模式時保留原模式")
	}
	if raw, err := tm.Swatches(); err != nil {
		p("色票設定：無法讀取")
	} else if _, invalid := render.ParseTheme(raw); len(invalid) > 0 {
		for _, name := range invalid {
			problems++
			p("色票設定：%s%s 的值無效，已退回預設（其餘色票照常套用；側欄不顯示此錯誤）", sidebar.ThemeOptionPrefix, name)
		}
	}
	if v, err := tm.GlobalValue("side-status"); err == nil {
		p("全域 side-status：%s%s", valueOr(v, "（未設定）"), map[bool]string{true: "（側欄隱藏；常駐程式仍持續追蹤，重新顯示不需重啟）", false: ""}[v == "off" || v == ""])
	}

	paths, err := instance.PathsFor(info.SocketPath)
	if err != nil {
		p("常駐實例：無法判斷（%v）", err)
		return 1
	}
	pid, held := instance.Holder(paths.Lock)
	var st instance.State
	haveState := false
	if held {
		if s, err := instance.ReadState(paths.State); err == nil && s.PID == pid {
			st, haveState = s, true
		}
	}
	switch {
	case !held:
		problems++
		p("常駐實例：未執行——不會顯示任何工作；請重新載入 tmux 設定（會呼叫 start）或執行 start")
	case !haveState:
		p("常駐實例：執行中（pid %d），尚未回報狀態", pid)
	default:
		p("常駐實例：執行中（pid %d），已執行 %s", pid, since(st.Started))
		if st.Tracking {
			p("生效模式：%s", st.Mode)
		} else {
			problems++
			p("生效模式：尚未開始追蹤（%s）", valueOr(st.ModeIssue, "模式設定無效"))
		}
		p("發現的 TUI：%d 個；追蹤的 tab：%d 個", st.TUIs, st.Tabs)
		p("服務連線：%s", serviceText(st))
		if st.Service == sidebar.ServiceDown {
			problems++
		}
		for _, i := range st.Issues {
			problems++
			p("目前問題：%s", i)
		}
		if len(st.Recent) == 0 {
			p("最近錯誤：無")
		} else {
			p("最近錯誤：")
			for _, r := range st.Recent {
				p("  %s  [%s] %s", r.At.Local().Format("15:04:05"), r.Source, r.Message)
			}
		}
	}

	problems += diagnoseJump(tm, held, p)

	ov, oerr := tm.SessionOverrides()
	switch {
	case oerr != nil:
		p("session 層級覆寫：無法讀取")
	case len(ov) == 0:
		p("session 層級覆寫：無")
	default:
		p("session 層級覆寫：%d 個 session 自行設定了 side-status 選項。這些 session 不跟隨全域設定：", len(ov))
		for _, x := range ov {
			p("  session %s：%s", x.TmuxSession, strings.Join(x.Options, "、"))
		}
		p("  影響：覆寫 side-status=off 的 session 看不到側欄；覆寫 side-status-width／format 的 session 會用自己的寬度或格式，")
		p("  不會顯示插件內容。插件不會清除或修改這些覆寫；要讓該 session 顯示，請自行 `set -u -t <session> <選項>`。")
	}
	if problems == 0 {
		p("結論：未發現問題")
	} else {
		p("結論：發現 %d 項問題（見上）", problems)
	}
	return 0
}

// diagnoseJump 輸出「鍵盤跳轉」一行並回傳要計入的問題數（無效設定、綁定失敗、已被覆寫各算一項）。
// 唯讀：只讀設定選項、@tos_jump_trigger 與 list-keys。不偵測觸發鍵在綁定前原本是什麼。
func diagnoseJump(tm *tmuxsrc.Client, running bool, p func(string, ...any)) int {
	set, err := tm.JumpTriggerSetting()
	if err != nil {
		p("鍵盤跳轉：無法讀取觸發鍵設定")
		return 0
	}
	configured := set.Key()
	shown := configured
	if !set.Set {
		shown = configured + "（未設定，預設）"
	}
	actual := tm.ActualJumpTrigger()
	switch {
	case configured == "" || tm.JumpKeyInvalid(configured):
		p("鍵盤跳轉：無效設定（%s 的值 %q 不是 tmux 認得的鍵名）；不綁任何觸發鍵，也不退回預設 A", tmuxsrc.JumpTriggerOption, configured)
		return 1
	case actual == "" && running:
		p("鍵盤跳轉：綁定失敗（設定的觸發鍵 %s，實際綁定的鍵：無）；tmux 拒絕了這個綁定", shown)
		return 1
	case actual == "":
		p("鍵盤跳轉：未綁定（設定的觸發鍵 %s；常駐程式未執行）", shown)
		return 0
	case actual != configured:
		p("鍵盤跳轉：設定的觸發鍵 %s，實際綁定的鍵 %s——設定尚未套用，重新載入 tmux 設定後生效", shown, actual)
		return 0
	case !tm.PrefixBindingIsJump(actual):
		p("鍵盤跳轉：已被覆寫（設定的觸發鍵 %s；prefix 表的這個鍵已不是跳轉綁定，可能被 tmux.conf 或其他插件改綁）", shown)
		return 1
	}
	p("鍵盤跳轉：已綁定（設定的觸發鍵 %s，實際綁定的鍵 %s；按 prefix 再按 %s 進入跳轉模式）", shown, actual, actual)
	return 0
}

func serviceText(s instance.State) string {
	switch s.Service {
	case sidebar.ServiceConnected:
		return "已連線（唯讀）"
	case sidebar.ServiceConnecting:
		return "連線中"
	case sidebar.ServiceDown:
		return "無法連線——" + valueOr(s.ServiceReason, "原因不明") + "；自動重試中（插件不會啟動或重啟 OpenCode 服務）"
	case sidebar.ServiceIdle:
		return "待命（沒有追蹤項目，刻意不連線、不查詢 session）"
	}
	return "未設定"
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func since(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}
