package sandbox

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Auth resolves credentials for sandbox connections.
// Implementations must be safe for concurrent use.
type Auth interface {
	// Resolve returns HTTP headers to attach to the connection request.
	Resolve(ctx context.Context) (map[string]string, error)
}

// StringAuth authenticates with a bearer token string.
type StringAuth struct {
	Token string
}

// Resolve returns an Authorization header with the bearer token.
func (a *StringAuth) Resolve(_ context.Context) (map[string]string, error) {
	return map[string]string{
		"Authorization": "Bearer " + a.Token,
	}, nil
}

// HeadersAuth provides static headers for authentication.
type HeadersAuth struct {
	Headers map[string]string
}

// Resolve returns the static headers.
func (a *HeadersAuth) Resolve(_ context.Context) (map[string]string, error) {
	headers := make(map[string]string, len(a.Headers))
	for k, v := range a.Headers {
		headers[k] = v
	}
	return headers, nil
}

// ProviderAuth resolves credentials dynamically via a callback.
type ProviderAuth struct {
	Provider func(ctx context.Context) (map[string]string, error)
}

// Resolve calls the provider function to obtain headers.
func (a *ProviderAuth) Resolve(ctx context.Context) (map[string]string, error) {
	if a.Provider == nil {
		return map[string]string{}, nil
	}
	return a.Provider(ctx)
}

// RequestSigner is an optional interface that Auth implementations can provide
// when the credentials depend on the HTTP method and path (e.g., Ed25519 signatures).
// The SDK checks for this interface and calls ResolveForRequest instead of Resolve
// when available.
type RequestSigner interface {
	ResolveForRequest(ctx context.Context, method, path string) (map[string]string, error)
}

// signedHeaders is the deterministic list of headers included in the signature,
// matching the daemon's identity.signedHeaders order.
var signedHeaders = []string{
	"X-Sandbox-Identity",
	"X-Sandbox-Max-Concurrent",
	"X-Sandbox-Max-TTL",
	"X-Sandbox-Max-Templates",
	"X-Sandbox-Timestamp",
}

// SignatureAuth authenticates requests using Ed25519 signatures over identity headers.
// This is used by backend services (proxies) that need to assert tenant identity
// to a sandboxd daemon running in multi-tenant mode.
type SignatureAuth struct {
	// PrivateKey is the Ed25519 private key used for signing.
	PrivateKey ed25519.PrivateKey
	// Identity is the tenant identity value (set in X-Sandbox-Identity).
	Identity string
	// MaxConcurrent optionally sets X-Sandbox-Max-Concurrent.
	MaxConcurrent int
	// MaxTTL optionally sets X-Sandbox-Max-TTL.
	MaxTTL int
	// MaxTemplates optionally sets X-Sandbox-Max-Templates.
	MaxTemplates int
}

// Resolve returns an error because SignatureAuth requires method/path context.
// Use with SDK functions that support RequestSigner.
func (a *SignatureAuth) Resolve(_ context.Context) (map[string]string, error) {
	return nil, fmt.Errorf("sandbox: SignatureAuth requires per-request signing; it implements RequestSigner")
}

// ResolveForRequest builds identity headers and signs them with the Ed25519 private key.
func (a *SignatureAuth) ResolveForRequest(_ context.Context, method, path string) (map[string]string, error) {
	headers := map[string]string{
		"X-Sandbox-Identity":  a.Identity,
		"X-Sandbox-Timestamp": strconv.FormatInt(time.Now().Unix(), 10),
	}
	if a.MaxConcurrent > 0 {
		headers["X-Sandbox-Max-Concurrent"] = strconv.Itoa(a.MaxConcurrent)
	}
	if a.MaxTTL > 0 {
		headers["X-Sandbox-Max-TTL"] = strconv.Itoa(a.MaxTTL)
	}
	if a.MaxTemplates > 0 {
		headers["X-Sandbox-Max-Templates"] = strconv.Itoa(a.MaxTemplates)
	}

	// Build signature payload: method\npath\nheader1\nheader2\n...
	var b strings.Builder
	b.WriteString(method)
	b.WriteByte('\n')
	b.WriteString(path)
	for _, h := range signedHeaders {
		b.WriteByte('\n')
		b.WriteString(headers[h])
	}

	sig := ed25519.Sign(a.PrivateKey, []byte(b.String()))
	headers["X-Sandbox-Signature"] = base64.StdEncoding.EncodeToString(sig)
	return headers, nil
}
