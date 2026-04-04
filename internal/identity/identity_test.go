package identity

import (
	"net/http/httptest"
	"testing"
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
}
