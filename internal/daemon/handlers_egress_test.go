package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/byggflow/sandbox/internal/netegress"
	"github.com/byggflow/sandbox/internal/netrules"
)

// TestRejectEncryptedParams confirms the daemon refuses E2E-encrypted
// params on rules.set. (net.fetch uses isEncryptedParams in the claim
// check to FORWARD encrypted requests to the agent's E2E handler
// instead of rejecting them, so rejectEncryptedParams there is just
// defense in depth.) Regression for the silent-data-loss bug where
// {_encrypted: "..."} unmarshalled into the rules shape produced an
// empty rule list (clearing all rules).
func TestRejectEncryptedParams(t *testing.T) {
	encrypted := json.RawMessage(`{"_encrypted":"AAECAwQF==base64ciphertext"}`)
	plain := json.RawMessage(`{"rules":[]}`)

	if err := rejectEncryptedParams(encrypted, "net.rules.set"); err == nil {
		t.Error("expected error for encrypted params")
	} else if !strings.Contains(err.Error(), "net.rules.set") {
		t.Errorf("error should name the method, got: %v", err)
	}
	if err := rejectEncryptedParams(plain, "net.rules.set"); err != nil {
		t.Errorf("plain params should pass: %v", err)
	}
	// Malformed JSON: don't reject (the actual decoder will catch it
	// and return a more useful error).
	if err := rejectEncryptedParams(json.RawMessage(`not-json`), "net.rules.set"); err != nil {
		t.Errorf("malformed params shouldn't be rejected by the encryption guard: %v", err)
	}
}

// TestClientLocalMethodsForwardsEncryptedFetch confirms the daemon
// declines to claim net.fetch when params are E2E-encrypted, so the
// frame forwards to the agent's E2E-decrypted handler. Without this,
// encrypted sandboxes lost sbx.net.fetch (the previous "always claim"
// behavior fired rejectEncryptedParams and surfaced 502).
func TestClientLocalMethodsForwardsEncryptedFetch(t *testing.T) {
	d := &Daemon{Egress: netegress.New()}
	defer d.Egress.Close()
	owns := d.clientLocalMethodsFor("sbx-1")

	plain := json.RawMessage(`{"url":"https://api.example.com/"}`)
	encrypted := json.RawMessage(`{"_encrypted":"ciphertext"}`)

	if !owns("net.fetch", plain) {
		t.Error("plain net.fetch must be claimed by daemon (rule application path)")
	}
	if owns("net.fetch", encrypted) {
		t.Error("encrypted net.fetch must NOT be claimed (forwards to agent for E2E decrypt)")
	}
	if !owns("net.rules.set", plain) {
		t.Error("net.rules.set must be claimed; rejectEncryptedParams handles the encrypted case")
	}
}

func TestServeRulesSetRefusesEncrypted(t *testing.T) {
	d := &Daemon{Egress: netegress.New()}
	defer d.Egress.Close()
	encrypted := json.RawMessage(`{"_encrypted":"x"}`)
	_, err := d.serveRulesSet(context.Background(), "sbx-1", encrypted)
	if err == nil {
		t.Fatal("expected error; encrypted params silently cleared rules in the buggy version")
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("error should explain E2E incompatibility, got: %v", err)
	}
}

func TestNetworkModeOffDisablesDaemonEgressHooks(t *testing.T) {
	d := &Daemon{Egress: netegress.New(), Registry: NewRegistry()}
	defer d.Egress.Close()
	if err := d.Registry.Add(&Sandbox{ID: "sbx-off", EgressEnabled: false}); err != nil {
		t.Fatal(err)
	}

	owns := d.clientLocalMethodsFor("sbx-off")
	if owns("net.fetch", json.RawMessage(`{"url":"https://example.com"}`)) {
		t.Fatal("net.fetch should forward to the agent when network_mode=off")
	}
	if !owns("net.rules.set", json.RawMessage(`{"rules":[]}`)) {
		t.Fatal("net.rules.set should still be claimed so the daemon can reject rule installs")
	}

	_, err := d.serveRulesSet(context.Background(), "sbx-off", mustJSON(t, map[string]interface{}{
		"rules": []netrules.Rule{{ID: "deny", Action: netrules.ActionDeny}},
	}))
	if err == nil || !strings.Contains(err.Error(), "network_mode=off") {
		t.Fatalf("expected network_mode=off error, got %v", err)
	}

	if _, err := d.serveRulesSet(context.Background(), "sbx-off", json.RawMessage(`{"rules":[]}`)); err != nil {
		t.Fatalf("clearing rules should remain allowed: %v", err)
	}
}

func mustJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
