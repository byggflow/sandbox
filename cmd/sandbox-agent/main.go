package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/byggflow/sandbox/agent"
)

func main() {
	port := os.Getenv("SANDBOX_AGENT_PORT")
	if port == "" {
		port = "9111"
	}

	transport := os.Getenv("SANDBOX_TRANSPORT")
	if transport == "" {
		// Auto-detect: use vsock if /dev/vsock exists.
		if _, err := os.Stat("/dev/vsock"); err == nil {
			transport = "vsock"
		} else {
			transport = "tcp"
		}
	}

	srv := agent.NewServer(fmt.Sprintf(":%s", port))

	switch transport {
	case "vsock":
		ln, err := listenVsock(port)
		if err != nil {
			slog.Error("vsock listen failed", "error", err)
			os.Exit(1)
		}
		slog.Info("sandbox-agent starting", "transport", "vsock", "port", port)
		if err := srv.Serve(ln); err != nil {
			slog.Error("fatal error", "error", err)
			os.Exit(1)
		}
	default:
		addr := fmt.Sprintf(":%s", port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			slog.Error("tcp listen failed", "error", err)
			os.Exit(1)
		}
		slog.Info("sandbox-agent starting", "transport", "tcp", "addr", addr)
		if err := srv.Serve(ln); err != nil {
			slog.Error("fatal error", "error", err)
			os.Exit(1)
		}
	}
}
