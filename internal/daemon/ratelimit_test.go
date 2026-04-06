package daemon

import (
	"fmt"
	"testing"
	"time"
)

func TestRateLimiterBasic(t *testing.T) {
	rl := newRateLimiter(3, 1*time.Minute, 10000)

	if rl.isBlocked("1.2.3.4") {
		t.Fatal("should not be blocked before any failures")
	}

	rl.record("1.2.3.4")
	rl.record("1.2.3.4")
	if rl.isBlocked("1.2.3.4") {
		t.Fatal("should not be blocked after 2 failures (limit is 3)")
	}

	rl.record("1.2.3.4")
	if !rl.isBlocked("1.2.3.4") {
		t.Fatal("should be blocked after 3 failures")
	}
}

func TestRateLimiterWindowExpiry(t *testing.T) {
	rl := newRateLimiter(2, 50*time.Millisecond, 10000)

	rl.record("10.0.0.1")
	rl.record("10.0.0.1")
	if !rl.isBlocked("10.0.0.1") {
		t.Fatal("should be blocked")
	}

	time.Sleep(60 * time.Millisecond)

	if rl.isBlocked("10.0.0.1") {
		t.Fatal("should not be blocked after window expires")
	}
}

func TestRateLimiterIsolation(t *testing.T) {
	rl := newRateLimiter(2, 1*time.Minute, 10000)

	rl.record("10.0.0.1")
	rl.record("10.0.0.1")
	if !rl.isBlocked("10.0.0.1") {
		t.Fatal("10.0.0.1 should be blocked")
	}
	if rl.isBlocked("10.0.0.2") {
		t.Fatal("10.0.0.2 should not be blocked")
	}
}

func TestRateLimiterEviction(t *testing.T) {
	// Use a small maxEntries to exercise the eviction path.
	rl := newRateLimiter(5, 1*time.Minute, 5)

	// Record from more unique IPs than maxEntries.
	for i := 0; i < 10; i++ {
		ip := fmt.Sprintf("10.0.0.%d", i)
		rl.record(ip)
	}

	// Map should never exceed maxEntries.
	rl.mu.Lock()
	count := len(rl.entries)
	rl.mu.Unlock()
	if count > 5 {
		t.Errorf("expected at most 5 entries, got %d", count)
	}

	// Verify the limiter still works correctly.
	rl2 := newRateLimiter(1, 1*time.Minute, 10000)
	rl2.record("test-ip")
	if !rl2.isBlocked("test-ip") {
		t.Fatal("should be blocked after 1 failure with limit 1")
	}
}
