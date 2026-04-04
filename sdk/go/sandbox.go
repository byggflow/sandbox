// Package sandbox provides a Go client SDK for sandboxd.
//
// Entry points are Create and Connect, which return a Sandbox with
// category accessors for filesystem, process, environment, network,
// and template operations.
//
//	sbx, err := sandbox.Create(ctx, &sandbox.Options{Image: "python:3.12"})
//	if err != nil { log.Fatal(err) }
//	defer sbx.Close()
//
//	result, err := sbx.Process().Exec(ctx, "echo hello", nil)
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Sandbox represents a connected sandbox instance.
type Sandbox struct {
	// ID is the unique identifier for this sandbox (e.g., "sbx-a1b2c3").
	ID string

	cc        *callContext
	transport RpcTransport
}

// FS returns the filesystem category for this sandbox.
func (s *Sandbox) FS() *FSCategory {
	return &FSCategory{cc: s.cc}
}

// Process returns the process execution category for this sandbox.
func (s *Sandbox) Process() *ProcessCategory {
	return &ProcessCategory{cc: s.cc}
}

// Env returns the environment variable category for this sandbox.
func (s *Sandbox) Env() *EnvCategory {
	return &EnvCategory{cc: s.cc}
}

// Net returns the network category for this sandbox.
func (s *Sandbox) Net() *NetCategory {
	return &NetCategory{cc: s.cc}
}

// Template returns the template category for this sandbox.
func (s *Sandbox) Template() *TemplateCategory {
	return &TemplateCategory{cc: s.cc}
}

// Close shuts down the connection to this sandbox.
func (s *Sandbox) Close() error {
	if s.transport != nil {
		return s.transport.Close()
	}
	return nil
}

// httpClientForEndpoint returns an HTTP client configured for the endpoint.
// For unix:// endpoints, it dials the Unix socket. For http/https, it uses default transport.
func httpClientForEndpoint(endpoint string) (*http.Client, string) {
	if strings.HasPrefix(endpoint, "unix://") {
		sockPath := strings.TrimPrefix(endpoint, "unix://")
		return &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
				},
			},
		}, "http://localhost"
	}
	return http.DefaultClient, endpoint
}

// resolveAuthHeaders resolves authentication headers from an Auth provider.
func resolveAuthHeaders(ctx context.Context, auth Auth) (map[string]string, error) {
	if auth == nil {
		return map[string]string{}, nil
	}
	return auth.Resolve(ctx)
}

// httpToWS converts an HTTP URL to a WebSocket URL.
func httpToWS(httpURL string) string {
	if strings.HasPrefix(httpURL, "https://") {
		return "wss://" + strings.TrimPrefix(httpURL, "https://")
	}
	if strings.HasPrefix(httpURL, "http://") {
		return "ws://" + strings.TrimPrefix(httpURL, "http://")
	}
	return httpURL
}

// Create provisions a new sandbox and returns a connected handle.
func Create(ctx context.Context, opts *Options) (*Sandbox, error) {
	if ctx == nil {
		return nil, fmt.Errorf("sandbox: context required")
	}

	endpoint := DefaultEndpoint
	if opts != nil && opts.Endpoint != "" {
		endpoint = opts.Endpoint
	}

	var auth Auth
	if opts != nil {
		auth = opts.Auth
	}
	headers, err := resolveAuthHeaders(ctx, auth)
	if err != nil {
		return nil, fmt.Errorf("sandbox: auth resolve: %w", err)
	}

	// Build the create request body.
	body := map[string]interface{}{}
	if opts != nil {
		if opts.Image != "" {
			body["image"] = opts.Image
		}
		if opts.Template != "" {
			body["template"] = opts.Template
		}
		if opts.Memory != "" {
			body["memory"] = opts.Memory
		}
		if opts.CPU > 0 {
			body["cpu"] = opts.CPU
		}
		if opts.TTL > 0 {
			body["ttl"] = opts.TTL
		}
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("sandbox: marshal create body: %w", err)
	}

	client, baseURL := httpClientForEndpoint(endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/sandboxes", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("sandbox: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, &ConnectionError{SandboxError: SandboxError{
			Message: fmt.Sprintf("create request: %v", err),
		}}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sandbox: create failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var info struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("sandbox: decode create response: %w", err)
	}

	// Connect WebSocket to the sandbox.
	wsURL := buildWSURL(endpoint, info.ID)
	wsTransport, err := dialWS(ctx, wsURL, headers)
	if err != nil {
		return nil, err
	}

	var transport RpcTransport = wsTransport

	// Negotiate E2E encryption if requested.
	if opts != nil && opts.Encrypted {
		session, err := negotiateE2E(ctx, wsTransport)
		if err != nil {
			wsTransport.Close()
			return nil, fmt.Errorf("sandbox: e2e negotiation: %w", err)
		}
		transport = &encryptedTransport{inner: wsTransport, session: session}
	}

	sbx := &Sandbox{
		ID:        info.ID,
		transport: transport,
		cc: &callContext{
			transport: transport,
			sandboxID: info.ID,
		},
	}
	return sbx, nil
}

// buildWSURL constructs the WebSocket URL for a sandbox.
func buildWSURL(endpoint, id string) string {
	if strings.HasPrefix(endpoint, "unix://") {
		sockPath := strings.TrimPrefix(endpoint, "unix://")
		return "ws+unix://" + sockPath + ":/sandboxes/" + id + "/ws"
	}
	return httpToWS(endpoint) + "/sandboxes/" + id + "/ws"
}

// Connect attaches to an existing sandbox by ID.
func Connect(ctx context.Context, id string, opts *ConnectOptions) (*Sandbox, error) {
	if ctx == nil {
		return nil, fmt.Errorf("sandbox: context required")
	}
	if id == "" {
		return nil, fmt.Errorf("sandbox: id required")
	}

	endpoint := DefaultEndpoint
	if opts != nil && opts.Endpoint != "" {
		endpoint = opts.Endpoint
	}

	var auth Auth
	if opts != nil {
		auth = opts.Auth
	}
	headers, err := resolveAuthHeaders(ctx, auth)
	if err != nil {
		return nil, fmt.Errorf("sandbox: auth resolve: %w", err)
	}

	// Connect WebSocket to the sandbox.
	wsURL := buildWSURL(endpoint, id)
	wsTransport, err := dialWS(ctx, wsURL, headers)
	if err != nil {
		return nil, err
	}

	var transport RpcTransport = wsTransport

	// Negotiate E2E encryption if requested.
	if opts != nil && opts.Encrypted {
		session, err := negotiateE2E(ctx, wsTransport)
		if err != nil {
			wsTransport.Close()
			return nil, fmt.Errorf("sandbox: e2e negotiation: %w", err)
		}
		transport = &encryptedTransport{inner: wsTransport, session: session}
	}

	sbx := &Sandbox{
		ID:        id,
		transport: transport,
		cc: &callContext{
			transport: transport,
			sandboxID: id,
		},
	}
	return sbx, nil
}
