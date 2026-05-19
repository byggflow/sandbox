package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/byggflow/sandbox/protocol"
	"github.com/coder/websocket"
)

// LocalHandler serves a JSON-RPC Request the daemon has chosen to own.
// Return (result, nil) for success or (nil, err) for an error response.
// Returning (nil, nil) is a runtime contract violation: the synchronous
// Claims check is supposed to be the only place that decides ownership.
type LocalHandler func(ctx context.Context, method string, params json.RawMessage) (interface{}, error)

// MethodSet is a synchronous "do I own this method?" check. It must be
// fast and side-effect-free — it runs in the WebSocket read loop before
// any goroutine is spawned. Methods that aren't claimed flow through
// unchanged so any binary follow-up frames stay in order with the JSON
// Request that owns them.
type MethodSet func(method string) bool

// Hooks wires daemon-side handlers into a proxy Session. AgentRequest is
// invoked when the AGENT sends a JSON-RPC Request to the daemon
// (bidirectional RPC); ClientRequest is invoked when the SDK CLIENT sends
// a JSON-RPC Request that the daemon wants to handle itself instead of
// forwarding to the agent.
//
// The *Methods fields are synchronous claim checks. The daemon MUST set
// these for any method it handles locally, otherwise the hook is ignored
// (failing safely toward forwarding). This split exists because frames
// following a JSON Request (binary uploads, file contents) must be
// forwarded in the same order they were received, and we can only know
// whether to detour the JSON before the next frame arrives.
type Hooks struct {
	AgentRequest         LocalHandler
	AgentRequestMethods  MethodSet
	ClientRequest        LocalHandler
	ClientRequestMethods MethodSet
}

// Session bridges a client WebSocket connection to a guest agent TCP connection.
// It translates JSON-RPC messages from the client into binary frames for the agent
// and relays responses and notifications back.
type Session struct {
	ws    *websocket.Conn
	agent *AgentConn
	log   *slog.Logger
	hooks atomic.Pointer[Hooks]

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	// Outbound Requests to the SDK (daemon-initiated). Each pending call
	// is keyed by its ID; the WS read loop delivers the Response back via
	// the channel.
	clientCallMu      sync.Mutex
	clientCallNextID  int
	clientCallPending map[int]chan *protocol.Response
}

func (s *Session) loadHooks() Hooks {
	h := s.hooks.Load()
	if h == nil {
		return Hooks{}
	}
	return *h
}

// NewSession creates a new proxy session.
func NewSession(ws *websocket.Conn, agent *AgentConn, log *slog.Logger) *Session {
	return newSession(ws, agent, log)
}

// NewSessionWithHooks creates a proxy session with daemon-side handlers
// installed. See Hooks.
func NewSessionWithHooks(ws *websocket.Conn, agent *AgentConn, log *slog.Logger, hooks Hooks) *Session {
	s := newSession(ws, agent, log)
	s.SetHooks(hooks)
	return s
}

func newSession(ws *websocket.Conn, agent *AgentConn, log *slog.Logger) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		ws:                ws,
		agent:             agent,
		log:               log,
		ctx:               ctx,
		cancel:            cancel,
		done:              make(chan struct{}),
		clientCallNextID:  1 << 30, // avoid colliding with SDK-originated IDs
		clientCallPending: make(map[int]chan *protocol.Response),
	}
}

// SetHooks atomically replaces the session's hooks. Safe to call at any
// time, including concurrently with the read loops.
func (s *Session) SetHooks(h Hooks) {
	s.hooks.Store(&h)
}

// WriteAgentFrame writes a raw frame to the agent connection. Used by
// daemon-side streaming handlers to push FrameStreamData /
// FrameStreamEnd frames addressed to a stream ID. Writes are
// serialized by AgentConn's internal mutex.
func (s *Session) WriteAgentFrame(frameType byte, payload []byte) error {
	return s.agent.WriteFrame(frameType, payload)
}

// CallClient sends a JSON-RPC Request to the SDK over the WebSocket and
// waits for the Response. This is the daemon-initiated analogue of the
// agent's phonehome client; the SDK's incoming-request dispatcher serves
// the request and writes a Response back.
//
// Returns an error if the timeout expires, the session closes, or the
// connection drops while waiting.
func (s *Session) CallClient(ctx context.Context, method string, params interface{}, timeout time.Duration) (*protocol.Response, error) {
	s.clientCallMu.Lock()
	id := s.clientCallNextID
	s.clientCallNextID++
	ch := make(chan *protocol.Response, 1)
	s.clientCallPending[id] = ch
	s.clientCallMu.Unlock()
	defer func() {
		s.clientCallMu.Lock()
		delete(s.clientCallPending, id)
		s.clientCallMu.Unlock()
	}()

	req := protocol.Request{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal client request: %w", err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.ws.Write(writeCtx, websocket.MessageText, data); err != nil {
		return nil, fmt.Errorf("send client request: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("client request timed out after %s", timeout)
	case <-s.ctx.Done():
		return nil, fmt.Errorf("session closed: %w", s.ctx.Err())
	}
}

// deliverClientResponse routes a Response read from the WebSocket to the
// pending CallClient that issued the matching Request. Returns true if
// the response was consumed; false means it's an unrelated frame (e.g.
// a response to an SDK-originated request that the daemon is forwarding).
func (s *Session) deliverClientResponse(resp *protocol.Response) bool {
	s.clientCallMu.Lock()
	ch, ok := s.clientCallPending[resp.ID]
	if ok {
		delete(s.clientCallPending, resp.ID)
	}
	s.clientCallMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- resp:
	default:
	}
	return true
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
// as binary frames to the agent. Requests whose method matches a registered
// ClientRequest hook are handled on the daemon side instead of forwarded.
func (s *Session) clientToAgent() error {
	for {
		msgType, data, err := s.ws.Read(s.ctx)
		if err != nil {
			return fmt.Errorf("read websocket: %w", err)
		}

		switch msgType {
		case websocket.MessageText:
			// Route Responses to daemon-initiated CallClient invocations
			// back to their pending channel. Other frames continue down
			// the normal forwarding/hook path.
			if kind, _ := protocol.ProbeFrame(data); kind == protocol.FrameKindResponse {
				var resp protocol.Response
				if err := json.Unmarshal(data, &resp); err == nil {
					if s.deliverClientResponse(&resp) {
						continue
					}
				}
				// Fall through: unrelated response (e.g. echo from agent
				// not delivered to a pending CallClient). Let it forward
				// normally so existing behavior is unchanged.
			}
			if s.maybeServeClientLocal(data) {
				continue
			}
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
// WebSocket messages to the client. Agent-initiated JSON-RPC Requests
// matching a registered AgentRequest hook are dispatched on the daemon side
// and the response is written back to the agent.
func (s *Session) agentToClient() error {
	for {
		frameType, payload, err := s.agent.ReadFrame()
		if err != nil {
			return fmt.Errorf("read agent frame: %w", err)
		}

		switch frameType {
		case protocol.FrameJSON:
			if s.maybeServeAgentLocal(payload) {
				continue
			}
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

// maybeServeAgentLocal returns true when the frame was a Request that the
// AgentRequest hook claimed and is now serving asynchronously. Returns
// false to forward to the WebSocket client.
func (s *Session) maybeServeAgentLocal(payload []byte) bool {
	h := s.loadHooks()
	if h.AgentRequest == nil || h.AgentRequestMethods == nil {
		return false
	}
	return s.maybeServeLocal(payload, h.AgentRequest, h.AgentRequestMethods, "agent")
}

// maybeServeClientLocal returns true when the frame was a Request that the
// ClientRequest hook claimed. Returns false to forward to the agent so
// any binary follow-up frames stay in order behind the JSON Request.
func (s *Session) maybeServeClientLocal(payload []byte) bool {
	h := s.loadHooks()
	if h.ClientRequest == nil || h.ClientRequestMethods == nil {
		return false
	}
	return s.maybeServeLocal(payload, h.ClientRequest, h.ClientRequestMethods, "client")
}

// maybeServeLocal claims an inbound JSON Request synchronously via the
// MethodSet check; only after a successful claim does it spawn the
// handler goroutine. The synchronous claim is the contract that keeps
// binary-follow-up frames in order with their owning JSON Request.
func (s *Session) maybeServeLocal(payload []byte, handler LocalHandler, owns MethodSet, side string) bool {
	kind, env := protocol.ProbeFrame(payload)
	if kind != protocol.FrameKindRequest {
		return false
	}
	if !owns(env.Method) {
		// Not ours — fall through synchronously so the read loop keeps
		// forwarding any binary frames that belong to this Request.
		return false
	}

	id := protocol.DecodeID(env.ID)
	go func(method string, params json.RawMessage, id int) {
		result, err := handler(s.ctx, method, params)
		resp := protocol.Response{JSONRPC: "2.0", ID: id}
		if err != nil {
			resp.Error = &protocol.RPCError{Code: -32000, Message: err.Error()}
		} else {
			resp.Result = result
		}
		data, mErr := json.Marshal(resp)
		if mErr != nil {
			s.log.Error("marshal "+side+"-request response", "error", mErr)
			return
		}
		if writeErr := s.writeLocalResponse(side, data); writeErr != nil {
			s.log.Error("write "+side+"-request response", "error", writeErr)
		}
	}(env.Method, env.Params, id)
	return true
}

// writeLocalResponse writes a Response back to whichever side originated
// the matching Request.
func (s *Session) writeLocalResponse(side string, data []byte) error {
	if side == "agent" {
		return s.agent.WriteFrame(protocol.FrameJSON, data)
	}
	return s.ws.Write(s.ctx, websocket.MessageText, data)
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
