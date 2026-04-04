package sandbox

import "context"

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
