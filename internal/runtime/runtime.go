package runtime

import (
	"context"
	"net"
	"time"
)

// Instance represents a running sandbox unit (container or microVM).
type Instance struct {
	ID        string // Container ID or VM ID.
	AgentAddr string // "ip:port" for Docker, "vsock:CID:port" for Firecracker.
}

// Stats holds resource usage metrics for a sandbox instance.
type Stats struct {
	CPUPercent    float64
	MemoryUsage   uint64
	MemoryLimit   uint64
	MemoryPercent float64
	NetRxBytes    uint64
	NetTxBytes    uint64
	PIDs          uint64
}

// CreateOpts holds parameters for creating a new sandbox instance.
type CreateOpts struct {
	Image     string
	Memory    int64   // bytes
	CPU       float64 // cores
	Storage   string  // tmpfs size (e.g. "500m", "1g")
	AuthToken string
	Labels    map[string]string
	Profile   string
}

// Runtime abstracts the sandbox execution backend (Docker, Firecracker, etc.).
type Runtime interface {
	// Name returns the runtime identifier (e.g. "docker", "firecracker").
	Name() string

	// Init performs one-time setup such as creating networks or validating
	// paths. Called once at daemon start.
	Init(ctx context.Context) error

	// Create creates and starts a new sandbox instance. It must wait for the
	// guest agent to become reachable before returning.
	Create(ctx context.Context, opts CreateOpts) (*Instance, error)

	// Destroy stops and removes a sandbox instance.
	Destroy(ctx context.Context, instanceID string) error

	// Stats returns resource usage for a sandbox instance.
	Stats(ctx context.Context, instanceID string) (*Stats, error)

	// DialAgent returns a connection to the agent inside the instance.
	DialAgent(ctx context.Context, instanceID string, timeout time.Duration) (net.Conn, error)

	// Close releases any resources held by the runtime.
	Close() error
}
