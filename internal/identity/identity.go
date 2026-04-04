package identity

import (
	"net/http"
)

// Identity represents a caller identity extracted from a request.
type Identity struct {
	Value string
}

// Extractor reads the caller identity from HTTP requests.
type Extractor struct {
	// Header is the HTTP header name to read identity from.
	// If empty, identity is not enforced (single-user mode).
	Header string

	// SystemIdentity is the value treated as the system/operator identity.
	SystemIdentity string
}

// Extract reads the identity from the request.
// Returns an empty Identity if no header is configured (single-user mode).
func (e *Extractor) Extract(r *http.Request) Identity {
	if e.Header == "" {
		return Identity{}
	}
	return Identity{Value: r.Header.Get(e.Header)}
}

// Required returns true if identity enforcement is enabled.
func (e *Extractor) Required() bool {
	return e.Header != ""
}

// IsSystem returns true if this identity is the system/operator identity.
func (id Identity) IsSystem() bool {
	return id.Value != "" && id.Value == "_system"
}

// IsSystemWith checks against a specific system identity value.
func (id Identity) IsSystemWith(systemID string) bool {
	return id.Value != "" && id.Value == systemID
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
