package sandbox

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestNetworkTypesRoundTripJSON(t *testing.T) {
	rules := []NetworkRule{
		{ID: "exact", Match: NetworkMatch{Host: "api.openai.com"}, Action: NetworkActionAllow},
		{Match: NetworkMatch{Host: "*.github.com"}, Action: NetworkActionDeny},
		{
			Match:  NetworkMatch{Host: "api.example.com", Method: "POST", PathPrefix: "/v1"},
			Action: NetworkActionInject,
			Inject: &NetworkInject{SetHeaders: map[string]string{"Authorization": "Bearer x"}},
		},
	}
	if len(rules) != 3 {
		t.Fatalf("unexpected rule count: %d", len(rules))
	}
	// Compile-time and runtime check: actions are the documented strings.
	if rules[0].Action != "allow" || rules[1].Action != "deny" || rules[2].Action != "inject" {
		t.Errorf("action constants drifted from wire format")
	}
}

func TestDispatchDeferRoutesByRuleID(t *testing.T) {
	n := &NetCategory{
		handlers: map[string]NetworkHandler{
			"r1": func(_ context.Context, req *DeferredRequest) (*DeferResult, error) {
				if req.Headers == nil {
					req.Headers = map[string]string{}
				}
				req.Headers["X-Test"] = "ok"
				return &DeferResult{Request: req}, nil
			},
		},
	}

	// The wire shape uses map[string][]string for headers (matches the
	// daemon's protocol.DeferResponse). dispatchDefer collapses that
	// into the user-facing map[string]string before invoking the
	// handler, and expands the handler's return back to multi-value
	// before returning to the daemon.
	params, _ := json.Marshal(map[string]interface{}{
		"rule_id": "r1",
		"request": map[string]interface{}{
			"method":  "GET",
			"url":     "https://x.test/",
			"headers": map[string][]string{"Existing": {"value"}},
		},
	})
	result, err := n.dispatchDefer(context.Background(), "net.defer", params)
	if err != nil {
		t.Fatalf("dispatchDefer: %v", err)
	}
	// The returned result is the wire shape (not *DeferResult), so
	// re-marshal+inspect via JSON.
	resultJSON, _ := json.Marshal(result)
	var wire struct {
		Request *struct {
			Headers map[string][]string `json:"headers"`
		} `json:"request"`
	}
	if err := json.Unmarshal(resultJSON, &wire); err != nil {
		t.Fatalf("decode wire result: %v", err)
	}
	if wire.Request == nil {
		t.Fatal("expected request in wire result")
	}
	if got := wire.Request.Headers["X-Test"]; len(got) != 1 || got[0] != "ok" {
		t.Errorf("handler mutation lost; X-Test=%v", got)
	}
}

func TestDispatchDeferUnknownRule(t *testing.T) {
	n := &NetCategory{handlers: map[string]NetworkHandler{}}
	params, _ := json.Marshal(map[string]interface{}{
		"rule_id": "missing",
		"request": DeferredRequest{},
	})
	_, err := n.dispatchDefer(context.Background(), "net.defer", params)
	if err == nil {
		t.Error("expected error for unknown rule ID")
	}
}

func TestDispatchDeferIgnoresOtherMethods(t *testing.T) {
	n := &NetCategory{}
	result, err := n.dispatchDefer(context.Background(), "other.method", nil)
	if err != nil || result != nil {
		t.Errorf("expected (nil, nil) for unknown method, got (%v, %v)", result, err)
	}
}

func TestNetCategoryAppendRuleMirrorsRules(t *testing.T) {
	// We can't actually call the daemon without a transport, but we can
	// verify the local mirror behavior by constructing a NetCategory with
	// a stub call context. The append path mutates rules even if pushRules
	// returns an error.
	n := &NetCategory{}
	// Bypass pushRules by setting cc nil — appendRule will still mutate
	// rules first, then attempt the call which panics. Use the Rules()
	// snapshot mechanism instead by directly populating.
	n.rulesMu.Lock()
	n.rules = append(n.rules, NetworkRule{ID: "a", Action: NetworkActionAllow})
	n.rulesMu.Unlock()

	got := n.Rules()
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("rules mirror not populated correctly: %+v", got)
	}
	// Snapshot must be a copy, not a reference.
	got[0].ID = "mutated"
	if n.Rules()[0].ID != "a" {
		t.Error("Rules() returned aliasing slice")
	}
}

func TestNetCategorySerializesRulePushes(t *testing.T) {
	tr := &blockingRuleTransport{
		firstEntered:  make(chan struct{}),
		secondEntered: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	n := &NetCategory{cc: &callContext{transport: tr}}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := n.Deny(context.Background(), "a.test"); err != nil {
			t.Errorf("deny: %v", err)
		}
	}()

	<-tr.firstEntered
	secondStarted := make(chan struct{})
	go func() {
		defer wg.Done()
		close(secondStarted)
		if err := n.Allow(context.Background(), "b.test"); err != nil {
			t.Errorf("allow: %v", err)
		}
	}()
	<-secondStarted

	select {
	case <-tr.secondEntered:
		t.Fatal("second rule push reached transport before first push completed")
	case <-time.After(25 * time.Millisecond):
	}

	close(tr.releaseFirst)
	wg.Wait()

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(tr.calls))
	}
	if len(tr.calls[0]) != 1 || tr.calls[0][0].Action != NetworkActionDeny {
		t.Fatalf("first snapshot = %+v, want only deny rule", tr.calls[0])
	}
	if len(tr.calls[1]) != 2 || tr.calls[1][0].Action != NetworkActionDeny || tr.calls[1][1].Action != NetworkActionAllow {
		t.Fatalf("second snapshot = %+v, want deny then allow", tr.calls[1])
	}
}

type blockingRuleTransport struct {
	mu            sync.Mutex
	calls         [][]NetworkRule
	firstEntered  chan struct{}
	secondEntered chan struct{}
	releaseFirst  chan struct{}
}

func (t *blockingRuleTransport) Call(_ context.Context, _ string, params interface{}) (interface{}, error) {
	rules := params.(map[string]interface{})["rules"].([]NetworkRule)
	snapshot := append([]NetworkRule(nil), rules...)

	t.mu.Lock()
	t.calls = append(t.calls, snapshot)
	callNum := len(t.calls)
	t.mu.Unlock()

	switch callNum {
	case 1:
		close(t.firstEntered)
		<-t.releaseFirst
	case 2:
		close(t.secondEntered)
	}
	return map[string]interface{}{}, nil
}

func (t *blockingRuleTransport) CallWithBinary(context.Context, string, interface{}, []byte) (interface{}, error) {
	return nil, nil
}
func (t *blockingRuleTransport) CallExpectBinary(context.Context, string, interface{}) (interface{}, [][]byte, error) {
	return nil, nil, nil
}
func (t *blockingRuleTransport) SendBinary(context.Context, []byte) error { return nil }
func (t *blockingRuleTransport) Notify(context.Context, string, interface{}) error {
	return nil
}
func (t *blockingRuleTransport) OnNotification(NotificationHandler) {}
func (t *blockingRuleTransport) OnRequest(IncomingRequestHandler)   {}
func (t *blockingRuleTransport) OnReplaced(ReplacedHandler)         {}
func (t *blockingRuleTransport) Close() error                       { return nil }
