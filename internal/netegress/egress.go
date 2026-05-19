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

	client *http.Client

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
	return &Handler{
		bySandbox: make(map[string]*netrules.Compiled),
		caBySbx:   make(map[string]*sandboxca.CA),
		destroyed: make(map[string]struct{}),
		client: &http.Client{
			Transport: tr,
			Timeout:   60 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Stop following redirects on the daemon side; let the sandbox
				// client decide. Some clients want to inspect 3xx themselves.
				return http.ErrUseLastResponse
			},
		},
	}
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

// Handle applies the sandbox's rules to req and dials upstream. The returned
// EgressResponse always has a meaningful Status — transport errors are
// rendered as 502 Bad Gateway with the error in the body.
//
// When a matched rule has Action == ActionDefer and deferFn is non-nil, the
// daemon calls deferFn to obtain the SDK's modified request or synthetic
// response. Pass nil deferFn to treat defer matches as plain allow.
func (h *Handler) Handle(ctx context.Context, sandboxID string, req *protocol.EgressRequest, deferFn DeferFunc) *protocol.EgressResponse {
	h.served.Add(1)

	parsed, err := url.Parse(req.URL)
	if err != nil {
		return synthetic(400, "invalid url: "+err.Error())
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return synthetic(400, "unsupported scheme: "+parsed.Scheme)
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
		return resp
	}

	// Defer to the SDK handler if the matched rule asks for it.
	if matched != nil && matched.Action == netrules.ActionDefer && deferFn != nil {
		dResp, err := deferFn(ctx, matched.ID, req)
		if err != nil {
			out := synthetic(502, "defer handler: "+err.Error())
			out.MatchedRuleID = matched.ID
			return out
		}
		if dResp != nil && dResp.Response != nil {
			// Short-circuit: SDK returned a synthetic response.
			dResp.Response.MatchedRuleID = matched.ID
			dResp.Response.Synthetic = true
			return dResp.Response
		}
		if dResp != nil && dResp.Request != nil {
			// Continue with the SDK-modified request.
			req = dResp.Request
			parsed, err = url.Parse(req.URL)
			if err != nil {
				return synthetic(400, "defer handler returned invalid url: "+err.Error())
			}
			method = strings.ToUpper(req.Method)
			if method == "" {
				method = "GET"
			}
		}
	}

	// Build the outbound headers: clone the inbound, drop hop-by-hop
	// (these are scoped to the sandbox<->agent hop and would corrupt the
	// upstream's connection semantics), then apply inject mutations.
	headers := cloneHeaders(req.Headers)
	stripHopByHop(headers)
	if matched != nil && matched.Action == netrules.ActionInject {
		for k, v := range matched.Inject.SetHeaders {
			headers[k] = []string{v}
		}
		for _, k := range matched.Inject.RemoveHeaders {
			delete(headers, k)
		}
		if len(matched.Inject.SetQuery) > 0 {
			q := parsed.Query()
			for k, v := range matched.Inject.SetQuery {
				q.Set(k, v)
			}
			parsed.RawQuery = q.Encode()
		}
	}

	// Fast-path: refuse URLs that hardcode a private IP literal, before
	// we waste a DNS lookup or socket on them. The dialer below ALSO
	// enforces this against resolved IPs (DNS-rebinding defense); this
	// block just gives a clearer synthetic error for the literal case.
	// Skipped when a rule matched — operator opted in.
	if matched == nil && isPrivateHost(parsed.Hostname()) {
		return synthetic(403, "egress to private address blocked")
	}

	body, err := decodeBody(req.Body)
	if err != nil {
		return synthetic(400, "invalid body encoding: "+err.Error())
	}

	// The dialer ALWAYS resolves the hostname and refuses private IPs
	// unless this context is marked allow-private. A matched rule is
	// the only thing that flips the bit; otherwise an attacker-controlled
	// hostname that resolves to 169.254.169.254 gets blocked at dial.
	reqCtx := ctx
	if matched != nil {
		reqCtx = withAllowPrivate(ctx)
	}
	httpReq, err := http.NewRequestWithContext(reqCtx, method, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return synthetic(400, "building request: "+err.Error())
	}
	for k, vs := range headers {
		// Use Header[k] = vs rather than Set/Add so the canonical-case
		// form chosen by the stdlib gets the multi-value list intact.
		httpReq.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	if hs, ok := headers["Host"]; ok && len(hs) > 0 {
		httpReq.Host = hs[0]
	}

	resp, err := h.client.Do(httpReq)
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

	out := &protocol.EgressResponse{
		Status:  resp.StatusCode,
		Headers: outHeaders,
		Body:    base64.StdEncoding.EncodeToString(respBody),
	}
	if matched != nil {
		out.MatchedRuleID = matched.ID
	}
	return out
}

// HandleStreaming is the streaming counterpart of Handle. It applies
// rules, dials upstream, returns the response headers as soon as they
// arrive, and spawns a goroutine that copies the body to sink in
// StreamChunkSize-bounded pieces.
//
// On a denied / synthetic / inject-only path the head is returned with
// a complete body that hasn't been streamed yet; the caller should call
// sink.WriteChunk + sink.End themselves. (The simpler integration: emit
// the head, then emit one chunk + clean End.)
//
// Returns the streaming header view plus a function the caller invokes
// to start copying the body. The body copy is synchronous on whatever
// goroutine calls it; pass it to a separate goroutine if you want the
// RPC response to return before the body finishes.
func (h *Handler) HandleStreaming(ctx context.Context, sandboxID string, req *protocol.EgressRequest, deferFn DeferFunc) (*protocol.StreamEgressResponse, BodyCopier, error) {
	h.served.Add(1)

	parsed, err := url.Parse(req.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, nil, fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
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

	// Synthetic / deny / defer-short-circuit paths still produce a full
	// in-memory body. Convert to the streaming shape by emitting a single
	// chunk + clean end in the BodyCopier closure.
	syntheticEarly := func(status int, msg string) (*protocol.StreamEgressResponse, BodyCopier, error) {
		head := &protocol.StreamEgressResponse{
			Status:    status,
			Headers:   map[string][]string{"Content-Type": {"text/plain; charset=utf-8"}},
			Synthetic: true,
		}
		if matched != nil {
			head.MatchedRuleID = matched.ID
		}
		body := []byte(msg)
		copier := func(sink StreamSink) error {
			if err := sink.WriteChunk(body); err != nil {
				return err
			}
			return sink.End(protocol.StreamEndOK, "")
		}
		return head, copier, nil
	}

	if matched != nil && matched.Action == netrules.ActionDeny {
		h.denied.Add(1)
		return syntheticEarly(403, "blocked by egress rule "+matched.ID)
	}

	if matched != nil && matched.Action == netrules.ActionDefer && deferFn != nil {
		dResp, err := deferFn(ctx, matched.ID, req)
		if err != nil {
			return syntheticEarly(502, "defer handler: "+err.Error())
		}
		if dResp != nil && dResp.Response != nil {
			head := &protocol.StreamEgressResponse{
				Status:        dResp.Response.Status,
				Headers:       dResp.Response.Headers,
				MatchedRuleID: matched.ID,
				Synthetic:     true,
			}
			body, _ := decodeBody(dResp.Response.Body)
			copier := func(sink StreamSink) error {
				if len(body) > 0 {
					if err := sink.WriteChunk(body); err != nil {
						return err
					}
				}
				return sink.End(protocol.StreamEndOK, "")
			}
			return head, copier, nil
		}
		if dResp != nil && dResp.Request != nil {
			req = dResp.Request
			parsed, err = url.Parse(req.URL)
			if err != nil {
				return syntheticEarly(400, "defer handler returned invalid url: "+err.Error())
			}
			method = strings.ToUpper(req.Method)
			if method == "" {
				method = "GET"
			}
		}
	}

	headers := cloneHeaders(req.Headers)
	stripHopByHop(headers)
	if matched != nil && matched.Action == netrules.ActionInject {
		for k, v := range matched.Inject.SetHeaders {
			headers[k] = []string{v}
		}
		for _, k := range matched.Inject.RemoveHeaders {
			delete(headers, k)
		}
		if len(matched.Inject.SetQuery) > 0 {
			q := parsed.Query()
			for k, v := range matched.Inject.SetQuery {
				q.Set(k, v)
			}
			parsed.RawQuery = q.Encode()
		}
	}

	// Fast-path: refuse URLs that hardcode a private IP literal. Dialer
	// also enforces against resolved IPs below; this is the early exit.
	if matched == nil && isPrivateHost(parsed.Hostname()) {
		return syntheticEarly(403, "egress to private address blocked")
	}

	body, err := decodeBody(req.Body)
	if err != nil {
		return syntheticEarly(400, "invalid body encoding: "+err.Error())
	}

	// Allow-private bit is per-request, scoped to matched rules only,
	// so DNS-rebinding via a hostname pointed at 169.254.169.254 cannot
	// reach IMDS unless an operator-installed rule already authorized
	// the URL.
	reqCtx := ctx
	if matched != nil {
		reqCtx = withAllowPrivate(ctx)
	}
	httpReq, err := http.NewRequestWithContext(reqCtx, method, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return syntheticEarly(400, "building request: "+err.Error())
	}
	for k, vs := range headers {
		httpReq.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	if hs, ok := headers["Host"]; ok && len(hs) > 0 {
		httpReq.Host = hs[0]
	}

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return syntheticEarly(502, "upstream: "+err.Error())
	}

	head := &protocol.StreamEgressResponse{
		Status:  resp.StatusCode,
		Headers: make(map[string][]string, len(resp.Header)),
	}
	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		head.Headers[k] = append([]string(nil), vs...)
	}
	if matched != nil {
		head.MatchedRuleID = matched.ID
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

