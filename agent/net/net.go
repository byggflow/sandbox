package net

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
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

// maxResponseBody is the maximum response body size (10 MB).
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

// validateFetchURL blocks requests to private/internal networks and non-HTTP schemes.
func validateFetchURL(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported scheme %q: only http and https are allowed", u.Scheme)
	}

	host := u.Hostname()

	ip := net.ParseIP(host)
	if ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("requests to private/internal addresses are not allowed")
		}
	}

	return nil
}

func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		network string
	}{
		{"10.0.0.0/8"},
		{"172.16.0.0/12"},
		{"192.168.0.0/16"},
		{"127.0.0.0/8"},
		{"169.254.0.0/16"}, // link-local, includes cloud metadata
		{"::1/128"},
		{"fc00::/7"},
		{"fe80::/10"},
	}
	for _, r := range privateRanges {
		_, cidr, _ := net.ParseCIDR(r.network)
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// Fetch performs an HTTP request and returns the response.
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
		return nil, fmt.Errorf("create request: %w", err)
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
		return nil, fmt.Errorf("read response body: %w", err)
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
