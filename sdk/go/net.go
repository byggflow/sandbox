package sandbox

import "context"

// FetchOptions configures an outbound HTTP request from the sandbox.
type FetchOptions struct {
	Method   string            `json:"method,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     []byte            `json:"body,omitempty"`
	Redirect string            `json:"redirect,omitempty"`
}

// FetchResult holds the response from a fetch operation.
type FetchResult struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	Body       []byte            `json:"body"`
	StatusText string            `json:"statusText"`
}

// NetCategory provides network operations on a sandbox.
type NetCategory struct {
	cc *callContext
}

// Fetch makes an HTTP request from inside the sandbox.
func (n *NetCategory) Fetch(ctx context.Context, url string, opts *FetchOptions) (*FetchResult, error) {
	params := map[string]interface{}{"url": url}
	if opts != nil {
		if opts.Method != "" {
			params["method"] = opts.Method
		}
		if opts.Headers != nil {
			params["headers"] = opts.Headers
		}
		if opts.Body != nil {
			params["body"] = opts.Body
		}
		if opts.Redirect != "" {
			params["redirect"] = opts.Redirect
		}
	}
	result, err := call(ctx, n.cc, op{
		Method: "net.fetch",
		Params: params,
	})
	if err != nil {
		return nil, err
	}
	if m, ok := result.(map[string]interface{}); ok {
		fr := &FetchResult{}
		if v, ok := m["status"].(float64); ok {
			fr.Status = int(v)
		}
		if v, ok := m["statusText"].(string); ok {
			fr.StatusText = v
		}
		if v, ok := m["headers"].(map[string]interface{}); ok {
			fr.Headers = make(map[string]string, len(v))
			for k, val := range v {
				if s, ok := val.(string); ok {
					fr.Headers[k] = s
				}
			}
		}
		if v, ok := m["body"].(string); ok {
			fr.Body = []byte(v)
		}
		return fr, nil
	}
	return nil, &SandboxError{Message: "unexpected response type for net.fetch"}
}
