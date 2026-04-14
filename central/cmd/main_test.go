package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/sdk/protocol"
)

type mockKernelCaller struct {
	call func(method string, params interface{}, result interface{}) error
}

func (m mockKernelCaller) CallKernel(ctx context.Context, method string, params interface{}, result interface{}) error {
	if m.call != nil {
		return m.call(method, params, result)
	}
	return nil
}

func TestCentralHandlerToolSchemasExposeOutputSchemas(t *testing.T) {
	h := &centralHandler{}
	schemas := h.ToolSchemas()
	if got, want := len(schemas), 5; got != want {
		t.Fatalf("unexpected schema count: got %d want %d", got, want)
	}
	tools := h.Tools()
	if len(tools) != len(schemas) {
		t.Fatalf("Tools and ToolSchemas diverged: %d vs %d", len(tools), len(schemas))
	}
	for i, schema := range schemas {
		if schema.Name != tools[i] {
			t.Fatalf("tool order mismatch at %d: %q vs %q", i, schema.Name, tools[i])
		}
		if len(schema.OutputSchema) == 0 {
			t.Fatalf("%s missing output schema", schema.Name)
		}
		if !json.Valid(schema.OutputSchema) {
			t.Fatalf("%s has invalid output schema: %s", schema.Name, string(schema.OutputSchema))
		}
	}
}

func TestCentralHandleToolsCallEchoReturnsStructuredResult(t *testing.T) {
	h := &centralHandler{}
	resp, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name":"central.echo",
		"arguments":{"message":"hi"}
	}`))
	if err != nil {
		t.Fatalf("HandleMethod returned error: %v", err)
	}
	result, ok := resp.(protocol.ToolCallResult)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.Tool != "central.echo" || result.IsError {
		t.Fatalf("unexpected result envelope: %+v", result)
	}
	var output map[string]string
	if err := result.DecodeOutput(&output); err != nil {
		t.Fatalf("DecodeOutput failed: %v", err)
	}
	if output["message"] != "hi" {
		t.Fatalf("unexpected echo output: %+v", output)
	}
}

func TestCentralHandleDelegateReturnsStructuredOutput(t *testing.T) {
	h := &centralHandler{
		caller: mockKernelCaller{
			call: func(method string, params interface{}, result interface{}) error {
				if method != protocol.MethodSubtaskDelegate {
					t.Fatalf("unexpected method: %s", method)
				}
				raw := result.(*json.RawMessage)
				*raw = json.RawMessage(`{
					"status":"completed",
					"output":{"ok":true,"summary":"done"}
				}`)
				return nil
			},
		},
	}

	resp, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name":"central.delegate",
		"arguments":{"target_kind":"code","instruction":"do it"}
	}`))
	if err != nil {
		t.Fatalf("HandleMethod returned error: %v", err)
	}
	result, ok := resp.(protocol.ToolCallResult)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.Tool != "central.delegate" || result.IsError {
		t.Fatalf("unexpected delegate result: %+v", result)
	}
	var output map[string]interface{}
	if err := result.DecodeOutput(&output); err != nil {
		t.Fatalf("DecodeOutput failed: %v", err)
	}
	if output["summary"] != "done" {
		t.Fatalf("unexpected delegate output: %+v", output)
	}
}
