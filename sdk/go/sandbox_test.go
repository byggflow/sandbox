package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestCreateRequiresContext(t *testing.T) {
	//nolint:staticcheck // intentionally passing nil context for test
	_, err := Create(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil context")
	}
	if !strings.Contains(err.Error(), "context required") {
		t.Fatalf("expected context-required error, got: %v", err)
	}
}

func TestConnectRequiresID(t *testing.T) {
	_, err := Connect(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error for empty id")
	}
	if !strings.Contains(err.Error(), "id required") {
		t.Fatalf("expected id-required error, got: %v", err)
	}
}

func TestConnectRequiresContext(t *testing.T) {
	//nolint:staticcheck // intentionally passing nil context for test
	_, err := Connect(nil, "sbx-test", nil)
	if err == nil {
		t.Fatal("expected error for nil context")
	}
	if !strings.Contains(err.Error(), "context required") {
		t.Fatalf("expected context-required error, got: %v", err)
	}
}

func TestCreateFailsOnUnreachableEndpoint(t *testing.T) {
	_, err := Create(context.Background(), &Options{
		Endpoint: "http://127.0.0.1:19999",
	})
	if err == nil {
		t.Fatal("expected error from Create with unreachable endpoint")
	}
}

func TestConnectFailsOnUnreachableEndpoint(t *testing.T) {
	_, err := Connect(context.Background(), "sbx-test123", &ConnectOptions{
		Endpoint: "http://127.0.0.1:19999",
	})
	if err == nil {
		t.Fatal("expected error from Connect with unreachable endpoint")
	}
}

func TestSandboxCategories(t *testing.T) {
	transport := &stubTransport{}
	sbx := &Sandbox{
		ID:        "sbx-test",
		transport: transport,
		cc: &callContext{
			transport: transport,
			sandboxID: "sbx-test",
		},
	}

	if sbx.FS() == nil {
		t.Fatal("FS() should not return nil")
	}
	if sbx.Process() == nil {
		t.Fatal("Process() should not return nil")
	}
	if sbx.Env() == nil {
		t.Fatal("Env() should not return nil")
	}
	if sbx.Net() == nil {
		t.Fatal("Net() should not return nil")
	}
	if sbx.Template() == nil {
		t.Fatal("Template() should not return nil")
	}
}

func TestSandboxClose(t *testing.T) {
	transport := &stubTransport{}
	sbx := &Sandbox{
		ID:        "sbx-test",
		transport: transport,
		cc: &callContext{
			transport: transport,
			sandboxID: "sbx-test",
		},
	}
	if err := sbx.Close(); err != nil {
		t.Fatalf("unexpected error from Close: %v", err)
	}
}

func TestSandboxCloseNilTransport(t *testing.T) {
	sbx := &Sandbox{ID: "sbx-test"}
	if err := sbx.Close(); err != nil {
		t.Fatalf("unexpected error from Close with nil transport: %v", err)
	}
}

func TestBuildWSURL(t *testing.T) {
	tests := []struct {
		endpoint string
		id       string
		want     string
	}{
		{"http://localhost:7522", "sbx-abc", "ws://localhost:7522/sandboxes/sbx-abc/ws"},
		{"https://sandbox.example.com", "sbx-123", "wss://sandbox.example.com/sandboxes/sbx-123/ws"},
		{"unix:///var/run/sandboxd/sandboxd.sock", "sbx-xyz", "ws+unix:///var/run/sandboxd/sandboxd.sock:/sandboxes/sbx-xyz/ws"},
	}
	for _, tt := range tests {
		got := buildWSURL(tt.endpoint, tt.id)
		if got != tt.want {
			t.Errorf("buildWSURL(%q, %q) = %q, want %q", tt.endpoint, tt.id, got, tt.want)
		}
	}
}

func TestHttpToWS(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://localhost:7522", "ws://localhost:7522"},
		{"https://example.com", "wss://example.com"},
		{"ws://already-ws", "ws://already-ws"},
	}
	for _, tt := range tests {
		got := httpToWS(tt.input)
		if got != tt.want {
			t.Errorf("httpToWS(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseUnixURL(t *testing.T) {
	tests := []struct {
		input    string
		wantSock string
		wantPath string
	}{
		{"unix:///var/run/sandboxd.sock:/sandboxes/id/ws", "/var/run/sandboxd.sock", "/sandboxes/id/ws"},
		{"ws+unix:///var/run/sandboxd.sock:/sandboxes/id/ws", "/var/run/sandboxd.sock", "/sandboxes/id/ws"},
		{"unix:///var/run/sandboxd.sock", "/var/run/sandboxd.sock", "/"},
	}
	for _, tt := range tests {
		sock, path := parseUnixURL(tt.input)
		if sock != tt.wantSock || path != tt.wantPath {
			t.Errorf("parseUnixURL(%q) = (%q, %q), want (%q, %q)", tt.input, sock, path, tt.wantSock, tt.wantPath)
		}
	}
}
