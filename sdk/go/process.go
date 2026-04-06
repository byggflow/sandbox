package sandbox

import (
	"context"
	"fmt"
	"sync"
)

// ExecOptions configures a process execution.
type ExecOptions struct {
	Env     map[string]string `json:"env,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

// ExecResult holds the output of a completed process.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// SpawnOptions configures a spawned process.
type SpawnOptions struct {
	Env map[string]string `json:"env,omitempty"`
}

// SpawnHandle represents a running spawned process.
type SpawnHandle struct {
	PID int
	cc  *callContext
}

// Kill sends a signal to the spawned process.
func (h *SpawnHandle) Kill(ctx context.Context, signal string) error {
	if signal == "" {
		signal = "SIGTERM"
	}
	_, err := call(ctx, h.cc, op{
		Method: "process.exec",
		Params: map[string]interface{}{"pid": h.PID, "signal": signal},
	})
	return err
}

// Wait blocks until the spawned process exits and returns the exit code.
func (h *SpawnHandle) Wait(ctx context.Context) (int, error) {
	// In a real implementation, this would listen for a process exit notification.
	// For now, this is a placeholder.
	return 0, &SandboxError{Message: "spawn wait not implemented"}
}

// PtyOptions configures a PTY session.
type PtyOptions struct {
	Command string            `json:"command,omitempty"`
	Cols    int               `json:"cols,omitempty"`
	Rows    int               `json:"rows,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// PtyHandle represents an active PTY session.
type PtyHandle struct {
	PID int
	cc  *callContext
}

// Write sends input data to the PTY.
func (h *PtyHandle) Write(ctx context.Context, data []byte) error {
	return notify(ctx, h.cc, op{
		Method: "process.pty",
		Params: map[string]interface{}{"pid": h.PID, "data": data},
	})
}

// Resize changes the PTY window dimensions.
func (h *PtyHandle) Resize(ctx context.Context, cols, rows int) error {
	return notify(ctx, h.cc, op{
		Method: "process.resize",
		Params: map[string]interface{}{"pid": h.PID, "cols": cols, "rows": rows},
	})
}

// Kill sends a signal to the PTY process.
func (h *PtyHandle) Kill(ctx context.Context, signal string) error {
	if signal == "" {
		signal = "SIGTERM"
	}
	_, err := call(ctx, h.cc, op{
		Method: "process.exec",
		Params: map[string]interface{}{"pid": h.PID, "signal": signal},
	})
	return err
}

// Wait blocks until the PTY process exits and returns the exit code.
func (h *PtyHandle) Wait(ctx context.Context) (int, error) {
	return 0, &SandboxError{Message: "pty wait not implemented"}
}

// ProcessCategory provides process execution operations on a sandbox.
type ProcessCategory struct {
	cc *callContext
}

// Exec runs a command and waits for it to complete.
func (p *ProcessCategory) Exec(ctx context.Context, command string, opts *ExecOptions) (*ExecResult, error) {
	params := map[string]interface{}{"command": command}
	if opts != nil {
		if opts.Env != nil {
			params["env"] = opts.Env
		}
		if opts.Timeout > 0 {
			params["timeout"] = opts.Timeout
		}
	}
	result, err := call(ctx, p.cc, op{
		Method: "process.exec",
		Params: params,
	})
	if err != nil {
		return nil, err
	}
	if m, ok := result.(map[string]interface{}); ok {
		er := &ExecResult{}
		if v, ok := m["stdout"].(string); ok {
			er.Stdout = v
		}
		if v, ok := m["stderr"].(string); ok {
			er.Stderr = v
		}
		if v, ok := m["exitCode"].(float64); ok {
			er.ExitCode = int(v)
		}
		return er, nil
	}
	return nil, &SandboxError{Message: "unexpected response type for exec"}
}

// StreamExecOptions configures a streaming process execution.
type StreamExecOptions struct {
	Env     map[string]string `json:"env,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
}

// OutputEvent represents a streaming output chunk from a process.
type OutputEvent struct {
	Stream string // "stdout" or "stderr"
	Data   string
}

// StreamExecResult holds the result of a streaming exec, including the exit code
// and channels that deliver output events as they arrive.
type StreamExecResult struct {
	// Output receives stdout/stderr events as they arrive. Closed when the process ends.
	Output <-chan OutputEvent
	// ExitCode returns the exit code once the process completes. Blocks until done.
	ExitCode func() (int, error)
}

// StreamExec runs a command and streams stdout/stderr as they arrive.
// Unlike Exec, output is delivered incrementally rather than buffered.
func (p *ProcessCategory) StreamExec(ctx context.Context, command string, opts *StreamExecOptions) (*StreamExecResult, error) {
	params := map[string]interface{}{"command": command}
	if opts != nil {
		if opts.Env != nil {
			params["env"] = opts.Env
		}
		if opts.Timeout > 0 {
			params["timeout"] = opts.Timeout
		}
		if opts.Cwd != "" {
			params["cwd"] = opts.Cwd
		}
	}

	outputCh := make(chan OutputEvent, 64)

	// Register a notification handler to capture process.output events.
	var exitCode int
	var exitErr error
	var once sync.Once
	done := make(chan struct{})

	p.cc.transport.OnNotification(func(method string, params interface{}) {
		if method != "process.output" {
			return
		}
		m, ok := params.(map[string]interface{})
		if !ok {
			return
		}
		stream, _ := m["stream"].(string)
		data, _ := m["data"].(string)
		select {
		case outputCh <- OutputEvent{Stream: stream, Data: data}:
		default:
			// Drop if channel is full to avoid blocking.
		}
	})

	// Make the RPC call — the response arrives after the process finishes.
	go func() {
		result, err := call(ctx, p.cc, op{
			Method: "process.stream",
			Params: params,
		})
		once.Do(func() {
			if err != nil {
				exitErr = err
			} else if m, ok := result.(map[string]interface{}); ok {
				if v, ok := m["exit_code"].(float64); ok {
					exitCode = int(v)
				}
			}
			close(outputCh)
			close(done)
		})
	}()

	return &StreamExecResult{
		Output: outputCh,
		ExitCode: func() (int, error) {
			<-done
			if exitErr != nil {
				return -1, fmt.Errorf("sandbox: process.stream: %w", exitErr)
			}
			return exitCode, nil
		},
	}, nil
}

// Spawn starts a long-running process with streaming I/O.
func (p *ProcessCategory) Spawn(ctx context.Context, command string, opts *SpawnOptions) (*SpawnHandle, error) {
	params := map[string]interface{}{"command": command}
	if opts != nil && opts.Env != nil {
		params["env"] = opts.Env
	}
	result, err := call(ctx, p.cc, op{
		Method: "process.spawn",
		Params: params,
	})
	if err != nil {
		return nil, err
	}
	handle := &SpawnHandle{cc: p.cc}
	if m, ok := result.(map[string]interface{}); ok {
		if v, ok := m["pid"].(float64); ok {
			handle.PID = int(v)
		}
	}
	return handle, nil
}

// Pty allocates a pseudo-terminal session.
func (p *ProcessCategory) Pty(ctx context.Context, opts *PtyOptions) (*PtyHandle, error) {
	params := map[string]interface{}{}
	if opts != nil {
		if opts.Command != "" {
			params["command"] = opts.Command
		}
		if opts.Cols > 0 {
			params["cols"] = opts.Cols
		}
		if opts.Rows > 0 {
			params["rows"] = opts.Rows
		}
		if opts.Env != nil {
			params["env"] = opts.Env
		}
	}
	result, err := call(ctx, p.cc, op{
		Method: "process.pty",
		Params: params,
	})
	if err != nil {
		return nil, err
	}
	handle := &PtyHandle{cc: p.cc}
	if m, ok := result.(map[string]interface{}); ok {
		if v, ok := m["pid"].(float64); ok {
			handle.PID = int(v)
		}
	}
	return handle, nil
}
