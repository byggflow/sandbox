package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/byggflow/sandbox/agent/env"
	"github.com/byggflow/sandbox/agent/fs"
	agentnet "github.com/byggflow/sandbox/agent/net"
	"github.com/byggflow/sandbox/agent/process"
	codec "github.com/byggflow/sandbox/agent/protocol"
	proto "github.com/byggflow/sandbox/protocol"
)

// SimpleHandler handles a request that only needs params and returns a result.
type SimpleHandler func(params json.RawMessage) (interface{}, error)

// ConnHandler handles a request that needs the connection for binary I/O.
type ConnHandler func(params json.RawMessage, conn io.ReadWriter) (interface{}, error)

// Dispatcher routes JSON-RPC methods to handlers.
type Dispatcher struct {
	simple map[string]SimpleHandler
	conn   map[string]ConnHandler
	mu     sync.RWMutex

	ptyMgr *process.PtyManager
}

// NewDispatcher creates a dispatcher wired to all handlers.
func NewDispatcher() *Dispatcher {
	envStore := env.NewStore()
	procMgr := process.NewManager()
	ptyMgr := process.NewPtyManager()

	d := &Dispatcher{
		simple: make(map[string]SimpleHandler),
		conn:   make(map[string]ConnHandler),
		ptyMgr: ptyMgr,
	}

	// Filesystem (simple)
	d.simple[proto.OpFsList] = fs.List
	d.simple[proto.OpFsStat] = fs.Stat
	d.simple[proto.OpFsRemove] = fs.Remove
	d.simple[proto.OpFsRename] = fs.Rename
	d.simple[proto.OpFsMkdir] = fs.Mkdir

	// Filesystem (conn-based)
	d.conn[proto.OpFsRead] = fs.Read
	d.conn[proto.OpFsWrite] = fs.Write
	d.conn[proto.OpFsUpload] = fs.Upload
	d.conn[proto.OpFsDownload] = fs.Download

	// Process
	d.simple[proto.OpProcessExec] = process.Exec
	d.conn[proto.OpProcessSpawn] = func(params json.RawMessage, c io.ReadWriter) (interface{}, error) {
		return procMgr.Spawn(params, c)
	}

	// PTY
	d.conn[proto.OpProcessPty] = func(params json.RawMessage, c io.ReadWriter) (interface{}, error) {
		return ptyMgr.StartPty(params, c)
	}
	d.simple[proto.OpProcessResize] = ptyMgr.Resize

	// Environment
	d.simple[proto.OpEnvGet] = envStore.Get
	d.simple[proto.OpEnvSet] = envStore.Set
	d.simple[proto.OpEnvDelete] = envStore.Delete
	d.simple[proto.OpEnvList] = envStore.List

	// Network
	d.simple[proto.OpNetFetch] = agentnet.Fetch

	return d
}

// PtyManager returns the PTY manager for binary frame routing.
func (d *Dispatcher) PtyManager() *process.PtyManager {
	return d.ptyMgr
}

// Handle dispatches a JSON-RPC request and writes the response.
func (d *Dispatcher) Handle(req *proto.Request, rw io.ReadWriter) {
	params, err := json.Marshal(req.Params)
	if err != nil {
		d.sendError(rw, req.ID, -32600, "invalid params")
		return
	}

	var result interface{}

	d.mu.RLock()
	if h, ok := d.conn[req.Method]; ok {
		d.mu.RUnlock()
		result, err = h(json.RawMessage(params), rw)
	} else if h, ok := d.simple[req.Method]; ok {
		d.mu.RUnlock()
		result, err = h(json.RawMessage(params))
	} else {
		d.mu.RUnlock()
		d.sendError(rw, req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
		return
	}

	if err != nil {
		log.Printf("error handling %s: %v", req.Method, err)
		d.sendError(rw, req.ID, -32000, err.Error())
		return
	}

	resp := proto.Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}

	if err := codec.WriteJSON(rw, resp); err != nil {
		log.Printf("error writing response for %s: %v", req.Method, err)
	}
}

func (d *Dispatcher) sendError(w io.Writer, id int, code int, msg string) {
	resp := proto.Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &proto.RPCError{
			Code:    code,
			Message: msg,
		},
	}
	if err := codec.WriteJSON(w, resp); err != nil {
		log.Printf("error writing error response: %v", err)
	}
}
