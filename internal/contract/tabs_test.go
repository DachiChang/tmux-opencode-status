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

// cwd 模式：只讀 TUI 實際工作目錄（行程 cwd）對應的清單，沿用來源 tab 順序。
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

// global 模式：讀 global 共享清單，與 TUI 目錄無關（即使該目錄有自己的 cwd 清單）。
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

// 目錄身分是字串精確比對：不做 symlink／大小寫／結尾斜線正規化；
// 沒有該目錄的項目 = 「有 TUI、無 tab」，與「檔案不可讀」是不同結果。
func TestCwdModeUsesExactDirectoryIdentity(t *testing.T) {
	file, err := contract.ParseTabs(fixture(t, "tabs.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"/work/dir-2/", "/WORK/dir-2", "/work/dir-2/../dir-2", "/work/unknown"} {
		if got := file.Select(contract.ModeCwd, dir); len(got) != 0 {
			t.Fatalf("cwd %q 應無 tab，實得 %v", dir, sessionIDs(got))
		}
	}
}

// 格式不相容或內容損毀必須是明確錯誤，不能退化成空清單。
func TestParseTabsRejectsIncompatibleContent(t *testing.T) {
	bad := map[string]string{
		"空檔":            ``,
		"非 JSON":        `{"global":`,
		"缺 global":      `{"cwd":{}}`,
		"缺 cwd":         `{"global":{"tabs":[],"unread":{}}}`,
		"tabs 型別錯誤":     `{"global":{"tabs":{},"unread":{}},"cwd":{}}`,
		"sessionID 非字串": `{"global":{"tabs":[{"sessionID":1}],"unread":{}},"cwd":{}}`,
		"sessionID 缺漏":  `{"global":{"tabs":[{"title":"x"}],"unread":{}},"cwd":{}}`,
	}
	for name, content := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.ParseTabs([]byte(content)); err == nil {
				t.Fatalf("內容 %q 應回報錯誤", content)
			}
		})
	}
}

// 未知欄位要被容忍（非公開格式可能增欄）；title 保留作備援（TUI 本身以 API 的
// session.title 為先、tabs.json 的 title 為備援）；unread 已棄用且恆為空，不暴露。
func TestParseTabsToleratesUnknownFieldsAndKeepsPersistedTitle(t *testing.T) {
	file, err := contract.ParseTabs([]byte(`{"global":{"tabs":[{"sessionID":"ses_A","title":"persisted","extra":1}],"unread":{"ses_A":true},"future":1},"cwd":{},"other":1}`))
	if err != nil {
		t.Fatalf("未知欄位應被容忍：%v", err)
	}
	tabs := file.Select(contract.ModeGlobal, "")
	if len(tabs) != 1 || tabs[0].SessionID != "ses_A" || tabs[0].Title != "persisted" {
		t.Fatalf("tabs = %+v", tabs)
	}
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }
