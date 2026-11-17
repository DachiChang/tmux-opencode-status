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

// 錄製的 SSE：第一個事件一定是 server.connected；心跳是註解行（": heartbeat"），
// 不是 data 事件，解析時不得當成事件。
func TestParseSSERecordedStream(t *testing.T) {
	_, events := replay(t, "events.sse")
	if len(events) != 25 {
		t.Fatalf("events = %d, want 25（心跳不算事件）", len(events))
	}
	if events[0].Type != contract.EventServerConnected {
		t.Fatalf("第一個事件 = %q，必須是 server.connected（重連同步的觸發點）", events[0].Type)
	}
	for _, e := range events {
		if e.Type == "" {
			t.Fatal("出現沒有 type 的事件")
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

// 依錄製順序重播事件，逐步驗證 tab 狀態：執行 → 失敗未讀；執行 → 成功未讀 → 已讀 → 閒置；
// 中斷不算失敗。
func TestTrackerReplayRunningThenUnreadThenViewed(t *testing.T) {
	tr, events := replay(t, "events.sse")
	type step struct {
		after string
		tab   string
		want  contract.Status
	}
	// 以事件 id 為斷點（fixture 內 id 為 evt_<n>，順序固定）。
	checks := map[string][]step{
		"evt_5":  {{"permission.asked", "ses_R", contract.WaitingApproval}},
		"evt_7":  {{"permission.replied", "ses_R", contract.WaitingAnswer}}, // form 仍未處理
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
				t.Fatalf("事件 %s(%s) 後 TabStatus(%s) = %v, want %v", e.ID, c.after, c.tab, got, c.want)
			}
		}
	}
}

// session.created 帶 parentID：家族關係可由事件即時建立；孫輩的核准要彙整到 tab。
func TestTrackerBuildsFamilyFromCreatedEvents(t *testing.T) {
	tr, events := replay(t, "events.sse")
	for _, e := range events[:5] { // 到 permission.asked 為止
		tr.Apply(e)
	}
	if got := tr.TabStatus("ses_R"); got != contract.WaitingApproval {
		t.Fatalf("ses_R = %v，孫輩 ses_G 的核准應彙整到 tab", got)
	}
	if got := tr.TabStatus("ses_C"); got != contract.WaitingApproval {
		t.Fatalf("ses_C = %v", got)
	}
}

// session.deleted 之後 tab 應被標為「不存在」，而不是閒置。
func TestTrackerTreatsDeletedAsMissingNotIdle(t *testing.T) {
	tr, events := replay(t, "events.sse")
	for _, e := range events {
		tr.Apply(e)
	}
	if !tr.Missing("ses_BAD") {
		t.Fatal("session.deleted 後應標記為不存在")
	}
	if tr.Missing("ses_OK") {
		t.Fatal("ses_OK 不應被標為不存在")
	}
}

// 重連後必須完整同步：server.connected 使事件推導的狀態失效，直到新 snapshot 套用；
// 失效期間不能沿用舊狀態當成最新事實。
func TestTrackerInvalidatesOnReconnectUntilSnapshotApplied(t *testing.T) {
	tr, events := replay(t, "events.sse")
	tr.Apply(events[0]) // server.connected
	if tr.Synced() {
		t.Fatal("尚未套用 snapshot 前不應視為已同步")
	}
	tr.ApplySnapshot(contract.Snapshot{})
	if !tr.Synced() {
		t.Fatal("套用 snapshot 後應視為已同步")
	}
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	if tr.Synced() {
		t.Fatal("再次重連後必須重新失效")
	}
}

// 初始化／重讀期間收到的事件，不得被較舊的 snapshot 覆蓋（避免狀態倒退）。
func TestTrackerSnapshotDoesNotOverwriteNewerEvents(t *testing.T) {
	tr, events := replay(t, "events.sse")
	for _, e := range events {
		tr.Apply(e)
		if e.ID == "evt_14" { // ses_OK 建立
			break
		}
	}
	tr.Apply(contract.Event{Type: contract.EventServerConnected}) // 重連，開始同步
	tr.Apply(contract.Event{ID: "x1", Type: "session.execution.succeeded", SessionID: "ses_OK", Created: 5000})

	// 較舊的 snapshot（在 succeeded 之前讀取）仍說 ses_OK 執行中。
	stale := contract.Snapshot{
		Sessions: []contract.Session{{ID: "ses_OK"}},
		Running:  map[string]bool{"ses_OK": true},
	}
	tr.ApplySnapshot(stale)
	if got := tr.TabStatus("ses_OK"); got != contract.FinishedUnread {
		t.Fatalf("較舊 snapshot 讓狀態倒退：%v", got)
	}
}

// 實測契約：執行結束事件的 created 就是 session.time.idle，session.viewed 的 data.idle
// 是被確認已讀的 idle。因此事件即可推導未讀，不需要額外讀取；
// 回歸基準取自同一次錄製的 REST 快照與 SSE。
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
				t.Errorf("%s：結束事件 created=%d，但 REST time.idle=%d", s.ID, got, s.Time.Idle)
			}
			if s.Time.Viewed != 0 {
				if got, ok := viewed[s.ID]; ok && got != s.Time.Viewed {
					t.Errorf("%s：session.viewed idle=%d，但 REST time.viewed=%d", s.ID, got, s.Time.Viewed)
				}
			}
		}
	}
}

// 同步期間收到事件的 session，snapshot 仍要補上事件不會帶的靜態／單調資料
// （父子關係、已讀時間），否則家族會缺成員、已讀會被遺漏；但不得讓事件結果倒退。
func TestTrackerSnapshotStillFillsParentAndViewedForTouchedSessions(t *testing.T) {
	tr := contract.NewTracker()
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	tr.Apply(contract.Event{Type: "session.execution.started", SessionID: "child"}) // 建立事件在連線前，沒有 parentID
	snap := contract.Snapshot{
		Sessions: []contract.Session{{ID: "root"}, {ID: "child", ParentID: "root"}},
		Running:  map[string]bool{"child": true},
	}
	tr.ApplySnapshot(snap)
	if got := tr.TabStatus("root"); got != contract.Running {
		t.Fatalf("後代在同步期間有事件，仍須歸屬到 tab 家族：%v", got)
	}

	tr = contract.NewTracker()
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	tr.Apply(contract.Event{Type: "session.execution.succeeded", SessionID: "root", Created: 900})
	old := contract.Session{ID: "root"}
	old.Time.Idle, old.Time.Viewed, old.Outcome = 500, 500, "failed" // 較舊的結果
	tr.ApplySnapshot(contract.Snapshot{Sessions: []contract.Session{old}})
	if got := tr.TabStatus("root"); got != contract.FinishedUnread {
		t.Fatalf("較舊 snapshot 讓事件結果倒退：%v", got)
	}

	fresh := contract.Session{ID: "root", Outcome: "succeeded"}
	fresh.Time.Idle, fresh.Time.Viewed = 900, 900 // 其他 client 已讀（事件在斷線期間遺失）
	tr.ApplySnapshot(contract.Snapshot{Sessions: []contract.Session{fresh}})
	if got := tr.TabStatus("root"); got != contract.Idle {
		t.Fatalf("snapshot 的已讀時間應被採用：%v", got)
	}
}

// 同步期間某 session 收到待處理事件，snapshot 仍要補上「連線前就存在、事件不會再送」的其他請求；
// 同步期間已被回覆／取消的請求，則不得被較舊的 snapshot 復活。
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
		t.Fatalf("連線前就存在的請求被遺漏：%v", got)
	}
	tr.Apply(contract.Event{Type: "permission.replied", SessionID: "s", RequestID: "old"})
	if got := tr.TabStatus("s"); got != contract.Idle {
		t.Fatalf("全部回覆後應回到閒置：%v", got)
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
		t.Fatalf("較舊 snapshot 讓已處理的請求復活：%v", got)
	}
}

func TestTrackerRequestAskedDuringSyncSurvivesOlderSnapshot(t *testing.T) {
	tr := contract.NewTracker()
	tr.Apply(contract.Event{Type: contract.EventServerConnected})
	tr.Apply(contract.Event{Type: "form.created", SessionID: "s", FormID: "f"})
	tr.ApplySnapshot(contract.Snapshot{Sessions: []contract.Session{{ID: "s"}}}) // 在 form 出現前讀取
	if got := tr.TabStatus("s"); got != contract.WaitingAnswer {
		t.Fatalf("同步期間出現的請求不得被較舊 snapshot 抹掉：%v", got)
	}
}
