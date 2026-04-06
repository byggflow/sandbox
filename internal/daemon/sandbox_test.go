package daemon

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerateID(t *testing.T) {
	// Without node ID.
	id, err := GenerateID("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(id, "sbx-") {
		t.Errorf("expected prefix sbx-, got %s", id)
	}
	if len(id) != 12 { // "sbx-" + 8 hex chars
		t.Errorf("expected length 12, got %d (%s)", len(id), id)
	}

	// With node ID.
	id2, err := GenerateID("eu1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(id2, "sbx-eu1-") {
		t.Errorf("expected prefix sbx-eu1-, got %s", id2)
	}
	// "sbx-eu1-" (8) + 8 hex chars = 16
	if len(id2) != 16 {
		t.Errorf("expected length 16, got %d (%s)", len(id2), id2)
	}
}

func TestRegistryBasicOperations(t *testing.T) {
	reg := NewRegistry()

	sbx := &Sandbox{ID: "sbx-test1", Image: "test:latest"}
	if err := reg.Add(sbx); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Duplicate.
	if err := reg.Add(sbx); err == nil {
		t.Error("expected error for duplicate")
	}

	// Get.
	got, ok := reg.Get("sbx-test1")
	if !ok || got.ID != "sbx-test1" {
		t.Error("get failed")
	}

	// Count.
	if reg.Count() != 1 {
		t.Errorf("count: got %d, want 1", reg.Count())
	}

	// Remove returns true on first call.
	if !reg.Remove("sbx-test1") {
		t.Error("expected Remove to return true")
	}
	_, ok = reg.Get("sbx-test1")
	if ok {
		t.Error("expected not found after remove")
	}

	// Remove returns false on second call.
	if reg.Remove("sbx-test1") {
		t.Error("expected Remove to return false for already-removed sandbox")
	}
}

func TestSandboxReaper(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-reaper"}

	var destroyed atomic.Int32

	sbx.StartReaper(50*time.Millisecond, func() {
		destroyed.Add(1)
	})

	// Wait for reaper to fire.
	time.Sleep(100 * time.Millisecond)

	if destroyed.Load() != 1 {
		t.Errorf("expected destroy to be called once, got %d", destroyed.Load())
	}
}

func TestSandboxReaperCancel(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-cancel"}

	var destroyed atomic.Int32

	sbx.StartReaper(100*time.Millisecond, func() {
		destroyed.Add(1)
	})

	// Cancel before TTL expires.
	time.Sleep(20 * time.Millisecond)
	sbx.CancelReaper()

	// Wait past the original TTL.
	time.Sleep(150 * time.Millisecond)

	if destroyed.Load() != 0 {
		t.Errorf("expected destroy not to be called, got %d", destroyed.Load())
	}
}

func TestSandboxReaperReplace(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-replace"}

	var count atomic.Int32

	sbx.StartReaper(100*time.Millisecond, func() {
		count.Add(1)
	})

	// Replace reaper with a shorter one.
	time.Sleep(20 * time.Millisecond)
	sbx.StartReaper(30*time.Millisecond, func() {
		count.Add(10)
	})

	// Wait for the new reaper.
	time.Sleep(100 * time.Millisecond)

	// Only the second reaper should have fired.
	got := count.Load()
	if got != 10 {
		t.Errorf("expected 10 (only second reaper), got %d", got)
	}
}

func TestSandboxStateTransitions(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-state", State: StateRunning}

	if sbx.GetState() != StateRunning {
		t.Error("expected running")
	}

	sbx.SetState(StateDisconnected)
	if sbx.GetState() != StateDisconnected {
		t.Error("expected disconnected")
	}

	sbx.SetState(StateRunning)
	if sbx.GetState() != StateRunning {
		t.Error("expected running again")
	}
}

func TestRegistryCountByIdentity(t *testing.T) {
	reg := NewRegistry()

	// Add sandboxes with different identities.
	reg.Add(&Sandbox{ID: "sbx-a1", IdentityStr: "alice"})
	reg.Add(&Sandbox{ID: "sbx-a2", IdentityStr: "alice"})
	reg.Add(&Sandbox{ID: "sbx-a3", IdentityStr: "alice"})
	reg.Add(&Sandbox{ID: "sbx-b1", IdentityStr: "bob"})
	reg.Add(&Sandbox{ID: "sbx-none", IdentityStr: ""})

	tests := []struct {
		identity string
		want     int
	}{
		{"alice", 3},
		{"bob", 1},
		{"charlie", 0},
		{"", 1},
	}

	for _, tt := range tests {
		got := reg.CountByIdentity(tt.identity)
		if got != tt.want {
			t.Errorf("CountByIdentity(%q) = %d, want %d", tt.identity, got, tt.want)
		}
	}

	// Remove one of alice's sandboxes and verify count updates.
	if !reg.Remove("sbx-a1") {
		t.Error("expected Remove to return true")
	}
	if got := reg.CountByIdentity("alice"); got != 2 {
		t.Errorf("after remove: CountByIdentity(alice) = %d, want 2", got)
	}
}

func TestRegistryRemoveConcurrent(t *testing.T) {
	const numSandboxes = 100
	const goroutinesPerSandbox = 2

	reg := NewRegistry()

	// Register all sandboxes.
	for i := 0; i < numSandboxes; i++ {
		id := fmt.Sprintf("sbx-%04d", i)
		if err := reg.Add(&Sandbox{ID: id, Image: "test:latest"}); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}

	// For each sandbox, track how many goroutines get true from Remove.
	wins := make([]atomic.Int32, numSandboxes)

	var wg sync.WaitGroup
	wg.Add(numSandboxes * goroutinesPerSandbox)

	// Use a start barrier so all goroutines race at the same time.
	start := make(chan struct{})

	for i := 0; i < numSandboxes; i++ {
		id := fmt.Sprintf("sbx-%04d", i)
		for g := 0; g < goroutinesPerSandbox; g++ {
			idx := i
			go func() {
				defer wg.Done()
				<-start
				if reg.Remove(id) {
					wins[idx].Add(1)
				}
			}()
		}
	}

	close(start)
	wg.Wait()

	for i := 0; i < numSandboxes; i++ {
		got := wins[i].Load()
		if got != 1 {
			t.Errorf("sandbox sbx-%04d: expected exactly 1 winner, got %d", i, got)
		}
	}

	if reg.Count() != 0 {
		t.Errorf("expected empty registry, got %d", reg.Count())
	}
}
