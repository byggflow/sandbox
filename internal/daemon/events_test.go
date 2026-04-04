package daemon

import (
	"testing"
	"time"
)

func TestEventBusPublishAndReceive(t *testing.T) {
	bus := NewEventBus()

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
	bus := NewEventBus()

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
	bus := NewEventBus()

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
	bus := NewEventBus()

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
	bus := NewEventBus()

	id, _ := bus.Subscribe(10)

	// Unsubscribe twice should not panic.
	bus.Unsubscribe(id)
	bus.Unsubscribe(id)
}
