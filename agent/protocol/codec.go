package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	proto "github.com/byggflow/sandbox/protocol"
)

// Frame represents a single protocol frame.
type Frame struct {
	Type    byte
	Payload []byte
}

// ReadFrame reads one frame from r.
// Wire format: [1-byte type][4-byte big-endian length][payload].
func ReadFrame(r io.Reader) (Frame, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return Frame{}, fmt.Errorf("read frame header: %w", err)
	}

	ftype := header[0]
	length := binary.BigEndian.Uint32(header[1:5])

	if length > proto.MaxFrameSize {
		return Frame{}, fmt.Errorf("frame size %d exceeds max %d", length, proto.MaxFrameSize)
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, fmt.Errorf("read frame payload: %w", err)
		}
	}

	return Frame{Type: ftype, Payload: payload}, nil
}

// WriteFrame writes a raw frame to w.
// The header and payload are written in a single Write call so that
// concurrent writers sharing the same w cannot interleave partial frames.
func WriteFrame(w io.Writer, ftype byte, payload []byte) error {
	buf := make([]byte, 5+len(payload))
	buf[0] = ftype
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)

	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// WriteJSON marshals v as JSON and writes it as a JSON frame.
func WriteJSON(w io.Writer, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	return WriteFrame(w, proto.FrameJSON, data)
}

// WriteBinary writes raw data as a binary frame.
func WriteBinary(w io.Writer, data []byte) error {
	return WriteFrame(w, proto.FrameBinary, data)
}

// WritePong writes a pong frame.
func WritePong(w io.Writer) error {
	return WriteFrame(w, proto.FramePing, []byte{proto.PingResponse})
}
