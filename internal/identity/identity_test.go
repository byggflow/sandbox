package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestExtract(t *testing.T) {
	t.Run("with header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set(Header, "cust_123")
		id := Extract(req)
		if id.Value != "cust_123" {
			t.Errorf("expected cust_123, got %s", id.Value)
		}
		if id.Empty() {
			t.Error("expected non-empty")
		}
	})

	t.Run("without header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		id := Extract(req)
		if !id.Empty() {
			t.Errorf("expected empty, got %s", id.Value)
		}
	})
}

func TestIdentityMatches(t *testing.T) {
	a := Identity{Value: "alice"}
	b := Identity{Value: "bob"}
	empty := Identity{}

	if !a.Matches(a) {
		t.Error("same identity should match")
	}
	if a.Matches(b) {
		t.Error("different identities should not match")
	}
	if !empty.Matches(empty) {
		t.Error("two empty identities should match (single-user)")
	}
	if empty.Matches(a) {
		t.Error("empty should not match non-empty")
	}
}

func TestExtractLimits(t *testing.T) {
	t.Run("all headers", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/sandboxes", nil)
		req.Header.Set(HeaderMaxConcurrent, "5")
		req.Header.Set(HeaderMaxTTL, "1800")
		req.Header.Set(HeaderMaxTemplates, "20")

		lim := ExtractLimits(req)
		if lim.MaxConcurrent != 5 {
			t.Errorf("expected MaxConcurrent=5, got %d", lim.MaxConcurrent)
		}
		if lim.MaxTTL != 1800 {
			t.Errorf("expected MaxTTL=1800, got %d", lim.MaxTTL)
		}
		if lim.MaxTemplates != 20 {
			t.Errorf("expected MaxTemplates=20, got %d", lim.MaxTemplates)
		}
	})

	t.Run("no headers", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/sandboxes", nil)
		lim := ExtractLimits(req)
		if lim.MaxConcurrent != 0 || lim.MaxTTL != 0 || lim.MaxTemplates != 0 {
			t.Errorf("expected all zeros, got %+v", lim)
		}
	})

	t.Run("invalid values ignored", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/sandboxes", nil)
		req.Header.Set(HeaderMaxConcurrent, "abc")
		lim := ExtractLimits(req)
		if lim.MaxConcurrent != 0 {
			t.Errorf("expected 0 for invalid value, got %d", lim.MaxConcurrent)
		}
	})

	t.Run("negative values clamped to zero", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/sandboxes", nil)
		req.Header.Set(HeaderMaxConcurrent, "-5")
		req.Header.Set(HeaderMaxTTL, "-100")
		req.Header.Set(HeaderMaxTemplates, "-1")
		lim := ExtractLimits(req)
		if lim.MaxConcurrent != 0 {
			t.Errorf("expected 0 for negative MaxConcurrent, got %d", lim.MaxConcurrent)
		}
		if lim.MaxTTL != 0 {
			t.Errorf("expected 0 for negative MaxTTL, got %d", lim.MaxTTL)
		}
		if lim.MaxTemplates != 0 {
			t.Errorf("expected 0 for negative MaxTemplates, got %d", lim.MaxTemplates)
		}
	})
}

// generateTestKeypair creates an Ed25519 keypair for testing.
func generateTestKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestNewVerifier(t *testing.T) {
	pub, _ := generateTestKeypair(t)

	t.Run("valid key", func(t *testing.T) {
		b64 := base64.StdEncoding.EncodeToString(pub)
		v, err := NewVerifier(b64)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v == nil {
			t.Fatal("expected non-nil verifier")
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		_, err := NewVerifier("not-valid-base64!!!")
		if err == nil {
			t.Error("expected error")
		}
	})

	t.Run("wrong key size", func(t *testing.T) {
		_, err := NewVerifier(base64.StdEncoding.EncodeToString([]byte("tooshort")))
		if err == nil {
			t.Error("expected error for wrong key size")
		}
	})
}

func TestVerifyValidSignature(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	b64 := base64.StdEncoding.EncodeToString(pub)
	v, err := NewVerifier(b64)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/sandboxes", nil)
	req.Header.Set(Header, "cust_123")
	req.Header.Set(HeaderMaxConcurrent, "50")
	req.Header.Set(HeaderMaxTTL, "86400")

	// Sign the request.
	sig := Sign(priv, req)
	req.Header.Set(HeaderSignature, sig)

	if err := v.Verify(req); err != nil {
		t.Errorf("expected valid signature, got: %v", err)
	}
}

func TestVerifyTamperedHeader(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	b64 := base64.StdEncoding.EncodeToString(pub)
	v, _ := NewVerifier(b64)

	req := httptest.NewRequest("POST", "/sandboxes", nil)
	req.Header.Set(Header, "cust_123")
	req.Header.Set(HeaderMaxConcurrent, "50")

	sig := Sign(priv, req)
	req.Header.Set(HeaderSignature, sig)

	// Tamper with the identity after signing.
	req.Header.Set(Header, "cust_evil")

	if err := v.Verify(req); err == nil {
		t.Error("expected signature verification to fail after tampering")
	}
}

func TestVerifyTamperedLimits(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	b64 := base64.StdEncoding.EncodeToString(pub)
	v, _ := NewVerifier(b64)

	req := httptest.NewRequest("POST", "/sandboxes", nil)
	req.Header.Set(Header, "cust_123")
	req.Header.Set(HeaderMaxConcurrent, "5")

	sig := Sign(priv, req)
	req.Header.Set(HeaderSignature, sig)

	// Tamper with the limit after signing.
	req.Header.Set(HeaderMaxConcurrent, "999999")

	if err := v.Verify(req); err == nil {
		t.Error("expected signature verification to fail after limit tampering")
	}
}

func TestVerifyMissingSignature(t *testing.T) {
	pub, _ := generateTestKeypair(t)
	b64 := base64.StdEncoding.EncodeToString(pub)
	v, _ := NewVerifier(b64)

	req := httptest.NewRequest("POST", "/sandboxes", nil)
	req.Header.Set(Header, "cust_123")

	if err := v.Verify(req); err == nil {
		t.Error("expected error for missing signature")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	pub1, _ := generateTestKeypair(t)
	_, priv2 := generateTestKeypair(t)

	b64 := base64.StdEncoding.EncodeToString(pub1)
	v, _ := NewVerifier(b64)

	req := httptest.NewRequest("POST", "/sandboxes", nil)
	req.Header.Set(Header, "cust_123")

	// Sign with a different private key.
	sig := Sign(priv2, req)
	req.Header.Set(HeaderSignature, sig)

	if err := v.Verify(req); err == nil {
		t.Error("expected error when signed with wrong key")
	}
}

func TestSignDeterministic(t *testing.T) {
	_, priv := generateTestKeypair(t)

	req1 := httptest.NewRequest("POST", "/sandboxes", nil)
	req1.Header.Set(Header, "cust_123")
	req1.Header.Set(HeaderMaxConcurrent, "5")

	req2 := httptest.NewRequest("POST", "/sandboxes", nil)
	req2.Header.Set(Header, "cust_123")
	req2.Header.Set(HeaderMaxConcurrent, "5")

	sig1 := Sign(priv, req1)
	sig2 := Sign(priv, req2)

	// Ed25519 signatures are deterministic.
	if sig1 != sig2 {
		t.Error("expected identical signatures for identical headers")
	}
}

func TestVerifyExpiredTimestamp(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	b64 := base64.StdEncoding.EncodeToString(pub)
	v, _ := NewVerifier(b64)

	req := httptest.NewRequest("POST", "/sandboxes", nil)
	req.Header.Set(Header, "cust_123")

	// Sign sets a current timestamp, override it with an old one after signing.
	sig := Sign(priv, req)
	req.Header.Set(HeaderSignature, sig)

	// Set timestamp to 60 seconds ago — well past the 30s window.
	// We need to re-sign with the old timestamp to get a valid signature for the stale payload.
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(time.Now().Unix()-60, 10))
	sig = signRaw(priv, req)
	req.Header.Set(HeaderSignature, sig)

	if err := v.Verify(req); err == nil {
		t.Error("expected error for expired timestamp")
	}
}

func TestVerifyMissingTimestamp(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	b64 := base64.StdEncoding.EncodeToString(pub)
	v, _ := NewVerifier(b64)

	req := httptest.NewRequest("POST", "/sandboxes", nil)
	req.Header.Set(Header, "cust_123")

	// Manually sign without setting a timestamp.
	payload := buildSignPayload(req)
	sig := ed25519.Sign(priv, payload)
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(sig))

	if err := v.Verify(req); err == nil {
		t.Error("expected error for missing timestamp")
	}
}

func TestVerifyTamperedMethod(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	b64 := base64.StdEncoding.EncodeToString(pub)
	v, _ := NewVerifier(b64)

	req := httptest.NewRequest("POST", "/sandboxes", nil)
	req.Header.Set(Header, "cust_123")

	sig := Sign(priv, req)
	req.Header.Set(HeaderSignature, sig)

	// Tamper: change method from POST to DELETE.
	req.Method = "DELETE"

	if err := v.Verify(req); err == nil {
		t.Error("expected signature verification to fail after method tampering")
	}
}

func TestVerifyTamperedPath(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	b64 := base64.StdEncoding.EncodeToString(pub)
	v, _ := NewVerifier(b64)

	req := httptest.NewRequest("POST", "/sandboxes", nil)
	req.Header.Set(Header, "cust_123")

	sig := Sign(priv, req)
	req.Header.Set(HeaderSignature, sig)

	// Tamper: change path.
	req.URL.Path = "/templates"

	if err := v.Verify(req); err == nil {
		t.Error("expected signature verification to fail after path tampering")
	}
}

// signRaw signs the request payload without modifying the timestamp header.
// Used by tests that need to sign with a pre-set (e.g. expired) timestamp.
func signRaw(privateKey ed25519.PrivateKey, r *http.Request) string {
	payload := buildSignPayload(r)
	sig := ed25519.Sign(privateKey, payload)
	return base64.StdEncoding.EncodeToString(sig)
}
