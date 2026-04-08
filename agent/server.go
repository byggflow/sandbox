package agent

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
)

// Server is the TCP server for the guest agent.
type Server struct {
	addr       string
	authToken  string // If set, clients must send auth.token as first RPC call.
	dispatcher *Dispatcher
	listener   net.Listener
	wg         sync.WaitGroup
	quit       chan struct{}
}

// NewServer creates a new agent server.
// If SANDBOX_AUTH_TOKEN is set, clients must authenticate before sending commands.
func NewServer(addr string) *Server {
	return &Server{
		addr:       addr,
		authToken:  os.Getenv("SANDBOX_AUTH_TOKEN"),
		dispatcher: NewDispatcher(),
		quit:       make(chan struct{}),
	}
}

// ListenAndServe starts a TCP listener on the configured address and serves connections.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	slog.Info("agent listening", "addr", s.addr)
	return s.Serve(ln)
}

// Serve accepts connections on the given listener. This enables the agent to
// serve over any transport (TCP, vsock, Unix socket) by passing the
// appropriate listener.
func (s *Server) Serve(ln net.Listener) error {
	s.listener = ln

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		close(s.quit)
		s.listener.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.quit:
				s.wg.Wait()
				return nil
			default:
				slog.Error("accept error", "error", err)
				continue
			}
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	slog.Info("connection accepted", "remote", conn.RemoteAddr())

	// If auth token is configured, require it as the first RPC call.
	if s.authToken != "" {
		if !s.authenticateConn(conn) {
			return
		}
	}

	rw := &connRW{conn: conn}

	for {
		select {
		case <-s.quit:
			return
		default:
		}

		frame, err := codec.ReadFrame(conn)
		if err != nil {
			if err != io.EOF {
				slog.Error("read frame error", "error", err)
			}
			return
		}

		switch frame.Type {
		case proto.FramePing:
			if len(frame.Payload) == 1 && frame.Payload[0] == proto.PingRequest {
				if err := codec.WritePong(conn); err != nil {
					slog.Error("write pong error", "error", err)
					return
				}
			}

		case proto.FrameJSON:
			var req proto.Request
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				slog.Warn("invalid json-rpc request", "error", err)
				continue
			}
			if req.JSONRPC != "2.0" {
				slog.Warn("invalid jsonrpc version", "version", req.JSONRPC)
				continue
			}
			s.dispatcher.Handle(&req, rw)

		case proto.FrameBinary:
			// Binary frame: PTY input. We need to determine which PTY to route to.
			// For now, we route to the most recently created PTY, or use a PID
			// prefix protocol: first 4 bytes are big-endian PID, rest is data.
			s.handleBinaryFrame(frame.Payload)

		default:
			slog.Warn("unexpected frame type", "type", fmt.Sprintf("0x%02x", frame.Type))
		}
	}
}

// handleBinaryFrame routes binary data to a PTY process.
// Protocol: first 4 bytes are big-endian PID, rest is data.
func (s *Server) handleBinaryFrame(payload []byte) {
	if len(payload) < 4 {
		slog.Warn("binary frame too short", "bytes", len(payload))
		return
	}

	pid := int(uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3]))
	data := payload[4:]

	ptyMgr := s.dispatcher.PtyManager()
	if err := ptyMgr.WritePtyInput(pid, data); err != nil {
		slog.Error("pty input error", "pid", pid, "error", err)
	}
}

// authenticateConn reads the first JSON-RPC frame and verifies it is an
// auth.token call with the correct token. Returns false if auth fails.
func (s *Server) authenticateConn(conn net.Conn) bool {
	// Set a deadline for the auth handshake.
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetDeadline(time.Time{}) // Clear deadline after auth.

	frame, err := codec.ReadFrame(conn)
	if err != nil {
		slog.Error("auth: failed to read frame", "error", err)
		return false
	}

	if frame.Type != proto.FrameJSON {
		slog.Warn("auth: expected JSON frame", "type", fmt.Sprintf("0x%02x", frame.Type))
		return false
	}

	var req proto.Request
	if err := json.Unmarshal(frame.Payload, &req); err != nil {
		slog.Warn("auth: invalid JSON-RPC", "error", err)
		return false
	}

	if req.Method != "auth.token" {
		slog.Warn("auth: expected auth.token", "method", req.Method)
		s.sendAuthError(conn, req.ID, "first call must be auth.token")
		return false
	}

	var params struct {
		Token string `json:"token"`
	}
	raw, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(raw, &params); err != nil || subtle.ConstantTimeCompare([]byte(params.Token), []byte(s.authToken)) != 1 {
		slog.Warn("auth: invalid token", "remote", conn.RemoteAddr())
		s.sendAuthError(conn, req.ID, "invalid token")
		return false
	}

	// Send success response.
	resp := proto.Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"authenticated": true},
	}
	codec.WriteJSON(conn, resp)

	slog.Info("auth: connection authenticated", "remote", conn.RemoteAddr())
	return true
}

func (s *Server) sendAuthError(conn net.Conn, id int, msg string) {
	resp := proto.Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &proto.RPCError{
			Code:    -32000,
			Message: msg,
		},
	}
	codec.WriteJSON(conn, resp)
}

// connRW wraps a net.Conn to implement io.ReadWriter.
// Reads go through the framed protocol (for binary frames expected by handlers).
type connRW struct {
	conn net.Conn
}

func (c *connRW) Read(p []byte) (int, error) {
	return c.conn.Read(p)
}

func (c *connRW) Write(p []byte) (int, error) {
	return c.conn.Write(p)
}
