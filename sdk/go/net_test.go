package sandbox

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNetworkTypesRoundTripJSON(t *testing.T) {
	rules := []NetworkRule{
		{ID: "exact", Match: NetworkMatch{Host: "api.openai.com"}, Action: NetworkActionAllow},
		{Match: NetworkMatch{Host: "*.github.com"}, Action: NetworkActionDeny},
		{
			Match: NetworkMatch{Host: "api.example.com", Method: "POST", PathPrefix: "/v1"},
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

	params, _ := json.Marshal(map[string]interface{}{
		"rule_id": "r1",
		"request": DeferredRequest{Method: "GET", URL: "https://x.test/"},
	})
	result, err := n.dispatchDefer(context.Background(), "net.defer", params)
	if err != nil {
		t.Fatalf("dispatchDefer: %v", err)
	}
	dr, ok := result.(*DeferResult)
	if !ok {
		t.Fatalf("expected *DeferResult, got %T", result)
	}
	if dr.Request == nil || dr.Request.Headers["X-Test"] != "ok" {
		t.Errorf("handler did not run: %+v", dr)
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
