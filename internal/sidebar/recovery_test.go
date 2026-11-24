package sidebar_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/fakeoc"
)

// ---- 資料失效、競爭與斷線恢復（issue #9）----
// 受控假 OpenCode 服務做故障注入（無聲斷線、延遲回應、單項失敗）；時間以短週期注入，
// 產品預設值（45 秒無聲逾時等）另由常數測試鎖定。從寫到 tmux 的側欄輸出觀察結果。

const downNotice = "Cannot connect to OpenCode"

func TestSilentDisconnectIsDetectedThenResyncedWithoutAnyReload(t *testing.T) {
	lw := startLiveOpts(t, liveOpts{idle: 200 * time.Millisecond, retry: 400 * time.Millisecond})
	lw.svc.SetHeartbeat(20 * time.Millisecond)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· job") })
	lw.svc.Start("ses_1")
	lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● job") })
	before := lw.svc.Connects()

	// 連線仍開著，但心跳與事件都不再送達（半開連線）；結束事件因此遺失
	lw.svc.Stall()
	lw.svc.Finish("ses_1", "succeeded")

	got := lw.eventually(t, "無聲逾時後不再把過期的 ● 當成事實", func(s string) bool { return hasLine(s, "! job") })
	if strings.Count(flat(got), flat(downNotice)) != 1 {
		t.Fatalf("斷線提示應只出現一次：\n%s", got)
	}
	lw.eventually(t, "自動重連並完整重讀（遺失的結束事件由 REST 補上）", func(s string) bool {
		return hasLine(s, "◆ job") && !strings.Contains(flat(s), flat(downNotice))
	})
	if lw.svc.Connects() <= before {
		t.Fatal("沒有重新連線")
	}
}

func TestHeartbeatsKeepAnIdleButHealthyConnectionAlive(t *testing.T) {
	lw := startLiveOpts(t, liveOpts{idle: 250 * time.Millisecond})
	lw.svc.SetHeartbeat(25 * time.Millisecond)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· job") })

	time.Sleep(900 * time.Millisecond) // 遠超過無聲逾時，但全程有心跳
	if n := lw.svc.Connects(); n != 1 {
		t.Fatalf("健康連線被誤判無聲而重連：連線 %d 次", n)
	}
	got := lw.eventually(t, "仍是正常狀態", func(s string) bool { return hasLine(s, "· job") })
	if strings.Contains(flat(got), flat(downNotice)) {
		t.Fatalf("不應有斷線提示：\n%s", got)
	}
}

func countReq(lw *liveWorld, exact string) int {
	n := 0
	for _, r := range lw.svc.Requests() {
		if r == exact {
			n++
		}
	}
	return n
}

// ---- 中斷事件：reason=shutdown 不是使用者中斷（契約：TUI 忽略；以重讀結果為準）----

func TestShutdownInterruptionIsNotTreatedAsFinishedAndIsConfirmedByReread(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· job") })
	lw.svc.Start("ses_1")
	lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● job") })

	// 事件單獨不能當成結束：服務資料仍說執行中，畫面不得變成 ◆
	lw.svc.EmitInterrupted("ses_1", "shutdown")
	time.Sleep(150 * time.Millisecond)
	got := lw.eventually(t, "shutdown 中斷不改變狀態", func(s string) bool { return hasLine(s, "● job") })
	if strings.Contains(got, "◆ job") {
		t.Fatalf("shutdown 不是使用者中斷，不得顯示結束未讀：\n%s", got)
	}

	// 事件觸發重讀：服務資料實際已結束（中斷、未讀）時，以資料為準
	lw.svc.Seed("ses_1", "", "interrupted", 900, 0, false)
	lw.svc.EmitInterrupted("ses_1", "shutdown")
	lw.eventually(t, "重讀後以服務資料為準", func(s string) bool { return hasLine(s, "◆ job") })
}

func TestUserInterruptionStillCountsAsFinishedUnreadNotFailure(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· job") })
	for _, reason := range []string{"user", "superseded", "inactivity"} {
		lw.svc.Start("ses_1")
		lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● job") })
		lw.svc.FinishInterrupted("ses_1", reason)
		got := lw.eventually(t, reason+" 中斷為結束未讀", func(s string) bool { return hasLine(s, "◆ job") })
		if strings.Contains(got, "✗") {
			t.Fatalf("中斷不得算失敗：\n%s", got)
		}
		lw.svc.View("ses_1")
		lw.eventually(t, "已讀", func(s string) bool { return hasLine(s, "· job") })
	}
}

// ---- 家族補漏重讀 ----

func TestDescendantCreatedWithoutParentInEventJoinsFamilyAtTheNextLowFrequencyResync(t *testing.T) {
	lw := startLiveOpts(t, liveOpts{resync: 150 * time.Millisecond})
	lw.svc.AddSession("ses_R", "")
	lw.familyTab(t, "root job")

	lw.svc.AddSessionWithoutParentInEvent("ses_C", "ses_R") // 事件缺 parentID：只靠事件歸不進家族
	lw.svc.Start("ses_C")
	lw.eventually(t, "補漏重讀後後代歸入家族", func(s string) bool { return hasLine(s, "● root job") })
}

func TestIdleHealthyConnectionDoesNotPollTheService(t *testing.T) {
	lw := startLive(t) // 產品預設補漏週期
	lw.publishService()
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.openTabs(t, "/work/api", [2]string{"ses_R", "root job"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· root job") })
	time.Sleep(100 * time.Millisecond)
	before := len(lw.svc.Requests())
	time.Sleep(600 * time.Millisecond)
	if after := len(lw.svc.Requests()); after != before {
		t.Fatalf("閒置期間不得輪詢：多了 %d 個請求：%v", after-before, lw.svc.Requests()[before:])
	}
}

// ---- 單一 tab 失敗隔離 ----

func TestOneTabsFailureNeitherAffectsNorRereadsTheOtherTabs(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_A", "")
	lw.svc.AddSession("ses_B", "")
	lw.svc.Break("/api/session/ses_B")
	lw.openTabs(t, "/work/api", [2]string{"ses_A", "good"}, [2]string{"ses_B", "bad"})
	lw.eventually(t, "B 失敗只影響 B", func(s string) bool { return hasLine(s, "· good") && hasLine(s, "! bad") })
	a1 := countReq(lw, "GET /api/session/ses_A")
	time.Sleep(300 * time.Millisecond) // B 重試多次
	if a2 := countReq(lw, "GET /api/session/ses_A"); a2 != a1 {
		t.Fatalf("B 的重試不得連帶重讀 A（%d → %d）", a1, a2)
	}
	if countReq(lw, "GET /api/session/ses_B") < 2 {
		t.Fatal("B 應該持續重試")
	}
	lw.svc.Start("ses_A") // A 在 B 失敗期間照常運作
	lw.eventually(t, "A 即時更新", func(s string) bool { return hasLine(s, "● good") && hasLine(s, "! bad") })
	lw.svc.Heal()
	lw.eventually(t, "B 恢復，A 不受影響", func(s string) bool { return hasLine(s, "● good") && hasLine(s, "· bad") })
}

func TestASlowTabDoesNotDelayTheOtherTabs(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_A", "")
	lw.svc.AddSession("ses_B", "")
	arrived, release := lw.svc.Hold("/api/session/ses_B")
	defer release()
	lw.openTabs(t, "/work/api", [2]string{"ses_A", "fast"}, [2]string{"ses_B", "slow"})
	<-arrived
	lw.eventually(t, "B 回應遲遲不來時 A 已正常", func(s string) bool { return hasLine(s, "· fast") && hasLine(s, "! slow") })
	release()
	lw.eventually(t, "B 回應到達後正常", func(s string) bool { return hasLine(s, "· fast") && hasLine(s, "· slow") })
}

func TestAddingATabReadsOnlyTheNewTab(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_A", "")
	lw.svc.AddSession("ses_B", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_A", "one"})
	lw.eventually(t, "A 正常", func(s string) bool { return hasLine(s, "· one") })
	a1 := countReq(lw, "GET /api/session/ses_A")
	lw.openTabs(t, "/work/api", [2]string{"ses_A", "one"}, [2]string{"ses_B", "two"})
	lw.eventually(t, "新 tab 正常", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "· two") })
	if a2 := countReq(lw, "GET /api/session/ses_A"); a2 != a1 {
		t.Fatalf("新增 tab 不得重讀既有 tab（%d → %d）", a1, a2)
	}
}

// ---- Session 不存在：保持區別、不重試、出現後自動恢復 ----

func TestMissingSessionIsKeptNotPolledAndRecoversWhenItAppears(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_ok", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_gone", "gone"}, [2]string{"ses_ok", "ok"})
	lw.eventually(t, "不存在的 session 保留為 !", func(s string) bool { return hasLine(s, "! gone") && hasLine(s, "· ok") })
	n1 := countReq(lw, "GET /api/session/ses_gone")
	time.Sleep(300 * time.Millisecond)
	if n2 := countReq(lw, "GET /api/session/ses_gone"); n2 != n1 {
		t.Fatalf("明確不存在不是暫時性錯誤，不得高頻重試（%d → %d）", n1, n2)
	}
	lw.svc.AddSession("ses_gone", "") // 之後才建立
	lw.eventually(t, "出現後自動恢復", func(s string) bool { return hasLine(s, "· gone") })
}

// ---- 提示：tab 清單不可讀與服務斷線並存 ----

// flat 去掉換行與空白：長提示會依寬度折行，斷言只關心文字內容。
func flat(s string) string { return strings.Join(strings.Fields(s), "") }

func TestUnreadableTabListAndServiceOutageAreBothReportedOnceEach(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "服務尚未開啟", func(s string) bool { return hasLine(s, "! job") && strings.Contains(flat(s), flat(downNotice)) })

	lw.tabs.fail(errBoom)
	lw.wake <- struct{}{}
	tabNotice := flat("Cannot read tab list")
	got := lw.eventually(t, "兩種提示並存", func(s string) bool {
		return strings.Contains(flat(s), tabNotice) && strings.Contains(flat(s), flat(downNotice))
	})
	if strings.Count(flat(got), tabNotice) != 1 || strings.Count(flat(got), flat(downNotice)) != 1 || !hasLine(got, "! job") {
		t.Fatalf("提示應各一次且保留最後清單：\n%s", got)
	}

	lw.publishService() // 服務恢復：只剩 tab 清單提示
	got = lw.eventually(t, "服務恢復後只剩 tab 清單提示", func(s string) bool {
		return hasLine(s, "· job") && strings.Contains(flat(s), tabNotice) && !strings.Contains(flat(s), flat(downNotice))
	})
	_ = got
}

// ---- 交錯與亂序 ----

func TestEventDuringASlowRereadWinsOverTheOlderResponse(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_1", "", "", 0, 0, true)
	arrived, release := lw.svc.Hold("/api/session/active") // 這個回應在讀取當下是「執行中」
	defer release()
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	<-arrived
	lw.svc.Finish("ses_1", "succeeded") // 較新的即時事件
	time.Sleep(50 * time.Millisecond)
	release() // 較舊的回應才抵達
	time.Sleep(100 * time.Millisecond)
	got := lw.eventually(t, "事件優先，狀態不倒退", func(s string) bool { return hasLine(s, "◆ job") })
	if strings.Contains(got, "● job") {
		t.Fatalf("舊回應讓狀態倒退：\n%s", got)
	}
}

func TestLateResponseFromAnOldConnectionNeverPollutesTheNewConnection(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_1", "", "", 0, 0, true)
	arrived, release := lw.svc.Hold("/api/session/active") // 舊連線的讀取：「執行中」
	defer release()
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	<-arrived

	lw.svc.Drop() // 舊連線中斷（讀取卡住中）；此後事件遺失
	lw.svc.Finish("ses_1", "succeeded")
	lw.eventually(t, "新連線完整重讀不必等舊回應", func(s string) bool { return hasLine(s, "◆ job") })
	release() // 舊回應姍姍來遲
	time.Sleep(150 * time.Millisecond)
	got := lw.eventually(t, "仍是新連線的結果", func(s string) bool { return hasLine(s, "◆ job") })
	if strings.Contains(got, "● job") {
		t.Fatalf("舊連線的延遲回應污染了新連線：\n%s", got)
	}
}

func TestLateResponseForARemovedTabNeverPollutesTheNewTabSet(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_old", "", "", 0, 0, true)
	lw.svc.AddSession("ses_new", "")
	arrived, release := lw.svc.Hold("/api/session/ses_old")
	defer release()
	lw.openTabs(t, "/work/api", [2]string{"ses_old", "old"})
	<-arrived
	// 切換模式：tab 清單整批換成另一份（舊 tab 的讀取仍卡著）
	lw.mode.use("global")
	lw.setTabs(t, `{"global":{"tabs":[{"sessionID":"ses_new","title":"shared"}]},"cwd":{}}`)
	lw.wake <- struct{}{}
	lw.eventually(t, "新模式的 tab 不受卡住的舊讀取影響", func(s string) bool { return hasLine(s, "· shared") && !strings.Contains(s, "old") })
	release()
	time.Sleep(150 * time.Millisecond)
	got := lw.eventually(t, "舊 tab 的遲到回應不影響新模式", func(s string) bool { return hasLine(s, "· shared") })
	if strings.Contains(got, "old") || strings.Contains(got, "●") {
		t.Fatalf("舊模式的結果污染了新模式：\n%s", got)
	}
}

// ---- 重新探索 ----

func TestServiceRestartedWithNewDiscoveryInfoIsFoundAndFullyResynced(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· job") })

	// 「重啟」：舊服務消失，另一個假服務以不同 port／狀態出現，探索檔改指向它
	next := fakeoc.New(t)
	next.Seed("ses_1", "", "failed", 500, 0, false)
	next.SeedPermission("per_x", "ses_1")
	lw.svc.Drop()
	_ = os.Remove(lw.svcFile)
	lw.eventually(t, "重啟空窗為 !", func(s string) bool { return hasLine(s, "! job") && strings.Contains(flat(s), flat(downNotice)) })
	next.WriteServiceFile(lw.svcFile)
	lw.eventually(t, "重新探索並完整同步", func(s string) bool { return hasLine(s, "? job") && !strings.Contains(flat(s), flat(downNotice)) })
	next.ReplyPermission("per_x")
	lw.eventually(t, "新服務的事件生效", func(s string) bool { return hasLine(s, "✗ job") })
	if w := next.Writes(); len(w) != 0 {
		t.Fatalf("寫入：%v", w)
	}
}

func TestStartupBeforeServiceExistsThenServiceAppearsWithoutReload(t *testing.T) {
	lw := startLive(t)
	lw.svc.Seed("ses_1", "", "succeeded", 500, 0, false)
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "服務未開", func(s string) bool { return hasLine(s, "! job") && strings.Contains(flat(s), flat(downNotice)) })
	time.Sleep(100 * time.Millisecond)
	lw.publishService()
	lw.eventually(t, "服務出現後自動接上", func(s string) bool { return hasLine(s, "◆ job") && !strings.Contains(flat(s), flat(downNotice)) })
}
