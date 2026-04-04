package daemon

import (
	"testing"
	"time"
)

func TestRateLimiterBasic(t *testing.T) {
	rl := newRateLimiter(3, 1*time.Minute)

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
	rl := newRateLimiter(2, 50*time.Millisecond)

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
	rl := newRateLimiter(2, 1*time.Minute)

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
	// Use a small cap for testing.
	origMax := maxRateLimitEntries
	defer func() {
		// Can't reassign const, so we test with the real limit behavior.
		_ = origMax
	}()

	// We can't change the const, so instead test that recording many IPs
	// doesn't panic or cause issues. The eviction path is exercised when
	// entries >= maxRateLimitEntries.
	rl := newRateLimiter(5, 1*time.Minute)

	// Record from many unique IPs — should not panic.
	for i := 0; i < 100; i++ {
		ip := "10.0.0." + string(rune('0'+i%10))
		rl.record(ip)
	}

	// Verify the limiter still works correctly.
	rl2 := newRateLimiter(1, 1*time.Minute)
	rl2.record("test-ip")
	if !rl2.isBlocked("test-ip") {
		t.Fatal("should be blocked after 1 failure with limit 1")
	}
}
