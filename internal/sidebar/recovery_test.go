package sidebar_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/fakeoc"
)

// ---- Data invalidation, races, and disconnect recovery (issue #9) ----
// A controlled fake OpenCode service injects faults (silent disconnects, delayed responses, single-item failures); time is injected with short periods,
// and the product defaults (45-second silence timeout, etc.) are pinned separately by constant tests. Results are observed from the sidebar output written to tmux.

const downNotice = "Cannot connect to OpenCode"

func TestSilentDisconnectIsDetectedThenResyncedWithoutAnyReload(t *testing.T) {
	lw := startLiveOpts(t, liveOpts{idle: 200 * time.Millisecond, retry: 400 * time.Millisecond})
	lw.svc.SetHeartbeat(20 * time.Millisecond)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· job") })
	lw.svc.Start("ses_1")
	lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● job") })
	before := lw.svc.Connects()

	// the connection stays open, but neither heartbeats nor events arrive anymore (half-open connection); the finish event is therefore lost
	lw.svc.Stall()
	lw.svc.Finish("ses_1", "succeeded")

	got := lw.eventually(t, "after the silence timeout a stale ● is no longer treated as fact", func(s string) bool { return hasLine(s, "! job") })
	if strings.Count(flat(got), flat(downNotice)) != 1 {
		t.Fatalf("the disconnect notice should appear only once:\n%s", got)
	}
	lw.eventually(t, "reconnects automatically and re-reads everything (the lost finish event is filled in by REST)", func(s string) bool {
		return hasLine(s, "◆ job") && !strings.Contains(flat(s), flat(downNotice))
	})
	if lw.svc.Connects() <= before {
		t.Fatal("did not reconnect")
	}
}

func TestHeartbeatsKeepAnIdleButHealthyConnectionAlive(t *testing.T) {
	lw := startLiveOpts(t, liveOpts{idle: 250 * time.Millisecond})
	lw.svc.SetHeartbeat(25 * time.Millisecond)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· job") })

	time.Sleep(900 * time.Millisecond) // far beyond the silence timeout, but heartbeats throughout
	if n := lw.svc.Connects(); n != 1 {
		t.Fatalf("a healthy connection was misjudged as silent and reconnected: connected %d times", n)
	}
	got := lw.eventually(t, "still in the normal state", func(s string) bool { return hasLine(s, "· job") })
	if strings.Contains(flat(got), flat(downNotice)) {
		t.Fatalf("there should be no disconnect notice:\n%s", got)
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

// ---- Interrupt events: reason=shutdown is not a user interrupt (contract: the TUI ignores it; the re-read result decides) ----

func TestShutdownInterruptionIsNotTreatedAsFinishedAndIsConfirmedByReread(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· job") })
	lw.svc.Start("ses_1")
	lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● job") })

	// the event alone cannot count as finished: the service data still says running, so the screen must not become ◆
	lw.svc.EmitInterrupted("ses_1", "shutdown")
	time.Sleep(150 * time.Millisecond)
	got := lw.eventually(t, "a shutdown interrupt does not change the status", func(s string) bool { return hasLine(s, "● job") })
	if strings.Contains(got, "◆ job") {
		t.Fatalf("shutdown is not a user interrupt and must not show finished unread:\n%s", got)
	}

	// the event triggers a re-read: when the service data has actually ended (interrupted, unread), the data wins
	lw.svc.Seed("ses_1", "", "interrupted", 900, 0, false)
	lw.svc.EmitInterrupted("ses_1", "shutdown")
	lw.eventually(t, "after the re-read the service data wins", func(s string) bool { return hasLine(s, "◆ job") })
}

func TestUserInterruptionStillCountsAsFinishedUnreadNotFailure(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· job") })
	for _, reason := range []string{"user", "superseded", "inactivity"} {
		lw.svc.Start("ses_1")
		lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● job") })
		lw.svc.FinishInterrupted("ses_1", reason)
		got := lw.eventually(t, reason+" an interrupt becomes finished unread", func(s string) bool { return hasLine(s, "◆ job") })
		if strings.Contains(got, "✗") {
			t.Fatalf("an interrupt must not count as a failure:\n%s", got)
		}
		lw.svc.View("ses_1")
		lw.eventually(t, "read", func(s string) bool { return hasLine(s, "· job") })
	}
}

// ---- Session family backstop re-read ----

func TestDescendantCreatedWithoutParentInEventJoinsFamilyAtTheNextLowFrequencyResync(t *testing.T) {
	lw := startLiveOpts(t, liveOpts{resync: 150 * time.Millisecond})
	lw.svc.AddSession("ses_R", "")
	lw.familyTab(t, "root job")

	lw.svc.AddSessionWithoutParentInEvent("ses_C", "ses_R") // the event lacks parentID: events alone cannot attribute it to the family
	lw.svc.Start("ses_C")
	lw.eventually(t, "after the backstop re-read the descendant joins the family", func(s string) bool { return hasLine(s, "● root job") })
}

func TestIdleHealthyConnectionDoesNotPollTheService(t *testing.T) {
	lw := startLive(t) // product default backstop period
	lw.publishService()
	lw.svc.AddSession("ses_R", "")
	lw.svc.AddSession("ses_C", "ses_R")
	lw.openTabs(t, "/work/api", [2]string{"ses_R", "root job"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· root job") })
	time.Sleep(100 * time.Millisecond)
	before := len(lw.svc.Requests())
	time.Sleep(600 * time.Millisecond)
	if after := len(lw.svc.Requests()); after != before {
		t.Fatalf("no polling while idle: %d extra requests: %v", after-before, lw.svc.Requests()[before:])
	}
}

// ---- Single tab failure isolation ----

func TestOneTabsFailureNeitherAffectsNorRereadsTheOtherTabs(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_A", "")
	lw.svc.AddSession("ses_B", "")
	lw.svc.Break("/api/session/ses_B")
	lw.openTabs(t, "/work/api", [2]string{"ses_A", "good"}, [2]string{"ses_B", "bad"})
	lw.eventually(t, "B's failure affects only B", func(s string) bool { return hasLine(s, "· good") && hasLine(s, "! bad") })
	a1 := countReq(lw, "GET /api/session/ses_A")
	time.Sleep(300 * time.Millisecond) // B retries several times
	if a2 := countReq(lw, "GET /api/session/ses_A"); a2 != a1 {
		t.Fatalf("B's retries must not cause A to be re-read (%d → %d)", a1, a2)
	}
	if countReq(lw, "GET /api/session/ses_B") < 2 {
		t.Fatal("B should keep retrying")
	}
	lw.svc.Start("ses_A") // A keeps working while B is failing
	lw.eventually(t, "A updates live", func(s string) bool { return hasLine(s, "● good") && hasLine(s, "! bad") })
	lw.svc.Heal()
	lw.eventually(t, "B recovers and A is unaffected", func(s string) bool { return hasLine(s, "● good") && hasLine(s, "· bad") })
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
	lw.eventually(t, "A is already normal while B's response is still pending", func(s string) bool { return hasLine(s, "· fast") && hasLine(s, "! slow") })
	release()
	lw.eventually(t, "normal after B's response arrives", func(s string) bool { return hasLine(s, "· fast") && hasLine(s, "· slow") })
}

func TestAddingATabReadsOnlyTheNewTab(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_A", "")
	lw.svc.AddSession("ses_B", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_A", "one"})
	lw.eventually(t, "A is normal", func(s string) bool { return hasLine(s, "· one") })
	a1 := countReq(lw, "GET /api/session/ses_A")
	lw.openTabs(t, "/work/api", [2]string{"ses_A", "one"}, [2]string{"ses_B", "two"})
	lw.eventually(t, "the new tab is normal", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "· two") })
	if a2 := countReq(lw, "GET /api/session/ses_A"); a2 != a1 {
		t.Fatalf("adding a tab must not re-read existing tabs (%d → %d)", a1, a2)
	}
}

// ---- Missing session: kept distinct, not retried, recovers automatically once it appears ----

func TestMissingSessionIsKeptNotPolledAndRecoversWhenItAppears(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_ok", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_gone", "gone"}, [2]string{"ses_ok", "ok"})
	lw.eventually(t, "a missing session is kept as !", func(s string) bool { return hasLine(s, "! gone") && hasLine(s, "· ok") })
	n1 := countReq(lw, "GET /api/session/ses_gone")
	time.Sleep(300 * time.Millisecond)
	if n2 := countReq(lw, "GET /api/session/ses_gone"); n2 != n1 {
		t.Fatalf("an explicit not-found is not a transient error and must not be retried at high frequency (%d → %d)", n1, n2)
	}
	lw.svc.AddSession("ses_gone", "") // created only later
	lw.eventually(t, "recovers automatically once it appears", func(s string) bool { return hasLine(s, "· gone") })
}

// ---- Notices: unreadable tab list and service disconnect together ----

// flat strips newlines and whitespace: long notices wrap by width, and the assertion only cares about the text content.
func flat(s string) string { return strings.Join(strings.Fields(s), "") }

func TestUnreadableTabListAndServiceOutageAreBothReportedOnceEach(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "service not started yet", func(s string) bool { return hasLine(s, "! job") && strings.Contains(flat(s), flat(downNotice)) })

	lw.tabs.fail(errBoom)
	lw.wake <- struct{}{}
	tabNotice := flat("Cannot read tab list")
	got := lw.eventually(t, "both notices coexist", func(s string) bool {
		return strings.Contains(flat(s), tabNotice) && strings.Contains(flat(s), flat(downNotice))
	})
	if strings.Count(flat(got), tabNotice) != 1 || strings.Count(flat(got), flat(downNotice)) != 1 || !hasLine(got, "! job") {
		t.Fatalf("each notice should appear once and the last list should be kept:\n%s", got)
	}

	lw.publishService() // service recovered: only the tab list notice remains
	got = lw.eventually(t, "after the service recovers only the tab list notice remains", func(s string) bool {
		return hasLine(s, "· job") && strings.Contains(flat(s), tabNotice) && !strings.Contains(flat(s), flat(downNotice))
	})
	_ = got
}

// ---- Interleaving and out-of-order responses ----

func TestEventDuringASlowRereadWinsOverTheOlderResponse(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_1", "", "", 0, 0, true)
	arrived, release := lw.svc.Hold("/api/session/active") // this response was "running" at the time of the read
	defer release()
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	<-arrived
	lw.svc.Finish("ses_1", "succeeded") // a newer live event
	time.Sleep(50 * time.Millisecond)
	release() // the older response arrives only now
	time.Sleep(100 * time.Millisecond)
	got := lw.eventually(t, "the event takes precedence and the status does not regress", func(s string) bool { return hasLine(s, "◆ job") })
	if strings.Contains(got, "● job") {
		t.Fatalf("the old response made the status regress:\n%s", got)
	}
}

func TestLateResponseFromAnOldConnectionNeverPollutesTheNewConnection(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_1", "", "", 0, 0, true)
	arrived, release := lw.svc.Hold("/api/session/active") // the old connection's read: "running"
	defer release()
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	<-arrived

	lw.svc.Drop() // the old connection dropped (read stuck); events are lost from here on
	lw.svc.Finish("ses_1", "succeeded")
	lw.eventually(t, "the new connection re-reads everything without waiting for the old response", func(s string) bool { return hasLine(s, "◆ job") })
	release() // the old response arrives belatedly
	time.Sleep(150 * time.Millisecond)
	got := lw.eventually(t, "still the new connection's result", func(s string) bool { return hasLine(s, "◆ job") })
	if strings.Contains(got, "● job") {
		t.Fatalf("the delayed response from the old connection polluted the new connection:\n%s", got)
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
	// switch mode: the whole tab list is swapped for another (the old tabs' reads are still stuck)
	lw.mode.use("global")
	lw.setTabs(t, `{"global":{"tabs":[{"sessionID":"ses_new","title":"shared"}]},"cwd":{}}`)
	lw.wake <- struct{}{}
	lw.eventually(t, "the new mode's tabs are unaffected by the stuck old reads", func(s string) bool { return hasLine(s, "· shared") && !strings.Contains(s, "old") })
	release()
	time.Sleep(150 * time.Millisecond)
	got := lw.eventually(t, "a late response from an old tab does not affect the new mode", func(s string) bool { return hasLine(s, "· shared") })
	if strings.Contains(got, "old") || strings.Contains(got, "●") {
		t.Fatalf("the old mode's result polluted the new mode:\n%s", got)
	}
}

// ---- Rediscovery ----

func TestServiceRestartedWithNewDiscoveryInfoIsFoundAndFullyResynced(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· job") })

	// "Restart": the old service disappears, another fake service appears with a different port/state, and the discovery file now points to it
	next := fakeoc.New(t)
	next.Seed("ses_1", "", "failed", 500, 0, false)
	next.SeedPermission("per_x", "ses_1")
	lw.svc.Drop()
	_ = os.Remove(lw.svcFile)
	lw.eventually(t, "the restart gap shows !", func(s string) bool { return hasLine(s, "! job") && strings.Contains(flat(s), flat(downNotice)) })
	next.WriteServiceFile(lw.svcFile)
	lw.eventually(t, "rediscovers and fully syncs", func(s string) bool { return hasLine(s, "? job") && !strings.Contains(flat(s), flat(downNotice)) })
	next.ReplyPermission("per_x")
	lw.eventually(t, "the new service's events take effect", func(s string) bool { return hasLine(s, "✗ job") })
	if w := next.Writes(); len(w) != 0 {
		t.Fatalf("writes: %v", w)
	}
}

func TestStartupBeforeServiceExistsThenServiceAppearsWithoutReload(t *testing.T) {
	lw := startLive(t)
	lw.svc.Seed("ses_1", "", "succeeded", 500, 0, false)
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "service not running", func(s string) bool { return hasLine(s, "! job") && strings.Contains(flat(s), flat(downNotice)) })
	time.Sleep(100 * time.Millisecond)
	lw.publishService()
	lw.eventually(t, "connects automatically once the service appears", func(s string) bool { return hasLine(s, "◆ job") && !strings.Contains(flat(s), flat(downNotice)) })
}
