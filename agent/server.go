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

	"github.com/byggflow/sandbox/agent/egressproxy"
	"github.com/byggflow/sandbox/agent/phonehome"
	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
)

// Server is the TCP server for the guest agent.
//
// Authentication model:
//   - If SANDBOX_AUTH_BOOTSTRAP is set, the very first connection must
//     present that nonce via auth.bootstrap; in return the agent
//     receives the long-lived auth token and stores it in process
//     memory only. The bootstrap env var is unset immediately so
//     child processes spawned via process.exec don't inherit it.
//   - All subsequent connections must present the long-lived token via
//     auth.token.
//   - If neither env var is set, the server runs unauthenticated
//     (test / single-user mode).
type Server struct {
	addr       string
	authMu     sync.RWMutex
	bootstrap  string // single-use nonce; cleared after first use
	authToken  string // long-lived token; populated after bootstrap
	dispatcher *Dispatcher
	listener   net.Listener
	wg         sync.WaitGroup
	quit       chan struct{}

	egressProxy *egressproxy.Server
}

// startEgressProxy binds the in-sandbox forward proxy listener so user
// processes can begin connecting immediately. The phonehome client is
// supplied per-connection via egressProxy.SetClient; until the first
// daemon connection arrives, the proxy responds with 502.
//
// The egress proxy is only started when the runtime injected the
// SANDBOX_EGRESS_PORT (or SANDBOX_EGRESS_ADDR) env var. This makes
// network_mode=off a true opt-out: the agent doesn't bind 127.0.0.1:8118
// and doesn't claim a user port the sandbox might want for its own
// service. Binding lazily inside handleConn (the previous design)
// created a window between sandbox-ready and listener-ready that user
// code could hit if it raced ahead of the first daemon connection.
func (s *Server) startEgressProxy() {
	addr := os.Getenv("SANDBOX_EGRESS_ADDR")
	if addr == "" {
		port := os.Getenv("SANDBOX_EGRESS_PORT")
		if port == "" {
			// No egress env from the runtime → opt-out. Don't bind.
			return
		}
		addr = "127.0.0.1:" + port
	}
	srv := egressproxy.New(addr, slog.Default())
	if err := srv.ListenAndServe(); err != nil {
		slog.Warn("egress proxy unavailable", "error", err)
		return
	}
	s.egressProxy = srv
}

// NewServer creates a new agent server. Reads SANDBOX_AUTH_BOOTSTRAP
// (a single-use nonce) and clears it from the process environment so
// children spawned via process.exec never observe it. The long-lived
// token is delivered by the daemon over the first connection via
// auth.bootstrap.
//
// SANDBOX_AUTH_TOKEN is honored as a fallback for the pre-bootstrap
// codepath; it is unset on read for the same reason.
func NewServer(addr string) *Server {
	bootstrap := os.Getenv("SANDBOX_AUTH_BOOTSTRAP")
	if bootstrap != "" {
		_ = os.Unsetenv("SANDBOX_AUTH_BOOTSTRAP")
	}
	tok := os.Getenv("SANDBOX_AUTH_TOKEN")
	if tok != "" {
		_ = os.Unsetenv("SANDBOX_AUTH_TOKEN")
	}
	return &Server{
		addr:       addr,
		bootstrap:  bootstrap,
		authToken:  tok,
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

	// Start the egress proxy eagerly so 127.0.0.1:8118 is listening before
	// any user process gets a chance to dial it. The proxy serves errors
	// until SetClient is called from the first daemon connection.
	s.startEgressProxy()

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

	// Require auth as the first RPC if either credential is configured.
	// Bootstrap (single-use nonce delivered via env) is allowed only
	// once; subsequent connections must use the long-lived token.
	if s.authConfigured() {
		if !s.authenticateConn(conn) {
			return
		}
	}

	rw := &connRW{conn: conn}

	// Phonehome client lets handlers (e.g. the local egress proxy) call
	// daemon-served RPCs over this same connection. The daemon's proxy
	// session intercepts these and serves them locally.
	phoneClient := phonehome.New(rw)

	// Swap this connection's phonehome client into the egress proxy so
	// any new egress requests use the current authenticated channel.
	// Reconnects pick up the fresh client without the proxy holding a
	// reference to a dead writer.
	if s.egressProxy != nil {
		s.egressProxy.SetClient(phoneClient)
		defer func() {
			// Always close streams allocated through this connection's
			// client. If a newer reconnect has already installed its
			// own client, those streams are tagged with that newer
			// owner and are left alone.
			s.egressProxy.CloseStreamsForClient(phoneClient)
			// Clear ONLY if the egress proxy still references this
			// connection's client. On reconnect/session-replacement,
			// a newer handleConn may have already installed its own
			// phoneClient; clearing unconditionally would wipe the
			// fresh one and leave the egress proxy with no daemon
			// connection — all sandbox HTTP would then fail with
			// "no active daemon connection" until another reconnect.
			s.egressProxy.ClearClientIf(phoneClient)
			phoneClient.Close()
		}()
	}

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

		case proto.FrameStreamData:
			if s.egressProxy == nil {
				continue
			}
			streamID, data, err := proto.DecodeStreamData(frame.Payload)
			if err != nil {
				slog.Warn("decode stream data", "error", err)
				continue
			}
			if !s.egressProxy.DeliverStreamData(streamID, data) {
				slog.Debug("orphan stream data frame", "stream_id", streamID)
			}

		case proto.FrameStreamEnd:
			if s.egressProxy == nil {
				continue
			}
			streamID, status, errMsg, err := proto.DecodeStreamEnd(frame.Payload)
			if err != nil {
				slog.Warn("decode stream end", "error", err)
				continue
			}
			s.egressProxy.DeliverStreamEnd(streamID, status, errMsg)

		case proto.FrameJSON:
			// Disambiguate Request vs Response. Responses to our own
			// phonehome calls have no method and must be routed back to
			// the caller. Requests are dispatched normally.
			kind, env := proto.ProbeFrame(frame.Payload)
			if kind == proto.FrameKindResponse {
				var resp proto.Response
				if err := json.Unmarshal(frame.Payload, &resp); err == nil {
					if phoneClient.Deliver(&resp) {
						continue
					}
				}
				slog.Warn("orphan response frame", "id", string(env.ID))
				continue
			}

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

// authConfigured returns true when the agent should require auth on
// inbound connections (either bootstrap nonce or long-lived token is
// set).
func (s *Server) authConfigured() bool {
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	return s.bootstrap != "" || s.authToken != ""
}

// peekBootstrap returns the configured nonce without clearing it.
// Used by handleBootstrap for the constant-time compare. Clearing
// before validation would let any caller with a bad guess permanently
// disable the bootstrap path (and worse — leave authConfigured() false
// so subsequent connections are accepted without authentication).
func (s *Server) peekBootstrap() string {
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	return s.bootstrap
}

// consumeBootstrap clears the nonce. Called ONLY after a successful
// constant-time match in handleBootstrap.
func (s *Server) consumeBootstrap() {
	s.authMu.Lock()
	s.bootstrap = ""
	s.authMu.Unlock()
}

// setAuthToken replaces the long-lived token. Used after a successful
// bootstrap to install the daemon-supplied token in process memory.
func (s *Server) setAuthToken(tok string) {
	s.authMu.Lock()
	s.authToken = tok
	s.authMu.Unlock()
}

// currentAuthToken returns the long-lived token snapshot.
func (s *Server) currentAuthToken() string {
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	return s.authToken
}

// authenticateConn reads the first JSON-RPC frame and verifies it is
// either auth.bootstrap (with the configured nonce, one-shot) or
// auth.token (with the long-lived token). Returns false if auth fails.
func (s *Server) authenticateConn(conn net.Conn) bool {
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetDeadline(time.Time{})

	frame, err := codec.ReadFrame(conn)
	if err != nil {
		slog.Error("auth: reading frame", "error", err)
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

	switch req.Method {
	case proto.OpAuthBootstrap:
		return s.handleBootstrap(conn, &req)
	case "auth.token":
		return s.handleAuthToken(conn, &req)
	default:
		slog.Warn("auth: expected auth.bootstrap or auth.token", "method", req.Method)
		s.sendAuthError(conn, req.ID, "first call must be auth.bootstrap or auth.token")
		return false
	}
}

// handleBootstrap accepts the daemon's one-time bootstrap call. The
// daemon presents the nonce delivered at boot time and supplies the
// long-lived token in the same params; on a constant-time match the
// agent installs the token in process memory and consumes the nonce.
func (s *Server) handleBootstrap(conn net.Conn, req *proto.Request) bool {
	var params struct {
		Nonce string `json:"nonce"`
		Token string `json:"token"`
	}
	raw, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(raw, &params); err != nil {
		s.sendAuthError(conn, req.ID, "invalid bootstrap params")
		return false
	}

	// Peek at the nonce without clearing — clearing before validation
	// would let any caller with a wrong guess permanently disable
	// bootstrap AND drop authConfigured() to false (which would then
	// accept all subsequent connections unauthenticated).
	expected := s.peekBootstrap()
	if expected == "" {
		// Bootstrap already used (or never configured). Reject.
		s.sendAuthError(conn, req.ID, "bootstrap unavailable")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(params.Nonce), []byte(expected)) != 1 {
		slog.Warn("auth: invalid bootstrap nonce", "remote", conn.RemoteAddr())
		s.sendAuthError(conn, req.ID, "invalid bootstrap nonce")
		// Nonce stays — the real daemon can still complete bootstrap.
		return false
	}
	if params.Token == "" {
		s.sendAuthError(conn, req.ID, "bootstrap requires token")
		return false
	}

	// Validated — install token then clear the nonce so it can't be
	// replayed.
	s.setAuthToken(params.Token)
	s.consumeBootstrap()
	codec.WriteJSON(conn, proto.Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"authenticated": true, "bootstrap": true},
	})
	slog.Info("auth: bootstrap accepted, long-lived token installed", "remote", conn.RemoteAddr())
	return true
}

// handleAuthToken is the standard token check for all post-bootstrap
// connections.
func (s *Server) handleAuthToken(conn net.Conn, req *proto.Request) bool {
	tok := s.currentAuthToken()
	if tok == "" {
		s.sendAuthError(conn, req.ID, "auth not initialized")
		return false
	}

	var params struct {
		Token string `json:"token"`
	}
	raw, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(raw, &params); err != nil || subtle.ConstantTimeCompare([]byte(params.Token), []byte(tok)) != 1 {
		slog.Warn("auth: invalid token", "remote", conn.RemoteAddr())
		s.sendAuthError(conn, req.ID, "invalid token")
		return false
	}

	codec.WriteJSON(conn, proto.Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"authenticated": true},
	})
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

// connRW wraps a net.Conn to implement io.ReadWriter with a write
// mutex. Reads are sequential (one read loop per connection), but
// writes happen from multiple goroutines: the dispatcher writes
// JSON-RPC responses, the phonehome client writes outbound RPCs, the
// streaming-egress copier writes FrameStreamData/End frames, and the
// auth handlers write Response frames. Each frame must reach the wire
// as one contiguous byte sequence — without serialization, two
// concurrent Write calls can interleave at the TCP layer and corrupt
// the agent protocol on the daemon side.
type connRW struct {
	conn   net.Conn
	writeM sync.Mutex
}

func (c *connRW) Read(p []byte) (int, error) {
	return c.conn.Read(p)
}

func (c *connRW) Write(p []byte) (int, error) {
	c.writeM.Lock()
	defer c.writeM.Unlock()
	return c.conn.Write(p)
}
