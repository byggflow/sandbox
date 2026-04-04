package daemon

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerateID(t *testing.T) {
	id, err := GenerateID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(id, "sbx-") {
		t.Errorf("expected prefix sbx-, got %s", id)
	}
	if len(id) != 12 { // "sbx-" + 8 hex chars
		t.Errorf("expected length 12, got %d (%s)", len(id), id)
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

	// Remove.
	reg.Remove("sbx-test1")
	_, ok = reg.Get("sbx-test1")
	if ok {
		t.Error("expected not found after remove")
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
