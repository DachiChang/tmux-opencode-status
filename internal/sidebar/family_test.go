package sidebar_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// ---- Session family: multi-level descendants rolled up into the tab (issue #7) ----
// A controlled fake OpenCode service plus real discovery/auth/REST/SSE clients; results are observed from the sidebar output written to tmux.
// Roles: R=root, C=child, G=grandchild; tabs list only the root (or the specified session), and descendants are not listed separately.

func (lw *liveWorld) familyTab(t *testing.T, title string) {
	t.Helper()
	lw.publishService()
	lw.openTabs(t, "/work/api", [2]string{"ses_R", title})
	lw.eventually(t, "idle once the family has been read", func(s string) bool { return hasLine(s, "· "+title) })
}

func TestGrandchildRunningShowsOnRootTabAndOnlyTabIsListed(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.AddSession("ses_G", "ses_C")
	lw.familyTab(t, "root job")

	lw.svc.Start("ses_G")
	got := lw.eventually(t, "a running grandchild makes the tab running", func(s string) bool { return hasLine(s, "● root job") })
	if strings.Contains(got, "ses_") || strings.Count(got, "●") != 3 { // one for the tab, one for the group summary, one for the overall summary
		t.Fatalf("descendants must not be listed as separate items:\n%s", got)
	}
	if !hasLine(got, "OPENCODE                      ●1") {
		t.Fatalf("summary mismatch:\n%s", got)
	}
	lw.svc.Finish("ses_G", "succeeded")
	lw.eventually(t, "returns to idle after descendants finish (descendant unread does not pollute the tab)", func(s string) bool { return hasLine(s, "· root job") })
}

func TestRootFinishesFirstWhileDescendantKeepsRunningThenRootUnreadAppears(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.AddSession("ses_G", "ses_C")
	lw.familyTab(t, "root job")

	lw.svc.Start("ses_R")
	lw.svc.Start("ses_G")
	lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● root job") })
	lw.svc.Finish("ses_R", "succeeded")
	time.Sleep(60 * time.Millisecond)
	got := lw.eventually(t, "the main session finished but a grandchild is still running: stays ●", func(s string) bool { return hasLine(s, "● root job") })
	if strings.Contains(got, "◆ root job") {
		t.Fatalf("must not show finished unread early:\n%s", got)
	}
	lw.svc.Finish("ses_G", "failed") // a descendant's failure is not the tab's failure; the tab itself finished successfully and is unread
	got = lw.eventually(t, "the whole family has finished: the tab's own finished unread", func(s string) bool { return hasLine(s, "◆ root job") })
	if strings.Contains(got, "✗") {
		t.Fatalf("a descendant failure must not become the tab's failed unread:\n%s", got)
	}
}

func TestParentWokenAgainWhileDescendantsAreDone(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.Seed("ses_R", "", "succeeded", 100, 0, false) // finished and unread before startup
	lw.publishService()
	lw.openTabs(t, "/work/api", [2]string{"ses_R", "x"})
	lw.eventually(t, "unread", func(s string) bool { return hasLine(s, "◆ x") })

	lw.svc.Start("ses_R") // the parent is woken after the child session reports back
	lw.eventually(t, "the parent is woken and returns to running", func(s string) bool { return hasLine(s, "● x") })
	lw.svc.Finish("ses_R", "succeeded")
	lw.eventually(t, "unread again after finishing", func(s string) bool { return hasLine(s, "◆ x") })
}

func TestPreexistingDescendantBlockersAreFoundWithoutAnyCreatedEvent(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	// no events at all: placed directly in the service (it existed before the plugin started)
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
	got := lw.eventually(t, "blocking from existing descendants", func(s string) bool {
		return hasLine(s, "? approve") && hasLine(s, "? answer") && hasLine(s, "· calm")
	})
	if !hasLine(got, "OPENCODE                      ?2") {
		t.Fatalf("summary mismatch:\n%s", got)
	}
	lw.svc.ReplyPermission("per_1")
	lw.eventually(t, "a grandchild is still running after the approval is handled", func(s string) bool { return hasLine(s, "● approve") })
}

func TestDescendantsCreatedAfterStartupJoinTheFamily(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.familyTab(t, "root job")

	lw.svc.AddSession("ses_C", "ses_R") // appears only after startup
	lw.svc.AddSession("ses_G", "ses_C")
	lw.svc.AddSession("ses_G2", "ses_G") // fourth level
	lw.svc.Start("ses_G2")
	lw.eventually(t, "the new descendant is running", func(s string) bool { return hasLine(s, "● root job") })
	lw.svc.AskPermission("per_1", "ses_G2")
	lw.eventually(t, "the new descendant is waiting for approval", func(s string) bool { return hasLine(s, "? root job") })
	lw.svc.ReplyPermission("per_1")
	lw.svc.AskForm("frm_1", "ses_C")
	lw.eventually(t, "the new descendant is waiting for an answer", func(s string) bool { return hasLine(s, "? root job") })
	lw.svc.AnswerForm("frm_1")
	lw.svc.Finish("ses_G2", "succeeded")
	lw.eventually(t, "everything finished; back to idle", func(s string) bool { return hasLine(s, "· root job") })
}

func TestUnrelatedFamiliesAncestorsAndSiblingsDoNotLeakIntoATab(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	// R has C and C2 under it; the tabs open are C (a child session) and X from an unrelated family
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.AddSession("ses_C2", "ses_R")
	lw.svc.AddSession("ses_X", "")
	lw.svc.AddSession("ses_X1", "ses_X")
	lw.openTabs(t, "/work/api", [2]string{"ses_C", "child tab"}, [2]string{"ses_X", "other"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· child tab") && hasLine(s, "· other") })

	lw.svc.Start("ses_R")  // ancestor
	lw.svc.Start("ses_C2") // sibling
	lw.svc.AskPermission("per_sib", "ses_C2")
	lw.svc.AskForm("frm_anc", "ses_R")
	time.Sleep(100 * time.Millisecond)
	got := lw.eventually(t, "ancestors and siblings do not affect the child tab", func(s string) bool { return hasLine(s, "· child tab") && hasLine(s, "· other") })
	if strings.Contains(got, "?") || strings.Contains(got, "●") {
		t.Fatalf("an ancestor or sibling leaked into the tab:\n%s", got)
	}

	lw.svc.Start("ses_X1") // an unrelated family affects only its own tab
	got = lw.eventually(t, "unrelated families are independent", func(s string) bool { return hasLine(s, "· child tab") && hasLine(s, "● other") })
	if !hasLine(got, "OPENCODE                      ●1") {
		t.Fatalf("summary mismatch:\n%s", got)
	}
}

func TestDescendantUnreadAndFailureNeverBecomeTheTabsUnread(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_R", "", "", 0, 0, false)
	lw.svc.Seed("ses_C", "ses_R", "failed", 500, 0, false) // a descendant that failed and was unread before startup
	lw.svc.Seed("ses_G", "ses_C", "succeeded", 600, 0, false)
	lw.openTabs(t, "/work/api", [2]string{"ses_R", "root job"})
	got := lw.eventually(t, "descendant unread does not pollute the tab", func(s string) bool { return hasLine(s, "· root job") })
	lw.svc.Start("ses_G")
	lw.svc.Finish("ses_G", "failed")
	lw.svc.Start("ses_C")
	lw.svc.Finish("ses_C", "failed")
	time.Sleep(100 * time.Millisecond)
	got = lw.eventually(t, "still idle", func(s string) bool { return hasLine(s, "· root job") })
	if strings.Contains(got, "✗") || strings.Contains(got, "◆ root") {
		t.Fatalf("descendant failure/unread polluted the tab:\n%s", got)
	}
}

func TestIncompleteFamilyDataNeverGuessesIdleAndRecovers(t *testing.T) {
	for name, broken := range map[string]string{
		"child list failure":                  "parentID=ses_C",
		"grandchild permission query failure": "/api/session/ses_G/permission",
		"grandchild question query failure":   "/api/session/ses_G/form",
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
			got := lw.eventually(t, "incomplete family: ! rather than idle", func(s string) bool { return hasLine(s, "! partial") && hasLine(s, "· fine") })
			if strings.Contains(got, "· partial") || strings.Contains(got, "● partial") {
				t.Fatalf("must not guess:\n%s", got)
			}
			lw.svc.Heal()
			lw.eventually(t, "recovers automatically once the data is complete (the grandchild is still running)", func(s string) bool { return hasLine(s, "● partial") && hasLine(s, "· fine") })
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
	// Two TUIs (different directories) with the same session appearing twice; another tab is R's descendant C (overlapping families)
	lw.panes.set(tui("%1", "/work/api"), tui("%2", "/work/web"))
	lw.setTabs(t, `{"global":{"tabs":[]},"cwd":{
		"/work/api":{"tabs":[{"sessionID":"ses_R","title":"root"},{"sessionID":"ses_C","title":"inner"}]},
		"/work/web":{"tabs":[{"sessionID":"ses_R","title":"root-again"}]}}}`)
	lw.wake <- struct{}{}
	got := lw.eventually(t, "all three tabs are waiting for approval", func(s string) bool {
		return hasLine(s, "? root") && hasLine(s, "? inner") && hasLine(s, "? root-again")
	})
	// The overall summary deduplicates by session: the two sessions ses_R and ses_C = ?2; each group counts its own tabs
	if !hasLine(got, "OPENCODE                      ?2") {
		t.Fatalf("the overall summary was not deduplicated by session:\n%s", got)
	}
	if !hasLine(got, "api                           ?2") || !hasLine(got, "web                           ?1") {
		t.Fatalf("group summary mismatch:\n%s", got)
	}
	// a duplicated request (the same request read through several tabs' families) is cleared once it is handled
	lw.svc.ReplyPermission("per_1")
	got = lw.eventually(t, "one reply clears every overlapping tab", func(s string) bool {
		return hasLine(s, "● root") && hasLine(s, "● inner") && hasLine(s, "● root-again")
	})
	if !hasLine(got, "OPENCODE                      ●2") {
		t.Fatalf("summary mismatch:\n%s", got)
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
	lw.eventually(t, "the first tab", func(s string) bool { return hasLine(s, "· first") })

	lw.openTabs(t, "/work/api", [2]string{"ses_A", "first"}, [2]string{"ses_R", "second"})
	lw.eventually(t, "the newly opened tab's existing grandchild blocking is detected, and the original tab is unaffected", func(s string) bool {
		return hasLine(s, "· first") && hasLine(s, "? second")
	})
}

func TestDescendantDeletedDuringOutageDoesNotLeaveAPhantomRunning(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.familyTab(t, "root job")
	lw.svc.Start("ses_C")
	lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● root job") })

	// during the disconnect the descendant disappears together with its run (events lost); after reconnecting the new full data wins
	_ = os.Remove(lw.svcFile)
	lw.svc.Drop()
	lw.eventually(t, "disconnect shows !", func(s string) bool { return hasLine(s, "! root job") })
	lw.svc.Delete("ses_C")
	lw.publishService()
	lw.eventually(t, "after reconnecting, no running state remains from the vanished descendant", func(s string) bool { return hasLine(s, "· root job") })
}

func TestDescendantDeletedWhileRunningOrBlockedStopsCounting(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.svc.AddSession("ses_G", "ses_C")
	lw.familyTab(t, "root job")
	lw.svc.Start("ses_G")
	lw.svc.AskPermission("per_1", "ses_G")
	lw.eventually(t, "waiting for approval", func(s string) bool { return hasLine(s, "? root job") })
	lw.svc.Delete("ses_G")
	lw.eventually(t, "after the descendant is deleted it no longer blocks or runs", func(s string) bool { return hasLine(s, "· root job") })
}

func TestFamilyLargerThanOnePageIsFullyDiscovered(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_R", "", "", 0, 0, false)
	for i := 0; i < 120; i++ {
		lw.svc.Seed(fmt.Sprintf("ses_c%03d", i), "ses_R", "", 0, 0, false)
	}
	lw.svc.Seed("ses_deep", "ses_c119", "", 0, 0, false) // a grandchild under a child on the last page
	lw.svc.SeedPermission("per_1", "ses_deep")
	lw.openTabs(t, "/work/api", [2]string{"ses_R", "big"})
	lw.eventually(t, "blocking from a descendant across pages is detected", func(s string) bool { return hasLine(s, "? big") })
}

func TestFamilyDiscoveryIsReadOnly(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.familyTab(t, "root job")
	lw.svc.Start("ses_C")
	lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● root job") })
	for _, r := range lw.svc.Requests() {
		if !strings.HasPrefix(r, "GET ") {
			t.Fatalf("non-read-only request: %s", r)
		}
	}
	if len(lw.svc.Writes()) != 0 {
		t.Fatalf("writes: %v", lw.svc.Writes())
	}
}
