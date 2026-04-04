package proxy

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/byggflow/sandbox/protocol"
)

// Dialer abstracts how connections are established to guest agents.
// The default implementation dials TCP, but alternatives (e.g., vsock for
// Firecracker microVMs) can be provided by implementing this interface.
type Dialer interface {
	Dial(addr string, timeout time.Duration) (net.Conn, error)
}

// TCPDialer is the default Dialer that connects over TCP.
type TCPDialer struct{}

// Dial connects to the agent at the given TCP address (host:port).
func (TCPDialer) Dial(addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, timeout)
}

// DefaultDialer is the package-level default dialer used by Dial().
var DefaultDialer Dialer = TCPDialer{}

// AgentConn manages a binary-framed connection to a guest agent.
type AgentConn struct {
	conn net.Conn
	mu   sync.Mutex // serializes writes
}

// Dial connects to the agent using the DefaultDialer.
func Dial(addr string, timeout time.Duration) (*AgentConn, error) {
	return DialWith(DefaultDialer, addr, timeout)
}

// DialWith connects to the agent using the provided Dialer.
func DialWith(d Dialer, addr string, timeout time.Duration) (*AgentConn, error) {
	conn, err := d.Dial(addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial agent %s: %w", addr, err)
	}
	return &AgentConn{conn: conn}, nil
}

// Wrap creates an AgentConn from an existing net.Conn.
func Wrap(conn net.Conn) *AgentConn {
	return &AgentConn{conn: conn}
}

// Close closes the underlying connection.
func (a *AgentConn) Close() error {
	return a.conn.Close()
}

// WriteFrame writes a binary-framed message: [1-byte type][4-byte length][payload].
func (a *AgentConn) WriteFrame(frameType byte, payload []byte) error {
	if len(payload) > protocol.MaxFrameSize {
		return fmt.Errorf("frame payload exceeds max size: %d > %d", len(payload), protocol.MaxFrameSize)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	header := make([]byte, 5)
	header[0] = frameType
	binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))

	if _, err := a.conn.Write(header); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if len(payload) > 0 {
		if _, err := a.conn.Write(payload); err != nil {
			return fmt.Errorf("write frame payload: %w", err)
		}
	}
	return nil
}

// ReadFrame reads a binary-framed message. Returns (type, payload, error).
func (a *AgentConn) ReadFrame() (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(a.conn, header); err != nil {
		return 0, nil, fmt.Errorf("read frame header: %w", err)
	}

	frameType := header[0]
	length := binary.BigEndian.Uint32(header[1:5])

	if length > uint32(protocol.MaxFrameSize) {
		return 0, nil, fmt.Errorf("frame payload too large: %d > %d", length, protocol.MaxFrameSize)
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(a.conn, payload); err != nil {
			return 0, nil, fmt.Errorf("read frame payload: %w", err)
		}
	}

	return frameType, payload, nil
}

// Ping sends a ping frame and waits for a pong response within the timeout.
func (a *AgentConn) Ping(timeout time.Duration) error {
	if err := a.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}
	defer a.conn.SetDeadline(time.Time{}) //nolint: reset deadline

	if err := a.WriteFrame(protocol.FramePing, []byte{protocol.PingRequest}); err != nil {
		return fmt.Errorf("send ping: %w", err)
	}

	frameType, payload, err := a.ReadFrame()
	if err != nil {
		return fmt.Errorf("read pong: %w", err)
	}

	if frameType != protocol.FramePing || len(payload) == 0 || payload[0] != protocol.PingResponse {
		return fmt.Errorf("unexpected response: type=0x%02x", frameType)
	}

	return nil
}

// Authenticate sends an auth.token RPC to the agent and verifies the response.
// Must be called before any other RPC when the agent requires a token.
func (a *AgentConn) Authenticate(token string, timeout time.Duration) error {
	if err := a.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set auth deadline: %w", err)
	}
	defer a.conn.SetDeadline(time.Time{})

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      0,
		Method:  "auth.token",
		Params:  map[string]string{"token": token},
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal auth request: %w", err)
	}

	if err := a.WriteFrame(protocol.FrameJSON, payload); err != nil {
		return fmt.Errorf("send auth request: %w", err)
	}

	frameType, respPayload, err := a.ReadFrame()
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if frameType != protocol.FrameJSON {
		return fmt.Errorf("unexpected auth response frame type: 0x%02x", frameType)
	}

	var resp protocol.Response
	if err := json.Unmarshal(respPayload, &resp); err != nil {
		return fmt.Errorf("decode auth response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("agent auth failed: %s", resp.Error.Message)
	}

	return nil
}

// SetDeadline sets the read and write deadline on the connection.
func (a *AgentConn) SetDeadline(t time.Time) error {
	return a.conn.SetDeadline(t)
}

// SetReadDeadline sets the read deadline.
func (a *AgentConn) SetReadDeadline(t time.Time) error {
	return a.conn.SetReadDeadline(t)
}

// SetWriteDeadline sets the write deadline.
func (a *AgentConn) SetWriteDeadline(t time.Time) error {
	return a.conn.SetWriteDeadline(t)
}
