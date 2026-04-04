package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
)

// Server is the TCP server for the guest agent.
type Server struct {
	addr       string
	dispatcher *Dispatcher
	listener   net.Listener
	wg         sync.WaitGroup
	quit       chan struct{}
}

// NewServer creates a new agent server.
func NewServer(addr string) *Server {
	return &Server{
		addr:       addr,
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
