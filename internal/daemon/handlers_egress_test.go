package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/byggflow/sandbox/internal/netegress"
)

// TestRejectEncryptedParams confirms the daemon refuses E2E-encrypted
// params on its network-middleware methods. Regression for the silent-
// data-loss bug where {_encrypted: "..."} unmarshalled into the rules
// shape produced an empty rule list (clearing all rules) or an empty
// fetch URL.
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
