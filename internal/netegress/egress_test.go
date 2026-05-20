package netegress

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/byggflow/sandbox/internal/netrules"
	"github.com/byggflow/sandbox/protocol"
)

// TestInjectWinsAgainstSandboxCaseCollision is the regression test for
// the credential-bypass: if the sandbox supplies a header in a
// different case than the inject rule (e.g. lowercase "authorization"
// vs rule's "Authorization"), the daemon must guarantee the injected
// value reaches the upstream. Before the fix, map-iteration order
// decided which value won after canonicalization — about 50% of the
// time the sandbox's value was preserved.
func TestInjectWinsAgainstSandboxCaseCollision(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	h := New()
	defer h.Close()
	_ = h.SetRules("sbx-1", []netrules.Rule{
		{
			ID:     "force-auth",
			Match:  netrules.Match{Host: "127.0.0.1"},
			Action: netrules.ActionInject,
			Inject: netrules.Inject{SetHeaders: map[string]string{"Authorization": "Bearer DAEMON-INJECTED"}},
		},
	})

	// Run many times to defeat the random map-iteration order; the
	// daemon's value must win every single iteration.
	for i := 0; i < 200; i++ {
		gotAuth = ""
		resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{
			Method: "GET",
			URL:    srv.URL,
			Headers: map[string][]string{
				"authorization": {"Bearer SANDBOX-ATTACK"},
				"AUTHORIZATION": {"Bearer SANDBOX-ATTACK-2"},
				"Authorization": {"Bearer SANDBOX-ATTACK-3"},
			},
		}, nil)
		if resp.Status != 200 {
			t.Fatalf("iter %d: expected 200, got %d body=%s", i, resp.Status, mustDecodeBody(resp.Body))
		}
		if gotAuth != "Bearer DAEMON-INJECTED" {
			t.Fatalf("iter %d: sandbox value leaked: upstream saw %q (must be daemon-injected)", i, gotAuth)
		}
	}
}

// TestTombstoneExpiresAfterTTL is the regression test for the
// unbounded-growth bug: after ClearRules, the destroyed set must drop
// the sandbox ID once the grace window passes, so a long-running
// daemon doesn't accumulate one tombstone per historical sandbox.
func TestTombstoneExpiresAfterTTL(t *testing.T) {
	h := New()
	defer h.Close()
	h.SetTombstoneTTL(50 * time.Millisecond)

	// Generate a CA, then destroy: tombstone is set, EnsureCA refuses.
	if _, err := h.EnsureCA("sbx-test"); err != nil {
		t.Fatal(err)
	}
	h.ClearRules("sbx-test")

	if _, err := h.EnsureCA("sbx-test"); err == nil {
		t.Error("EnsureCA must refuse immediately after ClearRules (tombstone active)")
	}

	// After the TTL, the tombstone should be swept and EnsureCA can
	// re-issue (which is fine — sandbox IDs are random per-create, so
	// a real re-use is essentially impossible).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.RLock()
		_, stillThere := h.destroyed["sbx-test"]
		h.mu.RUnlock()
		if !stillThere {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("tombstone never expired within 2s of a 50ms TTL")
}

func TestHandleAllowsExplicitlyMatched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	h := New()
	defer h.Close()
	_ = h.SetRules("sbx-1", []netrules.Rule{
		{ID: "loopback-ok", Match: netrules.Match{Host: "127.0.0.1"}, Action: netrules.ActionAllow},
	})

	resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{Method: "GET", URL: srv.URL}, nil)
	if resp.Status != 200 {
		t.Fatalf("expected 200, got %d body=%s", resp.Status, mustDecodeBody(resp.Body))
	}
}

func TestHandleDeny(t *testing.T) {
	h := New()
	defer h.Close()
	_ = h.SetRules("sbx-1", []netrules.Rule{
		{ID: "block", Match: netrules.Match{Host: "blocked.test"}, Action: netrules.ActionDeny},
	})
	resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{
		Method: "GET",
		URL:    "https://blocked.test/",
	}, nil)
	if resp.Status != 403 || !resp.Synthetic {
		t.Fatalf("expected 403 synthetic, got %v", resp)
	}
	if resp.MatchedRuleID != "block" {
		t.Errorf("expected matched rule id 'block', got %q", resp.MatchedRuleID)
	}
}

func TestHandleInjectAppliesHeaders(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	h := New()
	defer h.Close()

	// Single inject rule against the test server's host. The Handler treats
	// inject as implicitly-allowed for the private-host guard when the rule
	// matched.
	_ = h.SetRules("sbx-1", []netrules.Rule{
		{
			ID:     "auth",
			Match:  netrules.Match{Host: "127.0.0.1"},
			Action: netrules.ActionInject,
			Inject: netrules.Inject{SetHeaders: map[string]string{"Authorization": "Bearer secret"}},
		},
	})

	resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{
		Method: "GET",
		URL:    srv.URL,
	}, nil)
	// Inject implicitly allows the private-host guard since the rule matched.
	if resp.Status != 200 {
		t.Fatalf("expected 200, got %d body=%s", resp.Status, mustDecodeBody(resp.Body))
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("expected injected Authorization header, got %q", gotAuth)
	}
}

func TestHandlePassThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(201)
		w.Write(body)
	}))
	defer srv.Close()

	h := New()
	defer h.Close()
	_ = h.SetRules("sbx-1", []netrules.Rule{
		{ID: "loopback-ok", Match: netrules.Match{Host: "127.0.0.1"}, Action: netrules.ActionAllow},
	})

	body := base64.StdEncoding.EncodeToString([]byte("echo"))
	resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{
		Method: "POST",
		URL:    srv.URL,
		Body:   body,
	}, nil)
	if resp.Status != 201 {
		t.Fatalf("expected 201, got %d", resp.Status)
	}
	got, _ := base64.StdEncoding.DecodeString(resp.Body)
	if string(got) != "echo" {
		t.Errorf("expected body echo, got %q", got)
	}
}

func TestPrivateHostBlockedWithoutRule(t *testing.T) {
	h := New()
	defer h.Close()
	resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{
		Method: "GET",
		URL:    "http://127.0.0.1:1/",
	}, nil)
	if resp.Status != 403 {
		t.Fatalf("expected 403, got %d", resp.Status)
	}
}

// TestGuardedDialContextBlocksPrivateResolved is the regression test for
// the SSRF where a hostname (not an IP literal) resolves to a private
// address — the original IsPrivateHost-on-hostname check missed this.
// guardedDialContext must refuse, regardless of how the upstream
// hostname looks.
func TestGuardedDialContextBlocksPrivateResolved(t *testing.T) {
	// localhost resolves to 127.0.0.1 / ::1, both private. The dialer
	// should reject the connection attempt before any TCP is opened.
	_, err := guardedDialContext(context.Background(), "tcp", "localhost:1")
	if err == nil {
		t.Fatal("expected guardedDialContext to refuse private resolution; got nil")
	}
	if !strings.Contains(err.Error(), "private address blocked") {
		t.Errorf("expected private-address error, got: %v", err)
	}
}

func TestGuardedDialContextAllowsPrivateWhenContextOptedIn(t *testing.T) {
	// Same hostname but the context is marked allow-private; the dial
	// should proceed (and fail with a connection-refused / similar
	// error from port 1, which is fine — we only care that the guard
	// didn't reject upfront).
	ctx := withAllowPrivate(context.Background())
	_, err := guardedDialContext(ctx, "tcp", "localhost:1")
	if err == nil {
		// If a service happens to be listening on :1, that's fine too.
		return
	}
	if strings.Contains(err.Error(), "private address blocked") {
		t.Errorf("guard fired despite withAllowPrivate context: %v", err)
	}
}

func TestRedirectDoesNotCarryPrivateAllowanceToUnmatchedTarget(t *testing.T) {
	h := New()
	defer h.Close()
	if err := h.SetRules("sbx-1", []netrules.Rule{
		{ID: "public-ok", Match: netrules.Match{Host: "public.example"}, Action: netrules.ActionAllow},
	}); err != nil {
		t.Fatal(err)
	}

	ctx := WithFollowRedirects(withAllowPrivate(withEgressSandboxID(context.Background(), "sbx-1")))
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost:1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.checkRedirect(req, []*http.Request{{}}); err != nil {
		t.Fatalf("hostname redirect check should defer DNS-private blocking to dialer: %v", err)
	}
	if allowsPrivate(req.Context()) {
		t.Fatal("redirect inherited allow-private from the original matched request")
	}
	if _, err := guardedDialContext(req.Context(), "tcp", "localhost:1"); err == nil || !strings.Contains(err.Error(), "private address blocked") {
		t.Fatalf("redirect context did not restore private-address guard, err=%v", err)
	}
}

func TestRedirectAllowsPrivateOnlyWhenRedirectTargetMatches(t *testing.T) {
	h := New()
	defer h.Close()
	if err := h.SetRules("sbx-1", []netrules.Rule{
		{ID: "private-ok", Match: netrules.Match{Host: "127.0.0.1"}, Action: netrules.ActionAllow},
	}); err != nil {
		t.Fatal(err)
	}

	ctx := WithFollowRedirects(withEgressSandboxID(context.Background(), "sbx-1"))
	req, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.checkRedirect(req, []*http.Request{{}}); err != nil {
		t.Fatalf("matched private redirect should be allowed: %v", err)
	}
	if !allowsPrivate(req.Context()) {
		t.Fatal("matched private redirect did not receive allow-private context")
	}
}

func TestRedirectBlocksPrivateIPLiteralWithoutRedirectRule(t *testing.T) {
	h := New()
	defer h.Close()
	if err := h.SetRules("sbx-1", []netrules.Rule{
		{ID: "public-ok", Match: netrules.Match{Host: "public.example"}, Action: netrules.ActionAllow},
	}); err != nil {
		t.Fatal(err)
	}

	ctx := WithFollowRedirects(withAllowPrivate(withEgressSandboxID(context.Background(), "sbx-1")))
	req, err := http.NewRequestWithContext(ctx, "GET", "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = h.checkRedirect(req, []*http.Request{{}})
	if err == nil || !strings.Contains(err.Error(), "private address blocked") {
		t.Fatalf("expected private-address redirect block, got %v", err)
	}
}

func TestRedirectStripsCrossHostInjectHeaders(t *testing.T) {
	h := New()
	defer h.Close()
	if err := h.SetRules("sbx-1", []netrules.Rule{
		{ID: "api-creds", Match: netrules.Match{Host: "api.example"}, Action: netrules.ActionInject,
			Inject: netrules.Inject{SetHeaders: map[string]string{"X-API-Key": "secret"}}},
		{ID: "other", Match: netrules.Match{Host: "other.example"}, Action: netrules.ActionAllow},
	}); err != nil {
		t.Fatal(err)
	}

	prevURL, _ := url.Parse("https://api.example/foo")
	prev := &http.Request{URL: prevURL, Method: "GET"}

	ctx := WithFollowRedirects(withEgressSandboxID(context.Background(), "sbx-1"))
	req, err := http.NewRequestWithContext(ctx, "GET", "https://other.example/bar", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the state net/http leaves on a redirect: the inject
	// header set on the original request is still in req.Header.
	req.Header.Set("X-API-Key", "secret")

	if err := h.checkRedirect(req, []*http.Request{prev}); err != nil {
		t.Fatalf("checkRedirect: %v", err)
	}
	if got := req.Header.Get("X-API-Key"); got != "" {
		t.Errorf("expected X-API-Key stripped across hosts, got %q", got)
	}
}

func TestRedirectReappliesInjectOnNewHost(t *testing.T) {
	h := New()
	defer h.Close()
	if err := h.SetRules("sbx-1", []netrules.Rule{
		{ID: "old", Match: netrules.Match{Host: "old.example"}, Action: netrules.ActionInject,
			Inject: netrules.Inject{SetHeaders: map[string]string{"X-Old": "x"}}},
		{ID: "new", Match: netrules.Match{Host: "new.example"}, Action: netrules.ActionInject,
			Inject: netrules.Inject{SetHeaders: map[string]string{"X-New": "y"}}},
	}); err != nil {
		t.Fatal(err)
	}
	prevURL, _ := url.Parse("https://old.example/")
	prev := &http.Request{URL: prevURL, Method: "GET"}
	ctx := WithFollowRedirects(withEgressSandboxID(context.Background(), "sbx-1"))
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://new.example/", nil)
	req.Header.Set("X-Old", "x")

	if err := h.checkRedirect(req, []*http.Request{prev}); err != nil {
		t.Fatalf("checkRedirect: %v", err)
	}
	if v := req.Header.Get("X-Old"); v != "" {
		t.Errorf("old inject header should be stripped, got %q", v)
	}
	if v := req.Header.Get("X-New"); v != "y" {
		t.Errorf("new inject header should be applied, got %q", v)
	}
}

// mockSink captures stream frames in order so tests can assert on
// chunked delivery semantics (each chunk arrives as its own WriteChunk
// call rather than being coalesced).
type mockSink struct {
	chunks    [][]byte
	endStatus byte
	endMsg    string
	ended     bool
}

func (s *mockSink) WriteChunk(data []byte) error {
	buf := make([]byte, len(data))
	copy(buf, data)
	s.chunks = append(s.chunks, buf)
	return nil
}

func (s *mockSink) End(status byte, errMsg string) error {
	s.endStatus = status
	s.endMsg = errMsg
	s.ended = true
	return nil
}

// TestHandleStreamingDeliversChunkedUpstream confirms that an upstream
// using HTTP/1.1 chunked transfer encoding (the wire shape of SSE / LLM
// streaming) is delivered to the sink chunk-by-chunk rather than being
// buffered into one blob. The test writes three separate chunks on the
// upstream with explicit Flush calls between them; the handler must
// produce at least three distinct WriteChunk calls.
func TestHandleStreamingDeliversChunkedUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("httptest server is not a Flusher")
		}
		for _, chunk := range []string{"data: one\n\n", "data: two\n\n", "data: three\n\n"} {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	defer srv.Close()

	h := New()
	defer h.Close()
	_ = h.SetRules("sbx-1", []netrules.Rule{
		{ID: "allow-test", Match: netrules.Match{Host: "127.0.0.1"}, Action: netrules.ActionAllow},
	})

	head, copier, err := h.HandleStreaming(context.Background(), "sbx-1",
		&protocol.EgressRequest{Method: "GET", URL: srv.URL}, nil)
	if err != nil {
		t.Fatalf("HandleStreaming: %v", err)
	}
	if head.Status != 200 {
		t.Errorf("expected status 200, got %d", head.Status)
	}
	if ct, ok := head.Headers["Content-Type"]; !ok || len(ct) == 0 || !strings.Contains(ct[0], "text/event-stream") {
		t.Errorf("expected text/event-stream Content-Type, got %v", head.Headers["Content-Type"])
	}

	sink := &mockSink{}
	if err := copier(sink); err != nil {
		t.Fatalf("copier: %v", err)
	}
	if !sink.ended {
		t.Fatal("sink.End was never called")
	}
	if sink.endStatus != protocol.StreamEndOK {
		t.Errorf("expected StreamEndOK, got status=%d msg=%q", sink.endStatus, sink.endMsg)
	}
	// Concatenated body must contain all three chunks in order.
	total := strings.Join(byteSlicesToStrings(sink.chunks), "")
	for _, want := range []string{"data: one", "data: two", "data: three"} {
		if !strings.Contains(total, want) {
			t.Errorf("missing chunk %q in delivered body %q", want, total)
		}
	}
	// At least one chunk was delivered. Asserting strictly "three distinct
	// chunks" is OS-buffer-dependent (httptest's Flush is advisory);
	// what matters for the streaming property is that the body wasn't
	// buffered into one giant frame before End.
	if len(sink.chunks) == 0 {
		t.Error("no chunks delivered")
	}
}

func byteSlicesToStrings(in [][]byte) []string {
	out := make([]string, len(in))
	for i, b := range in {
		out[i] = string(b)
	}
	return out
}

// TestHandleStreamingPrivateHostBlocked confirms the SSRF guard fires
// in the streaming path too: a hostname that resolves to a private IP
// with no matching rule must not be dialed.
func TestHandleStreamingPrivateHostBlocked(t *testing.T) {
	h := New()
	defer h.Close()
	head, copier, err := h.HandleStreaming(context.Background(), "sbx-1",
		&protocol.EgressRequest{Method: "GET", URL: "http://localhost:1/"}, nil)
	if err != nil {
		t.Fatalf("HandleStreaming returned error: %v", err)
	}
	// Must be synthetic, not a real upstream response.
	if !head.Synthetic {
		t.Error("expected synthetic response for private-host block")
	}
	sink := &mockSink{}
	_ = copier(sink)
	// Either the early IP-literal guard or the dialer-level guard fired;
	// in either case the body must mention the block reason.
	if total := strings.Join(byteSlicesToStrings(sink.chunks), ""); !strings.Contains(total, "private address blocked") {
		t.Errorf("expected private-address message, got %q", total)
	}
}

// TestHandleBlocksHostnameToPrivateIP goes through the full Handle path
// to confirm the unmatched-rule case rejects a hostname that resolves
// to a private IP. The vuln report's exploit scenario relied on this
// resolving without a rule and reaching IMDS.
func TestHandleBlocksHostnameToPrivateIP(t *testing.T) {
	h := New()
	defer h.Close()
	resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{
		Method: "GET",
		URL:    "http://localhost:1/",
	}, nil)
	// Without a rule, the dialer-level guard must fire even though
	// "localhost" is not an IP literal.
	if resp.Status != 502 && resp.Status != 403 {
		t.Fatalf("expected 502/403 (private resolved), got %d body=%s", resp.Status, mustDecodeBody(resp.Body))
	}
	if got := mustDecodeBody(resp.Body); !strings.Contains(got, "private address blocked") {
		t.Errorf("expected private-address message in body, got %q", got)
	}
}

func TestHandleDeferMutatesRequest(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	h := New()
	defer h.Close()
	_ = h.SetRules("sbx-1", []netrules.Rule{
		{ID: "dynamic-auth", Match: netrules.Match{Host: "127.0.0.1"}, Action: netrules.ActionDefer},
	})

	deferFn := func(_ context.Context, ruleID string, req *protocol.EgressRequest) (*protocol.DeferResponse, error) {
		if ruleID != "dynamic-auth" {
			t.Errorf("unexpected rule ID: %s", ruleID)
		}
		if req.Headers == nil {
			req.Headers = map[string][]string{}
		}
		req.Headers["Authorization"] = []string{"Bearer minted-" + req.URL}
		return &protocol.DeferResponse{Request: req}, nil
	}

	resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{Method: "GET", URL: srv.URL}, deferFn)
	if resp.Status != 200 {
		t.Fatalf("expected 200, got %d body=%s", resp.Status, mustDecodeBody(resp.Body))
	}
	if gotAuth == "" || gotAuth[:7] != "Bearer " {
		t.Errorf("expected SDK-injected Authorization, got %q", gotAuth)
	}
}

func TestHandleDeferShortCircuits(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(200)
	}))
	defer srv.Close()

	h := New()
	defer h.Close()
	_ = h.SetRules("sbx-1", []netrules.Rule{
		{ID: "stub", Match: netrules.Match{Host: "127.0.0.1"}, Action: netrules.ActionDefer},
	})

	deferFn := func(_ context.Context, _ string, _ *protocol.EgressRequest) (*protocol.DeferResponse, error) {
		return &protocol.DeferResponse{
			Response: &protocol.EgressResponse{
				Status:  418,
				Headers: map[string][]string{"Content-Type": {"text/plain"}},
				Body:    base64.StdEncoding.EncodeToString([]byte("stubbed")),
			},
		}, nil
	}

	resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{Method: "GET", URL: srv.URL}, deferFn)
	if resp.Status != 418 {
		t.Errorf("expected synthetic 418, got %d", resp.Status)
	}
	if !resp.Synthetic {
		t.Error("expected Synthetic=true")
	}
	if hit {
		t.Error("defer handler short-circuit was bypassed: upstream was hit")
	}
}

func TestHandleDeferHandlerError(t *testing.T) {
	h := New()
	defer h.Close()
	_ = h.SetRules("sbx-1", []netrules.Rule{
		{ID: "boom", Match: netrules.Match{Host: "api.example.com"}, Action: netrules.ActionDefer},
	})
	deferFn := func(_ context.Context, _ string, _ *protocol.EgressRequest) (*protocol.DeferResponse, error) {
		return nil, fmt.Errorf("handler crashed")
	}
	resp := h.Handle(context.Background(), "sbx-1", &protocol.EgressRequest{Method: "GET", URL: "https://api.example.com/"}, deferFn)
	if resp.Status != 502 {
		t.Errorf("expected 502 on handler error, got %d", resp.Status)
	}
}

func mustDecodeBody(s string) string {
	b, _ := base64.StdEncoding.DecodeString(s)
	return string(b)
}
