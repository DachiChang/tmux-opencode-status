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

// ---- Run and unread lifecycle of a single session (issue #5) ----
// A controlled fake OpenCode service (httptest + SSE) plus real service discovery/auth/REST/SSE clients,
// with results observed from the sidebar output written to tmux.

type liveWorld struct {
	*world
	svc     *fakeoc.Server
	svcFile string
	wake    chan struct{}
}

// startLive starts the daemon loop (fake tmux data source plus fake OpenCode service). The service discovery file service.json
// is controlled by the test: before publishService the service is "not started yet".
func startLive(t *testing.T) *liveWorld { return startLiveWith(t, nil) }

// startLiveWith first configures the external world with setup (for example the mode option) before the daemon loop starts.
func startLiveWith(t *testing.T, setup func(*world)) *liveWorld {
	return startLiveOpts(t, liveOpts{setup: setup})
}

// liveOpts holds the time-related injection points: tests replace the product defaults (45-second silence, 60-second backstop re-read, 3-second retry) with short periods.
type liveOpts struct {
	setup  func(*world)
	idle   time.Duration // SSE silence timeout; 0 = product default
	resync time.Duration // session family backstop re-read period; 0 = product default
	retry  time.Duration // retry interval after a connection failure; 0 = 20ms
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
			t.Errorf("a write request was sent to OpenCode: %v", wr)
		}
		for _, r := range lw.svc.Requests() {
			if !strings.HasPrefix(r, "GET ") {
				t.Errorf("non-read-only request: %s", r)
			}
			if strings.Contains(r, "/view") || strings.Contains(r, "/reply") {
				t.Errorf("called the mark-as-read or reply API: %s", r)
			}
		}
	})
	return lw
}

func (lw *liveWorld) publishService() { lw.svc.WriteServiceFile(lw.svcFile) }

// openTab keeps a TUI open in dir, makes the tab list contain the given (sessionID,title) pairs, and wakes the loop.
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
	t.Fatalf("%s: not reached within 3 seconds; current screen:\n%s", what, last)
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

	// the service has not started: not enough data, stay at ! and do not show idle first
	lw.eventually(t, "stays ! while the service is not running", func(s string) bool { return hasLine(s, "! 修正登入") })

	lw.publishService()
	got := lw.eventually(t, "idle is shown after the snapshot", func(s string) bool { return hasLine(s, "· 修正登入") })
	if strings.Contains(got, "!") {
		t.Fatalf("there should be no abnormal symbols while idle:\n%s", got)
	}
}

func TestRunningThenFinishedUnreadThenReadByAnotherClient(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "修正登入"})
	lw.eventually(t, "idle after the snapshot", func(s string) bool { return hasLine(s, "· 修正登入") })

	lw.svc.Start("ses_1")
	got := lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● 修正登入") })
	if !hasLine(got, "OPENCODE                      ●1") || !hasLine(got, "api                           ●1") {
		t.Fatalf("summary not updated:\n%s", got)
	}

	lw.svc.Finish("ses_1", "succeeded")
	got = lw.eventually(t, "finished unread", func(s string) bool { return hasLine(s, "◆ 修正登入") })
	if !hasLine(got, "OPENCODE                      ◆1") {
		t.Fatalf("summary not updated:\n%s", got)
	}

	lw.svc.View("ses_1") // read by another client; the sidebar does not mark as read itself
	got = lw.eventually(t, "back to idle once read", func(s string) bool { return hasLine(s, "· 修正登入") })
	if hasLine(got, "OPENCODE                      ◆1") || strings.Contains(got, "●") {
		t.Fatalf("idle should not appear in the summary:\n%s", got)
	}
}

func TestFailedIsRedAndInterruptedIsNotAFailure(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_f", "")
	lw.svc.AddSession("ses_i", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_f", "fails"}, [2]string{"ses_i", "interrupted"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· fails") && hasLine(s, "· interrupted") })

	lw.svc.Start("ses_f")
	lw.svc.Finish("ses_f", "failed")
	lw.svc.Start("ses_i")
	lw.svc.Finish("ses_i", "interrupted")
	got := lw.eventually(t, "failed and interrupted", func(s string) bool { return hasLine(s, "✗ fails") && hasLine(s, "◆ interrupted") })
	if !hasLine(got, "OPENCODE                   ◆1 ✗1") {
		t.Fatalf("summary order should be ●, ?, ◆, ✗, !:\n%s", got)
	}
}

func TestUnreadStateBeforeStartupComesFromSnapshotAndSharedReadState(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.Seed("ses_a", "", "failed", 500, 0, false)      // failed unread
	lw.svc.Seed("ses_b", "", "succeeded", 500, 400, false) // viewed is earlier than idle: unread
	lw.svc.Seed("ses_c", "", "succeeded", 500, 500, false) // read
	lw.svc.Seed("ses_d", "", "interrupted", 500, 0, false) // interrupted unread
	lw.svc.Seed("ses_e", "", "", 0, 0, true)               // already running before startup
	lw.openTabs(t, "/work/api",
		[2]string{"ses_a", "A"}, [2]string{"ses_b", "B"}, [2]string{"ses_c", "C"}, [2]string{"ses_d", "D"}, [2]string{"ses_e", "E"})
	// The tabs' snapshots finish reading one after another, so the screen briefly shows ! along the way: wait until everything is in place before judging.
	lw.eventually(t, "snapshot statuses and summary", func(s string) bool {
		for _, want := range []string{"✗ A", "◆ B", "· C", "◆ D", "● E", "OPENCODE                ●1 ◆2 ✗1"} {
			if !hasLine(s, want) {
				return false
			}
		}
		return true
	})
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
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "· two-again") })

	lw.svc.Start("ses_2")
	got := lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● two") && hasLine(s, "● two-again") })
	order := []string{"· one", "● two", "● two-again"}
	last := -1
	for _, o := range order {
		i := strings.Index(got, o)
		if i <= last {
			t.Fatalf("tab order was changed (%q):\n%s", o, got)
		}
		last = i
	}
	// the same session appears in two groups: each group counts it once, and the overall summary deduplicates by session
	if !hasLine(got, "OPENCODE                      ●1") {
		t.Fatalf("the overall summary was not deduplicated by session:\n%s", got)
	}
	if !hasLine(got, "api                           ●1") || !hasLine(got, "web                           ●1") {
		t.Fatalf("group summary mismatch:\n%s", got)
	}
}

func TestServiceOutageShowsUnknownAndOneSharedNoticeThenRecovers(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.AddSession("ses_2", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "one"}, [2]string{"ses_2", "two"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "· two") })
	lw.svc.Start("ses_1")
	lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● one") })

	// the service disappears (discovery file removed, connection dropped): the old ● must not be reused as the latest fact
	_ = os.Remove(lw.svcFile)
	lw.svc.Drop()
	got := lw.eventually(t, "! after the disconnect", func(s string) bool { return hasLine(s, "! one") && hasLine(s, "! two") })
	if strings.Count(got, "Cannot connect to OpenCode") != 1 {
		t.Fatalf("the disconnect notice should appear only once:\n%s", got)
	}

	// events are lost during the disconnect; once the service is back everything is re-read, and events are not assumed to be replayable
	lw.svc.Finish("ses_1", "succeeded")
	lw.publishService()
	got = lw.eventually(t, "fully synced after reconnecting", func(s string) bool { return hasLine(s, "◆ one") && hasLine(s, "· two") })
	if strings.Contains(got, "Cannot connect") {
		t.Fatalf("the notice should disappear after recovery:\n%s", got)
	}
}

func TestSessionMissingKeepsTabAsUnknown(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_ok", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_gone", "gone"}, [2]string{"ses_ok", "ok"})
	got := lw.eventually(t, "a single tab's failure does not affect the others", func(s string) bool { return hasLine(s, "· ok") })
	if !hasLine(got, "! gone") {
		t.Fatalf("a tab whose session does not exist should be kept and marked !:\n%s", got)
	}
}

func TestSSEUsesOnlyDocumentedEventTypes(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "x"})
	lw.eventually(t, "connected", func(s string) bool { return hasLine(s, "· x") })
	qs := lw.svc.EventQueries()
	if len(qs) == 0 {
		t.Fatal("did not connect to /api/event")
	}
	for _, q := range qs {
		if !strings.Contains(q, "types=session.execution.started") || !strings.Contains(q, "types=session.viewed") {
			t.Fatalf("missing required event filter: %s", q)
		}
		if strings.Contains(q, "session.status") || strings.Contains(q, "session.idle") {
			t.Fatalf("must not depend on events the target version does not send: %s", q)
		}
	}
}

func TestNoTrackedTabsMeansNoSessionQueries(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.panes.set() // no TUI
	lw.wake <- struct{}{}
	lw.eventually(t, "no TUI", func(s string) bool { return strings.Contains(s, "No OpenCode TUI running") })
	time.Sleep(100 * time.Millisecond)
	if n := lw.svc.SessionRequests(); n != 0 {
		t.Fatalf("queried session data %d times with nothing tracked: %v", n, lw.svc.Requests())
	}
	if lw.svc.Connects() != 0 {
		t.Fatal("it should not connect with nothing tracked")
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
	lw.eventually(t, "GLOBAL group idle", func(s string) bool { return hasLine(s, "· shared") && strings.Contains(s, "GLOBAL") })
	lw.svc.Start("ses_g")
	lw.eventually(t, "GLOBAL group running", func(s string) bool { return hasLine(s, "● shared") })
}

func TestInvalidInitialModeDoesNotQueryTheService(t *testing.T) {
	lw := startLiveWith(t, func(w *world) { w.mode.use("globl") })
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "x"})
	lw.eventually(t, "notice", func(s string) bool { return strings.Contains(s, "Invalid mode") })
	time.Sleep(100 * time.Millisecond)
	if n := lw.svc.SessionRequests(); n != 0 || lw.svc.Connects() != 0 {
		t.Fatalf("connected/queried the service although tracking has not started: %v", lw.svc.Requests())
	}
}

// ---- Needs attention: waiting for approval and waiting for an answer (issue #6) ----

func TestPendingPermissionAndFormExistingAtStartupShowYellowQuestionMark(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_a", "")
	lw.svc.AddSession("ses_b", "")
	lw.svc.AddSession("ses_c", "")
	lw.svc.SeedPermission("per_1", "ses_a") // it existed before startup, so no event is ever sent
	lw.svc.SeedForm("frm_1", "ses_b")
	lw.openTabs(t, "/work/api", [2]string{"ses_a", "needs approval"}, [2]string{"ses_b", "needs answer"}, [2]string{"ses_c", "calm"})
	got := lw.eventually(t, "the pending items are read at initialization", func(s string) bool { return hasLine(s, "? needs approval") && hasLine(s, "? needs answer") })
	if !hasLine(got, "· calm") || !hasLine(got, "OPENCODE                      ?2") {
		t.Fatalf("summary or other tabs mismatch:\n%s", got)
	}
	// Colors: yellow ?, no background color blocks other than the title block, and not the old POC's yellow !
	// Look only at the normal layout: the labels in the jump layout (JumpOption) have a background color by design, and Options is a map with no fixed iteration order.
	opt := lw.out.styled()
	if strings.Contains(strings.ReplaceAll(opt, "#[bg=#A78BFA,fg=#18181B] OPENCODE ", ""), "bg=") {
		t.Fatalf("pending items must not use background color blocks other than the title: %q", opt)
	}
	if !strings.Contains(opt, "#[fg=#FACC15]?#[default] ") {
		t.Fatalf("no yellow ? found in the output: %q", opt)
	}
	if strings.Contains(opt, "#[fg=#FACC15]!") {
		t.Fatalf("the yellow ! must not be reused: %q", opt)
	}
}

func TestPermissionAndFormEventsAfterStartupAndTheirCompletionRestoreTheRightState(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· job") })

	lw.svc.AskPermission("per_1", "ses_1")
	lw.eventually(t, "waiting for approval", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.ReplyPermission("per_1")
	lw.eventually(t, "back to idle after the reply", func(s string) bool { return hasLine(s, "· job") })

	lw.svc.AskForm("frm_1", "ses_1")
	lw.eventually(t, "waiting for an answer", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.CancelForm("frm_1")
	lw.eventually(t, "back to idle after cancelling", func(s string) bool { return hasLine(s, "· job") })

	lw.svc.AskForm("frm_2", "ses_1")
	lw.eventually(t, "waiting for an answer", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.AnswerForm("frm_2")
	lw.eventually(t, "back to idle after answering", func(s string) bool { return hasLine(s, "· job") })
}

func TestApprovalBeatsAnswerAndBothBeatRunningAndUnread(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· job") })

	lw.svc.Start("ses_1")
	lw.eventually(t, "running", func(s string) bool { return hasLine(s, "● job") })
	lw.svc.AskForm("frm_1", "ses_1")
	got := lw.eventually(t, "waiting for an answer overrides running", func(s string) bool { return hasLine(s, "? job") })
	if strings.Contains(got, "●") {
		t.Fatalf("a tab may output only its highest-priority status:\n%s", got)
	}
	lw.svc.AskPermission("per_1", "ses_1")
	lw.svc.AnswerForm("frm_1") // the approval is still pending: stays ?
	lw.eventually(t, "still waiting for approval", func(s string) bool { return hasLine(s, "? job") })

	// idle is not guessed after a reply: the session is still running → back to running
	lw.svc.ReplyPermission("per_1")
	lw.eventually(t, "back to running after the reply", func(s string) bool { return hasLine(s, "● job") })

	// the run ended while pending: after the reply it is finished unread, not idle
	lw.svc.AskPermission("per_2", "ses_1")
	lw.eventually(t, "waiting for approval", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.Finish("ses_1", "failed")
	lw.eventually(t, "still ? although the run ended while pending", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.ReplyPermission("per_2")
	lw.eventually(t, "failed unread after the reply", func(s string) bool { return hasLine(s, "✗ job") })
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
	// Wait for three's snapshot to finish too (the summary briefly shows an extra !1 along the way), then judge that the 5 requests count as only 2 tabs.
	lw.eventually(t, "5 requests should count as only 2 tabs", func(s string) bool {
		return hasLine(s, "? one") && hasLine(s, "? two") && hasLine(s, "· three") &&
			hasLine(s, "OPENCODE                      ?2") && hasLine(s, "api                           ?2")
	})
	// one request is handled but the tab still has another: stays ?, and a single reply is not taken as everything handled
	lw.svc.ReplyPermission("per_1")
	lw.svc.AnswerForm("frm_2")
	lw.eventually(t, "interleaved handling", func(s string) bool { return hasLine(s, "? one") && hasLine(s, "? two") })
	lw.svc.ReplyPermission("per_2")
	lw.svc.CancelForm("frm_3")
	lw.svc.AnswerForm("frm_1")
	got := lw.eventually(t, "everything handled", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "· two") })
	if strings.Contains(got, "?") {
		t.Fatalf("no ? should remain:\n%s", got)
	}
}

func TestRequestsInterleavedWithSnapshotAreNeitherLostNorResurrected(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.SeedPermission("per_old", "ses_1")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "job"})
	lw.eventually(t, "initially waiting for approval", func(s string) bool { return hasLine(s, "? job") })

	// the initial request is replied to in the event stream while another new request appears at the same time: one of them must not be missed just because the session has events
	lw.svc.AskPermission("per_new", "ses_1")
	lw.svc.ReplyPermission("per_old")
	lw.eventually(t, "one is still waiting for approval", func(s string) bool { return hasLine(s, "? job") })
	lw.svc.ReplyPermission("per_new")
	lw.eventually(t, "all replied", func(s string) bool { return hasLine(s, "· job") })
}

func TestPendingRequestsAcrossReconnectComeFromTheFreshSnapshotOnly(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.AddSession("ses_2", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "one"}, [2]string{"ses_2", "two"})
	lw.eventually(t, "idle", func(s string) bool { return hasLine(s, "· one") })
	lw.svc.AskPermission("per_1", "ses_1")
	lw.eventually(t, "waiting for approval", func(s string) bool { return hasLine(s, "? one") })

	// during the disconnect: the event handling per_1 is lost, and a new question appears in ses_2
	_ = os.Remove(lw.svcFile)
	lw.svc.Drop()
	lw.eventually(t, "disconnect shows !", func(s string) bool { return hasLine(s, "! one") })
	lw.svc.ReplyPermission("per_1")
	lw.svc.AskForm("frm_1", "ses_2")
	lw.publishService()
	lw.eventually(t, "after reconnecting the latest data wins", func(s string) bool { return hasLine(s, "· one") && hasLine(s, "? two") })
}

func TestPendingRequestsVanishWhenServiceRestartsAndStaleOnesAreNotKept(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "one"})
	lw.svc.AskPermission("per_1", "ses_1")
	lw.eventually(t, "waiting for approval", func(s string) bool { return hasLine(s, "? one") })

	lw.svc.ForgetPending() // the service restarts: pending requests are not persistent
	lw.svc.Drop()
	lw.eventually(t, "old requests are not kept after reconnecting", func(s string) bool { return hasLine(s, "· one") })
}

func TestPendingRequestIsReadOnlyNothingIsEverReplied(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.SeedPermission("per_1", "ses_1")
	lw.svc.SeedForm("frm_1", "ses_1")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "one"})
	lw.eventually(t, "pending", func(s string) bool { return hasLine(s, "? one") })
	time.Sleep(100 * time.Millisecond)
	// the final Cleanup asserts that every request is a GET with no reply/view
	for _, r := range lw.svc.Requests() {
		if strings.Contains(r, "reply") {
			t.Fatalf("must not reply: %s", r)
		}
	}
}

// ---- Tab title: the API session.title wins and the title in tabs.json is only a fallback (contract §2) ----

func TestTabTitleFollowsAPISessionTitleOverTabsFileTitle(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.SeedTitle("ses_1", "API 最新標題")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "舊的備援標題"})
	got := lw.eventually(t, "the API title is read", func(s string) bool { return hasLine(s, "· API 最新標題") })
	if strings.Contains(got, "舊的備援標題") {
		t.Fatalf("the title from tabs.json should not be shown when an API title exists:\n%s", got)
	}
}

func TestTabTitleFallsBackToTabsFileWhenNoAPITitleIsAvailable(t *testing.T) {
	lw := startLive(t)
	lw.svc.AddSession("ses_1", "") // no title data
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "備援標題"})
	lw.eventually(t, "the fallback title is used while the service is not running", func(s string) bool { return hasLine(s, "! 備援標題") })
	lw.publishService()
	lw.eventually(t, "the fallback title is still used when the API has no title", func(s string) bool { return hasLine(s, "· 備援標題") })
}

func TestRenamedEventUpdatesTheTabTitleWithoutWaitingForTheBackstop(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.svc.SeedTitle("ses_1", "第一版標題")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "備援標題"})
	lw.eventually(t, "initial API title", func(s string) bool { return hasLine(s, "· 第一版標題") })

	lw.svc.Rename("ses_1", "改名後的標題")
	lw.eventually(t, "updated after session.renamed", func(s string) bool { return hasLine(s, "· 改名後的標題") })
}

func TestRenamedEventTypeIsSubscribed(t *testing.T) {
	lw := startLive(t)
	lw.publishService()
	lw.svc.AddSession("ses_1", "")
	lw.openTabs(t, "/work/api", [2]string{"ses_1", "x"})
	lw.eventually(t, "connection", func(s string) bool { return hasLine(s, "· x") })
	qs := lw.svc.EventQueries()
	if len(qs) == 0 || !strings.Contains(qs[0], "types=session.renamed") {
		t.Fatalf("the event connection must subscribe to session.renamed: %v", qs)
	}
}
