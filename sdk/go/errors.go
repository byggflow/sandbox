package sandbox

import "fmt"

// SandboxError is the base error type for all sandbox SDK errors.
type SandboxError struct {
	Message string
}

func (e *SandboxError) Error() string {
	return e.Message
}

// ConnectionError indicates a failure to connect to the daemon.
type ConnectionError struct {
	SandboxError
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("connection error: %s", e.Message)
}

func (e *ConnectionError) Unwrap() error {
	return &e.SandboxError
}

// RpcError indicates a JSON-RPC error returned by the daemon.
type RpcError struct {
	SandboxError
	Code int
}

func (e *RpcError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

func (e *RpcError) Unwrap() error {
	return &e.SandboxError
}

// TimeoutError indicates an operation exceeded its deadline.
type TimeoutError struct {
	SandboxError
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("timeout: %s", e.Message)
}

func (e *TimeoutError) Unwrap() error {
	return &e.SandboxError
}

// FsError indicates a filesystem operation failure.
type FsError struct {
	SandboxError
	FsCode string
}

func (e *FsError) Error() string {
	return fmt.Sprintf("fs error [%s]: %s", e.FsCode, e.Message)
}

func (e *FsError) Unwrap() error {
	return &e.SandboxError
}

// CapacityError indicates the daemon has no available capacity.
type CapacityError struct {
	SandboxError
	RetryAfter int
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("capacity error (retry after %ds): %s", e.RetryAfter, e.Message)
}

func (e *CapacityError) Unwrap() error {
	return &e.SandboxError
}

// SessionReplacedError indicates the current session was replaced by a new connection.
type SessionReplacedError struct {
	ConnectionError
}

func (e *SessionReplacedError) Error() string {
	return "session replaced by a new connection"
}

func (e *SessionReplacedError) Unwrap() error {
	return &e.ConnectionError
}

// NewSessionReplacedError creates a SessionReplacedError.
func NewSessionReplacedError() *SessionReplacedError {
	return &SessionReplacedError{
		ConnectionError: ConnectionError{
			SandboxError: SandboxError{Message: "session replaced by a new connection"},
		},
	}
}
