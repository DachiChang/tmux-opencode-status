package sidebar_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/fakeoc"
	"github.com/DachiChang/tmux-opencode-status/internal/ocsrc"
	"github.com/DachiChang/tmux-opencode-status/internal/sidebar"
)

// ---- 單一 session 的執行與未讀生命週期（issue #5）----
// 受控假 OpenCode 服務（httptest + SSE）+ 真實的服務探索／認證／REST／SSE 客戶端，
// 從寫到 tmux 的側欄輸出觀察結果。

type liveWorld struct {
	*world
	svc     *fakeoc.Server
	svcFile string
	wake    chan struct{}
}

// startLive 啟動常駐迴圈（假 tmux 資料來源 + 假 OpenCode 服務）。服務探索檔 service.json
// 由測試控制：publishService 之前服務「尚未開啟」。
func startLive(t *testing.T) *liveWorld { return startLiveWith(t, nil) }

// startLiveWith 在常駐迴圈啟動前先以 setup 設定外部世界（例如模式選項）。
func startLiveWith(t *testing.T, setup func(*world)) *liveWorld {
	return startLiveOpts(t, liveOpts{setup: setup})
}

// liveOpts 是時間相關的注入點：測試以短週期取代產品預設（45 秒無聲、60 秒補漏重讀、3 秒重試）。
type liveOpts struct {
	setup  func(*world)
	idle   time.Duration // SSE 無聲逾時；0 = 產品預設
	resync time.Duration // 家族補漏重讀週期；0 = 產品預設
	retry  time.Duration // 連線失敗後的重試間隔；0 = 20ms
}

func startLiveOpts(t *testing.T, o liveOpts) *liveWorld {
	t.Helper()
	w := newWorld(t)
	if o.setup != nil {
		o.setup(w)
	}
	if o.retry == 0 {
		o.retry = 20 * time.Millisecond
	}
	lw := &liveWorld{world: w, svc: fakeoc.New(t), svcFile: filepath.Join(t.TempDir(), "service.json"), wake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	w.d = sidebar.New(sidebar.Config{Width: 36, Debounce: time.Millisecond, Retry: o.retry, Resync: o.resync},
		sidebar.Deps{Panes: w.panes, Tabs: w.tabs, Out: w.out, Mode: w.mode, Wake: lw.wake, Service: ocsrc.Source{Path: lw.svcFile, IdleTimeout: o.idle}})
	done := make(chan error, 1)
	go func() { done <- w.d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if wr := lw.svc.Writes(); len(wr) != 0 {
			t.Errorf("對 OpenCode 發出了寫入請求：%v", wr)
		}
		for _, r := range lw.svc.Requests() {
			if !strings.HasPrefix(r, "GET ") {
				t.Errorf("非唯讀請求：%s", r)
			}
			if strings.Contains(r, "/view") || strings.Contains(r, "/reply") {
				t.Errorf("呼叫了標記已讀或回覆 API：%s", r)
			}
		}
	})
	return lw
}

func (lw *liveWorld) publishService() { lw.svc.WriteServiceFile(lw.svcFile) }

// openTab 讓 TUI 在 dir 開著、tab 清單含給定 (sessionID,title) 並喚醒迴圈。
func (lw *liveWorld) openTabs(t *testing.T, dir string, tabs ...[2]string) {
	t.Helper()
	var items []string
	for _, tb := range tabs {
		items = append(items, `{"sessionID":"`+tb[0]+`","title":"`+tb[1]+`"}`)
	}
	lw.panes.set(tui("%1", dir))
	lw.setTabs(t, `{"global":{"tabs":[]},"cwd":{"`+dir+`":{"tabs":[`+strings.Join(items, ",")+`]}}}`)
	lw.wake <- struct{}{}
}

func (lw *liveWorld) eventually(t *testing.T, what string, ok func(screen string) bool) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = strings.Join(lw.out.screen(30), "\n")
		if last != "" && ok(last) {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s：3 秒內未達成，目前畫面：\n%s", what, last)
	return ""
}

func hasLine(s, line string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimRight(l, " ") == line {
			return true
		}
	}
	return false
}

func TestIdleTabShowsGrayDotOnlyAfterSnapshot(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "修正登入"})

	// 服務尚未開啟：資料不足，保持 !，不先顯示閒置
	lw.eventually(t, "服務未開時保持 !", func(s string) bool { return hasLine(s, "! 修正登入") })

	lw.publishService()
	got := lw.eventually(t, "snapshot 後顯示閒置", func(s string) bool { return hasLine(s, "· 修正登入") })
	if strings.Contains(got, "!") {
		t.Fatalf("閒置時不應有任何異常符號：\n%s", got)
	}
}

func TestRunningThenFinishedUnreadThenReadByAnotherClient(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "修正登入"})
	lw.eventually(t, "snapshot 後閒置", func(s string) bool { return hasLine(s, "· 修正登入") })

	lw.svc.Start("ses_1")
	got := lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● 修正登入") })
	if !hasLine(got, "◆ OPENCODE                       ●1") || !hasLine(got, "api                              ●1") {
		t.Fatalf("摘要未更新：\n%s", got)
	}

	lw.svc.Finish("ses_1", "succeeded")
	got = lw.eventually(t, "結束未讀", func(s string) bool { return hasLine(s, "◆ 修正登入") })
	if !hasLine(got, "◆ OPENCODE                       ◆1") {
		t.Fatalf("摘要未更新：\n%s", got)
	}

	lw.svc.View("ses_1") // 其他 client 讀取；側欄不自己標記已讀
	got = lw.eventually(t, "已讀後回到閒置", func(s string) bool { return hasLine(s, "· 修正登入") })
	if hasLine(got, "◆ OPENCODE                       ◆1") || strings.Contains(got, "●") {
		t.Fatalf("閒置不應出現在摘要：\n%s", got)
	}
}

func TestFailedIsRedAndInterruptedIsNotAFailure(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_f", "")
	lw.svc.AddSession("ses_i", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_f", "fails"}, [2]string{"ses_i", "interrupted"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· fails") && hasLine(s, "· interrupted") })

	lw.svc.Start("ses_f")
	lw.svc.Finish("ses_f", "failed")
	lw.svc.Start("ses_i")
	lw.svc.Finish("ses_i", "interrupted")
	got := lw.eventually(t, "失敗與中斷", func(s string) bool { return hasLine(s, "✗ fails") && hasLine(s, "◆ interrupted") })
	if !hasLine(got, "◆ OPENCODE                    ◆1 ✗1") {
		t.Fatalf("摘要順序應為 ●、?、◆、✗、!：\n%s", got)
	}
}

func TestUnreadStateBeforeStartupComesFromSnapshotAndSharedReadState(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_a", "", "failed", 500, 0, false)      // 失敗未讀
	lw.svc.Seed("ses_b", "", "succeeded", 500, 400, false) // viewed 早於 idle：未讀
	lw.svc.Seed("ses_c", "", "succeeded", 500, 500, false) // 已讀
	lw.svc.Seed("ses_d", "", "interrupted", 500, 0, false) // 中斷未讀
	lw.svc.Seed("ses_e", "", "", 0, 0, true)               // 啟動前已在執行
	lw.openTabs(t, "/work/api",
		[2]string{"ses_a", "A"}, [2]string{"ses_b", "B"}, [2]string{"ses_c", "C"}, [2]string{"ses_d", "D"}, [2]string{"ses_e", "E"})
	got := lw.eventually(t, "snapshot 狀態", func(s string) bool { return hasLine(s, "✗ A") })
	for _, want := range []string{"◆ B", "· C", "◆ D", "● E"} {
		if !hasLine(got, want) {
			t.Fatalf("缺 %q：\n%s", want, got)
		}
	}
	if !hasLine(got, "◆ OPENCODE                 ●1 ◆2 ✗1") {
		t.Fatalf("摘要不符：\n%s", got)
	}
}

func TestStateChangesNeverReorderTabsAndSummaryDedupesBySession(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.AddSession("ses_2", "")
	lw.panes.set(tui("%1", "/work/api"), tui("%2", "/work/web"))
	lw.setTabs(t, `{"global":{"tabs":[]},"cwd":{
		"/work/api":{"tabs":[{"sessionID":"ses_1","title":"one"},{"sessionID":"ses_2","title":"two"}]},
		"/work/web":{"tabs":[{"sessionID":"ses_2","title":"two-again"}]}}}`)
	lw.wake <- struct{}{}
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "· two-again") })

	lw.svc.Start("ses_2")
	got := lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● two") && hasLine(s, "● two-again") })
	order := []string{"· one", "● two", "● two-again"}
	last := -1
	for _, o := range order {
		i := strings.Index(got, o)
		if i <= last {
			t.Fatalf("tab 順序被改變（%q）：\n%s", o, got)
		}
		last = i
	}
	// 同一 session 出現在兩個群組：群組各計一次，全體摘要按 session 去重
	if !hasLine(got, "◆ OPENCODE                       ●1") {
		t.Fatalf("全體摘要未按 session 去重：\n%s", got)
	}
	if !hasLine(got, "api                              ●1") || !hasLine(got, "web                              ●1") {
		t.Fatalf("群組摘要不符：\n%s", got)
	}
}

func TestServiceOutageShowsUnknownAndOneSharedNoticeThenRecovers(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.AddSession("ses_2", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "one"}, [2]string{"ses_2", "two"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "· two") })
	lw.svc.Start("ses_1")
	lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● one") })

	// 服務消失（探索檔移除、連線中斷）：不得沿用舊的 ● 當成最新事實
	_ = os.Remove(lw.svcFile)
	lw.svc.Drop()
	got := lw.eventually(t, "斷線後為 !", func(s string) bool { return hasLine(s, "! one") && hasLine(s, "! two") })
	if strings.Count(got, "無法連線 OpenCode 服務") != 1 {
		t.Fatalf("斷線提示應只出現一次：\n%s", got)
	}

	// 斷線期間事件遺失；服務回來後完整重讀，不假定事件可重播
	lw.svc.Finish("ses_1", "succeeded")
	lw.publishService()
	got = lw.eventually(t, "重連後完整同步", func(s string) bool { return hasLine(s, "◆ one") && hasLine(s, "· two") })
	if strings.Contains(got, "無法連線") {
		t.Fatalf("恢復後提示應消失：\n%s", got)
	}
}

func TestSessionMissingKeepsTabAsUnknown(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_ok", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_gone", "gone"}, [2]string{"ses_ok", "ok"})
	got := lw.eventually(t, "單一 tab 失敗不影響其他", func(s string) bool { return hasLine(s, "· ok") })
	if !hasLine(got, "! gone") {
		t.Fatalf("session 不存在的 tab 應保留並標 !：\n%s", got)
	}
}

func TestSSEUsesOnlyDocumentedEventTypes(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "x"})
	lw.eventually(t, "已連線", func(s string) bool { return hasLine(s, "· x") })
	qs := lw.svc.EventQueries()
	if len(qs) == 0 {
		t.Fatal("沒有連上 /api/event")
	}
	for _, q := range qs {
		if !strings.Contains(q, "types=session.execution.started") || !strings.Contains(q, "types=session.viewed") {
			t.Fatalf("缺必要事件過濾：%s", q)
		}
		if strings.Contains(q, "session.status") || strings.Contains(q, "session.idle") {
			t.Fatalf("不得依賴目標版本不會送出的事件：%s", q)
		}
	}
}

func TestNoTrackedTabsMeansNoSessionQueries(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.panes.set() // 沒有 TUI
	lw.wake <- struct{}{}
	lw.eventually(t, "沒有 TUI", func(s string) bool { return strings.Contains(s, "沒有 OpenCode TUI") })
	time.Sleep(100 * time.Millisecond)
	if n := lw.svc.SessionRequests(); n != 0 {
		t.Fatalf("沒有追蹤項目卻查詢了 session 資料 %d 次：%v", n, lw.svc.Requests())
	}
	if lw.svc.Connects() != 0 {
		t.Fatal("沒有追蹤項目不應連線")
	}
}

func TestGlobalModeGroupShowsLifecycleStatesAndInvalidModeNeverConnects(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_g", "")
	lw.mode.use("global")
	lw.panes.set(tui("%1", "/anywhere"))
	lw.setTabs(t, `{"global":{"tabs":[{"sessionID":"ses_g","title":"shared"}]},"cwd":{}}`)
	lw.wake <- struct{}{}
	lw.eventually(t, "GLOBAL 群組閒置", func(s string) bool { return hasLine(s, "· shared") && strings.Contains(s, "GLOBAL") })
	lw.svc.Start("ses_g")
	lw.eventually(t, "GLOBAL 群組執行中", func(s string) bool { return hasLine(s, "● shared") })
}

func TestInvalidInitialModeDoesNotQueryTheService(t *testing.T) {
	lw := startLiveWith(t, func(w *world) { w.mode.use("globl") })
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "x"})
	lw.eventually(t, "提示", func(s string) bool { return strings.Contains(s, "模式設定無效") })
	time.Sleep(100 * time.Millisecond)
	if n := lw.svc.SessionRequests(); n != 0 || lw.svc.Connects() != 0 {
		t.Fatalf("尚未開始追蹤卻連線／查詢服務：%v", lw.svc.Requests())
	}
}

// ---- 等你處理：待核准與待回答（issue #6）----

func TestPendingPermissionAndFormExistingAtStartupShowYellowQuestionMark(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_a", "")
	lw.svc.AddSession("ses_b", "")
	lw.svc.AddSession("ses_c", "")
	lw.svc.SeedPermission("per_1", "ses_a") // 啟動前就已存在，不會有任何事件
	lw.svc.SeedForm("frm_1", "ses_b")
	lw.openTabs(t, "/work/api", [2]string{"ses_a", "needs approval"}, [2]string{"ses_b", "needs answer"}, [2]string{"ses_c", "calm"})
	got := lw.eventually(t, "初始化即讀到待處理", func(s string) bool { return hasLine(s, "? needs approval") && hasLine(s, "? needs answer") })
	if !hasLine(got, "· calm") || !hasLine(got, "◆ OPENCODE                       ?2") {
		t.Fatalf("摘要或其他 tab 不符：\n%s", got)
	}
	// 顏色：黃色 ?，沒有背景色塊，也不是舊 POC 的黃色 !
	for _, opt := range lw.out.options() {
		if strings.Contains(opt, "bg=") {
			t.Fatalf("待處理不得使用背景色塊：%q", opt)
		}
		if !strings.Contains(opt, "#[fg=yellow,bold]?#[default] ") {
			continue
		}
		if strings.Contains(opt, "#[fg=yellow,bold]!") {
			t.Fatalf("不得沿用黃色 !：%q", opt)
		}
		return
	}
	t.Fatal("輸出中找不到黃色 ?")
}

func TestPermissionAndFormEventsAfterStartupAndTheirCompletionRestoreTheRightState(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· job") })

	lw.svc.AskPermission("per_1", "ses_1")
	lw.eventually(t, "待核准", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.ReplyPermission("per_1")
	lw.eventually(t, "回覆後回閒置", func(s string) bool { return hasLine(s, "· job") })

	lw.svc.AskForm("frm_1", "ses_1")
	lw.eventually(t, "待回答", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.CancelForm("frm_1")
	lw.eventually(t, "取消後回閒置", func(s string) bool { return hasLine(s, "· job") })

	lw.svc.AskForm("frm_2", "ses_1")
	lw.eventually(t, "待回答", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.AnswerForm("frm_2")
	lw.eventually(t, "回答後回閒置", func(s string) bool { return hasLine(s, "· job") })
}

func TestApprovalBeatsAnswerAndBothBeatRunningAndUnread(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· job") })

	lw.svc.Start("ses_1")
	lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● job") })
	lw.svc.AskForm("frm_1", "ses_1")
	got := lw.eventually(t, "待回答蓋過執行中", func(s string) bool { return hasLine(s, "? job") })
	if strings.Contains(got, "●") {
		t.Fatalf("同一 tab 只能輸出最高優先狀態：\n%s", got)
	}
	lw.svc.AskPermission("per_1", "ses_1")
	lw.svc.AnswerForm("frm_1") // 核准仍在：維持 ?
	lw.eventually(t, "仍待核准", func(s string) bool { return hasLine(s, "? job") })

	// 回覆後不猜測閒置：session 仍在執行 → 回到執行中
	lw.svc.ReplyPermission("per_1")
	lw.eventually(t, "回覆後回到執行中", func(s string) bool { return hasLine(s, "● job") })

	// 待處理期間執行結束：回覆後是結束未讀，不是閒置
	lw.svc.AskPermission("per_2", "ses_1")
	lw.eventually(t, "待核准", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.Finish("ses_1", "failed")
	lw.eventually(t, "待處理期間結束仍為 ?", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.ReplyPermission("per_2")
	lw.eventually(t, "回覆後是失敗未讀", func(s string) bool { return hasLine(s, "✗ job") })
}

func TestSummaryCountsTabsNotRequests(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.AddSession("ses_2", "")
	lw.svc.AddSession("ses_3", "")
	lw.svc.SeedPermission("per_1", "ses_1")
	lw.svc.SeedPermission("per_2", "ses_1")
	lw.svc.SeedForm("frm_1", "ses_1")
	lw.svc.SeedForm("frm_2", "ses_2")
	lw.svc.SeedForm("frm_3", "ses_2")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "one"}, [2]string{"ses_2", "two"}, [2]string{"ses_3", "three"})
	got := lw.eventually(t, "待處理", func(s string) bool { return hasLine(s, "? one") && hasLine(s, "? two") })
	if !hasLine(got, "◆ OPENCODE                       ?2") || !hasLine(got, "api                              ?2") {
		t.Fatalf("5 個請求只該算 2 個 tab：\n%s", got)
	}
	// 一個請求處理掉，tab 仍有另一個請求：維持 ?，不因單一回覆就當成已處理完
	lw.svc.ReplyPermission("per_1")
	lw.svc.AnswerForm("frm_2")
	lw.eventually(t, "交錯處理", func(s string) bool { return hasLine(s, "? one") && hasLine(s, "? two") })
	lw.svc.ReplyPermission("per_2")
	lw.svc.CancelForm("frm_3")
	lw.svc.AnswerForm("frm_1")
	got = lw.eventually(t, "全部處理完", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "· two") })
	if strings.Contains(got, "?") {
		t.Fatalf("不應殘留 ?：\n%s", got)
	}
}

func TestRequestsInterleavedWithSnapshotAreNeitherLostNorResurrected(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.SeedPermission("per_old", "ses_1")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "初始待核准", func(s string) bool { return hasLine(s, "? job") })

	// 初始請求在事件串流中被回覆，另一個新請求同時出現：不得因為 session 有事件就漏掉其中之一
	lw.svc.AskPermission("per_new", "ses_1")
	lw.svc.ReplyPermission("per_old")
	lw.eventually(t, "仍有一個待核准", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.ReplyPermission("per_new")
	lw.eventually(t, "全部回覆", func(s string) bool { return hasLine(s, "· job") })
}

func TestPendingRequestsAcrossReconnectComeFromTheFreshSnapshotOnly(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.AddSession("ses_2", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "one"}, [2]string{"ses_2", "two"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· one") })
	lw.svc.AskPermission("per_1", "ses_1")
	lw.eventually(t, "待核准", func(s string) bool { return hasLine(s, "? one") })

	// 斷線期間：per_1 被處理掉的事件遺失，另有新的問題出現在 ses_2
	_ = os.Remove(lw.svcFile)
	lw.svc.Drop()
	lw.eventually(t, "斷線為 !", func(s string) bool { return hasLine(s, "! one") })
	lw.svc.ReplyPermission("per_1")
	lw.svc.AskForm("frm_1", "ses_2")
	lw.publishService()
	lw.eventually(t, "重連後以最新資料為準", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "? two") })
}

func TestPendingRequestsVanishWhenServiceRestartsAndStaleOnesAreNotKept(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "one"})
	lw.svc.AskPermission("per_1", "ses_1")
	lw.eventually(t, "待核准", func(s string) bool { return hasLine(s, "? one") })

	lw.svc.ForgetPending() // 服務重啟：待處理請求不持久
	lw.svc.Drop()
	lw.eventually(t, "重連後不保留舊請求", func(s string) bool { return hasLine(s, "· one") })
}

func TestPendingRequestIsReadOnlyNothingIsEverReplied(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.SeedPermission("per_1", "ses_1")
	lw.svc.SeedForm("frm_1", "ses_1")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "one"})
	lw.eventually(t, "待處理", func(s string) bool { return hasLine(s, "? one") })
	time.Sleep(100 * time.Millisecond)
	// 結尾的 Cleanup 斷言所有請求都是 GET 且沒有 reply／view
	for _, r := range lw.svc.Requests() {
		if strings.Contains(r, "reply") {
			t.Fatalf("不得回覆：%s", r)
		}
	}
}
