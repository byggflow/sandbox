package identity

import (
	"net/http"
	"strconv"
)

// Header is the standard header used to pass identity from a proxy to sandboxd.
const Header = "X-Sandbox-Identity"

// Limit headers allow a proxy to set per-request enforcement limits.
const (
	HeaderMaxConcurrent = "X-Sandbox-Max-Concurrent" // Max concurrent sandboxes for this identity.
	HeaderMaxTTL        = "X-Sandbox-Max-TTL"        // Max TTL in seconds for this sandbox.
	HeaderMaxTemplates  = "X-Sandbox-Max-Templates"  // Max templates for this identity.
)

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
func ExtractLimits(r *http.Request) RequestLimits {
	var lim RequestLimits
	if v := r.Header.Get(HeaderMaxConcurrent); v != "" {
		lim.MaxConcurrent, _ = strconv.Atoi(v)
	}
	if v := r.Header.Get(HeaderMaxTTL); v != "" {
		lim.MaxTTL, _ = strconv.Atoi(v)
	}
	if v := r.Header.Get(HeaderMaxTemplates); v != "" {
		lim.MaxTemplates, _ = strconv.Atoi(v)
	}
	return lim
}
