package egressproxy

import (
	"io"
	"testing"
	"time"

	"github.com/byggflow/sandbox/agent/phonehome"
)

// TestCloseAllUnblocksConsumerEvenWithFullBuffer is the regression
// test for the dropped-end-marker hang. The reviewer's scenario:
//
//  1. agent connection drops while a stream's buffer is full
//  2. old closeAll did a non-blocking send for the terminal marker,
//     which silently failed because the buffer had no room
//  3. old closeAll then deleted the stream WITHOUT closing the
//     channel, so the consumer's range loop never observed EOF
//  4. consumer drained the buffered chunks then hung forever
//
// The fix drains the buffer first to make room, sends the terminal
// marker, then closes the channel. This test enforces both
// guarantees: an unblockable consumer drains all chunks AND
// terminates on a clean signal.
func TestCloseAllUnblocksConsumerEvenWithFullBuffer(t *testing.T) {
	r := newStreamRegistry()
	id, ch, release := r.allocate(phonehome.New(io.Discard))
	t.Cleanup(release)

	// Start the consumer first, then fill the buffer concurrently —
	// matches the production scenario where the HTTP handler is already
	// running when the agent's read loop fires closeAll on conn drop.
	chunks := 0
	gotEnd := false
	gotErr := ""
	done := make(chan struct{})
	go func() {
		for chunk := range ch {
			if chunk.end {
				gotEnd = true
				gotErr = chunk.errMsg
				continue
			}
			chunks++
		}
		close(done)
	}()

	// Fill the buffer to capacity via the public delivery path. The
	// consumer is already draining, so deliveries that block until
	// space frees up are the realistic shape.
	for i := 0; i < streamBuffer; i++ {
		if !r.deliver(id, streamChunk{data: []byte("x")}) {
			t.Fatalf("deliver returned false at iteration %d", i)
		}
	}

	// Tear down. Old impl would drop the terminal and leak the channel.
	doneTearDown := make(chan struct{})
	go func() {
		r.closeAll()
		close(doneTearDown)
	}()
	select {
	case <-doneTearDown:
	case <-time.After(2 * time.Second):
		t.Fatal("closeAll deadlocked")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer hung after closeAll — end marker was dropped or channel not closed")
	}

	if chunks != streamBuffer {
		t.Errorf("expected %d data chunks delivered, got %d", streamBuffer, chunks)
	}
	if !gotEnd {
		t.Error("consumer never observed terminal end marker")
	}
	if gotErr == "" {
		t.Error("terminal end marker missing error message; consumer would treat as clean EOF")
	}
}

func TestCloseAllForOwnerLeavesOtherStreamsOpen(t *testing.T) {
	r := newStreamRegistry()
	ownerA := phonehome.New(io.Discard)
	ownerB := phonehome.New(io.Discard)

	idA, chA, releaseA := r.allocate(ownerA)
	t.Cleanup(releaseA)
	idB, chB, releaseB := r.allocate(ownerB)
	t.Cleanup(releaseB)

	doneA := make(chan struct{})
	go func() {
		for range chA {
		}
		close(doneA)
	}()

	r.closeAllForOwner(ownerA)

	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("owner A stream did not close")
	}
	if r.deliver(idA, streamChunk{data: []byte("old")}) {
		t.Fatal("owner A stream still accepted data after closeAllForOwner")
	}
	if !r.deliver(idB, streamChunk{data: []byte("new")}) {
		t.Fatal("owner B stream was closed with owner A")
	}

	select {
	case chunk := <-chB:
		if string(chunk.data) != "new" {
			t.Fatalf("owner B stream got %q, want new", string(chunk.data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner B stream did not receive data after owner A close")
	}
}
