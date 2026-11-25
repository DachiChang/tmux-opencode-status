package contract_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Fixtures must not contain real session IDs, user paths, credentials or conversation content.
// Synthetic IDs may only be ses_<role> (short); real IDs are ses_ followed by 20+ alphanumeric characters.
func TestFixturesAreDeidentified(t *testing.T) {
	realSession := regexp.MustCompile(`ses_[A-Za-z0-9]{20,}`)
	realEvent := regexp.MustCompile(`(evt|per|frm|msg|prj|sh)_[0-9a-f]{6,}[A-Za-z0-9]{6,}`)
	forbidden := []string{"/Users/", "/home/", "/private/var", "dachichang", "password", "Bearer ", "Authorization", "sk-", "UNMAPPED"}
	files, err := filepath.Glob("../../testdata/opencode-2.3.0/*")
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures not found: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		if m := realSession.FindString(s); m != "" {
			t.Errorf("%s contains what looks like a real session ID: %s", f, m)
		}
		if m := realEvent.FindString(s); m != "" {
			t.Errorf("%s contains what looks like a real object ID: %s", f, m)
		}
		for _, bad := range forbidden {
			if strings.Contains(strings.ToLower(s), strings.ToLower(bad)) {
				t.Errorf("%s contains forbidden string %q", f, bad)
			}
		}
	}
}
