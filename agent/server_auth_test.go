package agent

import (
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
)

// TestAuthBootstrapHappyPath verifies the daemon-side bootstrap RPC
// trades the nonce for the long-lived token; the server then accepts
// auth.token using the token on a fresh connection.
func TestAuthBootstrapHappyPath(t *testing.T) {
	s := &Server{
		bootstrap:  "nonce-secret",
		dispatcher: NewDispatcher(),
		quit:       make(chan struct{}),
	}

	// First connection: bootstrap call swaps nonce for token.
	client, server := net.Pipe()
	go s.authenticateConnInTest(t, server)

	writeRPC(t, client, 1, proto.OpAuthBootstrap, map[string]string{
		"nonce": "nonce-secret",
		"token": "long-lived-T",
	})
	resp := readRPC(t, client)
	if resp.Error != nil {
		t.Fatalf("bootstrap rejected: %v", resp.Error)
	}
	_ = client.Close()

	// The token should now be installed.
	if got := s.currentAuthToken(); got != "long-lived-T" {
		t.Errorf("token not installed: %q", got)
	}
	// And the nonce should be consumed.
	if s.consumeBootstrap() != "" {
		t.Error("bootstrap nonce was not consumed by successful exchange")
	}
}

func TestAuthBootstrapRejectsBadNonce(t *testing.T) {
	s := &Server{
		bootstrap:  "good-nonce",
		dispatcher: NewDispatcher(),
		quit:       make(chan struct{}),
	}

	client, server := net.Pipe()
	go s.authenticateConnInTest(t, server)

	writeRPC(t, client, 1, proto.OpAuthBootstrap, map[string]string{
		"nonce": "wrong-nonce",
		"token": "T",
	})
	resp := readRPC(t, client)
	if resp.Error == nil {
		t.Fatal("expected auth error for bad nonce")
	}
	_ = client.Close()

	// Nonce should be consumed even on failure — single-use means
	// single-use, no retry.
	if s.consumeBootstrap() != "" {
		t.Error("bootstrap nonce should be consumed even on failed attempt")
	}
}

func TestAuthBootstrapRefusedAfterConsumption(t *testing.T) {
	s := &Server{
		bootstrap:  "n",
		dispatcher: NewDispatcher(),
		quit:       make(chan struct{}),
	}
	s.consumeBootstrap() // simulate a prior successful bootstrap

	client, server := net.Pipe()
	go s.authenticateConnInTest(t, server)

	writeRPC(t, client, 1, proto.OpAuthBootstrap, map[string]string{
		"nonce": "anything",
		"token": "T",
	})
	resp := readRPC(t, client)
	if resp.Error == nil {
		t.Fatal("expected bootstrap-unavailable error after consumption")
	}
	_ = client.Close()
}

func TestAuthTokenAcceptedAfterBootstrap(t *testing.T) {
	s := &Server{
		dispatcher: NewDispatcher(),
		quit:       make(chan struct{}),
	}
	s.setAuthToken("post-bootstrap-T")

	client, server := net.Pipe()
	go s.authenticateConnInTest(t, server)

	writeRPC(t, client, 1, "auth.token", map[string]string{"token": "post-bootstrap-T"})
	resp := readRPC(t, client)
	if resp.Error != nil {
		t.Errorf("auth.token rejected: %v", resp.Error)
	}
	_ = client.Close()
}

func TestAuthTokenRejectedBeforeBootstrap(t *testing.T) {
	// Server has a bootstrap configured but no token yet.
	s := &Server{
		bootstrap:  "n",
		dispatcher: NewDispatcher(),
		quit:       make(chan struct{}),
	}

	client, server := net.Pipe()
	go s.authenticateConnInTest(t, server)

	writeRPC(t, client, 1, "auth.token", map[string]string{"token": "anything"})
	resp := readRPC(t, client)
	if resp.Error == nil {
		t.Error("expected auth-not-initialized error when auth.token used before bootstrap")
	}
	_ = client.Close()
}

// authenticateConnInTest wraps authenticateConn for the test setup.
// The real server calls this from handleConn; we exercise it directly.
func (s *Server) authenticateConnInTest(t *testing.T, conn net.Conn) {
	t.Helper()
	s.authenticateConn(conn)
}

func writeRPC(t *testing.T, w io.Writer, id int, method string, params interface{}) {
	t.Helper()
	payload, err := json.Marshal(proto.Request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.WriteFrame(w, proto.FrameJSON, payload); err != nil {
		t.Fatal(err)
	}
}

func readRPC(t *testing.T, r io.Reader) proto.Response {
	t.Helper()
	type deadlineReader interface {
		SetReadDeadline(time.Time) error
	}
	if dr, ok := r.(deadlineReader); ok {
		_ = dr.SetReadDeadline(time.Now().Add(2 * time.Second))
	}
	frame, err := codec.ReadFrame(r)
	if err != nil {
		t.Fatalf("reading frame: %v", err)
	}
	var resp proto.Response
	if err := json.Unmarshal(frame.Payload, &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return resp
}
