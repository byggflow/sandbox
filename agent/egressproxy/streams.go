package egressproxy

import (
	"errors"
	"sync"
	"sync/atomic"
)

// streamChunk is one frame's worth of body bytes for a stream, or the
// terminal marker (end=true). errMsg, if non-empty, indicates the
// upstream/daemon reported an error and the stream did not complete
// cleanly.
type streamChunk struct {
	data   []byte
	end    bool
	errMsg string
}

// streamRegistry tracks active inbound streams keyed by stream ID. The
// agent's read loop routes incoming FrameStreamData / FrameStreamEnd
// frames into the channel registered here; the egress proxy reads
// chunks off the channel and writes them to the sandbox HTTP client.
//
// The channel is buffered so the read loop doesn't block on a slow
// sandbox client. When the buffer fills, the read loop blocks — that's
// the backpressure path. WebSocket's TCP underneath provides flow
// control for the upstream daemon connection.
type streamRegistry struct {
	mu      sync.Mutex
	streams map[uint32]chan streamChunk
	nextID  atomic.Uint32
}

// streamBuffer is the per-stream channel capacity. Each slot is one
// chunk (≤ StreamChunkSize, currently 64 KB), so this is roughly
// "how much body we'll buffer in agent memory while the sandbox client
// is slow to read."
const streamBuffer = 32

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{streams: make(map[uint32]chan streamChunk)}
}

// allocate reserves a fresh stream ID and returns the read-side
// channel plus a release func the caller must defer.
func (r *streamRegistry) allocate() (uint32, <-chan streamChunk, func()) {
	id := r.nextID.Add(1)
	ch := make(chan streamChunk, streamBuffer)
	r.mu.Lock()
	r.streams[id] = ch
	r.mu.Unlock()
	return id, ch, func() {
		r.mu.Lock()
		delete(r.streams, id)
		r.mu.Unlock()
	}
}

// deliver routes an inbound chunk to the matching stream. Returns true
// if a stream was registered for streamID; the caller can log or drop
// orphan frames.
func (r *streamRegistry) deliver(streamID uint32, chunk streamChunk) bool {
	r.mu.Lock()
	ch, ok := r.streams[streamID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	// Bounded send. If the consumer is slow we block, providing
	// backpressure to the connection read loop. The read loop is
	// per-connection so this only stalls one stream's pacing, not
	// other streams on the same connection (each has its own channel).
	ch <- chunk
	return true
}

// closeAll is called on agent connection teardown so any pending
// stream readers unblock with an error.
func (r *streamRegistry) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, ch := range r.streams {
		// Drain non-blocking attempts then send terminal error so
		// readers see "connection closed".
		select {
		case ch <- streamChunk{end: true, errMsg: "connection closed"}:
		default:
		}
		delete(r.streams, id)
	}
}

// ErrStreamClosed is returned when the registry tears down before the
// upstream finishes.
var ErrStreamClosed = errors.New("egress stream closed")
