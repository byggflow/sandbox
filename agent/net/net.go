// Package net is the agent-side fallback for OpNetFetch when the
// daemon cannot serve the request itself. It exists only to support
// E2E-encrypted sessions where the SDK encrypts params with a key the
// daemon doesn't have — the daemon's clientLocalMethodsFor refuses to
// claim those, so the frame forwards to the agent which decrypts via
// its E2E session and dispatches here.
//
// Encrypted sessions therefore do NOT get rule application (the
// daemon never sees the URL or headers). Network middleware and
// encrypted=true are mutually exclusive at the SDK boundary; this
// handler exists to keep sbx.net.fetch working for encrypted
// sandboxes that don't install rules.
//
// For plaintext sessions, OpNetFetch is claimed by the daemon and
// flows through internal/netegress/egress.go — that path applies
// rules, follows redirects (legacy net.fetch behavior), and uses the
// SSRF-hardened dialer with a single private-IP guard.
package net

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/byggflow/sandbox/protocol"
)

// FetchParams is the params for net.fetch.
type FetchParams struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// FetchResult is the result of net.fetch.
type FetchResult struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

const maxResponseBody = 10 * 1024 * 1024

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if err := validateFetchURL(req.URL); err != nil {
			return err
		}
		return nil
	},
}

// validateFetchURL blocks requests to private/internal networks and
// non-HTTP schemes. Uses the consolidated protocol.IsPrivateHost so
// the agent and daemon never drift on what counts as "private".
func validateFetchURL(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported scheme %q: only http and https are allowed", u.Scheme)
	}
	if protocol.IsPrivateHost(u.Hostname()) {
		return fmt.Errorf("requests to private/internal addresses are not allowed")
	}
	return nil
}

// Fetch performs an HTTP request and returns the response. Used only
// from the encrypted-session passthrough; the daemon handles all
// plaintext fetches through netegress.
func Fetch(raw json.RawMessage) (interface{}, error) {
	var p FetchParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	parsed, err := url.Parse(p.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if err := validateFetchURL(parsed); err != nil {
		return nil, err
	}

	method := p.Method
	if method == "" {
		method = "GET"
	}

	var bodyReader io.Reader
	if p.Body != "" {
		bodyReader = strings.NewReader(p.Body)
	}

	req, err := http.NewRequest(method, p.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	headers := make(map[string]string)
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	return &FetchResult{
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    string(body),
	}, nil
}
