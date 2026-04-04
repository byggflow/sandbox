package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
)

// Event represents a sandbox lifecycle event.
type Event struct {
	ID        uint64                 `json:"id"`                  // Monotonically increasing sequence number.
	Type      string                 `json:"type"`                // "sandbox.created", "sandbox.destroyed", "sandbox.disconnected", "sandbox.reconnected"
	SandboxID string                 `json:"sandbox_id"`
	Identity  string                 `json:"identity,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// EventBus manages pub/sub for sandbox lifecycle events with replay support.
//
// Events are assigned monotonically increasing sequence IDs and stored in a
// ring buffer. Subscribers that reconnect can replay missed events using
// Since(), and the SSE endpoint supports the standard Last-Event-ID header.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string]chan Event // subscriber ID -> channel

	seq    atomic.Uint64
	ringMu sync.RWMutex
	ring   []Event // fixed-size ring buffer
	head   int     // next write position in the ring
	count  int     // how many slots are occupied (up to len(ring))
}

// NewEventBus creates a new EventBus with the given ring buffer capacity.
// A capacity of 0 defaults to 10 000 events.
func NewEventBus(capacity int) *EventBus {
	if capacity <= 0 {
		capacity = 10_000
	}
	return &EventBus{
		subscribers: make(map[string]chan Event),
		ring:        make([]Event, capacity),
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

// Publish assigns a sequence ID to the event, stores it in the ring buffer,
// and fans it out to all live subscribers. Non-blocking: drops events for
// subscribers whose channels are full.
func (eb *EventBus) Publish(event Event) {
	// Assign sequence ID.
	event.ID = eb.seq.Add(1)

	// Store in ring buffer.
	eb.ringMu.Lock()
	eb.ring[eb.head] = event
	eb.head = (eb.head + 1) % len(eb.ring)
	if eb.count < len(eb.ring) {
		eb.count++
	}
	eb.ringMu.Unlock()

	// Fan out to subscribers.
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

// Since returns all buffered events with ID strictly greater than afterID,
// ordered by ID. If afterID is 0, returns the entire buffer.
// Returns the events and a boolean indicating whether the requested afterID
// was still in the buffer (true = complete, false = some events were lost).
func (eb *EventBus) Since(afterID uint64) ([]Event, bool) {
	eb.ringMu.RLock()
	defer eb.ringMu.RUnlock()

	if eb.count == 0 {
		return nil, true
	}

	// Collect events from the ring in chronological order.
	result := make([]Event, 0, 64)
	oldestIdx := (eb.head - eb.count + len(eb.ring)) % len(eb.ring)

	for i := 0; i < eb.count; i++ {
		idx := (oldestIdx + i) % len(eb.ring)
		ev := eb.ring[idx]
		if ev.ID > afterID {
			result = append(result, ev)
		}
	}

	// Check if the requested ID is still in the buffer.
	// If afterID > 0 and the oldest event's ID is greater than afterID+1,
	// we've lost events in between.
	oldestEvent := eb.ring[oldestIdx]
	complete := afterID == 0 || oldestEvent.ID <= afterID+1

	return result, complete
}

// Seq returns the current sequence number (the ID of the last published event).
func (eb *EventBus) Seq() uint64 {
	return eb.seq.Load()
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
