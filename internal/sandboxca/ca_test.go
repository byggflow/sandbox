package sandboxca

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"
)

func TestNewCAProducesValidCert(t *testing.T) {
	ca, err := New("sbx-test")
	if err != nil {
		t.Fatal(err)
	}
	if !ca.cert.IsCA {
		t.Error("certificate is not marked CA")
	}
	if ca.cert.NotAfter.Before(time.Now()) {
		t.Error("CA NotAfter is in the past")
	}
}

func TestLeafSignsForHost(t *testing.T) {
	ca, err := New("sbx-test")
	if err != nil {
		t.Fatal(err)
	}

	certPEM, keyPEM, err := ca.Leaf("api.example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Parse the leaf and verify it's signed by the CA.
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSName:   "api.example.com",
	}); err != nil {
		t.Errorf("verify leaf: %v", err)
	}
}

func TestLeafCacheHits(t *testing.T) {
	ca, err := New("sbx-test")
	if err != nil {
		t.Fatal(err)
	}

	c1, k1, err := ca.Leaf("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	c2, k2, err := ca.Leaf("api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	// Same PEM bytes (the LRU returned a cached copy).
	if string(c1) != string(c2) || string(k1) != string(k2) {
		t.Error("expected cached leaf to be reused")
	}

	if ca.CacheSize() != 1 {
		t.Errorf("expected cache size 1, got %d", ca.CacheSize())
	}
}

func TestLeafCacheEviction(t *testing.T) {
	ca, err := New("sbx-test")
	if err != nil {
		t.Fatal(err)
	}
	ca.cacheMax = 3

	for _, host := range []string{"a.test", "b.test", "c.test", "d.test"} {
		if _, _, err := ca.Leaf(host); err != nil {
			t.Fatal(err)
		}
	}

	if got := ca.CacheSize(); got != 3 {
		t.Errorf("expected cache size 3, got %d", got)
	}
}

func TestHostNormalization(t *testing.T) {
	cases := map[string]string{
		"Api.Example.COM":     "api.example.com",
		"api.example.com:443": "api.example.com",
		"[::1]:443":           "::1",
		"::1":                 "::1",
		"[fe80::1]:8080":      "fe80::1",
		"127.0.0.1:9000":      "127.0.0.1",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q): got %q want %q", in, got, want)
		}
	}
}
