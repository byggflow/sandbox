package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Tunnel represents an active host-port → container-port mapping.
type Tunnel struct {
	SandboxID   string
	Port        int    // container port
	HostPort    int    // allocated host port
	ContainerIP string
	listener    net.Listener
	cancel      context.CancelFunc
	activeConns atomic.Int64
	maxConns    int
}

// TunnelInfo is the JSON-serializable tunnel information returned by the API.
type TunnelInfo struct {
	Port     int    `json:"port"`
	HostPort int    `json:"host_port"`
	URL      string `json:"url"`
}

// TunnelManager manages host-port tunnels for sandboxes.
type TunnelManager struct {
	portMin  int
	portMax  int
	maxConns int
	log      *slog.Logger

	mu        sync.Mutex
	allocated map[int]bool // track ports we've allocated from the range
}

// NewTunnelManager creates a tunnel manager.
func NewTunnelManager(portMin, portMax, maxConns int, log *slog.Logger) *TunnelManager {
	return &TunnelManager{
		portMin:   portMin,
		portMax:   portMax,
		maxConns:  maxConns,
		log:       log,
		allocated: make(map[int]bool),
	}
}

// Expose creates a tunnel from a host port to container_ip:port.
// It probes the container port for readiness with exponential backoff up to timeout.
func (tm *TunnelManager) Expose(ctx context.Context, sbx *Sandbox, containerPort int, timeout time.Duration) (*Tunnel, error) {
	containerIP := sbx.ContainerIP()
	if containerIP == "" {
		return nil, fmt.Errorf("sandbox has no container IP")
	}

	target := net.JoinHostPort(containerIP, fmt.Sprintf("%d", containerPort))

	// Probe for port readiness with exponential backoff.
	if err := tm.waitForPort(ctx, target, timeout); err != nil {
		return nil, fmt.Errorf("port %d not ready: %w", containerPort, err)
	}

	// Allocate a host port.
	ln, err := tm.listen()
	if err != nil {
		return nil, fmt.Errorf("allocate host port: %w", err)
	}

	hostPort := ln.Addr().(*net.TCPAddr).Port

	tunnelCtx, cancel := context.WithCancel(context.Background())

	t := &Tunnel{
		SandboxID:   sbx.ID,
		Port:        containerPort,
		HostPort:    hostPort,
		ContainerIP: containerIP,
		listener:    ln,
		cancel:      cancel,
		maxConns:    tm.maxConns,
	}

	// Accept loop.
	go tm.acceptLoop(tunnelCtx, t, target)

	return t, nil
}

// Close shuts down a tunnel, draining active connections.
func (tm *TunnelManager) Close(t *Tunnel) {
	t.cancel()
	t.listener.Close()

	tm.mu.Lock()
	delete(tm.allocated, t.HostPort)
	tm.mu.Unlock()
}

// waitForPort probes target with exponential backoff until it accepts a TCP connection.
func (tm *TunnelManager) waitForPort(ctx context.Context, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	backoff := 10 * time.Millisecond
	const maxBackoff = 500 * time.Millisecond

	for {
		dialCtx, dialCancel := context.WithTimeout(ctx, 1*time.Second)
		conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", target)
		dialCancel()
		if err == nil {
			conn.Close()
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %v waiting for %s", timeout, target)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// listen allocates a host port listener.
func (tm *TunnelManager) listen() (net.Listener, error) {
	// OS-assigned ephemeral port.
	if tm.portMin == 0 && tm.portMax == 0 {
		return net.Listen("tcp", "127.0.0.1:0")
	}

	// Sequential scan within configured range.
	tm.mu.Lock()
	defer tm.mu.Unlock()

	for port := tm.portMin; port <= tm.portMax; port++ {
		if tm.allocated[port] {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue // port in use by another process
		}
		tm.allocated[port] = true
		return ln, nil
	}
	return nil, fmt.Errorf("no available ports in range %d-%d", tm.portMin, tm.portMax)
}

// acceptLoop accepts TCP connections and relays them to the target.
func (tm *TunnelManager) acceptLoop(ctx context.Context, t *Tunnel, target string) {
	var wg sync.WaitGroup
	defer wg.Wait()

	go func() {
		<-ctx.Done()
		t.listener.Close()
	}()

	for {
		conn, err := t.listener.Accept()
		if err != nil {
			return // listener closed
		}

		// Enforce connection limit.
		if t.maxConns > 0 && t.activeConns.Load() >= int64(t.maxConns) {
			conn.Close()
			continue
		}

		t.activeConns.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer t.activeConns.Add(-1)
			tm.relay(ctx, conn, target)
		}()
	}
}

// relay connects to the target and copies bytes bidirectionally.
func (tm *TunnelManager) relay(ctx context.Context, client net.Conn, target string) {
	defer client.Close()

	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()

	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		<-relayCtx.Done()
		client.Close()
		upstream.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(upstream, client)
		cancel()
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, upstream)
		cancel()
	}()
	wg.Wait()
}
