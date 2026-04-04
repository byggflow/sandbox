package sandbox

import "testing"

func TestDefaultEndpoint(t *testing.T) {
	want := "unix:///var/run/sandboxd/sandboxd.sock"
	if DefaultEndpoint != want {
		t.Fatalf("got %q, want %q", DefaultEndpoint, want)
	}
}

func TestResolveEndpoint(t *testing.T) {
	if got := resolveEndpoint(""); got != DefaultEndpoint {
		t.Fatalf("empty string should resolve to default, got %q", got)
	}
	custom := "tcp://localhost:7522"
	if got := resolveEndpoint(custom); got != custom {
		t.Fatalf("got %q, want %q", got, custom)
	}
}
