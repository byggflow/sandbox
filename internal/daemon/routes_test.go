package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/byggflow/sandbox/internal/config"
)

func TestHandleEventsHistory(t *testing.T) {
	d := &Daemon{
		Events: NewEventBus(100),
	}

	// Publish some events.
	d.Events.Publish(Event{Type: "sandbox.created", SandboxID: "sbx-1", Identity: "alice"})
	d.Events.Publish(Event{Type: "sandbox.destroyed", SandboxID: "sbx-1", Identity: "alice"})
	d.Events.Publish(Event{Type: "sandbox.created", SandboxID: "sbx-2", Identity: "bob"})

	t.Run("all events", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/events/history", nil)
		rec := httptest.NewRecorder()
		d.handleEventsHistory(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", rec.Code)
		}

		var resp struct {
			Events   []Event `json:"events"`
			Complete bool    `json:"complete"`
			Seq      uint64  `json:"seq"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.Events) != 3 {
			t.Errorf("expected 3 events, got %d", len(resp.Events))
		}
		if !resp.Complete {
			t.Error("expected complete=true")
		}
		if resp.Seq != 3 {
			t.Errorf("expected seq=3, got %d", resp.Seq)
		}
	})

	t.Run("after parameter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/events/history?after=1", nil)
		rec := httptest.NewRecorder()
		d.handleEventsHistory(rec, req)

		var resp struct {
			Events []Event `json:"events"`
		}
		json.NewDecoder(rec.Body).Decode(&resp)
		if len(resp.Events) != 2 {
			t.Errorf("expected 2 events after ID 1, got %d", len(resp.Events))
		}
		if resp.Events[0].ID != 2 {
			t.Errorf("expected first event ID=2, got %d", resp.Events[0].ID)
		}
	})

	t.Run("identity filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/events/history?identity=bob", nil)
		rec := httptest.NewRecorder()
		d.handleEventsHistory(rec, req)

		var resp struct {
			Events []Event `json:"events"`
		}
		json.NewDecoder(rec.Body).Decode(&resp)
		if len(resp.Events) != 1 {
			t.Errorf("expected 1 event for bob, got %d", len(resp.Events))
		}
	})

	t.Run("limit parameter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/events/history?limit=1", nil)
		rec := httptest.NewRecorder()
		d.handleEventsHistory(rec, req)

		var resp struct {
			Events []Event `json:"events"`
		}
		json.NewDecoder(rec.Body).Decode(&resp)
		if len(resp.Events) != 1 {
			t.Errorf("expected 1 event with limit=1, got %d", len(resp.Events))
		}
	})

	t.Run("invalid after", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/events/history?after=abc", nil)
		rec := httptest.NewRecorder()
		d.handleEventsHistory(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rec.Code)
		}
	})
}

func TestHandleEventsSSELastEventID(t *testing.T) {
	d := &Daemon{
		Events: NewEventBus(100),
	}

	// Publish events before the client connects.
	d.Events.Publish(Event{Type: "sandbox.created", SandboxID: "sbx-old"})
	d.Events.Publish(Event{Type: "sandbox.destroyed", SandboxID: "sbx-old"})
	d.Events.Publish(Event{Type: "sandbox.created", SandboxID: "sbx-new"})

	// Simulate SSE reconnect with Last-Event-ID: 1 (should replay events 2 and 3).
	req := httptest.NewRequest("GET", "/events", nil)
	req.Header.Set("Last-Event-ID", "1")

	// Cancel the context immediately so the handler writes replay then exits.
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	d.handleEvents(rec, req)

	body := rec.Body.String()

	// Should contain the replayed events (IDs 2 and 3).
	if !strings.Contains(body, "id: 2\n") {
		t.Errorf("expected replayed event with id: 2 in body:\n%s", body)
	}
	if !strings.Contains(body, "id: 3\n") {
		t.Errorf("expected replayed event with id: 3 in body:\n%s", body)
	}
}

func TestHandleEventsSSEGapNotification(t *testing.T) {
	// Tiny ring buffer — only 2 slots.
	d := &Daemon{
		Events: NewEventBus(2),
	}

	// Publish 4 events. Events 1-2 are evicted.
	for i := 0; i < 4; i++ {
		d.Events.Publish(Event{Type: "sandbox.created", SandboxID: "sbx-wrap"})
	}

	// Reconnect with Last-Event-ID: 1 — event 2 was evicted, so gap.
	req := httptest.NewRequest("GET", "/events", nil)
	req.Header.Set("Last-Event-ID", "1")
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	d.handleEvents(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: events.gap") {
		t.Errorf("expected events.gap notification in body:\n%s", body)
	}
}

func TestHandleHealthWithNodeID(t *testing.T) {
	cfg := config.Defaults()
	cfg.Server.NodeID = "eu1"
	d := &Daemon{
		Config:   cfg,
		Registry: NewRegistry(),
	}

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	d.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	nodeID, ok := resp["node_id"]
	if !ok {
		t.Fatal("expected node_id in health response")
	}
	if nodeID != "eu1" {
		t.Errorf("expected node_id=eu1, got %v", nodeID)
	}
}

func TestHandleHealthWithoutNodeID(t *testing.T) {
	d := &Daemon{
		Config:   config.Defaults(),
		Registry: NewRegistry(),
	}

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	d.handleHealth(rec, req)

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)

	if _, ok := resp["node_id"]; ok {
		t.Error("expected no node_id when not configured")
	}
}
