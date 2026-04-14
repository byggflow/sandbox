package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestTunnelManager_WaitForPort(t *testing.T) {
	tm := NewTunnelManager("127.0.0.1", 0, 0, 100, slog.Default())

	// Start a TCP listener after a short delay.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	target := ln.Addr().String()

	// waitForPort should succeed immediately since the port is already listening.
	if err := tm.waitForPort(context.Background(), target, 5*time.Second); err != nil {
		t.Fatalf("waitForPort failed for listening port: %v", err)
	}
}

func TestTunnelManager_WaitForPortTimeout(t *testing.T) {
	tm := NewTunnelManager("127.0.0.1", 0, 0, 100, slog.Default())

	// Use a port that nothing is listening on.
	target := "127.0.0.1:19999"

	err := tm.waitForPort(context.Background(), target, 200*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}

func TestTunnelManager_ListenEphemeral(t *testing.T) {
	tm := NewTunnelManager("127.0.0.1", 0, 0, 100, slog.Default())

	ln, err := tm.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	if port <= 0 {
		t.Fatalf("expected positive port, got %d", port)
	}
}

func TestTunnelManager_ListenRange(t *testing.T) {
	// Use a small range.
	tm := NewTunnelManager("127.0.0.1", 18700, 18705, 100, slog.Default())

	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}()

	// Should be able to allocate ports within the range.
	for i := 0; i < 6; i++ {
		ln, err := tm.listen()
		if err != nil {
			t.Fatalf("listen %d: %v", i, err)
		}
		listeners = append(listeners, ln)
		port := ln.Addr().(*net.TCPAddr).Port
		if port < 18700 || port > 18705 {
			t.Fatalf("port %d outside range 18700-18705", port)
		}
	}

	// One more should fail — range exhausted.
	_, err := tm.listen()
	if err == nil {
		t.Fatalf("expected error when range exhausted, got nil")
	}
}

func TestTunnelManager_Relay(t *testing.T) {
	tm := NewTunnelManager("127.0.0.1", 0, 0, 100, slog.Default())

	// Start a target server that echoes data back.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()

	go func() {
		for {
			conn, err := targetLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn) // echo
			}()
		}
	}()

	host, _, _ := net.SplitHostPort(targetLn.Addr().String())
	port := targetLn.Addr().(*net.TCPAddr).Port

	// Create a mock sandbox. AgentAddr must have the host as the part before ":".
	sbx := &Sandbox{
		ID:        "sbx-test",
		AgentAddr: fmt.Sprintf("%s:9111", host),
	}

	// Expose the target port.
	tunnel, err := tm.Expose(context.Background(), sbx, port, 5*time.Second)
	if err != nil {
		t.Fatalf("expose: %v", err)
	}
	defer tm.Close(tunnel)

	// Connect to the tunnel and send data.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.HostPort))
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer conn.Close()

	testData := []byte("hello tunnel")
	if _, err := conn.Write(testData); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read echoed data.
	buf := make([]byte, len(testData))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != string(testData) {
		t.Fatalf("expected %q, got %q", string(testData), string(buf[:n]))
	}
}

func TestTunnelManager_ConnectionLimit(t *testing.T) {
	tm := NewTunnelManager("127.0.0.1", 0, 0, 2, slog.Default()) // max 2 connections

	// Start a target server.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()

	go func() {
		for {
			conn, err := targetLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				// Hold connection open.
				buf := make([]byte, 1)
				conn.Read(buf)
			}()
		}
	}()

	host, _, _ := net.SplitHostPort(targetLn.Addr().String())
	port := targetLn.Addr().(*net.TCPAddr).Port

	sbx := &Sandbox{
		ID:        "sbx-test",
		AgentAddr: fmt.Sprintf("%s:9111", host),
	}

	tunnel, err := tm.Expose(context.Background(), sbx, port, 5*time.Second)
	if err != nil {
		t.Fatalf("expose: %v", err)
	}
	defer tm.Close(tunnel)

	// Open 2 connections — should succeed.
	conn1, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.HostPort))
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer conn1.Close()

	conn2, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.HostPort))
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer conn2.Close()

	// Give the accept loop time to register connections.
	time.Sleep(100 * time.Millisecond)

	// 3rd connection should be accepted at TCP level but immediately closed by the tunnel.
	conn3, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.HostPort))
	if err != nil {
		// Connection refused is also acceptable.
		return
	}
	defer conn3.Close()

	// Try to read — should get EOF (connection closed by tunnel).
	conn3.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 1)
	_, err = conn3.Read(buf)
	if err == nil {
		t.Fatalf("expected connection to be closed, but read succeeded")
	}
}
