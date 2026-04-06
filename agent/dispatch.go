package agent

import (
	"encoding/base64"
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
	"github.com/byggflow/sandbox/protocol/crypto"
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

	ptyMgr        *process.PtyManager
	cryptoSession *crypto.Session
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
	d.conn[proto.OpProcessStream] = func(params json.RawMessage, c io.ReadWriter) (interface{}, error) {
		return process.Stream(params, c)
	}
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

	// E2E encryption negotiation
	d.simple[proto.OpSessionNegotiateE2E] = d.negotiateE2E

	return d
}

// CryptoSession returns the negotiated crypto session, or nil if E2E is not active.
func (d *Dispatcher) CryptoSession() *crypto.Session {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cryptoSession
}

// PtyManager returns the PTY manager for binary frame routing.
func (d *Dispatcher) PtyManager() *process.PtyManager {
	return d.ptyMgr
}

// negotiateE2E handles the session.negotiate_e2e RPC.
// The client sends its X25519 public key; the agent generates a keypair,
// derives the shared secret, and returns its public key.
func (d *Dispatcher) negotiateE2E(params json.RawMessage) (interface{}, error) {
	var req struct {
		PublicKey string `json:"public_key"` // base64-encoded X25519 public key
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("parse e2e params: %w", err)
	}

	clientPubBytes, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode client public key: %w", err)
	}

	clientPub, err := crypto.PublicKeyFromBytes(clientPubBytes)
	if err != nil {
		return nil, fmt.Errorf("parse client public key: %w", err)
	}

	// Generate agent keypair.
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate agent keypair: %w", err)
	}

	// Derive shared secret.
	secret, err := crypto.DeriveSharedSecret(kp.Private, clientPub)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}

	session, err := crypto.NewSession(secret)
	if err != nil {
		return nil, fmt.Errorf("create crypto session: %w", err)
	}

	// Store the crypto session.
	d.mu.Lock()
	d.cryptoSession = session
	d.mu.Unlock()

	log.Printf("e2e encryption negotiated")

	return map[string]string{
		"public_key": base64.StdEncoding.EncodeToString(kp.Public.Bytes()),
	}, nil
}

// Handle dispatches a JSON-RPC request and writes the response.
func (d *Dispatcher) Handle(req *proto.Request, rw io.ReadWriter) {
	params, err := json.Marshal(req.Params)
	if err != nil {
		d.sendError(rw, req.ID, -32600, "invalid params")
		return
	}

	// If E2E encryption is active and this isn't the negotiation call,
	// decrypt the params._encrypted field.
	cs := d.CryptoSession()
	if cs != nil && req.Method != proto.OpSessionNegotiateE2E {
		params, err = d.decryptParams(cs, params)
		if err != nil {
			d.sendError(rw, req.ID, -32600, fmt.Sprintf("e2e decrypt: %v", err))
			return
		}
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

	// If E2E encryption is active, encrypt the result.
	if cs != nil && req.Method != proto.OpSessionNegotiateE2E {
		result, err = d.encryptResult(cs, result)
		if err != nil {
			d.sendError(rw, req.ID, -32000, fmt.Sprintf("e2e encrypt: %v", err))
			return
		}
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

// decryptParams extracts and decrypts the _encrypted field from params.
func (d *Dispatcher) decryptParams(cs *crypto.Session, params []byte) ([]byte, error) {
	var envelope struct {
		Encrypted string `json:"_encrypted"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return params, nil // Not encrypted, pass through.
	}
	if envelope.Encrypted == "" {
		return params, nil // No _encrypted field, pass through.
	}
	return cs.OpenBase64(envelope.Encrypted)
}

// encryptResult serializes and encrypts the result into an _encrypted envelope.
func (d *Dispatcher) encryptResult(cs *crypto.Session, result interface{}) (interface{}, error) {
	plaintext, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	encrypted, err := cs.SealBase64(plaintext)
	if err != nil {
		return nil, err
	}
	return map[string]string{"_encrypted": encrypted}, nil
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
