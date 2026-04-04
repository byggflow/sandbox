package sandbox

import (
	"errors"
	"testing"
)

func TestSandboxError(t *testing.T) {
	err := &SandboxError{Message: "something failed"}
	if err.Error() != "something failed" {
		t.Fatalf("unexpected message: %s", err.Error())
	}
}

func TestConnectionError(t *testing.T) {
	err := &ConnectionError{SandboxError: SandboxError{Message: "refused"}}
	if err.Error() != "connection error: refused" {
		t.Fatalf("unexpected message: %s", err.Error())
	}

	var sandboxErr *SandboxError
	if !errors.As(err, &sandboxErr) {
		t.Fatal("ConnectionError should unwrap to SandboxError")
	}
}

func TestRpcError(t *testing.T) {
	err := &RpcError{SandboxError: SandboxError{Message: "not found"}, Code: -32601}
	if err.Error() != "rpc error -32601: not found" {
		t.Fatalf("unexpected message: %s", err.Error())
	}
	if err.Code != -32601 {
		t.Fatalf("unexpected code: %d", err.Code)
	}

	var sandboxErr *SandboxError
	if !errors.As(err, &sandboxErr) {
		t.Fatal("RpcError should unwrap to SandboxError")
	}
}

func TestTimeoutError(t *testing.T) {
	err := &TimeoutError{SandboxError: SandboxError{Message: "exceeded 30s"}}
	if err.Error() != "timeout: exceeded 30s" {
		t.Fatalf("unexpected message: %s", err.Error())
	}

	var sandboxErr *SandboxError
	if !errors.As(err, &sandboxErr) {
		t.Fatal("TimeoutError should unwrap to SandboxError")
	}
}

func TestFsError(t *testing.T) {
	err := &FsError{SandboxError: SandboxError{Message: "file not found"}, FsCode: "ENOENT"}
	if err.Error() != "fs error [ENOENT]: file not found" {
		t.Fatalf("unexpected message: %s", err.Error())
	}
	if err.FsCode != "ENOENT" {
		t.Fatalf("unexpected fs code: %s", err.FsCode)
	}

	var sandboxErr *SandboxError
	if !errors.As(err, &sandboxErr) {
		t.Fatal("FsError should unwrap to SandboxError")
	}
}

func TestCapacityError(t *testing.T) {
	err := &CapacityError{SandboxError: SandboxError{Message: "no slots"}, RetryAfter: 2}
	if err.Error() != "capacity error (retry after 2s): no slots" {
		t.Fatalf("unexpected message: %s", err.Error())
	}
	if err.RetryAfter != 2 {
		t.Fatalf("unexpected retry after: %d", err.RetryAfter)
	}

	var sandboxErr *SandboxError
	if !errors.As(err, &sandboxErr) {
		t.Fatal("CapacityError should unwrap to SandboxError")
	}
}

func TestSessionReplacedError(t *testing.T) {
	err := NewSessionReplacedError()
	if err.Error() != "session replaced by a new connection" {
		t.Fatalf("unexpected message: %s", err.Error())
	}

	var connErr *ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatal("SessionReplacedError should unwrap to ConnectionError")
	}

	var sandboxErr *SandboxError
	if !errors.As(err, &sandboxErr) {
		t.Fatal("SessionReplacedError should unwrap to SandboxError")
	}
}

func TestErrorIs(t *testing.T) {
	base := &SandboxError{Message: "base"}
	conn := &ConnectionError{SandboxError: *base}
	replaced := &SessionReplacedError{ConnectionError: *conn}

	// errors.Is checks identity, not type. Use errors.As for type checks.
	var target *SessionReplacedError
	if !errors.As(replaced, &target) {
		t.Fatal("should match SessionReplacedError")
	}
}
