package main

import (
	"net"
	"sync"
	"testing"
	"time"
)

func TestNonLoopbackTCPListenerRejectsLoopback(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	ln := nonLoopbackTCPListener(inner)

	// Accept in a goroutine. The first connection (loopback) should be
	// dropped silently; the Accept call should NOT return until a
	// non-loopback connection arrives. To simulate that, fake a
	// connection from a non-loopback address using a Listen on the
	// container's external interface — skipped in this unit test.
	// Instead: dial loopback, then assert Accept times out.

	var (
		gotConn net.Conn
		gotErr  error
		wg      sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Set a short deadline by closing the listener after the dial.
		gotConn, gotErr = ln.Accept()
	}()

	c, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	// Give the filter time to reject.
	time.Sleep(50 * time.Millisecond)
	_ = c.Close()

	// Now close the listener so Accept returns.
	inner.Close()
	wg.Wait()

	if gotConn != nil {
		t.Errorf("loopback connection was not rejected: %v", gotConn.RemoteAddr())
		_ = gotConn.Close()
	}
	if gotErr == nil {
		t.Error("expected accept error after listener close")
	}
}

func TestNonLoopbackTCPListenerAcceptsNonTCPAddr(t *testing.T) {
	// The filter is conservative: addresses that aren't *net.TCPAddr
	// are accepted (test harnesses use net.Pipe which has no real
	// address). Verify that path.
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	ln := &filteredListener{
		inner: &oneShotListener{conn: a},
		allow: nonLoopbackTCPListener(nil).(*filteredListener).allow,
	}
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("expected non-nil conn for pipe peer")
	}
}

// oneShotListener returns conn once then returns net.ErrClosed.
type oneShotListener struct {
	conn net.Conn
	done bool
}

func (l *oneShotListener) Accept() (net.Conn, error) {
	if l.done {
		return nil, net.ErrClosed
	}
	l.done = true
	return l.conn, nil
}
func (l *oneShotListener) Close() error   { return nil }
func (l *oneShotListener) Addr() net.Addr { return &net.TCPAddr{} }
