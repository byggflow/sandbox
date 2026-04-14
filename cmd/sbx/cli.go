package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	sandbox "github.com/byggflow/sandbox/sdk/go"
)

var version = "0.0.0"

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: sbx <command> [options]

Commands:
  build       Build a template from a Dockerfile
  create      Create a new sandbox
  ls          List sandboxes
  rm          Remove a sandbox
  stats       Show resource stats for a sandbox
  exec        Execute a command in a sandbox
  attach      Attach to a sandbox with a PTY
  fs          Filesystem operations (read, write, ls, upload, download)
  tpl         Template operations (save, ls, rm)
  pool        Pool operations (status, resize, flush)
  health      Check daemon health
  update      Update sbx to the latest version
  version     Print version information

Environment:
  SANDBOXD_ENDPOINT  Daemon endpoint (default: http://localhost:7522)
  SBX_AUTH           Authentication token`)
}

// endpoint returns the daemon endpoint from env or default.
func endpoint() string {
	if e := os.Getenv("SANDBOXD_ENDPOINT"); e != "" {
		return e
	}
	return "http://localhost:7522"
}

// authFromEnv returns an Auth from SBX_AUTH env var, or nil.
func authFromEnv() sandbox.Auth {
	if tok := os.Getenv("SBX_AUTH"); tok != "" {
		return &sandbox.StringAuth{Token: tok}
	}
	return nil
}

// httpClient returns an HTTP client and base URL for the endpoint.
func httpClient() (*http.Client, string) {
	ep := endpoint()
	if strings.HasPrefix(ep, "unix://") {
		sockPath := strings.TrimPrefix(ep, "unix://")
		return &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
				},
			},
		}, "http://localhost"
	}
	return http.DefaultClient, ep
}

// doRequest performs an HTTP request with auth headers.
func doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	client, baseURL := httpClient()
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := os.Getenv("SBX_AUTH"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return client.Do(req)
}

// connectSDK connects to a sandbox via the SDK.
func connectSDK(ctx context.Context, id string) (*sandbox.Sandbox, error) {
	return sandbox.Connect(ctx, id, &sandbox.ConnectOptions{
		Endpoint: endpoint(),
		Auth:     authFromEnv(),
	})
}

// labelFlag is a repeatable flag that collects key=value pairs.
type labelFlag []string

func (f *labelFlag) String() string { return strings.Join(*f, ",") }
func (f *labelFlag) Set(value string) error {
	if !strings.Contains(value, "=") {
		return fmt.Errorf("label must be in key=value format")
	}
	*f = append(*f, value)
	return nil
}

// formatBytes formats a byte count into a human-readable string.
func formatBytes(b uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// formatDuration formats seconds into a human-readable duration string.
func formatDuration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
