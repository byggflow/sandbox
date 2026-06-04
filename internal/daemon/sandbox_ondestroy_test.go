package daemon

import (
	"sync/atomic"
	"testing"
)

func TestSandboxOnDestroyRunsCallbacks(t *testing.T) {
	s := &Sandbox{ID: "sbx-test"}

	var a, b atomic.Int32
	s.OnDestroy(func() { a.Add(1) })
	s.OnDestroy(func() { b.Add(1) })

	s.runDestroyCallbacks()

	if a.Load() != 1 || b.Load() != 1 {
		t.Errorf("expected each callback to run once: a=%d b=%d", a.Load(), b.Load())
	}

	// Running again should be a no-op (callbacks are consumed).
	s.runDestroyCallbacks()
	if a.Load() != 1 || b.Load() != 1 {
		t.Errorf("callbacks ran twice: a=%d b=%d", a.Load(), b.Load())
	}
}

func TestSandboxOnDestroyOrder(t *testing.T) {
	s := &Sandbox{ID: "sbx-test"}
	var order []string
	s.OnDestroy(func() { order = append(order, "first") })
	s.OnDestroy(func() { order = append(order, "second") })
	s.OnDestroy(func() { order = append(order, "third") })

	s.runDestroyCallbacks()

	if len(order) != 3 || order[0] != "first" || order[1] != "second" || order[2] != "third" {
		t.Errorf("callbacks did not run in registration order: %v", order)
	}
}

func TestSandboxOnDestroyIgnoresNil(t *testing.T) {
	s := &Sandbox{ID: "sbx-test"}
	s.OnDestroy(nil)
	// Should not panic.
	s.runDestroyCallbacks()
}
