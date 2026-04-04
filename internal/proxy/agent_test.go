package proxy

import (
	"net"
	"testing"
	"time"
)

func TestTCPDialerImplementsDialer(t *testing.T) {
	var _ Dialer = TCPDialer{}
}

func TestDefaultDialerIsTCP(t *testing.T) {
	_, ok := DefaultDialer.(TCPDialer)
	if !ok {
		t.Errorf("DefaultDialer is %T, want TCPDialer", DefaultDialer)
	}
}

// fakeDialer records calls and returns a fake connection.
type fakeDialer struct {
	addr    string
	timeout time.Duration
	err     error
}

func (f *fakeDialer) Dial(addr string, timeout time.Duration) (net.Conn, error) {
	f.addr = addr
	f.timeout = timeout
	if f.err != nil {
		return nil, f.err
	}
	// Return a pipe so the AgentConn has something to close.
	server, client := net.Pipe()
	server.Close()
	return client, nil
}

func TestDialWithCustomDialer(t *testing.T) {
	fd := &fakeDialer{}
	agent, err := DialWith(fd, "10.0.0.1:9111", 2*time.Second)
	if err != nil {
		t.Fatalf("DialWith: %v", err)
	}
	defer agent.Close()

	if fd.addr != "10.0.0.1:9111" {
		t.Errorf("dialer addr = %q, want %q", fd.addr, "10.0.0.1:9111")
	}
	if fd.timeout != 2*time.Second {
		t.Errorf("dialer timeout = %v, want %v", fd.timeout, 2*time.Second)
	}
}

func TestDialWithError(t *testing.T) {
	fd := &fakeDialer{err: net.ErrClosed}
	_, err := DialWith(fd, "10.0.0.1:9111", 2*time.Second)
	if err == nil {
		t.Fatal("expected error from DialWith")
	}
}
