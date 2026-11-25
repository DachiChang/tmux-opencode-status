package contract_test

import (
	"strings"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

func replay(t *testing.T, name string) (*contract.Tracker, []contract.Event) {
	t.Helper()
	events, err := contract.ParseSSE(strings.NewReader(string(fixture(t, name))))
	if err != nil {
		t.Fatal(err)
	}
	return contract.NewTracker(), events
}

// Recorded SSE: the first event is always server.connected; a heartbeat is a comment line (": heartbeat"),
// not a data event, and the parser must not treat it as an event.
func TestParseSSERecordedStream(t *testing.T) {
	_, events := replay(t, "events.sse")
	if len(events) != 25 {
		t.Fatalf("events = %d, want 25 (heartbeats are not events)", len(events))
	}
	if events[0].Type != contract.EventServerConnected {
		t.Fatalf("first event = %q, must be server.connected (the trigger for reconnect sync)", events[0].Type)
	}
	for _, e := range events {
		if e.Type == "" {
			t.Fatal("found an event without a type")
		}
	}
}

func TestParseSSEIgnoresCommentsAndHandlesCRLF(t *testing.T) {
	in := ": heartbeat\r\n\r\ndata: {\"id\":\"e1\",\"type\":\"session.viewed\",\"data\":{\"sessionID\":\"ses_A\",\"idle\":5}}\r\n\r\n: heartbeat\n\n"
	events, err := contract.ParseSSE(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "session.viewed" || events[0].SessionID != "ses_A" {
		t.Fatalf("events = %+v", events)
	}
}

// Replays events in recorded order and verifies tab status step by step: running → failed unread; running → succeeded unread → read → idle;
// an interruption is not a failure.
func TestTrackerReplayRunningThenUnreadThenViewed(t *testing.T) {
	tr, events := replay(t, "events.sse")
	type step struct {
		after string
		tab   string
		want  contract.Status
	}
	// Breakpoints are event ids (ids in the fixture are evt_<n>, in a fixed order).
	checks := map[string][]step{
		"evt_5":  {{"permission.asked", "ses_R", contract.WaitingApproval}},
		"evt_7":  {{"permission.replied", "ses_R", contract.WaitingAnswer}}, // the form is still unhandled
		"evt_8":  {{"form.cancelled", "ses_R", contract.Idle}},
		"evt_11": {{"execution.started", "ses_BAD", contract.Running}},
		"evt_13": {{"execution.failed", "ses_BAD", contract.FailedUnread}},
		"evt_16": {{"execution.started", "ses_OK", contract.Running}},
		"evt_18": {{"execution.succeeded", "ses_OK", contract.FinishedUnread}},
		"evt_19": {{"viewed", "ses_OK", contract.Idle}},
		"evt_22": {{"execution.started", "ses_I", contract.Running}},
		"evt_24": {{"execution.interrupted", "ses_I", contract.FinishedUnread}},
	}
	for _, e := range events {
		tr.Apply(e)
		for _, c := range checks[e.ID] {
			if got := tr.TabStatus(c.tab); got != c.want {
				t.Fatalf("after event %s(%s) TabStatus(%s) = %v, want %v", e.ID, c.after, c.tab, got, c.want)
			}
		}
	}
}

// session.created carries parentID: the family relationship can be built live from events; a grandchild's approval must roll up to the tab.
func TestTrackerBuildsFamilyFromCreatedEvents(t *testing.T) {
	tr, events := replay(t, "events.sse")
	for _, e := range events[:5] { // up to permission.asked
		tr.Apply(e)
	}
	if got := tr.TabStatus("ses_R"); got != contract.WaitingApproval {
		t.Fatalf("ses_R = %v, grandchild ses_G's approval should roll up to the tab", got)
	}
	if got := tr.TabStatus("ses_C"); got != contract.WaitingApproval {
		t.Fatalf("ses_C = %v", got)
	}
}

// After session.deleted the tab should be marked "missing", not idle.
func TestTrackerTreatsDeletedAsMissingNotIdle(t *testing.T) {
	tr, events := replay(t, "events.sse")
	for _, e := range events {
		tr.Apply(e)
	}
	if !tr.Missing("ses_BAD") {
		t.Fatal("should be marked missing after session.deleted")
	}
	if tr.Missing("ses_OK") {
		t.Fatal("ses_OK must not be marked missing")
	}
}

// After a reconnect it must fully resync: server.connected invalidates event-derived state until a new snapshot is applied;
// during invalidation the old state must not be treated as the latest fact.
func TestTrackerInvalidatesOnReconnectUntilSnapshotApplied(t *testing.T) {
	tr, events := replay(t, "events.sse")
	tr.Apply(events[0]) // server.connected
	if tr.Synced() {
		t.Fatal("must not count as synced before a snapshot is applied")
	}
	tr.ApplySnapshot(contract.Snapshot{})
	if !tr.Synced() {
		t.Fatal("should count as synced after a snapshot is applied")
	}
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	if tr.Synced() {
		t.Fatal("must be invalidated again after another reconnect")
	}
}

// Events received during initialization/reread must not be overwritten by an older snapshot (to avoid state regression).
func TestTrackerSnapshotDoesNotOverwriteNewerEvents(t *testing.T) {
	tr, events := replay(t, "events.sse")
	for _, e := range events {
		tr.Apply(e)
		if e.ID == "evt_14" { // ses_OK created
			break
		}
	}
	tr.Apply(contract.Event{Type: contract.EventServerConnected}) // reconnect, start syncing
	tr.Apply(contract.Event{ID: "x1", Type: "session.execution.succeeded", SessionID: "ses_OK", Created: 5000})

	// An older snapshot (read before succeeded) still says ses_OK is running.
	stale := contract.Snapshot{
		Sessions: []contract.Session{{ID: "ses_OK"}},
		Running:  map[string]bool{"ses_OK": true},
	}
	tr.ApplySnapshot(stale)
	if got := tr.TabStatus("ses_OK"); got != contract.FinishedUnread {
		t.Fatalf("older snapshot regressed the state: %v", got)
	}
}

// Observed contract: the created of a run-finished event is session.time.idle, and data.idle of session.viewed
// is the idle that was confirmed as read. So unread can be derived from events alone with no extra reads;
// the regression baseline comes from the REST snapshot and SSE of the same recording.
func TestEventTimestampsMatchSessionTimesInRecordedData(t *testing.T) {
	var file snapshotsFile
	if err := jsonUnmarshal(fixture(t, "snapshots.json"), &file); err != nil {
		t.Fatal(err)
	}
	_, events := replay(t, "events.sse")
	created := map[string]int64{}
	viewed := map[string]int64{}
	for _, e := range events {
		switch e.Type {
		case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
			created[e.SessionID] = e.Created
		case "session.viewed":
			viewed[e.SessionID] = e.Idle
		}
	}
	for _, snap := range file.Snapshots {
		for _, s := range snap.Sessions {
			if s.Time.Idle == 0 {
				continue
			}
			if got, ok := created[s.ID]; ok && got != s.Time.Idle {
				t.Errorf("%s: finish event created=%d, but REST time.idle=%d", s.ID, got, s.Time.Idle)
			}
			if s.Time.Viewed != 0 {
				if got, ok := viewed[s.ID]; ok && got != s.Time.Viewed {
					t.Errorf("%s: session.viewed idle=%d, but REST time.viewed=%d", s.ID, got, s.Time.Viewed)
				}
			}
		}
	}
}

// For a session that received events during sync, the snapshot must still fill in the static/monotonic data that events do not carry
// (parent-child relationship, viewed time), otherwise the family would miss members and reads would be lost; but it must not regress the event result.
func TestTrackerSnapshotStillFillsParentAndViewedForTouchedSessions(t *testing.T) {
	tr := contract.NewTracker()
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	tr.Apply(contract.Event{Type: "session.execution.started", SessionID: "child"}) // the creation event predates the connection, so there is no parentID
	snap := contract.Snapshot{
		Sessions: []contract.Session{{ID: "root"}, {ID: "child", ParentID: "root"}},
		Running:  map[string]bool{"child": true},
	}
	tr.ApplySnapshot(snap)
	if got := tr.TabStatus("root"); got != contract.Running {
		t.Fatalf("a descendant with events during sync must still belong to the tab family: %v", got)
	}

	tr = contract.NewTracker()
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	tr.Apply(contract.Event{Type: "session.execution.succeeded", SessionID: "root", Created: 900})
	old := contract.Session{ID: "root"}
	old.Time.Idle, old.Time.Viewed, old.Outcome = 500, 500, "failed" // older result
	tr.ApplySnapshot(contract.Snapshot{Sessions: []contract.Session{old}})
	if got := tr.TabStatus("root"); got != contract.FinishedUnread {
		t.Fatalf("older snapshot regressed the event result: %v", got)
	}

	fresh := contract.Session{ID: "root", Outcome: "succeeded"}
	fresh.Time.Idle, fresh.Time.Viewed = 900, 900 // another client has read it (the event was lost during the disconnect)
	tr.ApplySnapshot(contract.Snapshot{Sessions: []contract.Session{fresh}})
	if got := tr.TabStatus("root"); got != contract.Idle {
		t.Fatalf("the snapshot's viewed time should be adopted: %v", got)
	}
}

// When a session receives a pending event during sync, the snapshot must still fill in other requests that "existed before the connection and will not be sent again";
// requests that were replied to or cancelled during sync must not be revived by an older snapshot.
func TestTrackerSnapshotKeepsPreexistingRequestsOfTouchedSessions(t *testing.T) {
	tr := contract.NewTracker()
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	tr.Apply(contract.Event{Type: "permission.asked", SessionID: "s", RequestID: "new"})
	tr.ApplySnapshot(contract.Snapshot{
		Sessions:    []contract.Session{{ID: "s"}},
		Permissions: []contract.PermissionRequest{{ID: "old", SessionID: "s"}},
	})
	tr.Apply(contract.Event{Type: "permission.replied", SessionID: "s", RequestID: "new"})
	if got := tr.TabStatus("s"); got != contract.WaitingApproval {
		t.Fatalf("request that existed before the connection was missed: %v", got)
	}
	tr.Apply(contract.Event{Type: "permission.replied", SessionID: "s", RequestID: "old"})
	if got := tr.TabStatus("s"); got != contract.Idle {
		t.Fatalf("should return to idle after all are replied to: %v", got)
	}
}

func TestTrackerStaleSnapshotDoesNotResurrectRequestsResolvedDuringSync(t *testing.T) {
	tr := contract.NewTracker()
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	tr.Apply(contract.Event{Type: "permission.replied", SessionID: "s", RequestID: "p"})
	tr.Apply(contract.Event{Type: "form.cancelled", SessionID: "s", FormID: "f"})
	tr.ApplySnapshot(contract.Snapshot{
		Sessions:    []contract.Session{{ID: "s"}},
		Permissions: []contract.PermissionRequest{{ID: "p", SessionID: "s"}},
		Forms:       []contract.Form{{ID: "f", SessionID: "s"}},
	})
	if got := tr.TabStatus("s"); got != contract.Idle {
		t.Fatalf("older snapshot revived a handled request: %v", got)
	}
}

func TestTrackerRequestAskedDuringSyncSurvivesOlderSnapshot(t *testing.T) {
	tr := contract.NewTracker()
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	tr.Apply(contract.Event{Type: "form.created", SessionID: "s", FormID: "f"})
	tr.ApplySnapshot(contract.Snapshot{Sessions: []contract.Session{{ID: "s"}}}) // read before the form appeared
	if got := tr.TabStatus("s"); got != contract.WaitingAnswer {
		t.Fatalf("a request that appeared during sync must not be erased by an older snapshot: %v", got)
	}
}

// An interruption caused by service shutdown is not a finished run (the TUI ignores it too); the event alone does not change state.
func TestTrackerIgnoresShutdownInterruptionButKeepsOtherReasons(t *testing.T) {
	tr := contract.NewTracker()
	tr.ApplySnapshot(contract.Snapshot{Sessions: []contract.Session{{ID: "s"}}, Running: map[string]bool{"s": true}})
	tr.Apply(contract.Event{Type: "session.execution.interrupted", SessionID: "s", Reason: "shutdown", Created: 900})
	if got := tr.TabStatus("s"); got != contract.Running {
		t.Fatalf("a shutdown interruption must not end the run: %v", got)
	}
	for _, reason := range []string{"user", "superseded", "inactivity", ""} {
		tr.Apply(contract.Event{Type: "session.execution.started", SessionID: "s"})
		tr.Apply(contract.Event{Type: "session.execution.interrupted", SessionID: "s", Reason: reason, Created: 900})
		if got := tr.TabStatus("s"); got != contract.FinishedUnread {
			t.Fatalf("an interruption with reason=%q should be finished unread: %v", reason, got)
		}
	}
}

func TestParseSSEDecodesInterruptionReason(t *testing.T) {
	in := "data: {\"id\":\"e\",\"type\":\"session.execution.interrupted\",\"created\":5,\"data\":{\"sessionID\":\"s\",\"reason\":\"shutdown\"}}\n\n"
	events, err := contract.ParseSSE(strings.NewReader(in))
	if err != nil || len(events) != 1 || events[0].Reason != "shutdown" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

// Reading a single tab reconciles only that family: other tabs' state is unaffected; events received after the read began are newer than that read.
func TestTrackerApplyFamilyOnlyTouchesThatFamilyAndKeepsNewerEvents(t *testing.T) {
	tr := contract.NewTracker()
	tr.ApplySnapshot(contract.Snapshot{
		Sessions:    []contract.Session{{ID: "A"}, {ID: "B"}},
		Running:     map[string]bool{"B": true},
		Permissions: []contract.PermissionRequest{{ID: "pB", SessionID: "B"}},
	})
	mark := tr.Mark()
	tr.Apply(contract.Event{Type: "session.execution.started", SessionID: "A"}) // event during the read
	// An older read of A (did not see started); B is out of scope here
	tr.ApplyFamily("A", contract.Snapshot{Sessions: []contract.Session{{ID: "A"}}}, mark)
	if got := tr.TabStatus("A"); got != contract.Running {
		t.Fatalf("the event is newer, A must not regress: %v", got)
	}
	if got := tr.TabStatus("B"); got != contract.WaitingApproval {
		t.Fatalf("A's read must not change B: %v", got)
	}
	// A later new read (later since) follows the data
	tr.ApplyFamily("A", contract.Snapshot{Sessions: []contract.Session{{ID: "A"}}}, tr.Mark())
	if got := tr.TabStatus("A"); got != contract.Idle {
		t.Fatalf("the newer read should reconcile: %v", got)
	}
}
