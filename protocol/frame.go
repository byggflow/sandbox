package protocol

// Frame types for the daemon ↔ agent binary protocol.
const (
	FrameJSON   byte = 0x01 // JSON control message
	FrameBinary byte = 0x02 // Raw binary data (file content, tar, stdin/stdout)
	FramePing   byte = 0x03 // Ping/pong (payload: 0x00 = ping, 0x01 = pong)
)

// Ping/pong payload values.
const (
	PingRequest  byte = 0x00
	PingResponse byte = 0x01
)

// MaxFrameSize is the maximum allowed frame payload size (64MB).
// Frames exceeding this limit cause an immediate connection close.
const MaxFrameSize = 64 * 1024 * 1024
