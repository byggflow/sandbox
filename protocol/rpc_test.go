package protocol

import (
	"encoding/json"
	"testing"
)

func TestRequestJSON(t *testing.T) {
	req := Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  OpFsRead,
		Params:  map[string]string{"path": "/app/main.py"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want %q", decoded.JSONRPC, "2.0")
	}
	if decoded.ID != 1 {
		t.Errorf("id = %d, want 1", decoded.ID)
	}
	if decoded.Method != "fs.read" {
		t.Errorf("method = %q, want %q", decoded.Method, "fs.read")
	}
}

func TestResponseWithResult(t *testing.T) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      1,
		Result:  "file contents",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Error != nil {
		t.Error("expected nil error")
	}
	if decoded.Result == nil {
		t.Error("expected non-nil result")
	}
}

func TestResponseWithError(t *testing.T) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      1,
		Error: &RPCError{
			Code:    -32601,
			Message: "method not found",
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Error == nil {
		t.Fatal("expected non-nil error")
	}
	if decoded.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", decoded.Error.Code)
	}
}

func TestNotificationOmitsID(t *testing.T) {
	notif := Notification{
		JSONRPC: "2.0",
		Method:  OpSessionResumed,
		Params:  map[string]string{"sandbox": "sbx-abc123"},
	}

	data, err := json.Marshal(notif)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, hasID := raw["id"]; hasID {
		t.Error("notification should not have an id field")
	}
}
