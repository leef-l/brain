package sidecar

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/agent"
)

// testHandler is a minimal BrainHandler for unit tests.
type testHandler struct {
	kind    agent.Kind
	version string
	tools   []string
}

func (h *testHandler) Kind() agent.Kind { return h.kind }
func (h *testHandler) Version() string  { return h.version }
func (h *testHandler) Tools() []string  { return h.tools }
func (h *testHandler) HandleMethod(_ context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"content": []map[string]string{{"type": "text", "text": "ok:" + req.Name}},
		}, nil
	case "brain/execute":
		return map[string]interface{}{"status": "ok"}, nil
	default:
		return nil, ErrMethodNotFound
	}
}

func TestBrainHandler_Interface(t *testing.T) {
	h := &testHandler{kind: "test", version: "1.0.0", tools: []string{"t.a", "t.b"}}

	var _ BrainHandler = h // compile-time check

	if h.Kind() != "test" {
		t.Errorf("Kind()=%q, want test", h.Kind())
	}
	if h.Version() != "1.0.0" {
		t.Errorf("Version()=%q, want 1.0.0", h.Version())
	}
	if len(h.Tools()) != 2 {
		t.Errorf("Tools() len=%d, want 2", len(h.Tools()))
	}
}

func TestBrainHandler_HandleMethod_ToolsCall(t *testing.T) {
	h := &testHandler{kind: "test", version: "1.0.0", tools: []string{"t.echo"}}

	params := json.RawMessage(`{"name":"t.echo","arguments":{"msg":"hi"}}`)
	result, err := h.HandleMethod(context.Background(), "tools/call", params)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]interface{})
	content := m["content"].([]map[string]string)
	if content[0]["text"] != "ok:t.echo" {
		t.Errorf("text=%q, want ok:t.echo", content[0]["text"])
	}
}

func TestBrainHandler_HandleMethod_BrainExecute(t *testing.T) {
	h := &testHandler{kind: "test", version: "1.0.0"}

	result, err := h.HandleMethod(context.Background(), "brain/execute", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]interface{})
	if m["status"] != "ok" {
		t.Errorf("status=%v, want ok", m["status"])
	}
}

func TestBrainHandler_HandleMethod_UnknownMethod(t *testing.T) {
	h := &testHandler{kind: "test", version: "1.0.0"}

	_, err := h.HandleMethod(context.Background(), "nonexistent", nil)
	if err != ErrMethodNotFound {
		t.Errorf("err=%v, want ErrMethodNotFound", err)
	}
}

func TestErrMethodNotFound(t *testing.T) {
	if ErrMethodNotFound.Error() != "method not found" {
		t.Errorf("ErrMethodNotFound=%q, want 'method not found'", ErrMethodNotFound.Error())
	}
}
