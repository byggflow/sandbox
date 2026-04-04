package sandbox

import (
	"context"
	"testing"
)

func TestStringAuth(t *testing.T) {
	auth := &StringAuth{Token: "sk-abc123"}
	headers, err := auth.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Bearer sk-abc123"
	if headers["Authorization"] != want {
		t.Fatalf("got %q, want %q", headers["Authorization"], want)
	}
}

func TestHeadersAuth(t *testing.T) {
	auth := &HeadersAuth{Headers: map[string]string{
		"X-API-Key": "key123",
		"X-Tenant":  "acme",
	}}
	headers, err := auth.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if headers["X-API-Key"] != "key123" {
		t.Fatalf("got %q, want %q", headers["X-API-Key"], "key123")
	}
	if headers["X-Tenant"] != "acme" {
		t.Fatalf("got %q, want %q", headers["X-Tenant"], "acme")
	}
	// Verify it returns a copy, not the original map.
	headers["X-API-Key"] = "modified"
	h2, _ := auth.Resolve(context.Background())
	if h2["X-API-Key"] != "key123" {
		t.Fatal("HeadersAuth should return a copy of headers")
	}
}

func TestProviderAuth(t *testing.T) {
	called := false
	auth := &ProviderAuth{
		Provider: func(ctx context.Context) (map[string]string, error) {
			called = true
			return map[string]string{"Authorization": "Bearer dynamic-token"}, nil
		},
	}
	headers, err := auth.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("provider was not called")
	}
	if headers["Authorization"] != "Bearer dynamic-token" {
		t.Fatalf("got %q, want %q", headers["Authorization"], "Bearer dynamic-token")
	}
}

func TestProviderAuthNilProvider(t *testing.T) {
	auth := &ProviderAuth{}
	headers, err := auth.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(headers) != 0 {
		t.Fatalf("expected empty headers, got %v", headers)
	}
}
