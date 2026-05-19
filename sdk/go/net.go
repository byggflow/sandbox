package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// NetworkInject describes a mutation applied to a matched egress request.
type NetworkInject struct {
	SetHeaders    map[string]string `json:"set_headers,omitempty"`
	RemoveHeaders []string          `json:"remove_headers,omitempty"`
	SetQuery      map[string]string `json:"set_query,omitempty"`
}

// NetworkMatch selects which egress requests a rule applies to. Empty
// fields match anything.
type NetworkMatch struct {
	// Host matches by exact host, *.suffix glob, or /regex/.
	Host string `json:"host,omitempty"`
	// Port matches the destination port. 0 means any port.
	Port int `json:"port,omitempty"`
	// Method is a comma-separated method list, e.g. "GET,POST".
	Method string `json:"method,omitempty"`
	// PathPrefix matches if the request path starts with this string.
	PathPrefix string `json:"path_prefix,omitempty"`
}

// NetworkAction is what the daemon does on match.
type NetworkAction string

const (
	NetworkActionAllow  NetworkAction = "allow"
	NetworkActionDeny   NetworkAction = "deny"
	NetworkActionInject NetworkAction = "inject"
	NetworkActionDefer  NetworkAction = "defer"
)

// DeferredRequest is the request envelope passed to a NetworkHandler.
// Mutations to Headers, URL, or Method are sent back to the daemon for
// the actual upstream dial.
type DeferredRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	// Body is base64-encoded; empty when there is no body.
	Body string `json:"body,omitempty"`
}

// DeferredResponse is a synthetic response a handler can return to
// short-circuit dialing.
type DeferredResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// DeferResult is what a NetworkHandler returns. Set Request to continue
// dialing with a (possibly modified) request, or Response to short-circuit
// with a synthetic response.
type DeferResult struct {
	Request  *DeferredRequest  `json:"request,omitempty"`
	Response *DeferredResponse `json:"response,omitempty"`
}

// NetworkHandler is the user-supplied function invoked on each match of a
// rule whose Action is NetworkActionDefer. Runs in the SDK process.
type NetworkHandler func(ctx context.Context, req *DeferredRequest) (*DeferResult, error)

// NetworkRule is a single egress middleware directive.
type NetworkRule struct {
	ID     string         `json:"id,omitempty"`
	Match  NetworkMatch   `json:"match"`
	Action NetworkAction  `json:"action"`
	Inject *NetworkInject `json:"inject,omitempty"`
	// Handler is invoked when Action is NetworkActionDefer. Not serialized
	// to the daemon; the daemon addresses the handler by rule ID.
	Handler NetworkHandler `json:"-"`
}

// NetworkConfig configures network middleware installed at sandbox creation.
type NetworkConfig struct {
	Egress []NetworkRule `json:"egress,omitempty"`
	// Enabled controls whether the sandbox is wired through the
	// daemon's egress proxy. The zero value (false) keeps the historic
	// default of enabled — only an explicit Disabled flip turns it
	// off. Using a *bool would be cleaner but breaks the &NetworkConfig{Egress: [...]}
	// idiom. See Disabled for the opt-out.
	//
	// Disabled, when true, skips HTTP(S)_PROXY env injection, the
	// per-sandbox CA push, and the trust-bundle env vars. Sandbox
	// processes dial upstreams directly; rules don't evaluate for
	// sandbox-process traffic. sbx.Net().Fetch() still works (it
	// routes through the daemon either way).
	Disabled bool `json:"-"`
}

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

	// Local mirror of installed rules so Allow/Deny/Inject can append
	// incrementally without round-tripping the full list from the daemon.
	rulesMu  sync.Mutex
	rules    []NetworkRule
	handlers map[string]NetworkHandler

	// deferSeq is an atomic counter used to generate unique fallback
	// rule IDs for Defer calls that don't supply one. Using
	// len(rules) was a race in concurrent Defer() callers.
	deferSeq atomic.Uint64

	// dispatcherInstalled tracks whether the incoming-request dispatcher
	// for net.defer has been registered on the transport. Installed lazily
	// on the first Defer or Intercept that references handlers.
	dispatcherInstalled bool
}

// Intercept replaces the entire egress rule set for this sandbox. Rules are
// evaluated on the daemon, so any credentials injected via SetHeaders never
// enter the sandbox process.
func (n *NetCategory) Intercept(ctx context.Context, rules []NetworkRule) error {
	n.rulesMu.Lock()
	n.rules = append(n.rules[:0], rules...)
	snapshot := append([]NetworkRule(nil), n.rules...)
	n.rulesMu.Unlock()
	return n.pushRules(ctx, snapshot)
}

// Allow appends an allow rule for the given host glob.
func (n *NetCategory) Allow(ctx context.Context, host string) error {
	return n.appendRule(ctx, NetworkRule{Match: NetworkMatch{Host: host}, Action: NetworkActionAllow})
}

// Deny appends a deny rule for the given host glob.
func (n *NetCategory) Deny(ctx context.Context, host string) error {
	return n.appendRule(ctx, NetworkRule{Match: NetworkMatch{Host: host}, Action: NetworkActionDeny})
}

// Inject appends a header-injection rule for the given host glob.
func (n *NetCategory) Inject(ctx context.Context, host string, headers map[string]string) error {
	return n.appendRule(ctx, NetworkRule{
		Match:  NetworkMatch{Host: host},
		Action: NetworkActionInject,
		Inject: &NetworkInject{SetHeaders: headers},
	})
}

// Defer appends a programmatic-handler rule for the given host glob. The
// handler runs in this SDK process. ID is optional; one is generated
// from an atomic counter when empty so concurrent Defer calls don't
// collide.
func (n *NetCategory) Defer(ctx context.Context, host string, handler NetworkHandler, id ...string) error {
	ruleID := ""
	if len(id) > 0 {
		ruleID = id[0]
	}
	if ruleID == "" {
		ruleID = fmt.Sprintf("defer-%d", n.deferSeq.Add(1))
	}
	return n.appendRule(ctx, NetworkRule{
		ID:      ruleID,
		Match:   NetworkMatch{Host: host},
		Action:  NetworkActionDefer,
		Handler: handler,
	})
}

// Rules returns a snapshot of the locally-mirrored rule set.
func (n *NetCategory) Rules() []NetworkRule {
	n.rulesMu.Lock()
	defer n.rulesMu.Unlock()
	return append([]NetworkRule(nil), n.rules...)
}

func (n *NetCategory) appendRule(ctx context.Context, r NetworkRule) error {
	n.rulesMu.Lock()
	n.rules = append(n.rules, r)
	snapshot := append([]NetworkRule(nil), n.rules...)
	n.rulesMu.Unlock()
	return n.pushRules(ctx, snapshot)
}

func (n *NetCategory) pushRules(ctx context.Context, rules []NetworkRule) error {
	// Rebuild the handler registry and ensure the incoming-request
	// dispatcher is installed if any rule has a handler.
	hasHandler := false
	n.rulesMu.Lock()
	if n.handlers == nil {
		n.handlers = make(map[string]NetworkHandler)
	} else {
		for k := range n.handlers {
			delete(n.handlers, k)
		}
	}
	for _, r := range rules {
		if r.Action == NetworkActionDefer && r.Handler != nil {
			if r.ID == "" {
				n.rulesMu.Unlock()
				return fmt.Errorf("sandbox: defer rule requires ID")
			}
			n.handlers[r.ID] = r.Handler
			hasHandler = true
		}
	}
	installDispatcher := hasHandler && !n.dispatcherInstalled
	if installDispatcher {
		n.dispatcherInstalled = true
	}
	n.rulesMu.Unlock()

	if installDispatcher {
		n.cc.transport.OnRequest(n.dispatchDefer)
	}

	_, err := call(ctx, n.cc, op{
		Method: "net.rules.set",
		Params: map[string]interface{}{"rules": rules},
	})
	return err
}

// dispatchDefer is the IncomingRequestHandler installed on the transport.
// Looks up the rule's handler by ID, invokes it, and shapes the result for
// the daemon's DeferResponse.
func (n *NetCategory) dispatchDefer(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	if method != "net.defer" {
		return nil, nil
	}
	var req struct {
		RuleID  string          `json:"rule_id"`
		Request DeferredRequest `json:"request"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("sandbox: decode defer request: %w", err)
	}
	n.rulesMu.Lock()
	h := n.handlers[req.RuleID]
	n.rulesMu.Unlock()
	if h == nil {
		return nil, fmt.Errorf("sandbox: no handler for rule %s", req.RuleID)
	}
	result, err := h(ctx, &req.Request)
	if err != nil {
		return nil, err
	}
	if result == nil {
		// Treat as no-op: return the original request.
		return DeferResult{Request: &req.Request}, nil
	}
	return result, nil
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
