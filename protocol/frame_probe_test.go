package protocol

import "testing"

func TestProbeFrameClassifies(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    FrameKind
		method  string
		id      int
	}{
		{"request", `{"jsonrpc":"2.0","id":42,"method":"net.egress","params":{}}`, FrameKindRequest, "net.egress", 42},
		{"notification", `{"jsonrpc":"2.0","method":"process.output","params":{}}`, FrameKindNotification, "process.output", 0},
		{"response result", `{"jsonrpc":"2.0","id":42,"result":{}}`, FrameKindResponse, "", 42},
		{"response error", `{"jsonrpc":"2.0","id":42,"error":{"code":1,"message":"x"}}`, FrameKindResponse, "", 42},
		{"malformed", `{not json`, FrameKindUnknown, "", 0},
		{"empty", `{}`, FrameKindUnknown, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, env := ProbeFrame([]byte(c.payload))
			if kind != c.want {
				t.Errorf("kind: got %d want %d", kind, c.want)
			}
			if env.Method != c.method {
				t.Errorf("method: got %q want %q", env.Method, c.method)
			}
			if c.id != 0 {
				if got := DecodeID(env.ID); got != c.id {
					t.Errorf("id: got %d want %d", got, c.id)
				}
			}
		})
	}
}

func TestDecodeIDNonNumber(t *testing.T) {
	if got := DecodeID([]byte(`"string-id"`)); got != 0 {
		t.Errorf("expected 0 for string id, got %d", got)
	}
	if got := DecodeID(nil); got != 0 {
		t.Errorf("expected 0 for nil id, got %d", got)
	}
}
