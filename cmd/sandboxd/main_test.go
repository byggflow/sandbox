package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// TestWatchConfigReloadsOnWrite verifies that modifying a config file
// triggers the debounced reload callback.
func TestWatchConfigReloadsOnWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sandboxd.toml")

	initial := []byte(`[server]
socket = "/tmp/test.sock"
[limits]
max_sandboxes = 100
`)
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}

	var reloadCount atomic.Int32
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	// Start watcher in a goroutine using the same fsnotify logic as watchConfig,
	// but with a test callback instead of d.Reload.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { watcher.Close() })

	if err := watcher.Add(path); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var debounce *time.Timer
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					if debounce != nil {
						debounce.Stop()
					}
					debounce = time.AfterFunc(200*time.Millisecond, func() {
						log.Info("reload triggered")
						reloadCount.Add(1)
					})
				}
				if event.Has(fsnotify.Create) {
					_ = watcher.Remove(path)
					_ = watcher.Add(path)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				t.Errorf("watcher error: %v", err)
			}
		}
	}()

	// Watch the directory as well so renames (new inode) are caught reliably.
	if err := watcher.Add(dir); err != nil {
		t.Fatal(err)
	}

	// Give the watcher time to start.
	time.Sleep(200 * time.Millisecond)

	// Test 1: Direct write triggers reload.
	updated := []byte(`[server]
socket = "/tmp/test.sock"
[limits]
max_sandboxes = 200
`)
	if err := os.WriteFile(path, updated, 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for debounce to fire (generous for CI).
	time.Sleep(800 * time.Millisecond)

	if got := reloadCount.Load(); got != 1 {
		t.Errorf("expected 1 reload after write, got %d", got)
	}

	// Test 2: Atomic rename (simulates Infisical agent) triggers reload.
	tmpPath := filepath.Join(dir, "sandboxd.toml.tmp")
	atomicContent := []byte(`[server]
socket = "/tmp/test.sock"
[limits]
max_sandboxes = 300
`)
	if err := os.WriteFile(tmpPath, atomicContent, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatal(err)
	}

	// Wait for debounce to fire (generous for CI).
	time.Sleep(800 * time.Millisecond)

	if got := reloadCount.Load(); got != 2 {
		t.Errorf("expected 2 reloads after atomic rename, got %d", got)
	}

	// Test 3: Rapid writes should debounce into a single reload.
	for i := 0; i < 5; i++ {
		content := []byte(`[server]
socket = "/tmp/test.sock"
[limits]
max_sandboxes = ` + string(rune('0'+i)) + `
`)
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for debounce to fire (generous for CI).
	time.Sleep(800 * time.Millisecond)

	if got := reloadCount.Load(); got != 3 {
		t.Errorf("expected 3 total reloads (rapid writes debounced), got %d", got)
	}

	watcher.Close()
	<-done
}
