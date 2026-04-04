package agent

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

// ListenAndServe starts the TCP listener and accepts connections.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = ln
	log.Printf("agent listening on %s", s.addr)

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("received %v, shutting down", sig)
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
				log.Printf("accept error: %v", err)
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
	log.Printf("connection from %s", conn.RemoteAddr())

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
				log.Printf("read frame error: %v", err)
			}
			return
		}

		switch frame.Type {
		case proto.FramePing:
			if len(frame.Payload) == 1 && frame.Payload[0] == proto.PingRequest {
				if err := codec.WritePong(conn); err != nil {
					log.Printf("write pong error: %v", err)
					return
				}
			}

		case proto.FrameJSON:
			var req proto.Request
			if err := json.Unmarshal(frame.Payload, &req); err != nil {
				log.Printf("invalid json-rpc request: %v", err)
				continue
			}
			if req.JSONRPC != "2.0" {
				log.Printf("invalid jsonrpc version: %s", req.JSONRPC)
				continue
			}
			s.dispatcher.Handle(&req, rw)

		case proto.FrameBinary:
			// Binary frame: PTY input. We need to determine which PTY to route to.
			// For now, we route to the most recently created PTY, or use a PID
			// prefix protocol: first 4 bytes are big-endian PID, rest is data.
			s.handleBinaryFrame(frame.Payload)

		default:
			log.Printf("unexpected frame type: 0x%02x", frame.Type)
		}
	}
}

// handleBinaryFrame routes binary data to a PTY process.
// Protocol: first 4 bytes are big-endian PID, rest is data.
func (s *Server) handleBinaryFrame(payload []byte) {
	if len(payload) < 4 {
		log.Printf("binary frame too short: %d bytes", len(payload))
		return
	}

	pid := int(uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3]))
	data := payload[4:]

	ptyMgr := s.dispatcher.PtyManager()
	if err := ptyMgr.WritePtyInput(pid, data); err != nil {
		log.Printf("pty input error (pid %d): %v", pid, err)
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
		log.Printf("auth: failed to read frame: %v", err)
		return false
	}

	if frame.Type != proto.FrameJSON {
		log.Printf("auth: expected JSON frame, got 0x%02x", frame.Type)
		return false
	}

	var req proto.Request
	if err := json.Unmarshal(frame.Payload, &req); err != nil {
		log.Printf("auth: invalid JSON-RPC: %v", err)
		return false
	}

	if req.Method != "auth.token" {
		log.Printf("auth: expected auth.token, got %s", req.Method)
		s.sendAuthError(conn, req.ID, "first call must be auth.token")
		return false
	}

	var params struct {
		Token string `json:"token"`
	}
	raw, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(raw, &params); err != nil || subtle.ConstantTimeCompare([]byte(params.Token), []byte(s.authToken)) != 1 {
		log.Printf("auth: invalid token from %s", conn.RemoteAddr())
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

	log.Printf("auth: connection authenticated from %s", conn.RemoteAddr())
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
