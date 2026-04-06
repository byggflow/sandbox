package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/byggflow/sandbox/agent"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.SetOutput(os.Stderr)

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
			log.Fatalf("vsock listen: %v", err)
		}
		log.Printf("sandbox-agent starting on vsock port %s", port)
		if err := srv.Serve(ln); err != nil {
			log.Fatalf("fatal: %v", err)
		}
	default:
		addr := fmt.Sprintf(":%s", port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("tcp listen: %v", err)
		}
		log.Printf("sandbox-agent starting on tcp %s", addr)
		if err := srv.Serve(ln); err != nil {
			log.Fatalf("fatal: %v", err)
		}
	}
}
