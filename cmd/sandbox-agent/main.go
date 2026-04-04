package main

import (
	"fmt"
	"log"
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

	addr := fmt.Sprintf(":%s", port)
	srv := agent.NewServer(addr)

	log.Printf("sandbox-agent starting on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}
