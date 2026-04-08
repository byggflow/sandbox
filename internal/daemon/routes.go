package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/byggflow/sandbox/internal/identity"
)

// registerRoutes sets up all HTTP handlers on the mux.
func registerRoutes(mux *http.ServeMux, d *Daemon) {
	// Tenant-authenticated routes: require valid signature in multi-tenant mode.
	tenant := d.tenantAuth

	mux.HandleFunc("POST /sandboxes", tenant(limitBody(d.handleCreateSandbox)))
	mux.HandleFunc("GET /sandboxes", tenant(d.handleListSandboxes))
	mux.HandleFunc("DELETE /sandboxes/{id}", tenant(d.handleDestroySandbox))
	mux.HandleFunc("GET /sandboxes/{id}/stats", tenant(d.handleSandboxStats))
	mux.HandleFunc("GET /sandboxes/{id}/ws", tenant(d.handleSandboxWS))

	mux.HandleFunc("POST /templates", tenant(limitBody(d.handleCreateTemplate)))
	mux.HandleFunc("GET /templates", tenant(d.handleListTemplates))
	mux.HandleFunc("GET /templates/{id}", tenant(d.handleGetTemplate))
	mux.HandleFunc("DELETE /templates/{id}", tenant(d.handleDeleteTemplate))

	mux.HandleFunc("GET /profiles", tenant(d.handleListProfiles))

	// Port tunneling routes.
	mux.HandleFunc("POST /sandboxes/{id}/ports/{port}/expose", tenant(d.handleExposePort))
	mux.HandleFunc("DELETE /sandboxes/{id}/ports/{port}/expose", tenant(d.handleClosePort))
	mux.HandleFunc("GET /sandboxes/{id}/ports", tenant(d.handleListPorts))
	mux.HandleFunc("/sandboxes/{id}/ports/{port}/", tenant(d.handlePortProxy))
	mux.HandleFunc("/sandboxes/{id}/ports/{port}", tenant(d.handlePortProxy))

	// Admin routes — pool management. Not tenant-scoped; restricted to Unix socket.
	mux.HandleFunc("GET /pools", d.socketOnly(d.handlePoolStatus))
	mux.HandleFunc("PUT /pools/{profile}", d.socketOnly(limitBody(d.handlePoolResize)))
	mux.HandleFunc("POST /pools/{profile}/flush", d.socketOnly(d.handlePoolFlush))

	mux.HandleFunc("GET /events", tenant(d.handleEvents))
	mux.HandleFunc("GET /events/history", tenant(d.handleEventsHistory))

	// Operational endpoints — no auth required.
	mux.HandleFunc("GET /health", d.handleHealth)
	mux.HandleFunc("GET /metrics", d.handleMetrics)
}

// maxRequestBodySize is the maximum size of a request body (1MB).
const maxRequestBodySize = 1 << 20

// socketOnly restricts a handler to Unix socket connections only.
// Requests arriving over TCP are rejected with 403.
func (d *Daemon) socketOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isUnixSocket(r) {
			writeError(w, http.StatusForbidden, "admin routes are only accessible via Unix socket")
			return
		}
		next(w, r)
	}
}

// isUnixSocket returns true if the request arrived over a Unix domain socket.
// TCP connections have RemoteAddr in "ip:port" format; Unix socket connections
// use "@" or an empty string.
func isUnixSocket(r *http.Request) bool {
	addr := r.RemoteAddr
	return addr == "" || addr == "@" || addr[0] == '@'
}

// limitBody wraps the request body with a size limit to prevent OOM from oversized payloads.
func limitBody(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		}
		next(w, r)
	}
}

// tenantAuth is middleware that verifies Ed25519 signatures on identity headers
// when multi-tenant mode is enabled. In single-user mode it's a no-op passthrough.
func (d *Daemon) tenantAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v := d.Verifier(); v != nil {
			// Rate limit failed auth attempts per IP.
			ip := remoteIP(r)
			if d.AuthLimit.isBlocked(ip) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "too many authentication failures")
				return
			}

			if err := v.Verify(r); err != nil {
				d.AuthLimit.record(ip)
				d.Log.Warn("signature verification failed", "ip", ip, "error", err)
				writeError(w, http.StatusUnauthorized, "authentication failed")
				return
			}
			// In multi-tenant mode, identity is required.
			if r.Header.Get(identity.Header) == "" {
				d.AuthLimit.record(ip)
				writeError(w, http.StatusUnauthorized, "X-Sandbox-Identity header required")
				return
			}
		}
		next(w, r)
	}
}

// Label limits.
const (
	maxLabels     = 64
	maxLabelKey   = 128
	maxLabelValue = 256
)

func validateLabels(labels map[string]string) error {
	if len(labels) > maxLabels {
		return fmt.Errorf("too many labels: %d exceeds maximum of %d", len(labels), maxLabels)
	}
	for k, v := range labels {
		if len(k) > maxLabelKey {
			return fmt.Errorf("label key too long: %d exceeds maximum of %d", len(k), maxLabelKey)
		}
		if len(v) > maxLabelValue {
			return fmt.Errorf("label value too long: %d exceeds maximum of %d", len(v), maxLabelValue)
		}
	}
	return nil
}

// isSessionCloseError returns true for expected close errors (EOF, connection closed).
func isSessionCloseError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "closed") || strings.Contains(msg, "EOF")
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to write json response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}
