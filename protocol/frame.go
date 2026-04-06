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

// MaxFrameSize is the maximum allowed frame payload size (10MB).
// Frames exceeding this limit cause an immediate connection close.
const MaxFrameSize = 10 * 1024 * 1024

// ChunkSize is the size of each chunk for chunked file transfers (1MB).
// Files larger than this are split into multiple binary frames.
const ChunkSize = 1 * 1024 * 1024

// ChunkThreshold is the file size above which chunked transfer is used.
const ChunkThreshold = 1 * 1024 * 1024
