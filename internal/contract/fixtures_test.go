package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fixture 不得含真實 session ID、使用者路徑、憑證或對話內容。
// 合成 ID 只允許 ses_<角色>（短）；真實 ID 為 ses_ 後接 20+ 英數字。
func TestFixturesAreDeidentified(t *testing.T) {
	realSession := regexp.MustCompile(`ses_[A-Za-z0-9]{20,}`)
	realEvent := regexp.MustCompile(`(evt|per|frm|msg|prj|sh)_[0-9a-f]{6,}[A-Za-z0-9]{6,}`)
	forbidden := []string{"/Users/", "/home/", "/private/var", "dachichang", "password", "Bearer ", "Authorization", "sk-", "UNMAPPED"}
	files, err := filepath.Glob("../../testdata/opencode-2.3.0/*")
	if err != nil || len(files) == 0 {
		t.Fatalf("找不到 fixture：%v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		if m := realSession.FindString(s); m != "" {
			t.Errorf("%s 含疑似真實 session ID：%s", f, m)
		}
		if m := realEvent.FindString(s); m != "" {
			t.Errorf("%s 含疑似真實物件 ID：%s", f, m)
		}
		for _, bad := range forbidden {
			if strings.Contains(strings.ToLower(s), strings.ToLower(bad)) {
				t.Errorf("%s 含禁止字樣 %q", f, bad)
			}
		}
	}
}
