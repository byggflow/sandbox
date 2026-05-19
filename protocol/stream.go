package protocol

import (
	"encoding/binary"
	"fmt"
)

// EncodeStreamData builds the payload for a FrameStreamData frame:
// the 4-byte big-endian stream ID followed by the data bytes.
func EncodeStreamData(streamID uint32, data []byte) []byte {
	out := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(out[:4], streamID)
	copy(out[4:], data)
	return out
}

// DecodeStreamData parses a FrameStreamData payload and returns the
// stream ID and a reference to the underlying data slice (not a copy —
// callers that need to retain the bytes past the next read must copy).
func DecodeStreamData(payload []byte) (uint32, []byte, error) {
	if len(payload) < 4 {
		return 0, nil, fmt.Errorf("stream data payload too short: %d", len(payload))
	}
	return binary.BigEndian.Uint32(payload[:4]), payload[4:], nil
}

// EncodeStreamEnd builds the payload for a FrameStreamEnd frame:
// [4-byte streamID][1-byte status][optional error message].
func EncodeStreamEnd(streamID uint32, status byte, errMsg string) []byte {
	out := make([]byte, 5+len(errMsg))
	binary.BigEndian.PutUint32(out[:4], streamID)
	out[4] = status
	copy(out[5:], errMsg)
	return out
}

// DecodeStreamEnd parses a FrameStreamEnd payload.
func DecodeStreamEnd(payload []byte) (streamID uint32, status byte, errMsg string, err error) {
	if len(payload) < 5 {
		return 0, 0, "", fmt.Errorf("stream end payload too short: %d", len(payload))
	}
	streamID = binary.BigEndian.Uint32(payload[:4])
	status = payload[4]
	errMsg = string(payload[5:])
	return streamID, status, errMsg, nil
}
