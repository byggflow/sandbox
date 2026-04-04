package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/byggflow/sandbox/protocol"
	"nhooyr.io/websocket"
)

// Session bridges a client WebSocket connection to a guest agent TCP connection.
// It translates JSON-RPC messages from the client into binary frames for the agent
// and relays responses and notifications back.
type Session struct {
	ws    *websocket.Conn
	agent *AgentConn
	log   *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// NewSession creates a new proxy session.
func NewSession(ws *websocket.Conn, agent *AgentConn, log *slog.Logger) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		ws:     ws,
		agent:  agent,
		log:    log,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

// Run starts bidirectional proxying. It blocks until the session ends.
func (s *Session) Run(ctx context.Context) error {
	// Merge the external context with our internal one.
	go func() {
		select {
		case <-ctx.Done():
			s.cancel()
		case <-s.ctx.Done():
		}
	}()

	errc := make(chan error, 2)

	// Client -> Agent
	go func() {
		errc <- s.clientToAgent()
	}()

	// Agent -> Client
	go func() {
		errc <- s.agentToClient()
	}()

	// Wait for either direction to finish.
	err := <-errc
	s.Close()
	// Drain the other goroutine.
	<-errc
	return err
}

// Close terminates the session.
func (s *Session) Close() {
	s.once.Do(func() {
		s.cancel()
		s.ws.Close(websocket.StatusNormalClosure, "session closed")
		s.agent.Close()
		close(s.done)
	})
}

// Done returns a channel that is closed when the session ends.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// clientToAgent reads JSON-RPC messages from the WebSocket and forwards them
// as binary frames to the agent.
func (s *Session) clientToAgent() error {
	for {
		msgType, data, err := s.ws.Read(s.ctx)
		if err != nil {
			return fmt.Errorf("read websocket: %w", err)
		}

		switch msgType {
		case websocket.MessageText:
			// JSON-RPC request from client -> JSON frame to agent.
			if err := s.agent.WriteFrame(protocol.FrameJSON, data); err != nil {
				return fmt.Errorf("write json frame to agent: %w", err)
			}
		case websocket.MessageBinary:
			// Binary data (stdin, file upload) -> Binary frame to agent.
			if err := s.agent.WriteFrame(protocol.FrameBinary, data); err != nil {
				return fmt.Errorf("write binary frame to agent: %w", err)
			}
		}
	}
}

// agentToClient reads binary frames from the agent and forwards them as
// WebSocket messages to the client.
func (s *Session) agentToClient() error {
	for {
		frameType, payload, err := s.agent.ReadFrame()
		if err != nil {
			return fmt.Errorf("read agent frame: %w", err)
		}

		switch frameType {
		case protocol.FrameJSON:
			// JSON response or notification -> text WebSocket message.
			if err := s.ws.Write(s.ctx, websocket.MessageText, payload); err != nil {
				return fmt.Errorf("write text to websocket: %w", err)
			}
		case protocol.FrameBinary:
			// Binary data (stdout, file content) -> binary WebSocket message.
			if err := s.ws.Write(s.ctx, websocket.MessageBinary, payload); err != nil {
				return fmt.Errorf("write binary to websocket: %w", err)
			}
		case protocol.FramePing:
			// Agent ping -> respond with pong.
			if err := s.agent.WriteFrame(protocol.FramePing, []byte{protocol.PingResponse}); err != nil {
				return fmt.Errorf("write pong to agent: %w", err)
			}
		default:
			s.log.Warn("unknown frame type from agent", "type", frameType)
		}
	}
}

// SendRawJSON sends pre-marshaled JSON data as a text WebSocket message.
func (s *Session) SendRawJSON(data []byte) error {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	return s.ws.Write(ctx, websocket.MessageText, data)
}

// SendNotification sends a JSON-RPC notification to the client over WebSocket.
func (s *Session) SendNotification(method string, params interface{}) error {
	notif := protocol.Notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	return s.ws.Write(ctx, websocket.MessageText, data)
}
