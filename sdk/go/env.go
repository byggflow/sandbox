package sandbox

import "context"

// EnvCategory provides environment variable operations on a sandbox.
type EnvCategory struct {
	cc *callContext
}

// Get returns the value of an environment variable, or empty string if not set.
func (e *EnvCategory) Get(ctx context.Context, key string) (string, error) {
	result, err := call(ctx, e.cc, op{
		Method: "env.get",
		Params: map[string]interface{}{"key": key},
	})
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	if s, ok := result.(string); ok {
		return s, nil
	}
	return "", &SandboxError{Message: "unexpected response type for env.get"}
}

// Set sets an environment variable.
func (e *EnvCategory) Set(ctx context.Context, key, value string) error {
	_, err := call(ctx, e.cc, op{
		Method: "env.set",
		Params: map[string]interface{}{"key": key, "value": value},
	})
	return err
}

// Delete removes an environment variable.
func (e *EnvCategory) Delete(ctx context.Context, key string) error {
	_, err := call(ctx, e.cc, op{
		Method: "env.delete",
		Params: map[string]interface{}{"key": key},
	})
	return err
}

// List returns all environment variables as a map.
func (e *EnvCategory) List(ctx context.Context) (map[string]string, error) {
	result, err := call(ctx, e.cc, op{
		Method: "env.list",
		Params: nil,
	})
	if err != nil {
		return nil, err
	}
	if m, ok := result.(map[string]interface{}); ok {
		env := make(map[string]string, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				env[k] = s
			}
		}
		return env, nil
	}
	return nil, &SandboxError{Message: "unexpected response type for env.list"}
}
