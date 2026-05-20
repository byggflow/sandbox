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
	if s.peekBootstrap() != "" {
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

	// Nonce must NOT be consumed on a failed attempt — otherwise any
	// caller with a wrong guess would brick the daemon's bootstrap
	// and disable authConfigured() entirely. Real daemon retry must
	// still succeed.
	if got := s.peekBootstrap(); got != "good-nonce" {
		t.Errorf("bootstrap nonce must survive a failed bad-nonce attempt; got %q", got)
	}
	// Token should NOT be installed.
	if tok := s.currentAuthToken(); tok != "" {
		t.Errorf("token must not be installed by a failed bootstrap; got %q", tok)
	}
}

// TestAuthBootstrapBadGuessDoesNotDisableAuth is the regression test
// for the auth-bypass: an attacker (or buggy client) racing the
// daemon with a wrong-nonce bootstrap MUST NOT brick auth. Before
// the fix, the agent consumed the nonce before validating, so:
//   - bootstrap was cleared
//   - authToken stayed empty
//   - authConfigured() returned false on subsequent connections
//   - the agent then accepted EVERY incoming connection without auth
// The real daemon must still be able to complete bootstrap after the
// bad guess.
func TestAuthBootstrapBadGuessDoesNotDisableAuth(t *testing.T) {
	s := &Server{
		bootstrap:  "real-nonce",
		dispatcher: NewDispatcher(),
		quit:       make(chan struct{}),
	}

	// First: bad guess from attacker.
	c1, srv1 := net.Pipe()
	go s.authenticateConnInTest(t, srv1)
	writeRPC(t, c1, 1, proto.OpAuthBootstrap, map[string]string{"nonce": "wrong", "token": "attacker"})
	if r := readRPC(t, c1); r.Error == nil {
		t.Fatal("bad nonce should be rejected")
	}
	_ = c1.Close()

	// Auth must still be REQUIRED (nonce preserved, OR token set).
	if !s.authConfigured() {
		t.Fatal("auth disabled after bad bootstrap attempt — attacker can now connect unauthenticated")
	}

	// Second: real daemon completes bootstrap.
	c2, srv2 := net.Pipe()
	go s.authenticateConnInTest(t, srv2)
	writeRPC(t, c2, 1, proto.OpAuthBootstrap, map[string]string{"nonce": "real-nonce", "token": "real-T"})
	if r := readRPC(t, c2); r.Error != nil {
		t.Fatalf("real bootstrap rejected after bad-nonce: %v", r.Error)
	}
	_ = c2.Close()
	if got := s.currentAuthToken(); got != "real-T" {
		t.Errorf("real token not installed: %q", got)
	}
}

func TestAuthBootstrapRefusedAfterConsumption(t *testing.T) {
	s := &Server{
		bootstrap:  "n",
		dispatcher: NewDispatcher(),
		quit:       make(chan struct{}),
	}
	s.consumeBootstrap() // simulate a prior successful bootstrap (nonce now empty)

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
