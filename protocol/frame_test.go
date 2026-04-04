package protocol

import "testing"

func TestFrameTypeConstants(t *testing.T) {
	if FrameJSON != 0x01 {
		t.Errorf("FrameJSON = %#x, want 0x01", FrameJSON)
	}
	if FrameBinary != 0x02 {
		t.Errorf("FrameBinary = %#x, want 0x02", FrameBinary)
	}
	if FramePing != 0x03 {
		t.Errorf("FramePing = %#x, want 0x03", FramePing)
	}
}

func TestFrameTypeUniqueness(t *testing.T) {
	types := []byte{FrameJSON, FrameBinary, FramePing}
	seen := make(map[byte]bool)
	for _, ft := range types {
		if seen[ft] {
			t.Errorf("duplicate frame type: %#x", ft)
		}
		seen[ft] = true
	}
}

func TestPingPayloadValues(t *testing.T) {
	if PingRequest == PingResponse {
		t.Error("PingRequest and PingResponse must differ")
	}
}

func TestMaxFrameSize(t *testing.T) {
	expected := 64 * 1024 * 1024
	if MaxFrameSize != expected {
		t.Errorf("MaxFrameSize = %d, want %d (64MB)", MaxFrameSize, expected)
	}
}
