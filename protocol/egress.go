package protocol

// EgressRequest is the params for OpNetEgress. The agent's in-sandbox proxy
// constructs this from an inbound HTTP request and sends it to the daemon.
//
// Body is base64-encoded to keep the JSON-RPC payload self-describing.
// Body is bounded by MaxEgressBody. Streaming bodies will require a new
// opcode + frame extension; until then, oversized requests/responses
// surface as 413/502 rather than being silently truncated.
//
// Headers is multi-valued because some HTTP headers (Set-Cookie most
// notably) legitimately appear multiple times in one request/response.
// Collapsing them into one value silently corrupts cookie state.
type EgressRequest struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers,omitempty"`
	// Body is base64-encoded; empty when there is no body.
	Body string `json:"body,omitempty"`
	// RuleID, when set, hints which rule the SDK matched on its side. The
	// daemon may still apply its own rules; this is purely advisory.
	RuleID string `json:"rule_id,omitempty"`
}

// EgressResponse is the result of OpNetEgress.
type EgressResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"` // base64-encoded
	// MatchedRuleID is set when a rule was applied. Useful for observability.
	MatchedRuleID string `json:"matched_rule_id,omitempty"`
	// Synthetic is true when the response came from a deny rule or local
	// short-circuit (not from the upstream).
	Synthetic bool `json:"synthetic,omitempty"`
}

// MaxEgressBody is the per-request body cap for the OpNetEgress path
// and per-response cap when callers use the non-streaming path. 10 MB
// matches MaxFrameSize. Use OpNetEgressStream to lift the response cap.
const MaxEgressBody = 10 * 1024 * 1024

// StreamChunkSize is the target size for FrameStreamData payloads. The
// daemon reads up to this many bytes from an upstream response before
// emitting a stream frame. Smaller = lower latency per chunk, more
// frame overhead; larger = the inverse.
const StreamChunkSize = 64 * 1024

// StreamEgressRequest is the params for OpNetEgressStream. Same as
// EgressRequest plus a stream ID the agent has allocated for this
// request. The daemon will emit FrameStreamData with this stream ID
// followed by a FrameStreamEnd.
type StreamEgressRequest struct {
	StreamID uint32        `json:"stream_id"`
	Request  EgressRequest `json:"request"`
}

// StreamEgressResponse is the result of OpNetEgressStream. Headers and
// status arrive in this JSON-RPC response as soon as the upstream
// responds; the body is delivered separately via stream frames. The
// daemon writes a FrameStreamEnd to signal completion.
type StreamEgressResponse struct {
	Status        int                 `json:"status"`
	Headers       map[string][]string `json:"headers,omitempty"`
	MatchedRuleID string              `json:"matched_rule_id,omitempty"`
	Synthetic     bool                `json:"synthetic,omitempty"`
}

// LeafCertRequest is the params for OpNetCertLeaf.
type LeafCertRequest struct {
	Host string `json:"host"`
}

// LeafCertResponse is the result of OpNetCertLeaf. The daemon signs a
// short-lived leaf certificate for the given SNI host with the per-sandbox
// CA and returns the cert + key PEMs. The key never leaves the daemon
// except for this one host, scoped to LeafTTL.
type LeafCertResponse struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// CAInstallRequest is the params for OpNetCAInstall. The daemon pushes
// the per-sandbox CA PEM during the runtime readiness check; the agent
// writes it to disk so user processes can validate the leaf certs that
// will be minted later for HTTPS interception.
type CAInstallRequest struct {
	CertPEM string `json:"cert_pem"`
}

// DeferRequest is the params for OpNetDefer. The daemon forwards the
// matched EgressRequest along with the rule ID the SDK can use to look up
// the registered handler.
type DeferRequest struct {
	RuleID  string        `json:"rule_id"`
	Request EgressRequest `json:"request"`
}

// DeferResponse is what the SDK returns from a deferred-handler call.
// Exactly one of Request or Response should be set:
//
//   - Request set: continue dialing with this (possibly modified) request.
//     The daemon takes the URL, headers, method, and body from here.
//   - Response set: short-circuit. The daemon returns this directly to the
//     sandbox without dialing the upstream.
//
// If both are nil the daemon falls back to the original request unchanged.
type DeferResponse struct {
	Request  *EgressRequest  `json:"request,omitempty"`
	Response *EgressResponse `json:"response,omitempty"`
}
