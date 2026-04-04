package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Event represents a sandbox lifecycle event.
type Event struct {
	Type      string                 `json:"type"`                // "sandbox.created", "sandbox.destroyed", "sandbox.disconnected", "sandbox.reconnected"
	SandboxID string                 `json:"sandbox_id"`
	Identity  string                 `json:"identity,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// EventBus manages pub/sub for sandbox lifecycle events.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string]chan Event // subscriber ID -> channel
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[string]chan Event),
	}
}

// Subscribe registers a new subscriber and returns a unique ID and a read-only channel.
func (eb *EventBus) Subscribe(bufferSize int) (string, <-chan Event) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	id := generateSubscriberID()
	ch := make(chan Event, bufferSize)
	eb.subscribers[id] = ch
	return id, ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (eb *EventBus) Unsubscribe(id string) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if ch, ok := eb.subscribers[id]; ok {
		close(ch)
		delete(eb.subscribers, id)
	}
}

// Publish sends an event to all subscribers. Non-blocking: drops events
// for subscribers whose channels are full.
func (eb *EventBus) Publish(event Event) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	for _, ch := range eb.subscribers {
		select {
		case ch <- event:
		default:
			// Drop event if subscriber buffer is full.
		}
	}
}

// generateSubscriberID creates a random subscriber identifier.
func generateSubscriberID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "sub-" + hex.EncodeToString(b)
}

// publishSandboxEvent is a helper that publishes a lifecycle event with the given type.
func (d *Daemon) publishSandboxEvent(eventType string, sbx *Sandbox, data map[string]interface{}) {
	if d.Events == nil {
		return
	}
	d.Events.Publish(Event{
		Type:      eventType,
		SandboxID: sbx.ID,
		Identity:  sbx.IdentityStr,
		Timestamp: time.Now(),
		Data:      data,
	})
}
