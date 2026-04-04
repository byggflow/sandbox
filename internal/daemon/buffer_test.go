package daemon

import (
	"fmt"
	"testing"
)

func TestNotificationBufferStateNotifications(t *testing.T) {
	buf := NewNotificationBuffer()

	// Add state notifications.
	for i := 0; i < 10; i++ {
		buf.Add("process.exit", []byte(fmt.Sprintf(`{"pid":%d}`, i)))
	}

	state, stream, truncated := buf.Drain()
	if len(stream) != 0 {
		t.Errorf("expected 0 stream, got %d", len(stream))
	}
	if truncated != 0 {
		t.Errorf("expected 0 truncated, got %d", truncated)
	}
	if len(state) != 10 {
		t.Errorf("expected 10 state, got %d", len(state))
	}
}

func TestNotificationBufferStreamEviction(t *testing.T) {
	buf := NewNotificationBuffer()

	// Override limits for testing.
	buf.maxStream = 5
	buf.maxBytes = 100000

	for i := 0; i < 10; i++ {
		buf.Add("process.stdout", []byte(fmt.Sprintf(`{"data":"msg%d"}`, i)))
	}

	state, stream, truncated := buf.Drain()
	if len(state) != 0 {
		t.Errorf("expected 0 state, got %d", len(state))
	}
	if len(stream) != 5 {
		t.Errorf("expected 5 stream, got %d", len(stream))
	}
	if truncated != 5 {
		t.Errorf("expected 5 truncated, got %d", truncated)
	}

	// Check that the oldest messages were evicted (we should have msg5-msg9).
	for i, n := range stream {
		expected := fmt.Sprintf(`{"data":"msg%d"}`, i+5)
		if string(n.Payload) != expected {
			t.Errorf("stream[%d] = %s, want %s", i, string(n.Payload), expected)
		}
	}
}

func TestNotificationBufferByteLimit(t *testing.T) {
	buf := NewNotificationBuffer()

	buf.maxStream = 10000
	buf.maxBytes = 50 // very small byte limit

	// Each message is ~15 bytes.
	for i := 0; i < 10; i++ {
		buf.Add("process.stderr", []byte(fmt.Sprintf(`{"d":"msg%d"}`, i)))
	}

	_, stream, truncated := buf.Drain()
	// With 50 bytes max and ~13 bytes per message, we should have ~3-4 messages.
	if len(stream) > 5 {
		t.Errorf("expected stream to be limited by bytes, got %d messages", len(stream))
	}
	if truncated == 0 {
		t.Error("expected some truncation")
	}
}

func TestNotificationBufferMixed(t *testing.T) {
	buf := NewNotificationBuffer()

	buf.Add("process.exit", []byte(`{"pid":1,"exit_code":0}`))
	buf.Add("process.stdout", []byte(`{"data":"hello"}`))
	buf.Add("process.stderr", []byte(`{"data":"err"}`))
	buf.Add("process.error", []byte(`{"pid":2,"error":"crash"}`))
	buf.Add("process.stdout", []byte(`{"data":"world"}`))

	state, stream, truncated := buf.Drain()
	if truncated != 0 {
		t.Errorf("expected 0 truncated, got %d", truncated)
	}
	if len(state) != 2 {
		t.Errorf("expected 2 state (exit + error), got %d", len(state))
	}
	if len(stream) != 3 {
		t.Errorf("expected 3 stream (stdout + stderr + stdout), got %d", len(stream))
	}
}

func TestNotificationBufferDrainResets(t *testing.T) {
	buf := NewNotificationBuffer()

	buf.Add("process.exit", []byte(`{"pid":1}`))
	buf.Add("process.stdout", []byte(`{"data":"x"}`))

	// First drain.
	state, stream, _ := buf.Drain()
	if len(state) != 1 || len(stream) != 1 {
		t.Fatal("first drain failed")
	}

	// Second drain should be empty.
	state, stream, truncated := buf.Drain()
	if len(state) != 0 || len(stream) != 0 || truncated != 0 {
		t.Error("expected empty after drain")
	}
}

func TestNotificationBufferCount(t *testing.T) {
	buf := NewNotificationBuffer()

	if buf.Count() != 0 {
		t.Error("expected 0")
	}

	buf.Add("process.exit", []byte(`{}`))
	buf.Add("process.stdout", []byte(`{}`))

	if buf.Count() != 2 {
		t.Errorf("expected 2, got %d", buf.Count())
	}
}

func TestNotificationBufferStateRing(t *testing.T) {
	buf := NewNotificationBuffer()
	buf.maxState = 5

	// Add more than max state notifications.
	for i := 0; i < 10; i++ {
		buf.Add("process.exit", []byte(fmt.Sprintf(`%d`, i)))
	}

	state, _, _ := buf.Drain()
	if len(state) > 5 {
		t.Errorf("expected at most 5 state notifications, got %d", len(state))
	}
}
