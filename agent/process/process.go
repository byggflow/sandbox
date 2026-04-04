package process

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
)

// ExecParams is the params for process.exec.
type ExecParams struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
}

// ExecResult is the result of process.exec.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// SpawnParams is the params for process.spawn.
type SpawnParams struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
}

// StdinParams is the params for process.stdin.
type StdinParams struct {
	PID  int    `json:"pid"`
	Data string `json:"data"` // base64
}

// SignalParams is the params for process.signal.
type SignalParams struct {
	PID    int    `json:"pid"`
	Signal string `json:"signal"`
}

// SpawnedProcess tracks a running spawned process.
type SpawnedProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

// Manager tracks spawned processes.
type Manager struct {
	mu    sync.RWMutex
	procs map[int]*SpawnedProcess
}

// NewManager returns a new process manager.
func NewManager() *Manager {
	return &Manager{procs: make(map[int]*SpawnedProcess)}
}

func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// Exec runs a command synchronously and returns its output.
func Exec(raw json.RawMessage) (interface{}, error) {
	var p ExecParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	const maxTimeout = 300 * time.Second
	timeout := 30 * time.Second
	if p.Timeout > 0 {
		timeout = time.Duration(p.Timeout) * time.Second
		if timeout > maxTimeout {
			timeout = maxTimeout
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", p.Command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the entire process group when the context is cancelled,
	// so child processes don't survive the parent.
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if p.Cwd != "" {
		cmd.Dir = p.Cwd
	}
	if len(p.Env) > 0 {
		cmd.Env = buildEnv(p.Env)
	}

	var stdout, stderr []byte
	var stdoutBuf, stderrBuf safeBuffer

	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	stdout = stdoutBuf.Bytes()
	stderr = stderrBuf.Bytes()

	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("command timed out after %v", timeout)
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("exec: %w", err)
		}
	}

	return &ExecResult{
		Stdout:   string(stdout),
		Stderr:   string(stderr),
		ExitCode: exitCode,
	}, nil
}

// Conn abstracts the framed connection for sending notifications.
type Conn interface {
	io.Writer
}

// Spawn starts a process in the background and streams output as notifications.
func (m *Manager) Spawn(raw json.RawMessage, conn Conn) (interface{}, error) {
	var p SpawnParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	cmd := exec.Command("sh", "-c", p.Command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if p.Cwd != "" {
		cmd.Dir = p.Cwd
	}
	if len(p.Env) > 0 {
		cmd.Env = buildEnv(p.Env)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	pid := cmd.Process.Pid

	sp := &SpawnedProcess{cmd: cmd, stdin: stdin}
	m.mu.Lock()
	m.procs[pid] = sp
	m.mu.Unlock()

	// Stream stdout
	go streamOutput(conn, pid, "process.stdout", stdout)
	// Stream stderr
	go streamOutput(conn, pid, "process.stderr", stderr)

	// Wait for exit and send notification
	go func() {
		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
		}

		notif := proto.Notification{
			JSONRPC: "2.0",
			Method:  "process.exit",
			Params: map[string]interface{}{
				"pid":       pid,
				"exit_code": exitCode,
			},
		}
		codec.WriteJSON(conn, notif)

		m.mu.Lock()
		delete(m.procs, pid)
		m.mu.Unlock()
	}()

	return map[string]interface{}{"pid": pid}, nil
}

// WriteStdin writes data to a spawned process's stdin.
func (m *Manager) WriteStdin(raw json.RawMessage) (interface{}, error) {
	var p StdinParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	m.mu.RLock()
	sp, ok := m.procs[p.PID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no such process: %d", p.PID)
	}

	data, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}

	if _, err := sp.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("write stdin: %w", err)
	}

	return map[string]interface{}{"success": true}, nil
}

// Signal sends a signal to a spawned process.
func (m *Manager) Signal(raw json.RawMessage) (interface{}, error) {
	var p SignalParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	m.mu.RLock()
	sp, ok := m.procs[p.PID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no such process: %d", p.PID)
	}

	var sig os.Signal
	switch p.Signal {
	case "SIGTERM", "term":
		sig = syscall.SIGTERM
	case "SIGKILL", "kill":
		sig = syscall.SIGKILL
	case "SIGINT", "int":
		sig = syscall.SIGINT
	case "SIGHUP", "hup":
		sig = syscall.SIGHUP
	default:
		return nil, fmt.Errorf("unsupported signal: %s", p.Signal)
	}

	// Send to the entire process group so child processes are also signaled.
	if err := syscall.Kill(-sp.cmd.Process.Pid, sig.(syscall.Signal)); err != nil {
		return nil, fmt.Errorf("signal: %w", err)
	}

	return map[string]interface{}{"success": true}, nil
}

// Cleanup kills all spawned process groups.
func (m *Manager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for pid, sp := range m.procs {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		sp.stdin.Close()
	}
	m.procs = make(map[int]*SpawnedProcess)
}

// maxSpawnOutput is the maximum bytes streamed per spawn/stdout or spawn/stderr
// before output is truncated. Prevents unbounded memory and bandwidth usage.
const maxSpawnOutput = 10 * 1024 * 1024 // 10MB

func streamOutput(conn Conn, pid int, method string, r io.Reader) {
	buf := make([]byte, 4096)
	var totalBytes int64
	truncated := false

	for {
		n, err := r.Read(buf)
		if n > 0 {
			if truncated {
				// Drain the reader but don't send anything.
				if err != nil {
					break
				}
				continue
			}

			totalBytes += int64(n)
			if totalBytes > int64(maxSpawnOutput) {
				truncated = true
				// Send truncation notification.
				notif := proto.Notification{
					JSONRPC: "2.0",
					Method:  "process.output_truncated",
					Params: map[string]interface{}{
						"pid":    pid,
						"stream": method,
						"limit":  maxSpawnOutput,
					},
				}
				codec.WriteJSON(conn, notif)
				if err != nil {
					break
				}
				continue
			}

			encoded := base64.StdEncoding.EncodeToString(buf[:n])
			notif := proto.Notification{
				JSONRPC: "2.0",
				Method:  method,
				Params: map[string]interface{}{
					"pid":  pid,
					"data": encoded,
				},
			}
			codec.WriteJSON(conn, notif)
		}
		if err != nil {
			break
		}
	}
}

// maxOutputSize is the maximum bytes captured from stdout/stderr in exec.
const maxOutputSize = 50 * 1024 * 1024 // 50MB

// safeBuffer is a simple bytes.Buffer replacement for capturing output
// with a size cap to prevent memory exhaustion.
type safeBuffer struct {
	data      []byte
	truncated bool
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	if b.truncated {
		return len(p), nil // Discard but report success so the process continues.
	}
	remaining := maxOutputSize - len(b.data)
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.truncated = true
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *safeBuffer) Bytes() []byte {
	return b.data
}
