package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/byggflow/sandbox/internal/identity"
)

// connectionHasUpgrade checks if the Connection header value contains "upgrade",
// handling the case where the header has multiple comma-separated values.
func connectionHasUpgrade(value string) bool {
	for _, v := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(v), "upgrade") {
			return true
		}
	}
	return false
}

// lookupSandboxForPort is a helper that extracts the sandbox by {id} path param,
// checks tenant identity, and returns the sandbox or writes an error.
func (d *Daemon) lookupSandboxForPort(w http.ResponseWriter, r *http.Request) (*Sandbox, bool) {
	sbxID := r.PathValue("id")
	if sbxID == "" {
		writeError(w, http.StatusBadRequest, "sandbox id required")
		return nil, false
	}

	id := identity.Extract(r)

	sbx, ok := d.Registry.Get(sbxID)
	if !ok {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return nil, false
	}

	if !id.Empty() && !sbx.Identity.Matches(id) {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return nil, false
	}

	return sbx, true
}

// parsePort parses the {port} path parameter and validates it.
func parsePort(w http.ResponseWriter, r *http.Request) (int, bool) {
	portStr := r.PathValue("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, "invalid port number")
		return 0, false
	}
	return port, true
}

func (d *Daemon) handleExposePort(w http.ResponseWriter, r *http.Request) {
	sbx, ok := d.lookupSandboxForPort(w, r)
	if !ok {
		return
	}

	port, ok := parsePort(w, r)
	if !ok {
		return
	}

	if sbx.GetState() != StateRunning {
		writeError(w, http.StatusConflict, "sandbox is not running")
		return
	}

	// Reserve the tunnel slot under the lock to prevent TOCTOU races.
	sbx.mu.Lock()
	if sbx.Tunnels == nil {
		sbx.Tunnels = make(map[int]*Tunnel)
	}
	if _, exists := sbx.Tunnels[port]; exists {
		sbx.mu.Unlock()
		writeError(w, http.StatusConflict, "port already exposed")
		return
	}
	if len(sbx.Tunnels) >= d.Config.Limits.MaxTunnels {
		sbx.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("tunnel limit reached (%d)", d.Config.Limits.MaxTunnels))
		return
	}
	// Reserve the slot with a nil sentinel to block concurrent requests for the same port.
	sbx.Tunnels[port] = nil
	sbx.mu.Unlock()

	// Parse optional timeout from request body.
	timeout := 30 * time.Second
	if r.Body != nil {
		var body struct {
			Timeout int `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.Timeout > 0 {
			timeout = time.Duration(body.Timeout) * time.Second
			if timeout > 120*time.Second {
				timeout = 120 * time.Second
			}
		}
	}

	tunnel, err := d.Tunnels.Expose(r.Context(), sbx, port, timeout)
	if err != nil {
		// Remove the reserved slot on failure.
		sbx.mu.Lock()
		delete(sbx.Tunnels, port)
		sbx.mu.Unlock()
		writeError(w, http.StatusBadGateway, "port expose failed")
		return
	}

	sbx.mu.Lock()
	sbx.Tunnels[port] = tunnel
	sbx.mu.Unlock()

	d.Log.Info("port exposed", "sandbox", sbx.ID, "port", port, "host_port", tunnel.HostPort)

	writeJSON(w, http.StatusOK, TunnelInfo{
		Port:     port,
		HostPort: tunnel.HostPort,
		URL:      fmt.Sprintf("http://localhost:%d", tunnel.HostPort),
	})
}

func (d *Daemon) handleClosePort(w http.ResponseWriter, r *http.Request) {
	sbx, ok := d.lookupSandboxForPort(w, r)
	if !ok {
		return
	}

	port, ok := parsePort(w, r)
	if !ok {
		return
	}

	sbx.mu.Lock()
	tunnel, exists := sbx.Tunnels[port]
	if exists {
		delete(sbx.Tunnels, port)
	}
	sbx.mu.Unlock()

	if !exists {
		writeError(w, http.StatusNotFound, "port not exposed")
		return
	}

	d.Tunnels.Close(tunnel)
	d.Log.Info("port closed", "sandbox", sbx.ID, "port", port)

	w.WriteHeader(http.StatusNoContent)
}

func (d *Daemon) handleListPorts(w http.ResponseWriter, r *http.Request) {
	sbx, ok := d.lookupSandboxForPort(w, r)
	if !ok {
		return
	}

	sbx.mu.Lock()
	ports := make([]TunnelInfo, 0, len(sbx.Tunnels))
	for port, t := range sbx.Tunnels {
		ports = append(ports, TunnelInfo{
			Port:     port,
			HostPort: t.HostPort,
			URL:      fmt.Sprintf("http://localhost:%d", t.HostPort),
		})
	}
	sbx.mu.Unlock()

	writeJSON(w, http.StatusOK, ports)
}

func (d *Daemon) handlePortProxy(w http.ResponseWriter, r *http.Request) {
	sbx, ok := d.lookupSandboxForPort(w, r)
	if !ok {
		return
	}

	port, ok := parsePort(w, r)
	if !ok {
		return
	}

	// Verify the port has been explicitly exposed.
	sbx.mu.Lock()
	_, exposed := sbx.Tunnels[port]
	sbx.mu.Unlock()
	if !exposed {
		writeError(w, http.StatusForbidden, "port not exposed")
		return
	}

	containerIP := sbx.ContainerIP()
	if containerIP == "" {
		writeError(w, http.StatusBadGateway, "sandbox has no container IP")
		return
	}

	target, err := url.Parse(fmt.Sprintf("http://%s:%d", containerIP, port))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid target URL")
		return
	}

	// Check for WebSocket upgrade. The Connection header may contain multiple
	// comma-separated values per RFC 7230 (e.g., "keep-alive, upgrade").
	if connectionHasUpgrade(r.Header.Get("Connection")) &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		d.proxyWebSocket(w, r, containerIP, port)
		return
	}

	// Strip the /sandboxes/{id}/ports/{port} prefix from the request path.
	prefix := fmt.Sprintf("/sandboxes/%s/ports/%d", sbx.ID, port)
	originalPath := r.URL.Path
	r.URL.Path = strings.TrimPrefix(originalPath, prefix)
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		d.Log.Error("port proxy error", "error", err)
		writeError(w, http.StatusBadGateway, "proxy error")
	}

	// Set forwarding headers.
	r.Header.Set("X-Forwarded-For", remoteIP(r))
	r.Header.Set("X-Forwarded-Proto", "http")
	if r.TLS != nil {
		r.Header.Set("X-Forwarded-Proto", "https")
	}
	r.Header.Set("X-Forwarded-Host", r.Host)
	r.Header.Set("X-Sandbox-ID", sbx.ID)

	proxy.ServeHTTP(w, r)
}

// wsIdleTimeout is the maximum time a proxied WebSocket connection can be idle.
const wsIdleTimeout = 5 * time.Minute

// proxyWebSocket hijacks the connection and relays bytes to container_ip:port.
func (d *Daemon) proxyWebSocket(w http.ResponseWriter, r *http.Request, containerIP string, port int) {
	// Validate origin for TCP connections to prevent cross-site WebSocket hijacking.
	if !isUnixSocket(r) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			// Only allow same-origin requests.
			host := r.Host
			if !strings.HasSuffix(origin, "://"+host) {
				writeError(w, http.StatusForbidden, "cross-origin WebSocket request blocked")
				return
			}
		}
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, "WebSocket hijack not supported")
		return
	}

	target := net.JoinHostPort(containerIP, fmt.Sprintf("%d", port))
	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		d.Log.Error("upstream WebSocket dial failed", "error", err)
		writeError(w, http.StatusBadGateway, "upstream connection failed")
		return
	}

	client, buf, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}

	// Forward the original HTTP upgrade request to the upstream.
	if err := r.Write(upstream); err != nil {
		client.Close()
		upstream.Close()
		return
	}

	// Flush any buffered data from the hijacked connection.
	if buf.Reader.Buffered() > 0 {
		buffered := make([]byte, buf.Reader.Buffered())
		if _, err := buf.Read(buffered); err != nil {
			client.Close()
			upstream.Close()
			return
		}
		if _, err := upstream.Write(buffered); err != nil {
			client.Close()
			upstream.Close()
			return
		}
	}

	// Set initial idle deadline on both sides.
	client.SetDeadline(time.Now().Add(wsIdleTimeout))
	upstream.SetDeadline(time.Now().Add(wsIdleTimeout))

	// Bidirectional relay with idle timeout refresh.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyWithIdleTimeout(upstream, client, wsIdleTimeout)
		upstream.Close()
	}()
	go func() {
		defer wg.Done()
		copyWithIdleTimeout(client, upstream, wsIdleTimeout)
		client.Close()
	}()
	wg.Wait()
}

// copyWithIdleTimeout copies data from src to dst, refreshing the deadline
// on both connections after each successful read/write.
func copyWithIdleTimeout(dst, src net.Conn, timeout time.Duration) {
	buf := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			deadline := time.Now().Add(timeout)
			src.SetDeadline(deadline)
			dst.SetDeadline(deadline)
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}
