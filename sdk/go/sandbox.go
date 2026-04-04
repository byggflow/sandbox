// Package sandbox provides a Go client SDK for sandboxd.
//
// Entry points are Create and Connect, which return a Sandbox with
// category accessors for filesystem, process, environment, network,
// and template operations.
//
//	sbx, err := sandbox.Create(ctx, &sandbox.Options{Profile: "python"})
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

// SandboxStats contains resource usage statistics for a sandbox.
type SandboxStats struct {
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryUsageBytes uint64  `json:"memory_usage_bytes"`
	MemoryLimitBytes uint64  `json:"memory_limit_bytes"`
	MemoryPercent    float64 `json:"memory_percent"`
	NetworkRxBytes   uint64  `json:"network_rx_bytes"`
	NetworkTxBytes   uint64  `json:"network_tx_bytes"`
	PIDs             uint64  `json:"pids"`
	UptimeSeconds    float64 `json:"uptime_seconds"`
}

// Sandbox represents a connected sandbox instance.
type Sandbox struct {
	// ID is the unique identifier for this sandbox (e.g., "sbx-a1b2c3").
	ID string

	cc        *callContext
	transport RpcTransport

	// HTTP fields for REST endpoints (e.g., stats).
	httpClient  *http.Client
	httpBaseURL string
	authHeaders map[string]string
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

// Stats returns resource usage statistics for this sandbox.
// This calls the REST endpoint GET /sandboxes/{id}/stats.
func (s *Sandbox) Stats(ctx context.Context) (*SandboxStats, error) {
	if s.httpClient == nil {
		return nil, fmt.Errorf("sandbox: http client not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.httpBaseURL+"/sandboxes/"+s.ID+"/stats", nil)
	if err != nil {
		return nil, fmt.Errorf("sandbox: build stats request: %w", err)
	}
	for k, v := range s.authHeaders {
		req.Header.Set(k, v)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sandbox: stats request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sandbox: stats failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var stats SandboxStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("sandbox: decode stats response: %w", err)
	}
	return &stats, nil
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
// If the auth implements RequestSigner, it uses method and path for per-request signing.
func resolveAuthHeaders(ctx context.Context, auth Auth, method, path string) (map[string]string, error) {
	if auth == nil {
		return map[string]string{}, nil
	}
	if signer, ok := auth.(RequestSigner); ok {
		return signer.ResolveForRequest(ctx, method, path)
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
	headers, err := resolveAuthHeaders(ctx, auth, http.MethodPost, "/sandboxes")
	if err != nil {
		return nil, fmt.Errorf("sandbox: auth resolve: %w", err)
	}

	// Build the create request body.
	body := map[string]interface{}{}
	if opts != nil {
		if opts.Profile != "" {
			body["profile"] = opts.Profile
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
		if len(opts.Labels) > 0 {
			body["labels"] = opts.Labels
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
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			retryAfter := 60
			if v := resp.Header.Get("Retry-After"); v != "" {
				fmt.Sscanf(v, "%d", &retryAfter)
			}
			return nil, &CapacityError{
				SandboxError: SandboxError{Message: string(respBody)},
				RetryAfter:   retryAfter,
			}
		case http.StatusServiceUnavailable:
			retryAfter := 2
			if v := resp.Header.Get("Retry-After"); v != "" {
				fmt.Sscanf(v, "%d", &retryAfter)
			}
			return nil, &CapacityError{
				SandboxError: SandboxError{Message: string(respBody)},
				RetryAfter:   retryAfter,
			}
		default:
			return nil, fmt.Errorf("sandbox: create failed (status %d): %s", resp.StatusCode, string(respBody))
		}
	}

	var info struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("sandbox: decode create response: %w", err)
	}

	// Re-resolve auth for the WebSocket request if using per-request signing.
	wsHeaders := headers
	if _, ok := auth.(RequestSigner); ok {
		wsHeaders, err = resolveAuthHeaders(ctx, auth, http.MethodGet, "/sandboxes/"+info.ID+"/ws")
		if err != nil {
			return nil, fmt.Errorf("sandbox: auth resolve for ws: %w", err)
		}
	}

	// Connect WebSocket to the sandbox.
	wsURL := buildWSURL(endpoint, info.ID)
	wsTransport, err := dialWS(ctx, wsURL, wsHeaders)
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
		httpClient:  client,
		httpBaseURL: baseURL,
		authHeaders: headers,
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
	headers, err := resolveAuthHeaders(ctx, auth, http.MethodGet, "/sandboxes/"+id+"/ws")
	if err != nil {
		return nil, fmt.Errorf("sandbox: auth resolve: %w", err)
	}

	// Prepare HTTP client for REST endpoints.
	httpClient, httpBaseURL := httpClientForEndpoint(endpoint)

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
		httpClient:  httpClient,
		httpBaseURL: httpBaseURL,
		authHeaders: headers,
	}
	return sbx, nil
}
