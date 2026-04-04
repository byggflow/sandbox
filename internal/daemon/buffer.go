package daemon

import (
	"sync"
)

// BufferedNotification stores a notification that arrived while disconnected.
type BufferedNotification struct {
	Method  string
	Payload []byte // pre-marshaled JSON
}

// NotificationBuffer implements a two-tier buffer for notifications during disconnect.
// State notifications (process.exit, process.error) are stored in a non-evictable ring.
// Stream notifications (process.stdout, process.stderr) are stored in an evictable buffer.
type NotificationBuffer struct {
	mu sync.Mutex

	// State notifications: non-evictable ring, max 50 entries.
	state    []BufferedNotification
	maxState int

	// Stream notifications: evictable buffer, max 1000 messages or ~1MB total.
	stream       []BufferedNotification
	maxStream    int
	streamBytes  int
	maxBytes     int
	truncated    int // count of evicted stream messages
}

// NewNotificationBuffer creates a new notification buffer.
func NewNotificationBuffer() *NotificationBuffer {
	return &NotificationBuffer{
		maxState:  50,
		maxStream: 1000,
		maxBytes:  1 * 1024 * 1024, // 1MB
	}
}

// Add adds a notification to the buffer. It categorizes based on method.
func (b *NotificationBuffer) Add(method string, payload []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	notif := BufferedNotification{Method: method, Payload: payload}

	if isStateNotification(method) {
		if len(b.state) >= b.maxState {
			// Ring behavior: drop oldest to make room.
			b.state = b.state[1:]
		}
		b.state = append(b.state, notif)
		return
	}

	// Stream notification.
	payloadLen := len(payload)

	// Evict oldest stream entries if we exceed limits.
	for (len(b.stream) >= b.maxStream || b.streamBytes+payloadLen > b.maxBytes) && len(b.stream) > 0 {
		evicted := b.stream[0]
		b.stream = b.stream[1:]
		b.streamBytes -= len(evicted.Payload)
		b.truncated++
	}

	b.stream = append(b.stream, notif)
	b.streamBytes += payloadLen
}

// Drain returns all buffered notifications in replay order (state first, then stream)
// and resets the buffer. Returns the number of truncated stream messages.
func (b *NotificationBuffer) Drain() (state []BufferedNotification, stream []BufferedNotification, truncated int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	state = b.state
	stream = b.stream
	truncated = b.truncated

	b.state = nil
	b.stream = nil
	b.streamBytes = 0
	b.truncated = 0

	return state, stream, truncated
}

// Count returns the total number of buffered notifications.
func (b *NotificationBuffer) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.state) + len(b.stream)
}

func isStateNotification(method string) bool {
	return method == "process.exit" || method == "process.error"
}
