package egressproxy

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
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

// closeAll tears down every active stream so pending readers unblock
// with a clear "connection closed" error rather than hanging on a
// channel that will never receive an end marker.
//
// The old implementation used a single non-blocking send for the
// terminal frame and silently dropped it whenever the channel buffer
// was full — and never closed the channel — which left the HTTP proxy
// handler stuck in `for chunk := range ch` waiting for an end marker
// that would never arrive.
//
// The current implementation does a blocking send for the terminal
// marker (so previously-buffered chunks still reach the consumer)
// with a short timeout to bound the worst case, then closes the
// channel so even a missed marker exits the range loop on EOF.
//
// Safe to call without a concurrent producer — the agent invokes this
// in the handleConn defer, after the read loop (the only producer)
// has exited. The consumer (HTTP proxy handler) is responsible for
// draining the channel, which it does naturally via `for ... range`.
func (r *streamRegistry) closeAll() {
	r.mu.Lock()
	streams := r.streams
	r.streams = make(map[uint32]chan streamChunk)
	r.mu.Unlock()

	for _, ch := range streams {
		closeWithTerminal(ch, "connection closed")
	}
}

// closeWithTerminal sends an end marker (blocking with a deadline so
// previously-buffered chunks can drain) then closes the channel. The
// timeout bounds the worst case where the consumer goroutine is
// already dead — without it we'd leak.
func closeWithTerminal(ch chan streamChunk, errMsg string) {
	select {
	case ch <- streamChunk{end: true, errMsg: errMsg}:
	case <-time.After(closeAllTimeout):
		// Consumer didn't make room in time. The close below still
		// breaks the range loop; the error message is lost but the
		// consumer at least unblocks.
	}
	close(ch)
}

// closeAllTimeout caps how long we wait for a busy consumer to drain
// enough buffer for the terminal frame. 100ms is generous for any
// realistic HTTP-handler loop and bounds the agent shutdown cost.
const closeAllTimeout = 100 * time.Millisecond

// ErrStreamClosed is returned when the registry tears down before the
// upstream finishes.
var ErrStreamClosed = errors.New("egress stream closed")
