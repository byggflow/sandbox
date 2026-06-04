package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// requireDaemonReachableUpstream skips the calling test unless the
// caller has explicitly attested that the daemon can dial back to the
// test process. The network-middleware integration tests use
// httptest.NewServer (which binds to host 127.0.0.1) as a controlled
// upstream and have the sandbox issue a fetch to it; the daemon then
// dials that upstream from its own network namespace. In containerized
// CI the daemon runs in Docker, so 127.0.0.1 inside the daemon is the
// daemon's loopback — not the test process — and the dial fails with
// "connection refused" (surfaces as 502).
//
// Set SANDBOXD_INTEGRATION_NETWORK=1 when running locally with a
// daemon that shares a network namespace with the test process (e.g.
// sandboxd binary on the host). CI should leave this unset until we
// add proper containerized test fixtures.
func requireDaemonReachableUpstream(t *testing.T) {
	t.Helper()
	if os.Getenv("SANDBOXD_INTEGRATION_NETWORK") == "" {
		t.Skip("requires daemon and test process to share a network namespace; set SANDBOXD_INTEGRATION_NETWORK=1 to enable")
	}
}

// TestNetworkInjectHeader verifies that an inject rule installed via
// net.rules.set causes the daemon to add the configured header to outbound
// requests routed through sbx.net.fetch — without the header value ever
// reaching the sandbox process.
//
// Setup: spin up a local HTTP server that records what Authorization header
// it received, install an inject rule for that host, then call net.fetch
// from inside the sandbox via the WebSocket.
func TestNetworkInjectHeader(t *testing.T) {
	requireDaemonReachableUpstream(t)
	ep := endpoint(t)

	// Capture what the upstream sees.
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	info := createSandbox(t, ep)
	t.Cleanup(func() { destroySandbox(t, ep, info.ID) })

	ws := connectWS(t, ep, info.ID)
	defer ws.Close(websocket.StatusNormalClosure, "done")

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	if i := strings.Index(upstreamHost, ":"); i >= 0 {
		upstreamHost = upstreamHost[:i]
	}

	// Install the rule: inject Authorization on requests to the test upstream.
	resp := sendRPC(t, ws, 1, "net.rules.set", map[string]interface{}{
		"rules": []map[string]interface{}{
			{
				"id":     "inject-auth",
				"match":  map[string]interface{}{"host": upstreamHost},
				"action": "inject",
				"inject": map[string]interface{}{
					"set_headers": map[string]string{"Authorization": "Bearer test-secret"},
				},
			},
		},
	})
	if resp.Error != nil {
		t.Fatalf("net.rules.set: %s", resp.Error.Message)
	}

	// From inside the sandbox: call net.fetch against the upstream.
	resp = sendRPC(t, ws, 2, "net.fetch", map[string]interface{}{
		"url":    upstream.URL,
		"method": "GET",
	})
	if resp.Error != nil {
		t.Fatalf("net.fetch: %s", resp.Error.Message)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal fetch result: %v", err)
	}
	if status, _ := result["status"].(float64); int(status) != 200 {
		t.Fatalf("expected status 200, got %v", result["status"])
	}

	if gotAuth != "Bearer test-secret" {
		t.Errorf("upstream did not see injected header: got %q", gotAuth)
	}
}

// Note: defer-to-sdk end-to-end coverage uses the actual SDK from
// sdk/typescript/src/__integration__/network.integration.test.ts where
// the WebSocket frame pump is built into the transport. The Go raw-WS
// harness here uses single-shot sendRPC and can't easily handle the
// concurrent daemon-initiated Request while waiting on a Response.
// Unit-level coverage for the wire format lives in
// internal/netegress/egress_test.go (TestHandleDeferMutatesRequest).

// TestNetworkDeny verifies that a deny rule short-circuits a request with
// a synthetic 403 without dialing the upstream at all.
func TestNetworkDeny(t *testing.T) {
	ep := endpoint(t)

	// Upstream that records whether it was hit.
	hit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	info := createSandbox(t, ep)
	t.Cleanup(func() { destroySandbox(t, ep, info.ID) })

	ws := connectWS(t, ep, info.ID)
	defer ws.Close(websocket.StatusNormalClosure, "done")

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	if i := strings.Index(upstreamHost, ":"); i >= 0 {
		upstreamHost = upstreamHost[:i]
	}

	resp := sendRPC(t, ws, 1, "net.rules.set", map[string]interface{}{
		"rules": []map[string]interface{}{
			{
				"id":     "block",
				"match":  map[string]interface{}{"host": upstreamHost},
				"action": "deny",
			},
		},
	})
	if resp.Error != nil {
		t.Fatalf("net.rules.set: %s", resp.Error.Message)
	}

	resp = sendRPC(t, ws, 2, "net.fetch", map[string]interface{}{
		"url":    upstream.URL,
		"method": "GET",
	})
	if resp.Error != nil {
		t.Fatalf("net.fetch: %s", resp.Error.Message)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal fetch result: %v", err)
	}
	if status, _ := result["status"].(float64); int(status) != 403 {
		t.Fatalf("expected 403 from deny rule, got %v", result["status"])
	}
	if hit {
		t.Errorf("deny rule was bypassed: upstream was hit")
	}
}
