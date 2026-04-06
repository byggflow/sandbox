package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// FetchOptions configures an outbound HTTP request from the sandbox.
type FetchOptions struct {
	Method   string            `json:"method,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     []byte            `json:"body,omitempty"`
	Redirect string            `json:"redirect,omitempty"`
}

// FetchResult holds the response from a fetch operation.
type FetchResult struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	Body       []byte            `json:"body"`
	StatusText string            `json:"statusText"`
}

// TunnelInfo holds the result of exposing a port.
type TunnelInfo struct {
	Port     int    `json:"port"`
	HostPort int    `json:"host_port"`
	URL      string `json:"url"`
}

// ExposeOpts configures an Expose call.
type ExposeOpts struct {
	Timeout int // Seconds to wait for port readiness. Default 30.
}

// NetCategory provides network operations on a sandbox.
type NetCategory struct {
	cc          *callContext
	httpClient  *http.Client
	httpBaseURL string
	authHeaders map[string]string
	sandboxID   string
}

// Fetch makes an HTTP request from inside the sandbox.
func (n *NetCategory) Fetch(ctx context.Context, url string, opts *FetchOptions) (*FetchResult, error) {
	params := map[string]interface{}{"url": url}
	if opts != nil {
		if opts.Method != "" {
			params["method"] = opts.Method
		}
		if opts.Headers != nil {
			params["headers"] = opts.Headers
		}
		if opts.Body != nil {
			params["body"] = opts.Body
		}
		if opts.Redirect != "" {
			params["redirect"] = opts.Redirect
		}
	}
	result, err := call(ctx, n.cc, op{
		Method: "net.fetch",
		Params: params,
	})
	if err != nil {
		return nil, err
	}
	if m, ok := result.(map[string]interface{}); ok {
		fr := &FetchResult{}
		if v, ok := m["status"].(float64); ok {
			fr.Status = int(v)
		}
		if v, ok := m["statusText"].(string); ok {
			fr.StatusText = v
		}
		if v, ok := m["headers"].(map[string]interface{}); ok {
			fr.Headers = make(map[string]string, len(v))
			for k, val := range v {
				if s, ok := val.(string); ok {
					fr.Headers[k] = s
				}
			}
		}
		if v, ok := m["body"].(string); ok {
			fr.Body = []byte(v)
		}
		return fr, nil
	}
	return nil, &SandboxError{Message: "unexpected response type for net.fetch"}
}

// URL returns a path-based proxy URL for the given container port.
// No server call — the URL is constructed client-side.
func (n *NetCategory) URL(port int) string {
	return fmt.Sprintf("%s/sandboxes/%s/ports/%d", n.httpBaseURL, n.sandboxID, port)
}

// Expose allocates a host port for the given container port and waits for readiness.
func (n *NetCategory) Expose(ctx context.Context, port int, opts *ExposeOpts) (*TunnelInfo, error) {
	if n.httpClient == nil {
		return nil, fmt.Errorf("sandbox: http client not configured")
	}

	body := map[string]interface{}{}
	if opts != nil && opts.Timeout > 0 {
		body["timeout"] = opts.Timeout
	}
	bodyJSON, _ := json.Marshal(body)

	endpoint := fmt.Sprintf("%s/sandboxes/%s/ports/%d/expose", n.httpBaseURL, n.sandboxID, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("sandbox: build expose request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range n.authHeaders {
		req.Header.Set(k, v)
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sandbox: expose request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sandbox: expose failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var info TunnelInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("sandbox: decode expose response: %w", err)
	}
	return &info, nil
}

// Close releases an exposed port.
func (n *NetCategory) Close(ctx context.Context, port int) error {
	if n.httpClient == nil {
		return fmt.Errorf("sandbox: http client not configured")
	}

	endpoint := fmt.Sprintf("%s/sandboxes/%s/ports/%d/expose", n.httpBaseURL, n.sandboxID, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("sandbox: build close request: %w", err)
	}
	for k, v := range n.authHeaders {
		req.Header.Set(k, v)
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sandbox: close request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sandbox: close failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// Ports lists all exposed ports for this sandbox.
func (n *NetCategory) Ports(ctx context.Context) ([]TunnelInfo, error) {
	if n.httpClient == nil {
		return nil, fmt.Errorf("sandbox: http client not configured")
	}

	endpoint := fmt.Sprintf("%s/sandboxes/%s/ports", n.httpBaseURL, n.sandboxID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("sandbox: build ports request: %w", err)
	}
	for k, v := range n.authHeaders {
		req.Header.Set(k, v)
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sandbox: ports request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sandbox: ports failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var ports []TunnelInfo
	if err := json.NewDecoder(resp.Body).Decode(&ports); err != nil {
		return nil, fmt.Errorf("sandbox: decode ports response: %w", err)
	}
	return ports, nil
}
