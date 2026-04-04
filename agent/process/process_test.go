package process

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
)

func TestExecSimple(t *testing.T) {
	params, _ := json.Marshal(ExecParams{Command: "echo hello"})
	result, err := Exec(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	r := result.(*ExecResult)
	if r.Stdout != "hello\n" {
		t.Errorf("stdout = %q, want %q", r.Stdout, "hello\n")
	}
	if r.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", r.ExitCode)
	}
}

func TestExecNonZeroExit(t *testing.T) {
	params, _ := json.Marshal(ExecParams{Command: "exit 42"})
	result, err := Exec(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	r := result.(*ExecResult)
	if r.ExitCode != 42 {
		t.Errorf("exit_code = %d, want 42", r.ExitCode)
	}
}

func TestExecStderr(t *testing.T) {
	params, _ := json.Marshal(ExecParams{Command: "echo err >&2"})
	result, err := Exec(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	r := result.(*ExecResult)
	if r.Stderr != "err\n" {
		t.Errorf("stderr = %q, want %q", r.Stderr, "err\n")
	}
}

func TestExecWithCwd(t *testing.T) {
	params, _ := json.Marshal(ExecParams{Command: "pwd", Cwd: "/tmp"})
	result, err := Exec(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	r := result.(*ExecResult)
	// /tmp might resolve to /private/tmp on macOS
	if r.Stdout != "/tmp\n" && r.Stdout != "/private/tmp\n" {
		t.Errorf("stdout = %q, want /tmp or /private/tmp", r.Stdout)
	}
}

func TestExecWithEnv(t *testing.T) {
	params, _ := json.Marshal(ExecParams{
		Command: "echo $MY_VAR",
		Env:     map[string]string{"MY_VAR": "test_value"},
	})
	result, err := Exec(json.RawMessage(params))
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	r := result.(*ExecResult)
	if r.Stdout != "test_value\n" {
		t.Errorf("stdout = %q, want %q", r.Stdout, "test_value\n")
	}
}

func TestExecTimeout(t *testing.T) {
	params, _ := json.Marshal(ExecParams{Command: "sleep 10", Timeout: 1})
	_, err := Exec(json.RawMessage(params))
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestExecEmptyCommand(t *testing.T) {
	params, _ := json.Marshal(ExecParams{Command: ""})
	_, err := Exec(json.RawMessage(params))
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestSafeBufferTruncation(t *testing.T) {
	buf := &safeBuffer{}

	// Write exactly maxOutputSize bytes.
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = 'A'
	}

	writes := maxOutputSize / len(chunk)
	for i := 0; i < writes; i++ {
		n, err := buf.Write(chunk)
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if n != len(chunk) {
			t.Fatalf("write %d: n=%d, want %d", i, n, len(chunk))
		}
	}

	if len(buf.Bytes()) != maxOutputSize {
		t.Fatalf("buffer size = %d, want %d", len(buf.Bytes()), maxOutputSize)
	}

	// Next write should be accepted (returns len(p)) but data is truncated.
	n, err := buf.Write(chunk)
	if err != nil {
		t.Fatalf("overflow write: %v", err)
	}
	if n != len(chunk) {
		t.Fatalf("overflow write: n=%d, want %d", n, len(chunk))
	}

	// Buffer should not have grown beyond maxOutputSize.
	if len(buf.Bytes()) != maxOutputSize {
		t.Fatalf("buffer grew past max: %d", len(buf.Bytes()))
	}
	if !buf.truncated {
		t.Fatal("expected truncated flag to be set")
	}
}

func TestSafeBufferPartialTruncation(t *testing.T) {
	buf := &safeBuffer{}

	// Fill to almost full.
	almostFull := make([]byte, maxOutputSize-100)
	buf.Write(almostFull)

	// Write 200 bytes — only first 100 should be kept.
	overflow := make([]byte, 200)
	for i := range overflow {
		overflow[i] = 'B'
	}
	buf.Write(overflow)

	if len(buf.Bytes()) != maxOutputSize {
		t.Fatalf("buffer size = %d, want %d", len(buf.Bytes()), maxOutputSize)
	}
	if !buf.truncated {
		t.Fatal("expected truncated flag")
	}
}

// safeWriter is a thread-safe writer for testing spawn output.
type safeWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *safeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *safeWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]byte, w.buf.Len())
	copy(cp, w.buf.Bytes())
	return cp
}

func TestSpawnAndExit(t *testing.T) {
	mgr := NewManager()
	conn := &safeWriter{}

	params, _ := json.Marshal(SpawnParams{Command: "echo spawned"})
	result, err := mgr.Spawn(json.RawMessage(params), conn)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	m := result.(map[string]interface{})
	pid := m["pid"].(int)
	if pid <= 0 {
		t.Errorf("pid = %d, want > 0", pid)
	}

	// Wait for process to finish
	time.Sleep(500 * time.Millisecond)

	// Read notifications from conn
	data := conn.Bytes()
	buf := bytes.NewBuffer(data)

	foundStdout := false
	foundExit := false

	for buf.Len() > 0 {
		frame, err := codec.ReadFrame(buf)
		if err != nil {
			break
		}
		if frame.Type != proto.FrameJSON {
			continue
		}

		var notif proto.Notification
		json.Unmarshal(frame.Payload, &notif)

		switch notif.Method {
		case "process.stdout":
			params := notif.Params.(map[string]interface{})
			decoded, _ := base64.StdEncoding.DecodeString(params["data"].(string))
			if string(decoded) == "spawned\n" {
				foundStdout = true
			}
		case "process.exit":
			params := notif.Params.(map[string]interface{})
			if int(params["exit_code"].(float64)) == 0 {
				foundExit = true
			}
		}
	}

	if !foundStdout {
		t.Error("did not receive stdout notification")
	}
	if !foundExit {
		t.Error("did not receive exit notification")
	}
}
