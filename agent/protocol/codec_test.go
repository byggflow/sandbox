package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"

	proto "github.com/byggflow/sandbox/protocol"
)

func TestReadWriteFrame(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello")

	if err := WriteFrame(&buf, proto.FrameJSON, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	if frame.Type != proto.FrameJSON {
		t.Errorf("type = %d, want %d", frame.Type, proto.FrameJSON)
	}
	if !bytes.Equal(frame.Payload, payload) {
		t.Errorf("payload = %q, want %q", frame.Payload, payload)
	}
}

func TestReadFrameEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, proto.FrameBinary, nil); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	if frame.Type != proto.FrameBinary {
		t.Errorf("type = %d, want %d", frame.Type, proto.FrameBinary)
	}
	if len(frame.Payload) != 0 {
		t.Errorf("payload length = %d, want 0", len(frame.Payload))
	}
}

func TestReadFrameExceedsMax(t *testing.T) {
	var buf bytes.Buffer
	header := make([]byte, 5)
	header[0] = proto.FrameJSON
	binary.BigEndian.PutUint32(header[1:5], proto.MaxFrameSize+1)
	buf.Write(header)

	_, err := ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	msg := map[string]string{"key": "value"}

	if err := WriteJSON(&buf, msg); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	if frame.Type != proto.FrameJSON {
		t.Errorf("type = %d, want %d", frame.Type, proto.FrameJSON)
	}

	var got map[string]string
	if err := json.Unmarshal(frame.Payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("got %v, want key=value", got)
	}
}

func TestWriteBinary(t *testing.T) {
	var buf bytes.Buffer
	data := []byte{0x00, 0x01, 0x02}

	if err := WriteBinary(&buf, data); err != nil {
		t.Fatalf("WriteBinary: %v", err)
	}

	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	if frame.Type != proto.FrameBinary {
		t.Errorf("type = %d, want %d", frame.Type, proto.FrameBinary)
	}
	if !bytes.Equal(frame.Payload, data) {
		t.Errorf("payload = %v, want %v", frame.Payload, data)
	}
}

func TestWritePong(t *testing.T) {
	var buf bytes.Buffer

	if err := WritePong(&buf); err != nil {
		t.Fatalf("WritePong: %v", err)
	}

	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	if frame.Type != proto.FramePing {
		t.Errorf("type = %d, want %d", frame.Type, proto.FramePing)
	}
	if len(frame.Payload) != 1 || frame.Payload[0] != proto.PingResponse {
		t.Errorf("payload = %v, want [%d]", frame.Payload, proto.PingResponse)
	}
}

func TestMultipleFrames(t *testing.T) {
	var buf bytes.Buffer

	frames := []Frame{
		{Type: proto.FrameJSON, Payload: []byte(`{"a":1}`)},
		{Type: proto.FrameBinary, Payload: []byte{0xFF}},
		{Type: proto.FramePing, Payload: []byte{proto.PingRequest}},
	}

	for _, f := range frames {
		if err := WriteFrame(&buf, f.Type, f.Payload); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}

	for i, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame[%d]: %v", i, err)
		}
		if got.Type != want.Type {
			t.Errorf("[%d] type = %d, want %d", i, got.Type, want.Type)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("[%d] payload mismatch", i)
		}
	}
}
