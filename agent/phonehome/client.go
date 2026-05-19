// Package phonehome provides the agent's bidirectional RPC client used to
// invoke daemon-served methods (e.g. OpNetEgress) over the existing agent
// connection. The daemon's proxy.Session intercepts these requests and
// handles them locally instead of forwarding to the SDK.
//
// Concurrency model: a single phonehome.Client wraps the io.ReadWriter that
// the agent dispatcher already shares for a connection. Outbound writes are
// serialized by codec.WriteFrame on the underlying connection (which
// already holds the write mutex). Inbound responses are routed back to the
// caller via a map of pending IDs.
package phonehome

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	codec "github.com/byggflow/sandbox/agent/protocol"
	"github.com/byggflow/sandbox/protocol"
)

// Client lets the agent call daemon-served RPC methods over the existing
// connection. The daemon's proxy.Session matches the method and serves it
// locally rather than forwarding to the SDK.
type Client struct {
	w  io.Writer
	mu sync.Mutex

	nextID  atomic.Int64
	pending sync.Map // id -> chan *protocol.Response

	closed atomic.Bool
}

// New creates a Client that writes frames to w. The agent's read loop is
// responsible for calling Deliver when it observes a Response frame.
func New(w io.Writer) *Client {
	c := &Client{w: w}
	// Use a high starting ID to avoid colliding with the daemon's outbound
	// request IDs on the same connection. (IDs are scoped per-direction in
	// JSON-RPC, but a large gap helps with debug log grep.)
	c.nextID.Store(1 << 30)
	return c
}

// Call sends a JSON-RPC request and waits for the response, up to timeout.
// The daemon must serve the method; otherwise it will be forwarded to the
// SDK which is unlikely to know how to respond.
func (c *Client) Call(method string, params interface{}, timeout time.Duration) (*protocol.Response, error) {
	if c.closed.Load() {
		return nil, errors.New("phonehome: client closed")
	}

	id := int(c.nextID.Add(1))
	req := protocol.Request{JSONRPC: "2.0", ID: id, Method: method, Params: params}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	ch := make(chan *protocol.Response, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	c.mu.Lock()
	err = codec.WriteFrame(c.w, protocol.FrameJSON, payload)
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write frame: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("phonehome: timeout after %s", timeout)
	}
}

// Deliver routes a Response back to a pending Call. Returns true if the
// response was consumed by a pending call.
func (c *Client) Deliver(resp *protocol.Response) bool {
	v, ok := c.pending.LoadAndDelete(resp.ID)
	if !ok {
		return false
	}
	ch := v.(chan *protocol.Response)
	select {
	case ch <- resp:
	default:
	}
	return true
}

// IsPending returns true if id corresponds to an outbound call we made and
// are awaiting a response for. The agent's read loop uses this to decide
// whether an inbound JSON frame is a Response addressed to us versus a
// request to dispatch.
func (c *Client) IsPending(id int) bool {
	_, ok := c.pending.Load(id)
	return ok
}

// Close drops all pending calls and refuses further use.
func (c *Client) Close() {
	c.closed.Store(true)
	c.pending.Range(func(k, v any) bool {
		ch := v.(chan *protocol.Response)
		select {
		case ch <- &protocol.Response{ID: k.(int), Error: &protocol.RPCError{Code: -32099, Message: "connection closed"}}:
		default:
		}
		c.pending.Delete(k)
		return true
	})
}
