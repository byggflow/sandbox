package sandbox

import (
	"os"
	"testing"
)

func TestDefaultEndpoint(t *testing.T) {
	want := "unix:///var/run/sandboxd/sandboxd.sock"
	if DefaultEndpoint != want {
		t.Fatalf("got %q, want %q", DefaultEndpoint, want)
	}
}

func TestResolveEndpointExplicit(t *testing.T) {
	custom := "tcp://localhost:7522"
	if got := resolveEndpoint(custom); got != custom {
		t.Fatalf("got %q, want %q", got, custom)
	}
}

func TestResolveDefaultEndpointEnvVar(t *testing.T) {
	t.Setenv("SANDBOXD_ENDPOINT", "http://example.com:9000")
	if got := ResolveDefaultEndpoint(); got != "http://example.com:9000" {
		t.Fatalf("SANDBOXD_ENDPOINT should take precedence, got %q", got)
	}
}

func TestResolveDefaultEndpointFallback(t *testing.T) {
	// Make sure the env var is unset so the OS-level probe runs.
	t.Setenv("SANDBOXD_ENDPOINT", "")
	os.Unsetenv("SANDBOXD_ENDPOINT")

	got := ResolveDefaultEndpoint()
	// Either the socket exists (returning unix://...) or we fall back to TCP.
	if _, err := os.Stat(DefaultSocketPath); err == nil {
		if got != "unix://"+DefaultSocketPath {
			t.Fatalf("socket exists but got %q, want unix://...", got)
		}
	} else {
		if got != DefaultTCPEndpoint {
			t.Fatalf("no socket, want %q, got %q", DefaultTCPEndpoint, got)
		}
	}
}
