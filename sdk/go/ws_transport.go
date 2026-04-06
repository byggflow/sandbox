package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *int64      `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *int64           `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *jsonRPCError    `json:"error,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// jsonRPCError is a JSON-RPC 2.0 error object.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// pending represents a pending RPC call awaiting a response.
type pending struct {
	ch          chan jsonRPCResponse
	binaryBufs [][]byte     // collected binary frames before JSON response
	expectBin  bool          // if true, collect binary frames into binaryBufs
	binCh      chan [][]byte // channel to deliver collected binary buffers
}

// wsTransport implements RpcTransport over a WebSocket connection.
type wsTransport struct {
	conn    *websocket.Conn
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]*pending
	closed  chan struct{}

	// binPendingID tracks which request ID should receive binary frames.
	// Only one request can expect binary at a time (protocol is sequential per connection).
	binPendingID atomic.Int64

	notifMu   sync.RWMutex
	notifHandler NotificationHandler

	replacedMu   sync.RWMutex
	replacedHandler ReplacedHandler
}

// dialWS creates a new WebSocket transport connected to the given URL.
func dialWS(ctx context.Context, url string, headers map[string]string) (*wsTransport, error) {
	httpHeaders := http.Header{}
	for k, v := range headers {
		httpHeaders.Set(k, v)
	}

	// For Unix socket endpoints, we need a custom dialer.
	var conn *websocket.Conn
	var err error

	if strings.HasPrefix(url, "unix://") || strings.HasPrefix(url, "ws+unix://") {
		// Parse unix socket path and request path from URL.
		// Format: unix:///path/to/sock or ws+unix:///path/to/sock
		// The WebSocket path is encoded after the socket path.
		sockPath, wsPath := parseUnixURL(url)
		dialer := &net.Dialer{}
		httpClient := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", sockPath)
				},
			},
		}
		conn, _, err = websocket.Dial(ctx, "ws://localhost"+wsPath, &websocket.DialOptions{
			HTTPHeader: httpHeaders,
			HTTPClient: httpClient,
		})
	} else {
		conn, _, err = websocket.Dial(ctx, url, &websocket.DialOptions{
			HTTPHeader: httpHeaders,
		})
	}
	if err != nil {
		return nil, &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("websocket dial: %v", err),
		}}
	}

	// Match the daemon's MaxFrameSize protocol limit.
	conn.SetReadLimit(10 * 1024 * 1024) // 10MB

	t := &wsTransport{
		conn:    conn,
		pending: make(map[int64]*pending),
		closed:  make(chan struct{}),
	}

	go t.readLoop()

	return t, nil
}

// parseUnixURL extracts the socket path and the HTTP path from a Unix URL.
// Handles formats like:
//   - unix:///var/run/sandboxd/sandboxd.sock (path derived from sandbox ID set later)
//   - unix:///var/run/sandboxd/sandboxd.sock:/sandboxes/id/ws
func parseUnixURL(rawURL string) (sockPath, httpPath string) {
	// Strip scheme.
	s := rawURL
	for _, prefix := range []string{"ws+unix://", "unix://"} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			break
		}
	}

	// Check for colon separator between socket path and HTTP path.
	if idx := strings.Index(s, ":"); idx > 0 {
		sockPath = s[:idx]
		httpPath = s[idx+1:]
	} else {
		sockPath = s
		httpPath = "/"
	}
	return
}

// readLoop continuously reads messages from the WebSocket and dispatches them.
func (t *wsTransport) readLoop() {
	defer close(t.closed)
	ctx := context.Background()

	for {
		typ, data, err := t.conn.Read(ctx)
		if err != nil {
			// Connection closed or error — cancel all pending requests.
			t.mu.Lock()
			for id, p := range t.pending {
				p.ch <- jsonRPCResponse{
					Error: &jsonRPCError{
						Code:    -32000,
						Message: fmt.Sprintf("connection closed: %v", err),
					},
				}
				delete(t.pending, id)
			}
			t.mu.Unlock()
			return
		}

		if typ == websocket.MessageBinary {
			// Route binary frames to the pending request that expects them.
			binID := t.binPendingID.Load()
			if binID > 0 {
				t.mu.Lock()
				if p, ok := t.pending[binID]; ok && p.expectBin {
					// Make a copy of data since the websocket buffer may be reused.
					buf := make([]byte, len(data))
					copy(buf, data)
					p.binaryBufs = append(p.binaryBufs, buf)
				}
				t.mu.Unlock()
			} else {
				// Fall back to notification handler for backward compatibility.
				t.notifMu.RLock()
				h := t.notifHandler
				t.notifMu.RUnlock()
				if h != nil {
					h("_binary", data)
				}
			}
			continue
		}

		// Text message — parse as JSON-RPC.
		var resp jsonRPCResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue // Skip malformed messages.
		}

		// Check for session.replaced notification.
		if resp.ID == nil && resp.Method == "session.replaced" {
			t.replacedMu.RLock()
			h := t.replacedHandler
			t.replacedMu.RUnlock()
			if h != nil {
				h()
			}
			continue
		}

		// Notification (no ID).
		if resp.ID == nil && resp.Method != "" {
			t.notifMu.RLock()
			h := t.notifHandler
			t.notifMu.RUnlock()
			if h != nil {
				var params interface{}
				if resp.Params != nil {
					json.Unmarshal(resp.Params, &params)
				}
				h(resp.Method, params)
			}
			continue
		}

		// Response (has ID).
		if resp.ID != nil {
			t.mu.Lock()
			p, ok := t.pending[*resp.ID]
			if ok {
				delete(t.pending, *resp.ID)
				// Clear binPendingID if this was the binary-expecting request.
				if p.expectBin {
					t.binPendingID.Store(0)
					if p.binCh != nil {
						p.binCh <- p.binaryBufs
					}
				}
			}
			t.mu.Unlock()
			if ok {
				p.ch <- resp
			}
		}
	}
}

// Call sends a JSON-RPC request and waits for the response.
func (t *wsTransport) Call(ctx context.Context, method string, params interface{}) (interface{}, error) {
	id := t.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	p := &pending{ch: make(chan jsonRPCResponse, 1)}

	t.mu.Lock()
	t.pending[id] = p
	t.mu.Unlock()

	if err := t.conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("write: %v", err),
		}}
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, &TimeoutError{SandboxError: SandboxError{
			Message: ctx.Err().Error(),
		}}
	case resp := <-p.ch:
		if resp.Error != nil {
			return nil, &RpcError{
				SandboxError: SandboxError{Message: resp.Error.Message},
				Code:         resp.Error.Code,
			}
		}
		var result interface{}
		if resp.Result != nil {
			if err := json.Unmarshal(resp.Result, &result); err != nil {
				return nil, fmt.Errorf("unmarshal result: %w", err)
			}
		}
		return result, nil
	case <-t.closed:
		return nil, &ConnectionError{SandboxError: SandboxError{
			Message: "connection closed",
		}}
	}
}

// SendBinary sends raw binary data over the WebSocket connection.
func (t *wsTransport) SendBinary(ctx context.Context, data []byte) error {
	if err := t.conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		return &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("write binary: %v", err),
		}}
	}
	return nil
}

// CallWithBinary sends a JSON-RPC request, then sends binary data frame(s),
// and waits for the JSON response. This is the correct protocol for fs.write/fs.upload.
func (t *wsTransport) CallWithBinary(ctx context.Context, method string, params interface{}, data []byte) (interface{}, error) {
	id := t.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	p := &pending{ch: make(chan jsonRPCResponse, 1)}

	t.mu.Lock()
	t.pending[id] = p
	t.mu.Unlock()

	// Send JSON request.
	if err := t.conn.Write(ctx, websocket.MessageText, reqData); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("write: %v", err),
		}}
	}

	// Send binary data in chunks if needed.
	chunkSize := 1024 * 1024 // 1MB
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		if err := t.conn.Write(ctx, websocket.MessageBinary, data[offset:end]); err != nil {
			t.mu.Lock()
			delete(t.pending, id)
			t.mu.Unlock()
			return nil, &ConnectionError{SandboxError: SandboxError{
				Message: fmt.Sprintf("write binary: %v", err),
			}}
		}
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, &TimeoutError{SandboxError: SandboxError{
			Message: ctx.Err().Error(),
		}}
	case resp := <-p.ch:
		if resp.Error != nil {
			return nil, &RpcError{
				SandboxError: SandboxError{Message: resp.Error.Message},
				Code:         resp.Error.Code,
			}
		}
		var result interface{}
		if resp.Result != nil {
			if err := json.Unmarshal(resp.Result, &result); err != nil {
				return nil, fmt.Errorf("unmarshal result: %w", err)
			}
		}
		return result, nil
	case <-t.closed:
		return nil, &ConnectionError{SandboxError: SandboxError{
			Message: "connection closed",
		}}
	}
}

// CallExpectBinary sends a JSON-RPC request and collects binary frames
// that arrive before the JSON response. Used for fs.read/fs.download.
func (t *wsTransport) CallExpectBinary(ctx context.Context, method string, params interface{}) (interface{}, [][]byte, error) {
	id := t.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}

	binCh := make(chan [][]byte, 1)
	p := &pending{
		ch:        make(chan jsonRPCResponse, 1),
		expectBin: true,
		binCh:     binCh,
	}

	t.mu.Lock()
	t.pending[id] = p
	t.mu.Unlock()

	// Tell readLoop to route binary frames to this request.
	t.binPendingID.Store(id)

	if err := t.conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		t.binPendingID.Store(0)
		return nil, nil, &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("write: %v", err),
		}}
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		t.binPendingID.Store(0)
		return nil, nil, &TimeoutError{SandboxError: SandboxError{
			Message: ctx.Err().Error(),
		}}
	case resp := <-p.ch:
		// Collect the binary buffers that were accumulated.
		var bufs [][]byte
		select {
		case bufs = <-binCh:
		default:
			bufs = p.binaryBufs
		}

		if resp.Error != nil {
			return nil, bufs, &RpcError{
				SandboxError: SandboxError{Message: resp.Error.Message},
				Code:         resp.Error.Code,
			}
		}
		var result interface{}
		if resp.Result != nil {
			if err := json.Unmarshal(resp.Result, &result); err != nil {
				return nil, bufs, fmt.Errorf("unmarshal result: %w", err)
			}
		}
		return result, bufs, nil
	case <-t.closed:
		t.binPendingID.Store(0)
		return nil, nil, &ConnectionError{SandboxError: SandboxError{
			Message: "connection closed",
		}}
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (t *wsTransport) Notify(ctx context.Context, method string, params interface{}) error {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	if err := t.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("write: %v", err),
		}}
	}

	return nil
}

// OnNotification registers a handler for incoming notifications.
func (t *wsTransport) OnNotification(handler NotificationHandler) {
	t.notifMu.Lock()
	defer t.notifMu.Unlock()
	t.notifHandler = handler
}

// OnReplaced registers a handler for session replaced events.
func (t *wsTransport) OnReplaced(handler ReplacedHandler) {
	t.replacedMu.Lock()
	defer t.replacedMu.Unlock()
	t.replacedHandler = handler
}

// Close shuts down the WebSocket connection.
func (t *wsTransport) Close() error {
	return t.conn.Close(websocket.StatusNormalClosure, "closing")
}
