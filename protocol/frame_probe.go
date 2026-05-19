package protocol

import "encoding/json"

// FrameKind classifies a JSON frame's payload without fully decoding it.
type FrameKind int

const (
	// FrameKindUnknown means the payload didn't parse as a valid JSON-RPC
	// envelope (no method, no ID).
	FrameKindUnknown FrameKind = iota
	// FrameKindRequest carries Method + ID and expects a Response.
	FrameKindRequest
	// FrameKindNotification carries Method without ID.
	FrameKindNotification
	// FrameKindResponse carries ID without Method (result or error).
	FrameKindResponse
)

// Envelope is the minimal JSON-RPC view used to classify an inbound frame.
// Method, ID, and Params are extracted; everything else is ignored.
type Envelope struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

// ProbeFrame peeks at payload to determine whether it's a Request,
// Notification, or Response — without fully decoding. Used by the daemon
// and agent to route inbound JSON frames on a bidirectional connection.
//
// The returned Envelope is populated even on FrameKindUnknown; callers
// that need the structured fields should still check the kind.
func ProbeFrame(payload []byte) (FrameKind, Envelope) {
	var env Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return FrameKindUnknown, env
	}
	hasMethod := env.Method != ""
	hasID := len(env.ID) > 0
	switch {
	case hasMethod && hasID:
		return FrameKindRequest, env
	case hasMethod:
		return FrameKindNotification, env
	case hasID:
		return FrameKindResponse, env
	default:
		return FrameKindUnknown, env
	}
}

// DecodeID extracts a numeric ID from a raw JSON-RPC id value. Returns 0
// if the id is not a number (JSON-RPC also permits strings; we always use
// integers internally).
func DecodeID(raw json.RawMessage) int {
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	return 0
}
