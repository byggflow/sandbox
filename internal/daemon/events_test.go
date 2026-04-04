package daemon

import (
	"testing"
	"time"
)

func TestEventBusPublishAndReceive(t *testing.T) {
	bus := NewEventBus(0)

	id, ch := bus.Subscribe(10)
	defer bus.Unsubscribe(id)

	event := Event{
		Type:      "sandbox.created",
		SandboxID: "sbx-1234",
		Identity:  "user-1",
		Timestamp: time.Now(),
		Data:      map[string]interface{}{"image": "ubuntu:latest"},
	}

	bus.Publish(event)

	select {
	case got := <-ch:
		if got.Type != "sandbox.created" {
			t.Errorf("expected type sandbox.created, got %s", got.Type)
		}
		if got.SandboxID != "sbx-1234" {
			t.Errorf("expected sandbox_id sbx-1234, got %s", got.SandboxID)
		}
		if got.Identity != "user-1" {
			t.Errorf("expected identity user-1, got %s", got.Identity)
		}
		if got.Data["image"] != "ubuntu:latest" {
			t.Errorf("expected data.image ubuntu:latest, got %v", got.Data["image"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBusMultipleSubscribers(t *testing.T) {
	bus := NewEventBus(0)

	id1, ch1 := bus.Subscribe(10)
	defer bus.Unsubscribe(id1)

	id2, ch2 := bus.Subscribe(10)
	defer bus.Unsubscribe(id2)

	event := Event{
		Type:      "sandbox.destroyed",
		SandboxID: "sbx-5678",
		Timestamp: time.Now(),
	}

	bus.Publish(event)

	// Both subscribers should receive the event.
	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Type != "sandbox.destroyed" {
				t.Errorf("subscriber %d: expected type sandbox.destroyed, got %s", i, got.Type)
			}
			if got.SandboxID != "sbx-5678" {
				t.Errorf("subscriber %d: expected sandbox_id sbx-5678, got %s", i, got.SandboxID)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}
}

func TestEventBusDropWhenBufferFull(t *testing.T) {
	bus := NewEventBus(0)

	// Buffer size of 2.
	id, ch := bus.Subscribe(2)
	defer bus.Unsubscribe(id)

	// Publish 5 events — only 2 should be buffered, rest dropped.
	for i := 0; i < 5; i++ {
		bus.Publish(Event{
			Type:      "sandbox.created",
			SandboxID: "sbx-full",
			Timestamp: time.Now(),
		})
	}

	// Drain the channel.
	received := 0
	for {
		select {
		case <-ch:
			received++
		default:
			goto done
		}
	}
done:
	if received != 2 {
		t.Errorf("expected 2 events (buffer size), got %d", received)
	}
}

func TestEventBusUnsubscribe(t *testing.T) {
	bus := NewEventBus(0)

	id, ch := bus.Subscribe(10)

	// Unsubscribe.
	bus.Unsubscribe(id)

	// Channel should be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close")
	}

	// Publishing after unsubscribe should not panic.
	bus.Publish(Event{
		Type:      "sandbox.created",
		SandboxID: "sbx-after",
		Timestamp: time.Now(),
	})
}

func TestEventBusUnsubscribeIdempotent(t *testing.T) {
	bus := NewEventBus(0)

	id, _ := bus.Subscribe(10)

	// Unsubscribe twice should not panic.
	bus.Unsubscribe(id)
	bus.Unsubscribe(id)
}

func TestEventBusSequenceIDs(t *testing.T) {
	bus := NewEventBus(0)

	id, ch := bus.Subscribe(10)
	defer bus.Unsubscribe(id)

	for i := 0; i < 3; i++ {
		bus.Publish(Event{Type: "sandbox.created", SandboxID: "sbx-seq"})
	}

	for i := 1; i <= 3; i++ {
		ev := <-ch
		if ev.ID != uint64(i) {
			t.Errorf("event %d: expected ID %d, got %d", i, i, ev.ID)
		}
	}

	if bus.Seq() != 3 {
		t.Errorf("expected Seq()=3, got %d", bus.Seq())
	}
}

func TestEventBusSinceReplay(t *testing.T) {
	bus := NewEventBus(100)

	// Publish 5 events.
	for i := 0; i < 5; i++ {
		bus.Publish(Event{Type: "sandbox.created", SandboxID: "sbx-replay"})
	}

	// Replay from event 3 — should get events 4 and 5.
	events, complete := bus.Since(3)
	if !complete {
		t.Error("expected complete=true")
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].ID != 4 {
		t.Errorf("expected first replayed ID=4, got %d", events[0].ID)
	}
	if events[1].ID != 5 {
		t.Errorf("expected second replayed ID=5, got %d", events[1].ID)
	}
}

func TestEventBusSinceAll(t *testing.T) {
	bus := NewEventBus(100)

	for i := 0; i < 3; i++ {
		bus.Publish(Event{Type: "sandbox.created", SandboxID: "sbx-all"})
	}

	// Since(0) returns everything.
	events, complete := bus.Since(0)
	if !complete {
		t.Error("expected complete=true")
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestEventBusMaxSubscribers(t *testing.T) {
	bus := NewEventBus(0)

	// Fill up to max subscribers.
	ids := make([]string, 0, maxSubscribers)
	for i := 0; i < maxSubscribers; i++ {
		id, ch := bus.Subscribe(1)
		if ch == nil {
			t.Fatalf("subscribe %d: expected channel, got nil", i)
		}
		ids = append(ids, id)
	}

	// Next subscribe should be rejected.
	id, ch := bus.Subscribe(1)
	if ch != nil || id != "" {
		t.Fatal("expected nil channel when subscriber limit reached")
	}

	// Unsubscribe one and try again — should succeed.
	bus.Unsubscribe(ids[0])
	id, ch = bus.Subscribe(1)
	if ch == nil {
		t.Fatal("expected channel after freeing a slot")
	}
	bus.Unsubscribe(id)

	// Cleanup.
	for _, id := range ids[1:] {
		bus.Unsubscribe(id)
	}
}

func TestEventBusSinceGap(t *testing.T) {
	// Small ring buffer — only holds 3 events.
	bus := NewEventBus(3)

	// Publish 5 events. Events 1-2 are evicted.
	for i := 0; i < 5; i++ {
		bus.Publish(Event{Type: "sandbox.created", SandboxID: "sbx-gap"})
	}

	// Ask for events after ID 1 — but ID 2 has been evicted.
	events, complete := bus.Since(1)
	if complete {
		t.Error("expected complete=false because events were evicted")
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events (3,4,5), got %d", len(events))
	}
	if events[0].ID != 3 {
		t.Errorf("expected first event ID=3, got %d", events[0].ID)
	}
}
