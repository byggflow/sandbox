package sandbox

import "context"

// NotificationHandler handles incoming JSON-RPC notifications.
type NotificationHandler func(method string, params interface{})

// ReplacedHandler handles session replaced events.
type ReplacedHandler func()

// RpcTransport defines the interface for JSON-RPC communication with the daemon.
type RpcTransport interface {
	// Call sends a JSON-RPC request and waits for the response.
	Call(ctx context.Context, method string, params interface{}) (interface{}, error)
	// CallWithBinary sends a JSON-RPC request followed by binary frame(s), then waits for the response.
	CallWithBinary(ctx context.Context, method string, params interface{}, data []byte) (interface{}, error)
	// CallExpectBinary sends a JSON-RPC request and collects binary frames before the JSON response.
	CallExpectBinary(ctx context.Context, method string, params interface{}) (interface{}, [][]byte, error)
	// SendBinary sends raw binary data over the transport.
	SendBinary(ctx context.Context, data []byte) error
	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, method string, params interface{}) error
	// OnNotification registers a handler for incoming notifications.
	OnNotification(handler NotificationHandler)
	// OnReplaced registers a handler for session replaced events.
	OnReplaced(handler ReplacedHandler)
	// Close shuts down the transport connection.
	Close() error
}

// stubTransport is a transport that returns errors for all operations.
// Used as a placeholder until the real WebSocket transport is implemented.
type stubTransport struct{}

func (t *stubTransport) Call(_ context.Context, _ string, _ interface{}) (interface{}, error) {
	return nil, &SandboxError{Message: "transport not implemented"}
}

func (t *stubTransport) CallWithBinary(_ context.Context, _ string, _ interface{}, _ []byte) (interface{}, error) {
	return nil, &SandboxError{Message: "transport not implemented"}
}

func (t *stubTransport) CallExpectBinary(_ context.Context, _ string, _ interface{}) (interface{}, [][]byte, error) {
	return nil, nil, &SandboxError{Message: "transport not implemented"}
}

func (t *stubTransport) SendBinary(_ context.Context, _ []byte) error {
	return &SandboxError{Message: "transport not implemented"}
}

func (t *stubTransport) Notify(_ context.Context, _ string, _ interface{}) error {
	return &SandboxError{Message: "transport not implemented"}
}

func (t *stubTransport) OnNotification(_ NotificationHandler) {}

func (t *stubTransport) OnReplaced(_ ReplacedHandler) {}

func (t *stubTransport) Close() error { return nil }
