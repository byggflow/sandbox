package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// endpoint returns the sandboxd base URL from the environment.
func endpoint(t *testing.T) string {
	t.Helper()
	ep := os.Getenv("SANDBOXD_ENDPOINT")
	if ep == "" {
		t.Skip("SANDBOXD_ENDPOINT not set")
	}
	return strings.TrimRight(ep, "/")
}

// wsEndpoint converts an HTTP endpoint to a WebSocket endpoint.
func wsEndpoint(httpEndpoint string) string {
	if strings.HasPrefix(httpEndpoint, "https://") {
		return "wss://" + strings.TrimPrefix(httpEndpoint, "https://")
	}
	return "ws://" + strings.TrimPrefix(httpEndpoint, "http://")
}

// sandboxInfo is the JSON shape returned by POST /sandboxes and GET /sandboxes.
type sandboxInfo struct {
	ID      string `json:"id"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Created string `json:"created"`
	TTL     int    `json:"ttl"`
	Profile string `json:"profile,omitempty"`
}

// jsonrpcRequest is a JSON-RPC 2.0 request.
type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// jsonrpcResponse is a JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// createSandbox creates a sandbox via the REST API and returns its info.
func createSandbox(t *testing.T, ep string) sandboxInfo {
	t.Helper()
	body := bytes.NewBufferString(`{}`)
	resp, err := http.Post(ep+"/sandboxes", "application/json", body)
	if err != nil {
		t.Fatalf("POST /sandboxes failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /sandboxes returned %d: %s", resp.StatusCode, string(data))
	}

	var info sandboxInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode sandbox info: %v", err)
	}
	return info
}

// destroySandbox destroys a sandbox via the REST API.
func destroySandbox(t *testing.T, ep, id string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, ep+"/sandboxes/"+id, nil)
	if err != nil {
		t.Fatalf("create DELETE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("DELETE /sandboxes/%s failed: %v", id, err)
		return
	}
	resp.Body.Close()
}

// connectWS opens a WebSocket connection to a sandbox.
func connectWS(t *testing.T, ep, id string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := wsEndpoint(ep) + "/sandboxes/" + id + "/ws"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("websocket dial %s: %v", url, err)
	}
	return conn
}

// sendRPC sends a JSON-RPC request over the WebSocket and returns the response.
func sendRPC(t *testing.T, conn *websocket.Conn, id int, method string, params interface{}) jsonrpcResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal rpc request: %v", err)
	}

	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write rpc request: %v", err)
	}

	// Read response — skip notifications (those have no ID).
	for {
		msgType, msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read rpc response: %v", err)
		}
		if msgType != websocket.MessageText {
			// Binary message — skip for now (handled by caller when expected).
			continue
		}
		var resp jsonrpcResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			t.Fatalf("unmarshal rpc response: %v", err)
		}
		// Skip notifications (no ID field set or ID == 0 when we expect a different ID).
		if resp.ID == id {
			return resp
		}
	}
}

// readBinaryMessage reads the next binary WebSocket message, skipping text messages.
func readBinaryMessage(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read binary message: %v", err)
		}
		if msgType == websocket.MessageBinary {
			return data
		}
	}
}

// readTextMessage reads the next text WebSocket message, skipping binary messages.
func readTextMessage(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read text message: %v", err)
		}
		if msgType == websocket.MessageText {
			return data
		}
	}
}

func TestHealth(t *testing.T) {
	ep := endpoint(t)

	resp, err := http.Get(ep + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}

	status, ok := body["status"].(string)
	if !ok || status != "ok" {
		t.Fatalf("expected status=ok, got %v", body["status"])
	}
}

func TestCreateAndDestroySandbox(t *testing.T) {
	ep := endpoint(t)

	info := createSandbox(t, ep)
	t.Cleanup(func() { destroySandbox(t, ep, info.ID) })

	if !strings.HasPrefix(info.ID, "sbx-") {
		t.Fatalf("expected id to start with sbx-, got %q", info.ID)
	}
	if info.State != "running" {
		t.Fatalf("expected state=running, got %q", info.State)
	}

	// Destroy it.
	req, err := http.NewRequest(http.MethodDelete, ep+"/sandboxes/"+info.ID, nil)
	if err != nil {
		t.Fatalf("create DELETE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /sandboxes/%s failed: %v", info.ID, err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 on delete, got %d", resp.StatusCode)
	}
}

func TestListSandboxes(t *testing.T) {
	ep := endpoint(t)

	info := createSandbox(t, ep)
	t.Cleanup(func() { destroySandbox(t, ep, info.ID) })

	resp, err := http.Get(ep + "/sandboxes")
	if err != nil {
		t.Fatalf("GET /sandboxes failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var list []sandboxInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode sandbox list: %v", err)
	}

	found := false
	for _, s := range list {
		if s.ID == info.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created sandbox %s not found in list of %d sandboxes", info.ID, len(list))
	}
}

func TestExecViaWebSocket(t *testing.T) {
	ep := endpoint(t)

	info := createSandbox(t, ep)
	t.Cleanup(func() { destroySandbox(t, ep, info.ID) })

	ws := connectWS(t, ep, info.ID)
	defer ws.Close(websocket.StatusNormalClosure, "done")

	resp := sendRPC(t, ws, 1, "process.exec", map[string]interface{}{
		"command": "echo hello",
	})

	if resp.Error != nil {
		t.Fatalf("exec returned error: %s", resp.Error.Message)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal exec result: %v", err)
	}

	stdout, _ := result["stdout"].(string)
	if stdout != "hello\n" {
		t.Fatalf("expected stdout=%q, got %q", "hello\n", stdout)
	}

	exitCode, _ := result["exitCode"].(float64)
	if int(exitCode) != 0 {
		t.Fatalf("expected exitCode=0, got %v", exitCode)
	}
}

func TestFsWriteAndRead(t *testing.T) {
	ep := endpoint(t)

	info := createSandbox(t, ep)
	t.Cleanup(func() { destroySandbox(t, ep, info.ID) })

	ws := connectWS(t, ep, info.ID)
	defer ws.Close(websocket.StatusNormalClosure, "done")

	testContent := []byte("hello from integration test")
	testPath := "/tmp/integration-test.txt"

	// Write a file: send JSON-RPC request, then binary content.
	ctx := context.Background()
	writeReq := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "fs.write",
		Params: map[string]interface{}{
			"path": testPath,
			"size": len(testContent),
		},
	}
	writeData, _ := json.Marshal(writeReq)

	writeCtx, writeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer writeCancel()

	if err := ws.Write(writeCtx, websocket.MessageText, writeData); err != nil {
		t.Fatalf("write fs.write request: %v", err)
	}
	if err := ws.Write(writeCtx, websocket.MessageBinary, testContent); err != nil {
		t.Fatalf("write file content: %v", err)
	}

	// Read the JSON-RPC response for the write.
	writeRespData := readTextMessage(t, ws)
	var writeResp jsonrpcResponse
	if err := json.Unmarshal(writeRespData, &writeResp); err != nil {
		t.Fatalf("unmarshal write response: %v", err)
	}
	if writeResp.Error != nil {
		t.Fatalf("fs.write returned error: %s", writeResp.Error.Message)
	}

	// Read the file back.
	readReq := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "fs.read",
		Params: map[string]interface{}{
			"path": testPath,
		},
	}
	readData, _ := json.Marshal(readReq)

	readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
	defer readCancel()

	if err := ws.Write(readCtx, websocket.MessageText, readData); err != nil {
		t.Fatalf("write fs.read request: %v", err)
	}

	// Expect a binary message with the file content, then a text JSON-RPC response.
	content := readBinaryMessage(t, ws)
	readRespData := readTextMessage(t, ws)

	var readResp jsonrpcResponse
	if err := json.Unmarshal(readRespData, &readResp); err != nil {
		t.Fatalf("unmarshal read response: %v", err)
	}
	if readResp.Error != nil {
		t.Fatalf("fs.read returned error: %s", readResp.Error.Message)
	}

	if !bytes.Equal(content, testContent) {
		t.Fatalf("file content mismatch: got %q, want %q", string(content), string(testContent))
	}
}

func TestEnvSetAndGet(t *testing.T) {
	ep := endpoint(t)

	info := createSandbox(t, ep)
	t.Cleanup(func() { destroySandbox(t, ep, info.ID) })

	ws := connectWS(t, ep, info.ID)
	defer ws.Close(websocket.StatusNormalClosure, "done")

	// Set an env var.
	setResp := sendRPC(t, ws, 1, "env.set", map[string]interface{}{
		"key":   "TEST_VAR",
		"value": "integration_value",
	})
	if setResp.Error != nil {
		t.Fatalf("env.set returned error: %s", setResp.Error.Message)
	}

	// Get it back.
	getResp := sendRPC(t, ws, 2, "env.get", map[string]interface{}{
		"key": "TEST_VAR",
	})
	if getResp.Error != nil {
		t.Fatalf("env.get returned error: %s", getResp.Error.Message)
	}

	var value interface{}
	if err := json.Unmarshal(getResp.Result, &value); err != nil {
		t.Fatalf("unmarshal env.get result: %v", err)
	}

	// The result could be a string directly or a map with "value".
	switch v := value.(type) {
	case string:
		if v != "integration_value" {
			t.Fatalf("expected value=%q, got %q", "integration_value", v)
		}
	case map[string]interface{}:
		val, _ := v["value"].(string)
		if val != "integration_value" {
			t.Fatalf("expected value=%q, got %q", "integration_value", val)
		}
	default:
		t.Fatalf("unexpected env.get result type: %T (%v)", value, value)
	}
}

func TestCreateMultipleSandboxes(t *testing.T) {
	ep := endpoint(t)

	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		info := createSandbox(t, ep)
		ids[i] = info.ID
		t.Cleanup(func() { destroySandbox(t, ep, info.ID) })
	}

	resp, err := http.Get(ep + "/sandboxes")
	if err != nil {
		t.Fatalf("GET /sandboxes failed: %v", err)
	}
	defer resp.Body.Close()

	var list []sandboxInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode sandbox list: %v", err)
	}

	listed := make(map[string]bool)
	for _, s := range list {
		listed[s.ID] = true
	}

	for _, id := range ids {
		if !listed[id] {
			t.Fatalf("sandbox %s not found in list", id)
		}
	}
}

func TestDestroyNonexistent(t *testing.T) {
	ep := endpoint(t)

	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/sandboxes/sbx-nonexistent", ep), nil)
	if err != nil {
		t.Fatalf("create DELETE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}
