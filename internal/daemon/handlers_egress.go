package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/byggflow/sandbox/internal/netegress"
	"github.com/byggflow/sandbox/internal/netrules"
	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/byggflow/sandbox/protocol"
)

func base64encode(b []byte) string         { return base64.StdEncoding.EncodeToString(b) }
func decodeBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// agentLocalMethods is the synchronous claim check for agent->daemon
// Requests. Listed methods are routed to handlers that never have binary
// follow-up frames, so claiming them at the WS-read goroutine is safe.
func agentLocalMethods(method string) bool {
	switch method {
	case protocol.OpNetEgress, protocol.OpNetEgressStream, protocol.OpNetCertLeaf:
		return true
	}
	return false
}

// agentStreamSink writes FrameStreamData / FrameStreamEnd frames to the
// agent connection underlying the given session. Used by the daemon's
// streaming egress handler to push response body chunks.
type agentStreamSink struct {
	sess     *proxy.Session
	streamID uint32
}

func (s *agentStreamSink) WriteChunk(data []byte) error {
	return s.sess.WriteAgentFrame(protocol.FrameStreamData, protocol.EncodeStreamData(s.streamID, data))
}

func (s *agentStreamSink) End(status byte, errMsg string) error {
	return s.sess.WriteAgentFrame(protocol.FrameStreamEnd, protocol.EncodeStreamEnd(s.streamID, status, errMsg))
}

// clientLocalMethodsFor builds the claim check for SDK->daemon Requests.
// OpNetRulesSet and OpNetFetch are ALWAYS served locally on the daemon
// when the egress handler is configured. The previous gating-by-rules
// behavior left a divergent code path through the agent's net.Fetch
// (with its own private-IP guard that drifted from the daemon's). One
// path is simpler and means the SSRF guard, header canonicalization,
// and CIDR list have a single source of truth.
//
// fs.write / fs.upload / fs.read NEVER appear here because the claim
// check is methodname-exact and those names aren't in our list, so
// their JSON+binary frame pairs always go through in order.
func (d *Daemon) clientLocalMethodsFor(_ string) proxy.MethodSet {
	return func(method string) bool {
		switch method {
		case protocol.OpNetRulesSet:
			return true
		case protocol.OpNetFetch:
			return d.Egress != nil
		}
		return false
	}
}

// makeAgentRequestHandler builds a proxy.LocalHandler that dispatches
// agent-initiated RPCs on the daemon side. Methods reaching here have
// already been claimed by agentLocalMethods; the default branch is an
// internal error rather than a fall-through.
func (d *Daemon) makeAgentRequestHandler(sbxID string, sess *proxy.Session) func(context.Context, string, json.RawMessage) (interface{}, error) {
	deferFn := d.makeDeferFunc(sess)
	return func(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
		switch method {
		case protocol.OpNetEgress:
			return d.serveEgress(ctx, sbxID, deferFn, params)
		case protocol.OpNetEgressStream:
			return d.serveEgressStream(ctx, sbxID, deferFn, sess, params)
		case protocol.OpNetCertLeaf:
			return d.serveLeafCert(sbxID, params)
		}
		return nil, fmt.Errorf("agent handler claim/serve mismatch: %s", method)
	}
}

// serveEgressStream handles an OpNetEgressStream request: dial upstream,
// return status+headers immediately, then spawn a goroutine that pushes
// body chunks back as FrameStreamData frames over the agent connection.
func (d *Daemon) serveEgressStream(ctx context.Context, sbxID string, deferFn netegress.DeferFunc, sess *proxy.Session, params json.RawMessage) (interface{}, error) {
	var req protocol.StreamEgressRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decoding stream egress params: %w", err)
	}
	if req.StreamID == 0 {
		return nil, fmt.Errorf("stream id required")
	}

	head, copier, err := d.Egress.HandleStreaming(ctx, sbxID, &req.Request, deferFn)
	if err != nil {
		return nil, err
	}

	sink := &agentStreamSink{sess: sess, streamID: req.StreamID}
	// Copy body asynchronously: we want the JSON response (head) to
	// return now so the agent can start writing the HTTP response to
	// the sandbox client before the upstream body is done.
	go func() {
		if err := copier(sink); err != nil {
			// copier always emits a terminal End; this branch covers
			// pathological cases where sink.End itself failed.
			d.Log.Debug("stream egress body copy", "stream_id", req.StreamID, "error", err)
		}
	}()
	return head, nil
}

// makeDeferFunc builds the DeferFunc that the egress handler invokes when
// a matched rule has Action == ActionDefer. The function round-trips the
// request envelope to the SDK over the session and decodes the reply.
func (d *Daemon) makeDeferFunc(sess *proxy.Session) netegress.DeferFunc {
	if sess == nil {
		return nil
	}
	return func(ctx context.Context, ruleID string, req *protocol.EgressRequest) (*protocol.DeferResponse, error) {
		resp, err := sess.CallClient(ctx, protocol.OpNetDefer, &protocol.DeferRequest{
			RuleID:  ruleID,
			Request: *req,
		}, 60*time.Second)
		if err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("sdk handler: %s", resp.Error.Message)
		}
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			return nil, fmt.Errorf("marshal defer result: %w", err)
		}
		var out protocol.DeferResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decode defer result: %w", err)
		}
		return &out, nil
	}
}

// serveLeafCert issues a leaf certificate for an SNI host signed by the
// per-sandbox CA. The agent calls this from its CONNECT handler to
// terminate TLS for MITM interception.
func (d *Daemon) serveLeafCert(sbxID string, params json.RawMessage) (interface{}, error) {
	var req protocol.LeafCertRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode leaf cert params: %w", err)
	}
	if req.Host == "" {
		return nil, fmt.Errorf("leaf cert: host required")
	}
	certPEM, keyPEM, err := d.Egress.IssueLeaf(sbxID, req.Host)
	if err != nil {
		return nil, fmt.Errorf("issue leaf cert: %w", err)
	}
	return &protocol.LeafCertResponse{
		CertPEM: string(certPEM),
		KeyPEM:  string(keyPEM),
	}, nil
}

// makeClientRequestHandler builds a proxy.LocalHandler that dispatches
// SDK-initiated RPCs claimed by clientLocalMethodsFor.
func (d *Daemon) makeClientRequestHandler(sbxID string, sess *proxy.Session) func(context.Context, string, json.RawMessage) (interface{}, error) {
	deferFn := d.makeDeferFunc(sess)
	return func(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
		switch method {
		case protocol.OpNetRulesSet:
			return d.serveRulesSet(ctx, sbxID, params)
		case protocol.OpNetFetch:
			return d.serveSDKFetch(ctx, sbxID, deferFn, params)
		}
		return nil, fmt.Errorf("client handler claim/serve mismatch: %s", method)
	}
}

// serveEgress handles an OpNetEgress request from the in-sandbox proxy.
func (d *Daemon) serveEgress(ctx context.Context, sbxID string, deferFn netegress.DeferFunc, params json.RawMessage) (interface{}, error) {
	var req protocol.EgressRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode egress params: %w", err)
	}
	resp := d.Egress.Handle(ctx, sbxID, &req, deferFn)
	return resp, nil
}

// serveRulesSet replaces the registered rule list for a sandbox.
func (d *Daemon) serveRulesSet(_ context.Context, sbxID string, params json.RawMessage) (interface{}, error) {
	var req struct {
		Rules []netrules.Rule `json:"rules"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("decode rules params: %w", err)
	}
	if err := d.Egress.SetRules(sbxID, req.Rules); err != nil {
		return nil, fmt.Errorf("install rules: %w", err)
	}
	return map[string]interface{}{"installed": len(req.Rules)}, nil
}

// serveSDKFetch translates a net.fetch RPC from the SDK into an
// EgressRequest and runs it through the daemon-side handler. Only called
// when clientLocalMethodsFor confirms this sandbox has rules registered,
// so we always produce a real response (no fall-through to agent).
func (d *Daemon) serveSDKFetch(ctx context.Context, sbxID string, deferFn netegress.DeferFunc, params json.RawMessage) (interface{}, error) {
	var p struct {
		URL     string            `json:"url"`
		Method  string            `json:"method,omitempty"`
		Headers map[string]string `json:"headers,omitempty"`
		Body    string            `json:"body,omitempty"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("decoding fetch params: %w", err)
	}
	headers := make(map[string][]string, len(p.Headers))
	for k, v := range p.Headers {
		headers[k] = []string{v}
	}
	req := &protocol.EgressRequest{
		Method:  p.Method,
		URL:     p.URL,
		Headers: headers,
		Body:    encodeBodyForFetch(p.Body),
	}
	resp := d.Egress.Handle(ctx, sbxID, req, deferFn)
	body, _ := decodeBase64(resp.Body)
	// Legacy net.fetch result shape used single-valued headers. Collapse
	// repeated values with comma joining (correct for most non-cookie
	// headers) so existing SDK clients don't break. The OpNetEgress
	// shape preserves multi-value semantics for code that uses it
	// directly.
	flat := make(map[string]string, len(resp.Headers))
	for k, vs := range resp.Headers {
		if len(vs) > 0 {
			flat[k] = strings.Join(vs, ", ")
		}
	}
	return map[string]interface{}{
		"status":  resp.Status,
		"headers": flat,
		"body":    string(body),
	}, nil
}

func encodeBodyForFetch(s string) string {
	if s == "" {
		return ""
	}
	return base64encode([]byte(s))
}
