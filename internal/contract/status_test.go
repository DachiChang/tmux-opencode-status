package contract_test

import (
	"encoding/json"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

type snapshotsFile struct {
	Snapshots []struct {
		Name     string             `json:"name"`
		Sessions []contract.Session `json:"sessions"`
		Active   map[string]struct {
			Type string `json:"type"`
		} `json:"active"`
		Permissions []contract.PermissionRequest `json:"permissions"`
		Forms       []contract.Form              `json:"forms"`
	} `json:"snapshots"`
}

// 以家族 R(ses_R) → C → G 與獨立 session 的實測快照，驗證 API 資料到 tab 狀態的對應。
// 期望值取自父 spec 的狀態表，不是由實作反推。
func TestTabStatusFromRecordedSnapshots(t *testing.T) {
	var file snapshotsFile
	if err := json.Unmarshal(fixture(t, "snapshots.json"), &file); err != nil {
		t.Fatal(err)
	}
	type tabWant struct {
		tab  string
		want contract.Status
	}
	want := map[string][]tabWant{
		"01-created":                      {{"ses_R", contract.Idle}},
		"02-permission-in-grandchild":     {{"ses_R", contract.WaitingApproval}},
		"03-permission-and-form":          {{"ses_R", contract.WaitingApproval}},
		"04-permission-replied-form-open": {{"ses_R", contract.WaitingAnswer}},
		"05-all-resolved":                 {{"ses_R", contract.Idle}},
		"06-ok-running":                   {{"ses_OK", contract.Running}},
		"07-failed-and-succeeded-unread":  {{"ses_BAD", contract.FailedUnread}, {"ses_OK", contract.FinishedUnread}, {"ses_R", contract.Idle}},
		"08-succeeded-viewed":             {{"ses_OK", contract.Idle}},
		"09-running":                      {{"ses_I", contract.Running}},
		"10-interrupted-unread":           {{"ses_I", contract.FinishedUnread}},
	}
	for _, snap := range file.Snapshots {
		cases, ok := want[snap.Name]
		if !ok {
			t.Fatalf("快照 %q 沒有對應的期望值", snap.Name)
		}
		in := contract.Snapshot{Sessions: snap.Sessions, Permissions: snap.Permissions, Forms: snap.Forms, Running: map[string]bool{}}
		for id := range snap.Active {
			in.Running[id] = true
		}
		for _, c := range cases {
			t.Run(snap.Name+"/"+c.tab, func(t *testing.T) {
				if got := in.TabStatus(c.tab); got != c.want {
					t.Fatalf("TabStatus(%s) = %v, want %v", c.tab, got, c.want)
				}
			})
		}
	}
}

// 狀態優先序：待核准 > 待回答 > 執行中 > 失敗未讀 > 結束未讀 > 閒置；
// 未讀只看 tab 本身，後代的未讀與失敗不污染 tab。
func TestTabStatusPriorityAndFamilyRules(t *testing.T) {
	sess := func(id, parent string, outcome string, idle, viewed int64) contract.Session {
		s := contract.Session{ID: id, ParentID: parent, Outcome: outcome}
		s.Time.Idle, s.Time.Viewed = idle, viewed
		return s
	}
	perm := func(id string) contract.PermissionRequest { return contract.PermissionRequest{SessionID: id} }
	form := func(id string) contract.Form { return contract.Form{SessionID: id} }
	family := []contract.Session{sess("A", "", "succeeded", 10, 10), sess("B", "A", "failed", 20, 0), sess("C", "B", "", 0, 0)}

	t.Run("後代執行中時，主 session 結束仍呈現執行中", func(t *testing.T) {
		s := contract.Snapshot{Sessions: family, Running: map[string]bool{"C": true}}
		if got := s.TabStatus("A"); got != contract.Running {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("後代的失敗未讀不污染 tab", func(t *testing.T) {
		s := contract.Snapshot{Sessions: family}
		if got := s.TabStatus("A"); got != contract.Idle {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("核准優先於回答，兩者優先於執行中", func(t *testing.T) {
		s := contract.Snapshot{Sessions: family, Running: map[string]bool{"A": true},
			Permissions: []contract.PermissionRequest{perm("C")}, Forms: []contract.Form{form("B")}}
		if got := s.TabStatus("A"); got != contract.WaitingApproval {
			t.Fatalf("got %v", got)
		}
		s.Permissions = nil
		if got := s.TabStatus("A"); got != contract.WaitingAnswer {
			t.Fatalf("got %v", got)
		}
		s.Forms = nil
		if got := s.TabStatus("A"); got != contract.Running {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("未讀判定：idle 存在且 viewed 缺漏或較舊", func(t *testing.T) {
		for name, c := range map[string]struct {
			s    contract.Session
			want contract.Status
		}{
			"未曾 viewed":    {sess("X", "", "succeeded", 10, 0), contract.FinishedUnread},
			"viewed 較舊":    {sess("X", "", "succeeded", 10, 9), contract.FinishedUnread},
			"viewed 相等":    {sess("X", "", "succeeded", 10, 10), contract.Idle},
			"失敗且未讀":        {sess("X", "", "failed", 10, 0), contract.FailedUnread},
			"中斷不算失敗":       {sess("X", "", "interrupted", 10, 0), contract.FinishedUnread},
			"從未執行（無 idle）": {sess("X", "", "", 0, 0), contract.Idle},
		} {
			t.Run(name, func(t *testing.T) {
				in := contract.Snapshot{Sessions: []contract.Session{c.s}}
				if got := in.TabStatus("X"); got != c.want {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			})
		}
	})
	t.Run("viewed 的意義：time.idle 是 0 值（缺漏）時不算未讀", func(t *testing.T) {
		in := contract.Snapshot{Sessions: []contract.Session{sess("X", "", "succeeded", 0, 0)}}
		if got := in.TabStatus("X"); got != contract.Idle {
			t.Fatalf("got %v", got)
		}
	})
}
