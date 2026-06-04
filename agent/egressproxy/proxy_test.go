package egressproxy

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/byggflow/sandbox/protocol"
)

// TestStreamBodyToCleanEOF: terminal frame with status 0 → nil.
func TestStreamBodyToCleanEOF(t *testing.T) {
	ch := make(chan streamChunk, 4)
	ch <- streamChunk{data: []byte("hello")}
	ch <- streamChunk{end: true} // clean
	close(ch)

	var buf bytes.Buffer
	if err := streamBodyTo(ch, &buf, nil); err != nil {
		t.Errorf("expected nil on clean EOF, got %v", err)
	}
	if buf.String() != "hello" {
		t.Errorf("body: got %q want %q", buf.String(), "hello")
	}
}

// TestStreamBodyToCarriesError: terminal frame with errMsg → that error.
func TestStreamBodyToCarriesError(t *testing.T) {
	ch := make(chan streamChunk, 4)
	ch <- streamChunk{data: []byte("partial")}
	ch <- streamChunk{end: true, errMsg: "upstream EOF mid-stream"}
	close(ch)

	var buf bytes.Buffer
	err := streamBodyTo(ch, &buf, nil)
	if err == nil || err.Error() != "upstream EOF mid-stream" {
		t.Errorf("expected upstream error to surface, got %v", err)
	}
}

// TestStreamBodyToTruncationOnCloseWithoutTerminal is the regression
// test for the silent-truncation finding: when closeAll's 100ms
// deadline expires and the channel closes without a terminal frame,
// streamBodyTo MUST return an error so the caller can break the
// underlying connection rather than write a clean chunked terminator.
//
// The previous behavior returned nil for this case, which made
// truncated bodies look successful to the sandbox client.
func TestStreamBodyToTruncationOnCloseWithoutTerminal(t *testing.T) {
	ch := make(chan streamChunk, 4)
	ch <- streamChunk{data: []byte("part 1")}
	ch <- streamChunk{data: []byte("part 2")}
	close(ch) // no end marker — simulates closeAll timeout fallback

	var buf bytes.Buffer
	err := streamBodyTo(ch, &buf, nil)
	if err == nil {
		t.Fatal("expected truncation error when channel closes without terminal")
	}
	if !errors.Is(err, errStreamClosedWithoutTerminal) {
		t.Errorf("expected errStreamClosedWithoutTerminal, got %v", err)
	}
	// Buffered chunks should still have been written.
	if got := buf.String(); got != "part 1part 2" {
		t.Errorf("body: got %q want %q", got, "part 1part 2")
	}
}

// TestStreamBodyToWriteFailureStops verifies that a write failure to
// the downstream stops the loop and surfaces the wrapped error.
func TestStreamBodyToWriteFailureStops(t *testing.T) {
	ch := make(chan streamChunk, 4)
	ch <- streamChunk{data: []byte("x")}
	close(ch)
	err := streamBodyTo(ch, errWriter{}, nil)
	if err == nil {
		t.Fatal("expected write error")
	}
}

type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return 0, io.ErrShortWrite }

// Sanity: ensure protocol.StreamEndOK is what we treat as clean.
func TestStreamEndOKConstant(t *testing.T) {
	if protocol.StreamEndOK != 0x00 {
		t.Errorf("StreamEndOK changed; close-without-terminal test assumes 0 = clean")
	}
}
