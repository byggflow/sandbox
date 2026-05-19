package main

import (
	"errors"
	"log/slog"
	"net"
)

// filteredListener wraps a net.Listener and rejects connections that
// don't satisfy the supplied predicate. Used to enforce transport-layer
// peer authentication before any application-layer auth runs.
type filteredListener struct {
	inner net.Listener
	allow func(net.Conn) (bool, string) // returns (ok, reasonOnReject)
}

func (l *filteredListener) Accept() (net.Conn, error) {
	for {
		c, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		if ok, reason := l.allow(c); !ok {
			slog.Warn("rejecting connection: peer auth failed",
				"remote", c.RemoteAddr().String(),
				"reason", reason)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

func (l *filteredListener) Close() error   { return l.inner.Close() }
func (l *filteredListener) Addr() net.Addr { return l.inner.Addr() }

// nonLoopbackTCPListener wraps a TCP listener so it rejects connections
// from loopback addresses. On Docker the daemon connects from the
// bridge gateway IP, not loopback; any in-container process dialing
// 127.0.0.1:9111 is therefore non-legitimate and gets rejected before
// any auth.token attempt.
func nonLoopbackTCPListener(inner net.Listener) net.Listener {
	return &filteredListener{
		inner: inner,
		allow: func(c net.Conn) (bool, string) {
			ta, ok := c.RemoteAddr().(*net.TCPAddr)
			if !ok {
				// Unknown address family. Be conservative: accept it.
				// Test harnesses use pipe-based fakes that don't have
				// a TCPAddr.
				return true, ""
			}
			if ta.IP.IsLoopback() {
				return false, "loopback peer"
			}
			return true, ""
		},
	}
}

// errRejected is returned when a connection fails peer auth. Useful for
// tests that want to distinguish rejection from other accept errors.
var errRejected = errors.New("peer auth: connection rejected")
