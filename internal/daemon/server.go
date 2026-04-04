package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// Server manages the HTTP listeners for the daemon.
type Server struct {
	daemon     *Daemon
	httpServer *http.Server
	unixLn     net.Listener
	tcpLn      net.Listener
	wg         sync.WaitGroup
}

// NewServer creates a new HTTP server for the daemon.
func NewServer(d *Daemon) *Server {
	mux := http.NewServeMux()
	registerRoutes(mux, d)

	// Add metrics and logging middleware.
	handler := logMiddleware(d, metricsMiddleware(d.Metrics, mux))

	return &Server{
		daemon: d,
		httpServer: &http.Server{
			Handler: handler,
		},
	}
}

// Start begins listening on the configured socket and optionally TCP.
func (s *Server) Start() error {
	socketPath := s.daemon.Config.Server.Socket

	// Ensure the socket directory exists.
	socketDir := filepath.Dir(socketPath)
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		return fmt.Errorf("create socket dir %s: %w", socketDir, err)
	}

	// Remove stale socket file.
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket %s: %w", socketPath, err)
	}

	// Listen on Unix socket.
	unixLn, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", socketPath, err)
	}
	s.unixLn = unixLn

	// Set socket permissions.
	if err := os.Chmod(socketPath, 0660); err != nil {
		unixLn.Close()
		return fmt.Errorf("set socket permissions on %s: %w", socketPath, err)
	}

	s.daemon.Log.Info("listening on unix socket", "path", socketPath)

	// Serve on Unix socket.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.httpServer.Serve(unixLn); err != nil && err != http.ErrServerClosed {
			s.daemon.Log.Error("unix server error", "error", err)
		}
	}()

	// Optionally listen on TCP (with TLS if configured).
	if addr := s.daemon.Config.Server.TCP; addr != "" {
		var tcpLn net.Listener

		if s.daemon.Config.Server.TLSCert != "" {
			cert, err := tls.LoadX509KeyPair(s.daemon.Config.Server.TLSCert, s.daemon.Config.Server.TLSKey)
			if err != nil {
				return fmt.Errorf("load TLS keypair: %w", err)
			}
			tlsCfg := &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			}
			tcpLn, err = tls.Listen("tcp", addr, tlsCfg)
			if err != nil {
				return fmt.Errorf("listen tls %s: %w", addr, err)
			}
			s.daemon.Log.Info("listening on tcp with TLS", "addr", addr)
		} else {
			var err error
			tcpLn, err = net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listen tcp %s: %w", addr, err)
			}
			s.daemon.Log.Warn("listening on tcp WITHOUT TLS — use tls_cert/tls_key for encrypted connections", "addr", addr)
		}

		s.tcpLn = tcpLn

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := s.httpServer.Serve(tcpLn); err != nil && err != http.ErrServerClosed {
				s.daemon.Log.Error("tcp server error", "error", err)
			}
		}()
	}

	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.httpServer.Shutdown(ctx)
	s.wg.Wait()

	// Clean up socket file.
	socketPath := s.daemon.Config.Server.Socket
	if socketPath != "" {
		os.Remove(socketPath)
	}

	return err
}

// logMiddleware logs incoming HTTP requests.
func logMiddleware(d *Daemon, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.Log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
		)
		next.ServeHTTP(w, r)
	})
}
