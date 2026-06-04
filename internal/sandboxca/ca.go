// Package sandboxca generates a per-sandbox ECDSA P-256 certificate
// authority and signs short-lived leaf certificates on demand.
//
// The CA private key never leaves the daemon. Leaf certificates and their
// private keys are issued one per (sandbox, SNI host) and shipped to the
// agent inside the sandbox so the agent can terminate TLS for MITM
// interception. A leaf can only impersonate the host it was issued for,
// and the upstream trust path (real public CAs) is unaffected because we
// only inject our CA into the sandbox's trust bundle, never globally.
//
// The CA uses ECDSA P-256 for ~10× cheaper key generation versus RSA-2048
// (about 5ms vs 50ms on commodity hardware). Leaves are cached in a
// bounded LRU per CA to keep CONNECT handshakes O(1) after warm-up.
package sandboxca

import (
	"bytes"
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// LeafTTL is how long an issued leaf certificate is valid. Short TTLs
// reduce blast radius if a leaf is leaked from sandbox memory.
const LeafTTL = 24 * time.Hour

// CATTL is how long the CA itself is valid. Bounded so a daemon-wide bug
// can't produce certs valid forever.
const CATTL = 30 * 24 * time.Hour

// DefaultLeafCacheSize is the per-CA leaf LRU cap.
const DefaultLeafCacheSize = 256

// CA holds a generated certificate authority and an LRU of issued leaves.
type CA struct {
	cert    *x509.Certificate
	certDER []byte
	certPEM []byte
	key     *ecdsa.PrivateKey

	mu       sync.Mutex
	cache    map[string]*list.Element // hostname -> LRU node
	lru      *list.List
	cacheMax int
}

type leafEntry struct {
	host     string
	certPEM  []byte
	keyPEM   []byte
	expires  time.Time
}

// New generates a fresh CA. sandboxID is embedded in the certificate
// subject to make ops-side debugging easier.
func New(sandboxID string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ca key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate ca serial: %w", err)
	}

	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "sandboxd egress CA " + sandboxID,
			Organization: []string{"sandboxd"},
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(CATTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("self-sign ca: %w", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("re-parse ca: %w", err)
	}

	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return nil, fmt.Errorf("encode ca pem: %w", err)
	}

	return &CA{
		cert:     cert,
		certDER:  der,
		certPEM:  buf.Bytes(),
		key:      key,
		cache:    make(map[string]*list.Element),
		lru:      list.New(),
		cacheMax: DefaultLeafCacheSize,
	}, nil
}

// CertPEM returns the CA's PEM-encoded certificate. Safe to share with
// sandbox processes — it lets them validate leaf certs we issue but does
// not let them impersonate any host.
func (c *CA) CertPEM() []byte { return append([]byte(nil), c.certPEM...) }

// Leaf returns (certPEM, keyPEM) for an end-entity certificate that signs
// for host. Cached and refreshed before LeafTTL/2 expires.
func (c *CA) Leaf(host string) (certPEM, keyPEM []byte, err error) {
	host = normalizeHost(host)

	c.mu.Lock()
	if node, ok := c.cache[host]; ok {
		entry := node.Value.(*leafEntry)
		if time.Now().Before(entry.expires.Add(-LeafTTL / 2)) {
			c.lru.MoveToFront(node)
			out := *entry
			c.mu.Unlock()
			return append([]byte(nil), out.certPEM...), append([]byte(nil), out.keyPEM...), nil
		}
		// Stale: drop and reissue.
		c.lru.Remove(node)
		delete(c.cache, host)
	}
	c.mu.Unlock()

	cert, key, err := c.issue(host)
	if err != nil {
		return nil, nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// A concurrent miss for the same host may have raced ahead and
	// already inserted an entry. Drop it before pushing ours so the
	// list never holds an orphan node that the map can no longer reach
	// (the eviction loop is gated by len(c.cache), so orphans would
	// otherwise accumulate as a slow memory leak).
	if existing, ok := c.cache[host]; ok {
		c.lru.Remove(existing)
		delete(c.cache, host)
	}
	// Evict if at capacity.
	for len(c.cache) >= c.cacheMax {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.lru.Remove(oldest)
		delete(c.cache, oldest.Value.(*leafEntry).host)
	}
	entry := &leafEntry{
		host:    host,
		certPEM: cert,
		keyPEM:  key,
		expires: time.Now().Add(LeafTTL),
	}
	node := c.lru.PushFront(entry)
	c.cache[host] = node

	return append([]byte(nil), cert...), append([]byte(nil), key...), nil
}

func (c *CA) issue(host string) (certPEM, keyPEM []byte, err error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate leaf serial: %w", err)
	}

	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     time.Now().Add(LeafTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &leafKey.PublicKey, c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("sign leaf: %w", err)
	}

	var certBuf bytes.Buffer
	if err := pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return nil, nil, fmt.Errorf("encode leaf cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal leaf key: %w", err)
	}
	var keyBuf bytes.Buffer
	if err := pem.Encode(&keyBuf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return nil, nil, fmt.Errorf("encode leaf key: %w", err)
	}

	return certBuf.Bytes(), keyBuf.Bytes(), nil
}

// CacheSize returns the current number of cached leaves. Useful for tests
// and metrics.
func (c *CA) CacheSize() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cache)
}

// normalizeHost strips an optional port suffix and lowercases the host.
// Handles bare IPv6 literals ("::1"), bracketed IPv6 ("[::1]:443"),
// IPv4 ("10.0.0.1:443"), and DNS names ("Api.Example.COM:443") correctly.
//
// Why not just net.SplitHostPort: SplitHostPort returns an error for
// inputs without a port. We want a port-stripping no-op in that case,
// not an error. We try SplitHostPort first; on error, fall back to the
// raw input.
func normalizeHost(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	// Strip surrounding brackets from IPv6 literals so the cache key for
	// "[::1]:443" and "::1" agree.
	if len(h) >= 2 && h[0] == '[' && h[len(h)-1] == ']' {
		h = h[1 : len(h)-1]
	}
	return strings.ToLower(h)
}
