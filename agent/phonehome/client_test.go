package phonehome

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"
	"time"

	codec "github.com/byggflow/sandbox/agent/protocol"
	"github.com/byggflow/sandbox/protocol"
)

// TestCallDeliver simulates the daemon sending a response after the client
// makes a Call.
func TestCallDeliver(t *testing.T) {
	var buf safeBuffer
	c := New(&buf)

	done := make(chan *protocol.Response, 1)
	go func() {
		resp, err := c.Call("net.egress", map[string]string{"url": "https://example.com"}, 2*time.Second)
		if err != nil {
			t.Errorf("call: %v", err)
		}
		done <- resp
	}()

	// Wait for the call to write the frame.
	deadline := time.Now().Add(2 * time.Second)
	for buf.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// Decode the outbound frame to get the ID.
	frame, err := codec.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("read outbound frame: %v", err)
	}
	var req protocol.Request
	if err := json.Unmarshal(frame.Payload, &req); err != nil {
		t.Fatalf("decode outbound request: %v", err)
	}
	if req.Method != "net.egress" {
		t.Errorf("expected method net.egress, got %s", req.Method)
	}

	// Deliver a response.
	ok := c.Deliver(&protocol.Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{"status": "ok"}})
	if !ok {
		t.Fatal("Deliver returned false; expected pending call")
	}

	select {
	case resp := <-done:
		if resp.ID != req.ID {
			t.Errorf("response ID mismatch: got %d want %d", resp.ID, req.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("call did not return after Deliver")
	}
}

func TestCallTimeout(t *testing.T) {
	var buf safeBuffer
	c := New(&buf)
	_, err := c.Call("never.responds", nil, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestCloseDrainsPending(t *testing.T) {
	var buf safeBuffer
	c := New(&buf)
	done := make(chan error, 1)
	go func() {
		_, err := c.Call("hangs", nil, 5*time.Second)
		done <- err
	}()
	// Let the call register.
	deadline := time.Now().Add(time.Second)
	for buf.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	c.Close()
	select {
	case err := <-done:
		if err != nil {
			// expected: we wrote a synthetic "connection closed" response so
			// Call returns successfully with an error in Response.Error;
			// nothing to assert beyond non-deadlock.
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not drain pending calls")
	}
}

// safeBuffer is a goroutine-safe bytes.Buffer for use in tests.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *safeBuffer) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Read(p)
}
func (s *safeBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}
