package process

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"

	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
	"github.com/creack/pty"
)

// PtyParams is the params for process.pty.
type PtyParams struct {
	Command string            `json:"command,omitempty"`
	Cols    uint16            `json:"cols,omitempty"`
	Rows    uint16            `json:"rows,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
}

// ResizeParams is the params for process.resize.
type ResizeParams struct {
	PID  int    `json:"pid"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// PtyProcess tracks a running PTY process.
type PtyProcess struct {
	cmd *exec.Cmd
	ptm *os.File
}

// PtyManager tracks PTY processes.
type PtyManager struct {
	mu    sync.RWMutex
	procs map[int]*PtyProcess
}

// NewPtyManager returns a new PTY manager.
func NewPtyManager() *PtyManager {
	return &PtyManager{procs: make(map[int]*PtyProcess)}
}

// StartPty starts a command with a PTY and streams output as binary frames.
func (m *PtyManager) StartPty(raw json.RawMessage, conn io.ReadWriter) (interface{}, error) {
	var p PtyParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	command := p.Command
	if command == "" {
		command = "/bin/sh"
	}

	cmd := exec.Command("sh", "-c", command)
	if p.Cwd != "" {
		cmd.Dir = p.Cwd
	}
	if len(p.Env) > 0 {
		cmd.Env = buildEnv(p.Env)
	}

	// Set initial window size.
	winSize := &pty.Winsize{
		Cols: 80,
		Rows: 24,
	}
	if p.Cols > 0 {
		winSize.Cols = p.Cols
	}
	if p.Rows > 0 {
		winSize.Rows = p.Rows
	}

	// Start with PTY.
	ptm, err := pty.StartWithSize(cmd, winSize)
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	pid := cmd.Process.Pid

	pp := &PtyProcess{cmd: cmd, ptm: ptm}
	m.mu.Lock()
	m.procs[pid] = pp
	m.mu.Unlock()

	// Stream PTY output as binary frames.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptm.Read(buf)
			if n > 0 {
				if writeErr := codec.WriteBinary(conn, buf[:n]); writeErr != nil {
					log.Printf("pty write binary error (pid %d): %v", pid, writeErr)
					break
				}
			}
			if err != nil {
				break
			}
		}
	}()

	// Wait for exit and send notification.
	go func() {
		err := cmd.Wait()
		ptm.Close()

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

// WritePtyInput writes binary data to a PTY's master fd.
func (m *PtyManager) WritePtyInput(pid int, data []byte) error {
	m.mu.RLock()
	pp, ok := m.procs[pid]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no such pty process: %d", pid)
	}

	_, err := pp.ptm.Write(data)
	return err
}

// Resize changes the terminal size of a PTY process.
func (m *PtyManager) Resize(raw json.RawMessage) (interface{}, error) {
	var p ResizeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if p.Cols == 0 || p.Rows == 0 {
		return nil, fmt.Errorf("cols and rows are required")
	}

	m.mu.RLock()
	pp, ok := m.procs[p.PID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no such pty process: %d", p.PID)
	}

	if err := pty.Setsize(pp.ptm, &pty.Winsize{
		Cols: p.Cols,
		Rows: p.Rows,
	}); err != nil {
		return nil, fmt.Errorf("resize pty: %w", err)
	}

	return map[string]interface{}{"success": true}, nil
}

// HasPty checks if a PID belongs to a PTY process.
func (m *PtyManager) HasPty(pid int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.procs[pid]
	return ok
}
