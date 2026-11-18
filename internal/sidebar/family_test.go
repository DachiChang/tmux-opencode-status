package sidebar_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// ---- Session 家族：多層後代彙整到 tab（issue #7）----
// 受控假 OpenCode 服務 + 真實的探索／認證／REST／SSE 客戶端；從寫到 tmux 的側欄輸出觀察。
// 角色：R=root、C=child、G=grandchild；tab 只列 root（或指定的 session），後代不另列。

func (lw *liveWorld) familyTab(t *testing.T, title string) {
	t.Helper()
	lw.publishService()
	lw.openTabs(t, "/work/api", [2]string{"ses_R", title})
	lw.eventually(t, "家族讀完後閒置", func(s string) bool { return hasLine(s, "· "+title) })
}

func TestGrandchildRunningShowsOnRootTabAndOnlyTabIsListed(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.AddSession("ses_G", "ses_C")
	lw.familyTab(t, "root job")

	lw.svc.Start("ses_G")
	got := lw.eventually(t, "孫輩執行中算 tab 執行中", func(s string) bool { return hasLine(s, "● root job") })
	if strings.Contains(got, "ses_") || strings.Count(got, "●") != 3 { // tab 一個、群組摘要一個、全體摘要一個
		t.Fatalf("後代不得另列為項目：\n%s", got)
	}
	if !hasLine(got, "OPENCODE                      ●1") {
		t.Fatalf("摘要不符：\n%s", got)
	}
	lw.svc.Finish("ses_G", "succeeded")
	lw.eventually(t, "後代結束後回閒置（後代的未讀不污染 tab）", func(s string) bool { return hasLine(s, "· root job") })
}

func TestRootFinishesFirstWhileDescendantKeepsRunningThenRootUnreadAppears(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.AddSession("ses_G", "ses_C")
	lw.familyTab(t, "root job")

	lw.svc.Start("ses_R")
	lw.svc.Start("ses_G")
	lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● root job") })
	lw.svc.Finish("ses_R", "succeeded")
	time.Sleep(60 * time.Millisecond)
	got := lw.eventually(t, "主 session 結束但孫輩仍執行：維持 ●", func(s string) bool { return hasLine(s, "● root job") })
	if strings.Contains(got, "◆ root job") {
		t.Fatalf("不得提前顯示結束未讀：\n%s", got)
	}
	lw.svc.Finish("ses_G", "failed") // 後代失敗不是 tab 的失敗；tab 自己是成功結束未讀
	got = lw.eventually(t, "家族全部結束：tab 自己的結束未讀", func(s string) bool { return hasLine(s, "◆ root job") })
	if strings.Contains(got, "✗") {
		t.Fatalf("後代失敗不得變成 tab 失敗未讀：\n%s", got)
	}
}

func TestParentWokenAgainWhileDescendantsAreDone(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.Seed("ses_R", "", "succeeded", 100, 0, false) // 啟動前已結束且未讀
	lw.publishService()
	lw.openTabs(t, "/work/api", [2]string{"ses_R", "x"})
	lw.eventually(t, "未讀", func(s string) bool { return hasLine(s, "◆ x") })

	lw.svc.Start("ses_R") // 子 session 回報後父被喚醒
	lw.eventually(t, "父被喚醒回到執行中", func(s string) bool { return hasLine(s, "● x") })
	lw.svc.Finish("ses_R", "succeeded")
	lw.eventually(t, "結束後再次未讀", func(s string) bool { return hasLine(s, "◆ x") })
}

func TestPreexistingDescendantBlockersAreFoundWithoutAnyCreatedEvent(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	// 完全不經事件：直接放在服務裡（插件啟動前就存在）
	lw.svc.Seed("ses_R", "", "", 0, 0, false)
	lw.svc.Seed("ses_C", "ses_R", "", 0, 0, false)
	lw.svc.Seed("ses_G", "ses_C", "", 0, 0, true)
	lw.svc.Seed("ses_C2", "ses_R", "", 0, 0, false)
	lw.svc.Seed("ses_G2", "ses_C2", "", 0, 0, false)
	lw.svc.Seed("ses_Q", "", "", 0, 0, false)
	lw.svc.Seed("ses_Q1", "ses_Q", "", 0, 0, false)
	lw.svc.Seed("ses_Z", "", "", 0, 0, false)
	lw.svc.Seed("ses_Z1", "ses_Z", "", 0, 0, false)
	lw.svc.SeedPermission("per_1", "ses_G2")
	lw.svc.SeedForm("frm_1", "ses_Q1")
	lw.openTabs(t, "/work/api", [2]string{"ses_R", "approve"}, [2]string{"ses_Q", "answer"}, [2]string{"ses_Z", "calm"})
	got := lw.eventually(t, "既有後代的阻塞", func(s string) bool {
		return hasLine(s, "? approve") && hasLine(s, "? answer") && hasLine(s, "· calm")
	})
	if !hasLine(got, "OPENCODE                      ?2") {
		t.Fatalf("摘要不符：\n%s", got)
	}
	lw.svc.ReplyPermission("per_1")
	lw.eventually(t, "核准處理後仍有孫輩在執行", func(s string) bool { return hasLine(s, "● approve") })
}

func TestDescendantsCreatedAfterStartupJoinTheFamily(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.familyTab(t, "root job")

	lw.svc.AddSession("ses_C", "ses_R") // 啟動後才出現
	lw.svc.AddSession("ses_G", "ses_C")
	lw.svc.AddSession("ses_G2", "ses_G") // 第四層
	lw.svc.Start("ses_G2")
	lw.eventually(t, "新後代執行中", func(s string) bool { return hasLine(s, "● root job") })
	lw.svc.AskPermission("per_1", "ses_G2")
	lw.eventually(t, "新後代待核准", func(s string) bool { return hasLine(s, "? root job") })
	lw.svc.ReplyPermission("per_1")
	lw.svc.AskForm("frm_1", "ses_C")
	lw.eventually(t, "新後代待回答", func(s string) bool { return hasLine(s, "? root job") })
	lw.svc.AnswerForm("frm_1")
	lw.svc.Finish("ses_G2", "succeeded")
	lw.eventually(t, "全部結束回閒置", func(s string) bool { return hasLine(s, "· root job") })
}

func TestUnrelatedFamiliesAncestorsAndSiblingsDoNotLeakIntoATab(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	// R 底下有 C 與 C2；tab 開的是 C（子 session）與另一個無關家族 X
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.AddSession("ses_C2", "ses_R")
	lw.svc.AddSession("ses_X", "")
	lw.svc.AddSession("ses_X1", "ses_X")
	lw.openTabs(t, "/work/api", [2]string{"ses_C", "child tab"}, [2]string{"ses_X", "other"})
	lw.eventually(t, "閒置", func(s string) bool { return hasLine(s, "· child tab") && hasLine(s, "· other") })

	lw.svc.Start("ses_R")  // 祖先
	lw.svc.Start("ses_C2") // 兄弟
	lw.svc.AskPermission("per_sib", "ses_C2")
	lw.svc.AskForm("frm_anc", "ses_R")
	time.Sleep(100 * time.Millisecond)
	got := lw.eventually(t, "祖先與兄弟不影響 child tab", func(s string) bool { return hasLine(s, "· child tab") && hasLine(s, "· other") })
	if strings.Contains(got, "?") || strings.Contains(got, "●") {
		t.Fatalf("祖先或兄弟洩漏到 tab：\n%s", got)
	}

	lw.svc.Start("ses_X1") // 無關家族只影響自己的 tab
	got = lw.eventually(t, "無關家族各自獨立", func(s string) bool { return hasLine(s, "· child tab") && hasLine(s, "● other") })
	if !hasLine(got, "OPENCODE                      ●1") {
		t.Fatalf("摘要不符：\n%s", got)
	}
}

func TestDescendantUnreadAndFailureNeverBecomeTheTabsUnread(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_R", "", "", 0, 0, false)
	lw.svc.Seed("ses_C", "ses_R", "failed", 500, 0, false) // 啟動前就失敗未讀的後代
	lw.svc.Seed("ses_G", "ses_C", "succeeded", 600, 0, false)
	lw.openTabs(t, "/work/api", [2]string{"ses_R", "root job"})
	got := lw.eventually(t, "後代未讀不污染 tab", func(s string) bool { return hasLine(s, "· root job") })
	lw.svc.Start("ses_G")
	lw.svc.Finish("ses_G", "failed")
	lw.svc.Start("ses_C")
	lw.svc.Finish("ses_C", "failed")
	time.Sleep(100 * time.Millisecond)
	got = lw.eventually(t, "仍為閒置", func(s string) bool { return hasLine(s, "· root job") })
	if strings.Contains(got, "✗") || strings.Contains(got, "◆ root") {
		t.Fatalf("後代失敗／未讀污染了 tab：\n%s", got)
	}
}

func TestIncompleteFamilyDataNeverGuessesIdleAndRecovers(t *testing.T) {
	for name, broken := range map[string]string{
		"子層列表失敗":    "parentID=ses_C",
		"孫輩的權限查詢失敗": "/api/session/ses_G/permission",
		"孫輩的問題查詢失敗": "/api/session/ses_G/form",
	} {
		t.Run(name, func(t *testing.T) {
			lw := startLive(t)
			lw.publishService()
			lw.svc.Seed("ses_R", "", "", 0, 0, false)
			lw.svc.Seed("ses_C", "ses_R", "", 0, 0, false)
			lw.svc.Seed("ses_G", "ses_C", "", 0, 0, true)
			lw.svc.Seed("ses_Q", "", "", 0, 0, false)
			lw.svc.Break(broken)
			lw.openTabs(t, "/work/api", [2]string{"ses_R", "partial"}, [2]string{"ses_Q", "fine"})
			got := lw.eventually(t, "家族不完整：! 而不是閒置", func(s string) bool { return hasLine(s, "! partial") && hasLine(s, "· fine") })
			if strings.Contains(got, "· partial") || strings.Contains(got, "● partial") {
				t.Fatalf("不得猜測：\n%s", got)
			}
			lw.svc.Heal()
			lw.eventually(t, "資料補齊後自動恢復（孫輩仍在執行）", func(s string) bool { return hasLine(s, "● partial") && hasLine(s, "· fine") })
		})
	}
}

func TestSameSessionInManyTabsAndSourcesCountsOnceInSummaries(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.AddSession("ses_G", "ses_C")
	lw.svc.Start("ses_G")
	lw.svc.SeedPermission("per_1", "ses_G")
	// 兩個 TUI（不同目錄）、同一 session 出現兩次；另一個 tab 是 R 的後代 C（家族重疊）
	lw.panes.set(tui("%1", "/work/api"), tui("%2", "/work/web"))
	lw.setTabs(t, `{"global":{"tabs":[]},"cwd":{
		"/work/api":{"tabs":[{"sessionID":"ses_R","title":"root"},{"sessionID":"ses_C","title":"inner"}]},
		"/work/web":{"tabs":[{"sessionID":"ses_R","title":"root-again"}]}}}`)
	lw.wake <- struct{}{}
	got := lw.eventually(t, "三個 tab 都待核准", func(s string) bool {
		return hasLine(s, "? root") && hasLine(s, "? inner") && hasLine(s, "? root-again")
	})
	// 全體摘要按 session 去重：ses_R 與 ses_C 兩個 session = ?2；群組各按自己的 tab
	if !hasLine(got, "OPENCODE                      ?2") {
		t.Fatalf("全體摘要未按 session 去重：\n%s", got)
	}
	if !hasLine(got, "api                           ?2") || !hasLine(got, "web                           ?1") {
		t.Fatalf("群組摘要不符：\n%s", got)
	}
	// 重複的請求（同一個請求經多個 tab 的家族都讀到）處理一次就解除
	lw.svc.ReplyPermission("per_1")
	got = lw.eventually(t, "一次回覆解除所有重疊 tab", func(s string) bool {
		return hasLine(s, "● root") && hasLine(s, "● inner") && hasLine(s, "● root-again")
	})
	if !hasLine(got, "OPENCODE                      ●2") {
		t.Fatalf("摘要不符：\n%s", got)
	}
}

func TestNewTabOpenedLaterDiscoversItsExistingFamily(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_A", "")
	lw.svc.Seed("ses_R", "", "", 0, 0, false)
	lw.svc.Seed("ses_C", "ses_R", "", 0, 0, false)
	lw.svc.Seed("ses_G", "ses_C", "", 0, 0, false)
	lw.svc.SeedPermission("per_1", "ses_G")
	lw.openTabs(t, "/work/api", [2]string{"ses_A", "first"})
	lw.eventually(t, "第一個 tab", func(s string) bool { return hasLine(s, "· first") })

	lw.openTabs(t, "/work/api", [2]string{"ses_A", "first"}, [2]string{"ses_R", "second"})
	lw.eventually(t, "新開 tab 的既有孫輩阻塞被發現，原 tab 不受影響", func(s string) bool {
		return hasLine(s, "· first") && hasLine(s, "? second")
	})
}

func TestDescendantDeletedDuringOutageDoesNotLeaveAPhantomRunning(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.familyTab(t, "root job")
	lw.svc.Start("ses_C")
	lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● root job") })

	// 斷線期間後代連同其執行一起消失（事件遺失）；重連後以新的完整資料為準
	_ = os.Remove(lw.svcFile)
	lw.svc.Drop()
	lw.eventually(t, "斷線為 !", func(s string) bool { return hasLine(s, "! root job") })
	lw.svc.Delete("ses_C")
	lw.publishService()
	lw.eventually(t, "重連後不殘留已消失後代的執行中", func(s string) bool { return hasLine(s, "· root job") })
}

func TestDescendantDeletedWhileRunningOrBlockedStopsCounting(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.AddSession("ses_G", "ses_C")
	lw.familyTab(t, "root job")
	lw.svc.Start("ses_G")
	lw.svc.AskPermission("per_1", "ses_G")
	lw.eventually(t, "待核准", func(s string) bool { return hasLine(s, "? root job") })
	lw.svc.Delete("ses_G")
	lw.eventually(t, "後代刪除後不再阻塞或執行", func(s string) bool { return hasLine(s, "· root job") })
}

func TestFamilyLargerThanOnePageIsFullyDiscovered(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_R", "", "", 0, 0, false)
	for i := 0; i < 120; i++ {
		lw.svc.Seed(fmt.Sprintf("ses_c%03d", i), "ses_R", "", 0, 0, false)
	}
	lw.svc.Seed("ses_deep", "ses_c119", "", 0, 0, false) // 最後一頁的子層底下的孫輩
	lw.svc.SeedPermission("per_1", "ses_deep")
	lw.openTabs(t, "/work/api", [2]string{"ses_R", "big"})
	lw.eventually(t, "跨頁後代的阻塞被發現", func(s string) bool { return hasLine(s, "? big") })
}

func TestFamilyDiscoveryIsReadOnly(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.familyTab(t, "root job")
	lw.svc.Start("ses_C")
	lw.eventually(t, "執行中", func(s string) bool { return hasLine(s, "● root job") })
	for _, r := range lw.svc.Requests() {
		if !strings.HasPrefix(r, "GET ") {
			t.Fatalf("非唯讀請求：%s", r)
		}
	}
	if len(lw.svc.Writes()) != 0 {
		t.Fatalf("寫入：%v", lw.svc.Writes())
	}
}
