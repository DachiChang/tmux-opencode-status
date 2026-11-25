package contract_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/opencode-2.3.0/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sessionIDs(tabs []contract.Tab) []string {
	var ids []string
	for _, tab := range tabs {
		ids = append(ids, tab.SessionID)
	}
	return ids
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// cwd mode: reads only the list for the TUI's actual working directory (the process cwd), keeping the source tab order.
func TestCwdModeReadsOnlyTheRunningTUIDirectory(t *testing.T) {
	file, err := contract.ParseTabs(fixture(t, "tabs.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := sessionIDs(file.Select(contract.ModeCwd, "/work/dir-2"))
	if want := []string{"ses_T3", "ses_T4", "ses_T5"}; !equal(got, want) {
		t.Fatalf("cwd /work/dir-2 = %v, want %v", got, want)
	}
}

// global mode: reads the shared global list, independent of the TUI directory (even if that directory has its own cwd list).
func TestGlobalModeReadsSharedListRegardlessOfDirectory(t *testing.T) {
	file, err := contract.ParseTabs(fixture(t, "tabs.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"/work/dir-1", "/work/dir-2", "/work/not-listed"} {
		got := sessionIDs(file.Select(contract.ModeGlobal, dir))
		if want := []string{"ses_T1"}; !equal(got, want) {
			t.Fatalf("global (dir=%s) = %v, want %v", dir, got, want)
		}
	}
}

// Directory identity is an exact string comparison: no symlink/case/trailing-slash normalization;
// no entry for the directory = "a TUI exists but has no tabs", which is a different result from "file unreadable".
func TestCwdModeUsesExactDirectoryIdentity(t *testing.T) {
	file, err := contract.ParseTabs(fixture(t, "tabs.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"/work/dir-2/", "/WORK/dir-2", "/work/dir-2/../dir-2", "/work/unknown"} {
		if got := file.Select(contract.ModeCwd, dir); len(got) != 0 {
			t.Fatalf("cwd %q should have no tabs, got %v", dir, sessionIDs(got))
		}
	}
}

// An incompatible format or corrupted content must be an explicit error and must not degrade into an empty list.
func TestParseTabsRejectsIncompatibleContent(t *testing.T) {
	bad := map[string]string{
		"empty file":           ``,
		"not JSON":             `{"global":`,
		"missing global":       `{"cwd":{}}`,
		"missing cwd":          `{"global":{"tabs":[],"unread":{}}}`,
		"wrong tabs type":      `{"global":{"tabs":{},"unread":{}},"cwd":{}}`,
		"non-string sessionID": `{"global":{"tabs":[{"sessionID":1}],"unread":{}},"cwd":{}}`,
		"missing sessionID":    `{"global":{"tabs":[{"title":"x"}],"unread":{}},"cwd":{}}`,
	}
	for name, content := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.ParseTabs([]byte(content)); err == nil {
				t.Fatalf("content %q should report an error", content)
			}
		})
	}
}

// Unknown fields must be tolerated (the non-public format may gain fields); title is kept as a fallback (the TUI itself prefers the API's
// session.title and uses the title in tabs.json as the fallback); unread is deprecated and always empty, so it is not exposed.
func TestParseTabsToleratesUnknownFieldsAndKeepsPersistedTitle(t *testing.T) {
	file, err := contract.ParseTabs([]byte(`{"global":{"tabs":[{"sessionID":"ses_A","title":"persisted","extra":1}],"unread":{"ses_A":true},"future":1},"cwd":{},"other":1}`))
	if err != nil {
		t.Fatalf("unknown fields should be tolerated: %v", err)
	}
	tabs := file.Select(contract.ModeGlobal, "")
	if len(tabs) != 1 || tabs[0].SessionID != "ses_A" || tabs[0].Title != "persisted" {
		t.Fatalf("tabs = %+v", tabs)
	}
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }
