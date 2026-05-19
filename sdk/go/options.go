package sandbox

import "os"

// DefaultSocketPath is the Unix socket path checked during auto-discovery.
const DefaultSocketPath = "/var/run/sandboxd/sandboxd.sock"

// DefaultTCPEndpoint is the TCP fallback used when the socket isn't reachable.
const DefaultTCPEndpoint = "http://localhost:7522"

// DefaultEndpoint is the default sandboxd endpoint as a string. Kept as a
// stable constant; prefer ResolveDefaultEndpoint for runtime discovery.
const DefaultEndpoint = "unix://" + DefaultSocketPath

// Options configures sandbox creation.
type Options struct {
	// Endpoint is the daemon address. When empty, ResolveDefaultEndpoint is used.
	Endpoint string
	// Auth provides credentials for the connection.
	Auth Auth
	// Profile selects a pre-configured base image profile (e.g., "default", "python").
	Profile string
	// Template is the template ID to create from.
	Template string
	// Memory is the memory limit (e.g., "512m").
	Memory string
	// CPU is the CPU limit (e.g., 1.0).
	CPU float64
	// TTL is the time-to-live in seconds.
	TTL int
	// Labels is a set of user-defined key-value metadata for the sandbox.
	Labels map[string]string
	// Encrypted enables end-to-end encryption.
	Encrypted bool
	// Network installs egress middleware on the daemon at create time. Rules
	// run on the daemon, so any credentials injected via SetHeaders never
	// enter the sandbox. See NetCategory.Intercept for the runtime API.
	Network *NetworkConfig
}

// ConnectOptions configures connecting to an existing sandbox.
type ConnectOptions struct {
	// Endpoint is the daemon address. When empty, ResolveDefaultEndpoint is used.
	Endpoint string
	// Auth provides credentials for the connection.
	Auth Auth
	// Encrypted enables end-to-end encryption.
	Encrypted bool
	// Retry enables automatic retry on transient failures.
	Retry bool
}

// ResolveDefaultEndpoint returns the daemon endpoint that should be used when
// the caller doesn't pass one explicitly. The order is:
//
//  1. SANDBOXD_ENDPOINT environment variable, if set.
//  2. Unix socket at /var/run/sandboxd/sandboxd.sock, if it exists.
//  3. http://localhost:7522 (TCP fallback).
func ResolveDefaultEndpoint() string {
	if v := os.Getenv("SANDBOXD_ENDPOINT"); v != "" {
		return v
	}
	if _, err := os.Stat(DefaultSocketPath); err == nil {
		return "unix://" + DefaultSocketPath
	}
	return DefaultTCPEndpoint
}

// defaultAuth returns an Auth from the SBX_AUTH environment variable, or nil.
func defaultAuth() Auth {
	if tok := os.Getenv("SBX_AUTH"); tok != "" {
		return &StringAuth{Token: tok}
	}
	return nil
}

func resolveEndpoint(endpoint string) string {
	if endpoint == "" {
		return ResolveDefaultEndpoint()
	}
	return endpoint
}
