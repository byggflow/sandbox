// Package netegress implements the daemon-side egress handler: applies
// per-sandbox rules to an EgressRequest forwarded by the agent's in-sandbox
// proxy, then dials the upstream with a pooled HTTP client and returns the
// response.
//
// Rules and pooled connections live entirely on the daemon side. The
// sandbox process never observes injected credentials or the upstream
// connection state.
package netegress

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/byggflow/sandbox/internal/netrules"
	"github.com/byggflow/sandbox/internal/sandboxca"
	"github.com/byggflow/sandbox/protocol"
)

// Handler dials upstream egress targets on behalf of agents, applying the
// rule set registered for each sandbox.
//
// bySandbox and caBySbx are unbounded by themselves; their size is capped
// indirectly by the daemon's MaxSandboxes limit, and each entry is
// removed via ClearRules in the sandbox's OnDestroy callback. Adding
// per-sandbox state? Drop it in ClearRules so it gets the same lifecycle.
//
// destroyed is a tombstone set: a sandbox ID is added on ClearRules and
// blocks any subsequent EnsureCA so a racing in-flight CONNECT can't
// lazily resurrect the CA after teardown.
type Handler struct {
	mu        sync.RWMutex
	bySandbox map[string]*netrules.Compiled
	caBySbx   map[string]*sandboxca.CA
	destroyed map[string]struct{}

	// client is for buffered Handle() — has a 60s overall timeout
	// because the body is read entirely into memory and we don't
	// want a slow upstream to leak file descriptors.
	client *http.Client

	// streamClient is for HandleStreaming() — same transport (so the
	// connection pool is shared), but no overall Timeout. Streaming
	// responses (SSE, LLM token streams, large downloads) legitimately
	// run for minutes; cancellation is via the request context.
	streamClient *http.Client

	// Stats.
	served atomic.Uint64
	denied atomic.Uint64
}

// allowPrivateKey is the per-request context value that opts a single
// dial out of the default private-IP block. The handler sets it to true
// only when a rule matched — the operator has explicitly authorized
// this URL, so reaching e.g. an internal API on RFC1918 is intended.
type allowPrivateKey struct{}

func withAllowPrivate(ctx context.Context) context.Context {
	return context.WithValue(ctx, allowPrivateKey{}, true)
}
func allowsPrivate(ctx context.Context) bool {
	v, _ := ctx.Value(allowPrivateKey{}).(bool)
	return v
}

// guardedDialContext is the safe DialContext we install on the egress
// HTTP client. It resolves the hostname itself, refuses to dial any IP
// in protocol.privateCIDRs (unless the per-request context opts out),
// and then dials by IP literal so a subsequent re-resolution can't
// race the check — defeating both DNS-rebinding and plain DNS-pointed-
// at-metadata attacks.
//
// The TLS handshake the http.Transport performs on top of this conn
// still uses the URL host for SNI and certificate verification, so
// dialing by IP doesn't break HTTPS validation.
func guardedDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("splitting host:port %q: %w", addr, err)
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}

	allow := allowsPrivate(ctx)
	if !allow {
		for _, ip := range ips {
			if protocol.IsPrivateIP(ip.IP) {
				return nil, fmt.Errorf("egress to private address blocked: %s -> %s", host, ip.IP)
			}
		}
	}

	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

// New creates a Handler with a default-tuned HTTP client.
func New() *Handler {
	// One shared http.Transport gives us connection pooling per (host, scheme).
	// MaxIdleConnsPerHost is the dial that prevents repeated TCP+TLS to hot
	// upstreams like api.openai.com.
	tr := &http.Transport{
		Proxy:                 nil, // we are the proxy, no chaining
		DialContext:           guardedDialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true, // body is opaque to us; upstream sees Accept-Encoding from sandbox
	}
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		// Stop following redirects on the daemon side; let the
		// sandbox client decide. Some clients want to inspect 3xx
		// themselves. serveSDKFetch flips this via context value to
		// preserve the legacy follow-redirects behavior of the old
		// agent-side net.Fetch.
		if followsRedirects(req.Context()) {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		}
		return http.ErrUseLastResponse
	}
	return &Handler{
		bySandbox: make(map[string]*netrules.Compiled),
		caBySbx:   make(map[string]*sandboxca.CA),
		destroyed: make(map[string]struct{}),
		client: &http.Client{
			Transport:     tr,
			Timeout:       60 * time.Second,
			CheckRedirect: checkRedirect,
		},
		streamClient: &http.Client{
			Transport:     tr,
			// No Timeout — streaming bodies legitimately run for
			// minutes (SSE, LLM token streams, large downloads).
			// Cancellation is via the request context.
			CheckRedirect: checkRedirect,
		},
	}
}

// followRedirectsKey is the per-request context value that opts the
// CheckRedirect into following 3xx. Used by serveSDKFetch to preserve
// the legacy net.fetch behavior; the egress proxy path keeps the
// no-follow default so the sandbox sees raw redirects.
type followRedirectsKey struct{}

// WithFollowRedirects returns a context that opts the egress dial into
// following HTTP 3xx redirects (up to 10 hops). Exported so daemon
// handlers serving the legacy net.fetch RPC preserve the old
// agent-side follow-redirects behavior.
func WithFollowRedirects(ctx context.Context) context.Context {
	return context.WithValue(ctx, followRedirectsKey{}, true)
}
func followsRedirects(ctx context.Context) bool {
	v, _ := ctx.Value(followRedirectsKey{}).(bool)
	return v
}

// SetRules replaces the compiled ruleset for a sandbox. Pass nil/zero rules
// to clear.
func (h *Handler) SetRules(sandboxID string, rules []netrules.Rule) error {
	c, err := netrules.Compile(rules)
	if err != nil {
		return err
	}
	h.mu.Lock()
	if len(rules) == 0 {
		delete(h.bySandbox, sandboxID)
	} else {
		h.bySandbox[sandboxID] = c
	}
	h.mu.Unlock()
	return nil
}

// ClearRules removes any registered rules for sandboxID and marks the
// sandbox tombstoned: future EnsureCA calls for this ID will refuse to
// re-generate, which prevents a racing in-flight CONNECT handler from
// lazily resurrecting state after teardown.
func (h *Handler) ClearRules(sandboxID string) {
	h.mu.Lock()
	delete(h.bySandbox, sandboxID)
	delete(h.caBySbx, sandboxID)
	h.destroyed[sandboxID] = struct{}{}
	h.mu.Unlock()
}

// ErrSandboxDestroyed is returned by EnsureCA / IssueLeaf when the
// sandbox's rules have been cleared (destroy path).
var ErrSandboxDestroyed = fmt.Errorf("sandbox destroyed")

// EnsureCA returns the CA for sandboxID, generating one lazily on first
// use. Returns ErrSandboxDestroyed if the sandbox has already been torn
// down.
func (h *Handler) EnsureCA(sandboxID string) (*sandboxca.CA, error) {
	h.mu.RLock()
	if _, dead := h.destroyed[sandboxID]; dead {
		h.mu.RUnlock()
		return nil, ErrSandboxDestroyed
	}
	ca := h.caBySbx[sandboxID]
	h.mu.RUnlock()
	if ca != nil {
		return ca, nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, dead := h.destroyed[sandboxID]; dead {
		return nil, ErrSandboxDestroyed
	}
	if ca = h.caBySbx[sandboxID]; ca != nil {
		return ca, nil
	}
	new, err := sandboxca.New(sandboxID)
	if err != nil {
		return nil, fmt.Errorf("generating sandbox ca: %w", err)
	}
	h.caBySbx[sandboxID] = new
	return new, nil
}

// CA returns the existing CA for sandboxID, or nil if none has been
// generated yet. Use EnsureCA for the lazy-create variant.
func (h *Handler) CA(sandboxID string) *sandboxca.CA {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.caBySbx[sandboxID]
}

// IssueLeaf returns a PEM-encoded leaf cert+key for host, signed by the
// per-sandbox CA. Generates the CA lazily on first call.
func (h *Handler) IssueLeaf(sandboxID, host string) (certPEM, keyPEM []byte, err error) {
	ca, err := h.EnsureCA(sandboxID)
	if err != nil {
		return nil, nil, err
	}
	return ca.Leaf(host)
}

// Rules returns a snapshot of the compiled ruleset for a sandbox, or nil.
func (h *Handler) Rules(sandboxID string) *netrules.Compiled {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.bySandbox[sandboxID]
}

// DeferFunc is invoked when a matched rule's Action is ActionDefer. The
// daemon-side session uses this to round-trip the request to the SDK's
// programmatic handler. The returned DeferResponse decides whether to
// continue dialing (DeferResponse.Request set) or short-circuit
// (DeferResponse.Response set).
type DeferFunc func(ctx context.Context, ruleID string, req *protocol.EgressRequest) (*protocol.DeferResponse, error)

// StreamSink is the interface the daemon's streaming egress handler
// writes body chunks to. The concrete implementation in the daemon
// emits FrameStreamData / FrameStreamEnd frames on the agent
// connection; in tests it can be backed by a buffer or channel.
type StreamSink interface {
	// WriteChunk emits one body chunk for this stream.
	WriteChunk(data []byte) error
	// End closes the stream. status 0 = clean, non-zero = error
	// with errMsg payload.
	End(status byte, errMsg string) error
}

// prepResult is the output of prepareUpstream. Exactly one of Req or
// Synthetic is non-nil. MatchedID is the rule that fired (empty when
// no rule matched).
type prepResult struct {
	Req       *http.Request
	Synthetic *protocol.EgressResponse
	MatchedID string
}

// prepareUpstream is the shared "from EgressRequest to *http.Request"
// path used by both Handle and HandleStreaming. It parses the URL,
// looks up the matching rule, applies deny/defer/inject, runs the
// private-host fast-path, builds the outbound headers, and returns a
// request that's ready to hand to h.client.Do. When a rule short-
// circuits (deny, defer-stub, defer-error, body decode error,
// private-host block) Synthetic is set instead and the caller returns
// it directly.
//
// Behavior is bit-for-bit equivalent to the inlined versions that
// previously lived in Handle and HandleStreaming.
func (h *Handler) prepareUpstream(ctx context.Context, sandboxID string, req *protocol.EgressRequest, deferFn DeferFunc) prepResult {
	parsed, err := url.Parse(req.URL)
	if err != nil {
		return prepResult{Synthetic: synthetic(400, "invalid url: "+err.Error())}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return prepResult{Synthetic: synthetic(400, "unsupported scheme: "+parsed.Scheme)}
	}

	port := portFromURL(parsed)
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = "GET"
	}

	rules := h.Rules(sandboxID)
	var matched *netrules.Rule
	if rules != nil {
		matched = rules.Match(parsed.Hostname(), port, method, parsed.Path)
	}

	if matched != nil && matched.Action == netrules.ActionDeny {
		h.denied.Add(1)
		resp := synthetic(403, "blocked by egress rule "+matched.ID)
		resp.MatchedRuleID = matched.ID
		return prepResult{Synthetic: resp, MatchedID: matched.ID}
	}

	// Defer to the SDK handler if the matched rule asks for it. The
	// handler can either return a synthetic response (short-circuit)
	// or a modified request that we continue dialing with.
	if matched != nil && matched.Action == netrules.ActionDefer && deferFn != nil {
		dResp, err := deferFn(ctx, matched.ID, req)
		if err != nil {
			out := synthetic(502, "defer handler: "+err.Error())
			out.MatchedRuleID = matched.ID
			return prepResult{Synthetic: out, MatchedID: matched.ID}
		}
		if dResp != nil && dResp.Response != nil {
			dResp.Response.MatchedRuleID = matched.ID
			dResp.Response.Synthetic = true
			return prepResult{Synthetic: dResp.Response, MatchedID: matched.ID}
		}
		if dResp != nil && dResp.Request != nil {
			req = dResp.Request
			parsed, err = url.Parse(req.URL)
			if err != nil {
				return prepResult{Synthetic: synthetic(400, "defer handler returned invalid url: "+err.Error()), MatchedID: matched.ID}
			}
			method = strings.ToUpper(req.Method)
			if method == "" {
				method = "GET"
			}
		}
	}

	// Build the outbound headers: clone the inbound, drop hop-by-hop
	// (these are scoped to the sandbox<->agent hop), then apply inject
	// mutations.
	headers := cloneHeaders(req.Headers)
	stripHopByHop(headers)
	if matched != nil && matched.Action == netrules.ActionInject {
		for k, v := range matched.Inject.SetHeaders {
			// CRITICAL: remove any existing entries whose canonical
			// form collides with the inject key. Without this, the
			// sandbox can suppress an injected credential by sending
			// the same header in different case (e.g. lowercase
			// "authorization" alongside our "Authorization") — both
			// keys live in the map until http.CanonicalHeaderKey
			// collapses them at write time, and Go's randomized map
			// iteration order picks which one wins. With the cleanup,
			// the inject value is guaranteed to be the one that
			// reaches the upstream.
			canonK := http.CanonicalHeaderKey(k)
			for existingK := range headers {
				if http.CanonicalHeaderKey(existingK) == canonK {
					delete(headers, existingK)
				}
			}
			headers[canonK] = []string{v}
		}
		for _, k := range matched.Inject.RemoveHeaders {
			canonK := http.CanonicalHeaderKey(k)
			for existingK := range headers {
				if http.CanonicalHeaderKey(existingK) == canonK {
					delete(headers, existingK)
				}
			}
		}
		if len(matched.Inject.SetQuery) > 0 {
			q := parsed.Query()
			for k, v := range matched.Inject.SetQuery {
				q.Set(k, v)
			}
			parsed.RawQuery = q.Encode()
		}
	}

	// Fast-path: refuse URLs that hardcode a private IP literal. The
	// dialer also enforces this against resolved IPs (DNS-rebinding
	// defense); this block gives a clearer synthetic error.
	// Skipped when a rule matched — operator opted in.
	if matched == nil && isPrivateHost(parsed.Hostname()) {
		return prepResult{Synthetic: synthetic(403, "egress to private address blocked")}
	}

	body, err := decodeBody(req.Body)
	if err != nil {
		return prepResult{Synthetic: synthetic(400, "invalid body encoding: "+err.Error())}
	}

	// Allow-private is per-request, scoped to matched rules only, so
	// DNS-rebinding via a hostname pointed at 169.254.169.254 cannot
	// reach IMDS unless an operator-installed rule already authorized.
	reqCtx := ctx
	if matched != nil {
		reqCtx = withAllowPrivate(ctx)
	}
	httpReq, err := http.NewRequestWithContext(reqCtx, method, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return prepResult{Synthetic: synthetic(400, "building request: "+err.Error())}
	}
	for k, vs := range headers {
		httpReq.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	if hs, ok := headers["Host"]; ok && len(hs) > 0 {
		httpReq.Host = hs[0]
	}

	matchedID := ""
	if matched != nil {
		matchedID = matched.ID
	}
	return prepResult{Req: httpReq, MatchedID: matchedID}
}

// Handle applies the sandbox's rules to req and dials upstream. The returned
// EgressResponse always has a meaningful Status — transport errors are
// rendered as 502 Bad Gateway with the error in the body.
//
// When a matched rule has Action == ActionDefer and deferFn is non-nil, the
// daemon calls deferFn to obtain the SDK's modified request or synthetic
// response. Pass nil deferFn to treat defer matches as plain allow.
func (h *Handler) Handle(ctx context.Context, sandboxID string, req *protocol.EgressRequest, deferFn DeferFunc) *protocol.EgressResponse {
	h.served.Add(1)

	prep := h.prepareUpstream(ctx, sandboxID, req, deferFn)
	if prep.Synthetic != nil {
		return prep.Synthetic
	}

	resp, err := h.client.Do(prep.Req)
	if err != nil {
		return synthetic(502, "upstream: "+err.Error())
	}
	defer resp.Body.Close()

	// LimitReader+ReadAll silently truncates oversized bodies. Read one
	// byte past the limit so we can distinguish "exactly at the cap" from
	// "exceeded the cap" and fail loudly in the latter case.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxEgressBody+1))
	if err != nil {
		return synthetic(502, "reading upstream body: "+err.Error())
	}
	if int64(len(respBody)) > protocol.MaxEgressBody {
		return synthetic(502, fmt.Sprintf("upstream response exceeds %d bytes; streaming bodies are not yet supported", protocol.MaxEgressBody))
	}

	// Build response headers as multi-valued; drop hop-by-hop entries so
	// the sandbox client doesn't see Connection / Transfer-Encoding from
	// the upstream hop.
	outHeaders := make(map[string][]string, len(resp.Header))
	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		outHeaders[k] = append([]string(nil), vs...)
	}

	return &protocol.EgressResponse{
		Status:        resp.StatusCode,
		Headers:       outHeaders,
		Body:          base64.StdEncoding.EncodeToString(respBody),
		MatchedRuleID: prep.MatchedID,
	}
}

// HandleStreaming is the streaming counterpart of Handle. It applies
// rules (via the shared prepareUpstream), dials upstream, returns the
// response headers as soon as they arrive, and returns a BodyCopier
// that streams the body to a sink in StreamChunkSize-bounded chunks.
//
// On synthetic short-circuits (deny, defer-stub, defer-error, private-
// host block, body decode error) the head + copier still describe a
// well-formed response — the copier emits the synthetic body as a
// single chunk and then a clean End.
//
// The body copy is synchronous on whatever goroutine calls the copier;
// the daemon's session handler runs it in its own goroutine so the RPC
// response (the head) returns to the agent before the body finishes.
func (h *Handler) HandleStreaming(ctx context.Context, sandboxID string, req *protocol.EgressRequest, deferFn DeferFunc) (*protocol.StreamEgressResponse, BodyCopier, error) {
	h.served.Add(1)

	prep := h.prepareUpstream(ctx, sandboxID, req, deferFn)
	if prep.Synthetic != nil {
		head, copier := syntheticAsStream(prep.Synthetic)
		return head, copier, nil
	}

	// Use streamClient (no Timeout) so SSE / long downloads aren't
	// guillotined at 60s. Cancellation flows via prep.Req.Context().
	resp, err := h.streamClient.Do(prep.Req)
	if err != nil {
		head, copier := syntheticAsStream(synthetic(502, "upstream: "+err.Error()))
		return head, copier, nil
	}

	head := &protocol.StreamEgressResponse{
		Status:        resp.StatusCode,
		Headers:       make(map[string][]string, len(resp.Header)),
		MatchedRuleID: prep.MatchedID,
	}
	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		head.Headers[k] = append([]string(nil), vs...)
	}

	// Defer body copy to the caller-supplied goroutine. Closes resp.Body
	// when it finishes (one way or another).
	copier := func(sink StreamSink) error {
		defer resp.Body.Close()
		buf := make([]byte, protocol.StreamChunkSize)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if werr := sink.WriteChunk(buf[:n]); werr != nil {
					return sink.End(protocol.StreamEndError, "writing chunk: "+werr.Error())
				}
			}
			if readErr == io.EOF {
				return sink.End(protocol.StreamEndOK, "")
			}
			if readErr != nil {
				return sink.End(protocol.StreamEndError, "reading upstream: "+readErr.Error())
			}
		}
	}
	return head, copier, nil
}

// BodyCopier copies an upstream response body to a StreamSink in
// StreamChunkSize chunks. Always emits a terminal sink.End call,
// either with status 0 on a clean EOF or status 1 with a message on
// any error.
type BodyCopier func(sink StreamSink) error

// syntheticAsStream converts a buffered EgressResponse (the shape
// produced by prepareUpstream's short-circuit paths) into the
// streaming pair: a head with the same status/headers/MatchedRuleID,
// and a copier that emits the body as a single chunk followed by a
// clean End. The caller never needs to look at EgressResponse for
// streaming responses — only this conversion does.
func syntheticAsStream(r *protocol.EgressResponse) (*protocol.StreamEgressResponse, BodyCopier) {
	head := &protocol.StreamEgressResponse{
		Status:        r.Status,
		Headers:       r.Headers,
		MatchedRuleID: r.MatchedRuleID,
		Synthetic:     r.Synthetic,
	}
	body, _ := decodeBody(r.Body)
	copier := func(sink StreamSink) error {
		if len(body) > 0 {
			if err := sink.WriteChunk(body); err != nil {
				return err
			}
		}
		return sink.End(protocol.StreamEndOK, "")
	}
	return head, copier
}

// Served returns the total egress requests handled across all sandboxes.
func (h *Handler) Served() uint64 { return h.served.Load() }

// Denied returns the total egress requests blocked by deny rules.
func (h *Handler) Denied() uint64 { return h.denied.Load() }

// Close releases pooled connections held by the underlying transport.
func (h *Handler) Close() {
	if tr, ok := h.client.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
}

func synthetic(status int, msg string) *protocol.EgressResponse {
	body := base64.StdEncoding.EncodeToString([]byte(msg))
	return &protocol.EgressResponse{
		Status:    status,
		Headers:   map[string][]string{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:      body,
		Synthetic: true,
	}
}

func decodeBody(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func cloneHeaders(in map[string][]string) map[string][]string {
	if in == nil {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(in))
	for k, vs := range in {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// hopByHopHeaders is the canonical-case list from RFC 7230 §6.1 plus
// proxy-* headers that scope to the proxy-to-target hop.
var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Proxy-Connection":    {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

func isHopByHop(name string) bool {
	_, ok := hopByHopHeaders[http.CanonicalHeaderKey(name)]
	return ok
}

func stripHopByHop(h map[string][]string) {
	for k := range h {
		if isHopByHop(k) {
			delete(h, k)
		}
	}
}

func portFromURL(u *url.URL) int {
	if p := u.Port(); p != "" {
		var n int
		for _, c := range p {
			if c < '0' || c > '9' {
				return 0
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}

func isPrivateHost(host string) bool { return protocol.IsPrivateHost(host) }

