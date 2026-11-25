package tabsrc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitWake(t *testing.T, wake <-chan struct{}, within time.Duration) bool {
	t.Helper()
	select {
	case <-wake:
		return true
	case <-time.After(within):
		return false
	}
}

// Startup gap: tabs.json changes before the watcher is installed, so no file event fires.
// Once the watcher is ready it must wake the caller once so it rereads, rather than
// waiting for the low-frequency catch-up.
func TestWatchWakesOnceWhenReadyEvenWithoutAnyFileEvent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tabs.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	go Source{Path: filepath.Join(dir, "tabs.json")}.Watch(ctx, wake)

	if !waitWake(t, wake, time.Second) {
		t.Fatal("expected one wake within 1s of the watcher becoming ready")
	}
	if waitWake(t, wake, 300*time.Millisecond) {
		t.Fatal("must not wake again when no file changed")
	}
}

func TestWatchStillWakesOnRenameUpdates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tabs.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	go Source{Path: path}.Watch(ctx, wake)
	waitWake(t, wake, time.Second) // ready wake (if any)
	time.Sleep(200 * time.Millisecond)
	for len(wake) > 0 {
		<-wake
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	if !waitWake(t, wake, time.Second) {
		t.Fatal("expected a wake within 1s when tabs.json is updated via rename")
	}
}
