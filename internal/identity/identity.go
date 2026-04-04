package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header is the standard header used to pass identity from a proxy to sandboxd.
const Header = "X-Sandbox-Identity"

// Limit headers allow a proxy to set per-request enforcement limits.
const (
	HeaderMaxConcurrent = "X-Sandbox-Max-Concurrent" // Max concurrent sandboxes for this identity.
	HeaderMaxTTL        = "X-Sandbox-Max-TTL"        // Max TTL in seconds for this sandbox.
	HeaderMaxTemplates  = "X-Sandbox-Max-Templates"  // Max templates for this identity.
)

// HeaderSignature carries the Ed25519 signature over the identity headers.
const HeaderSignature = "X-Sandbox-Signature"

// HeaderTimestamp carries the Unix epoch second when the proxy signed the request.
const HeaderTimestamp = "X-Sandbox-Timestamp"

// MaxSignatureAge is the maximum age of a signed request before it is rejected.
const MaxSignatureAge = 30 * time.Second

// signedHeaders is the deterministic list of headers included in the signature,
// in the order they are concatenated for signing.
var signedHeaders = []string{
	Header,
	HeaderMaxConcurrent,
	HeaderMaxTTL,
	HeaderMaxTemplates,
	HeaderTimestamp,
}

// Identity represents a caller identity extracted from a request.
type Identity struct {
	Value string
}

// Empty returns true if no identity is set.
func (id Identity) Empty() bool {
	return id.Value == ""
}

// Matches returns true if two identities are the same,
// or if identity enforcement is not active (both empty).
func (id Identity) Matches(other Identity) bool {
	if id.Empty() && other.Empty() {
		return true
	}
	return id.Value == other.Value
}

// Extract reads the identity from the request's X-Sandbox-Identity header.
// Returns an empty Identity if the header is absent (single-user mode).
func Extract(r *http.Request) Identity {
	return Identity{Value: r.Header.Get(Header)}
}

// RequestLimits holds per-request limit overrides sent by the proxy.
// Zero values mean "no override, use global config."
type RequestLimits struct {
	MaxConcurrent int // From X-Sandbox-Max-Concurrent.
	MaxTTL        int // From X-Sandbox-Max-TTL.
	MaxTemplates  int // From X-Sandbox-Max-Templates.
}

// ExtractLimits reads per-request limit headers from the request.
// Negative values are clamped to zero.
func ExtractLimits(r *http.Request) RequestLimits {
	var lim RequestLimits
	if v := r.Header.Get(HeaderMaxConcurrent); v != "" {
		n, _ := strconv.Atoi(v)
		if n > 0 {
			lim.MaxConcurrent = n
		}
	}
	if v := r.Header.Get(HeaderMaxTTL); v != "" {
		n, _ := strconv.Atoi(v)
		if n > 0 {
			lim.MaxTTL = n
		}
	}
	if v := r.Header.Get(HeaderMaxTemplates); v != "" {
		n, _ := strconv.Atoi(v)
		if n > 0 {
			lim.MaxTemplates = n
		}
	}
	return lim
}

// Verifier holds a parsed Ed25519 public key for signature verification.
type Verifier struct {
	publicKey ed25519.PublicKey
}

// NewVerifier parses a base64-encoded Ed25519 public key and returns a Verifier.
func NewVerifier(publicKeyB64 string) (*Verifier, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: got %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	return &Verifier{publicKey: ed25519.PublicKey(raw)}, nil
}

// Verify checks that the request carries a valid Ed25519 signature over
// the method, path, identity, limit, and timestamp headers. Rejects requests
// with missing/expired timestamps or invalid signatures.
func (v *Verifier) Verify(r *http.Request) error {
	sigB64 := r.Header.Get(HeaderSignature)
	if sigB64 == "" {
		return fmt.Errorf("missing %s header", HeaderSignature)
	}

	// Validate timestamp to prevent replay attacks.
	tsStr := r.Header.Get(HeaderTimestamp)
	if tsStr == "" {
		return fmt.Errorf("missing %s header", HeaderTimestamp)
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}
	age := time.Duration(math.Abs(float64(time.Now().Unix()-ts))) * time.Second
	if age > MaxSignatureAge {
		return fmt.Errorf("signature expired: age %s exceeds %s", age, MaxSignatureAge)
	}

	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	payload := buildSignPayload(r)
	if !ed25519.Verify(v.publicKey, payload, sig) {
		return fmt.Errorf("invalid signature")
	}

	return nil
}

// buildSignPayload constructs the byte payload that is signed/verified.
// Format: method\npath\nheader1\nheader2\n... in deterministic order.
// Missing headers contribute an empty line.
func buildSignPayload(r *http.Request) []byte {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte('\n')
	b.WriteString(r.URL.Path)
	for _, h := range signedHeaders {
		b.WriteByte('\n')
		b.WriteString(r.Header.Get(h))
	}
	return []byte(b.String())
}

// Sign sets the timestamp header and produces an Ed25519 signature over the
// method, path, identity, limit, and timestamp headers. This is used by the
// proxy (or tests) to sign outgoing requests.
func Sign(privateKey ed25519.PrivateKey, r *http.Request) string {
	r.Header.Set(HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
	payload := buildSignPayload(r)
	sig := ed25519.Sign(privateKey, payload)
	return base64.StdEncoding.EncodeToString(sig)
}
