package sandbox

// DefaultEndpoint is the default Unix socket path for sandboxd.
const DefaultEndpoint = "unix:///var/run/sandboxd/sandboxd.sock"

// Options configures sandbox creation.
type Options struct {
	// Endpoint is the daemon address. Defaults to DefaultEndpoint.
	Endpoint string
	// Auth provides credentials for the connection.
	Auth Auth
	// Image is the container image to use.
	Image string
	// Template is the template ID to create from.
	Template string
	// Memory is the memory limit (e.g., "512m").
	Memory string
	// CPU is the CPU limit (e.g., 1.0).
	CPU float64
	// TTL is the time-to-live in seconds.
	TTL int
	// Encrypted enables end-to-end encryption.
	Encrypted bool
}

// ConnectOptions configures connecting to an existing sandbox.
type ConnectOptions struct {
	// Endpoint is the daemon address. Defaults to DefaultEndpoint.
	Endpoint string
	// Auth provides credentials for the connection.
	Auth Auth
	// Encrypted enables end-to-end encryption.
	Encrypted bool
	// Retry enables automatic retry on transient failures.
	Retry bool
}

func resolveEndpoint(endpoint string) string {
	if endpoint == "" {
		return DefaultEndpoint
	}
	return endpoint
}
