package sidecar

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
)

type testTool struct {
	name   string
	output json.RawMessage
	err    bool
	nilRes bool
}

func (t *testTool) Name() string { return t.name }
func (t *testTool) Schema() tool.Schema {
	return tool.Schema{Name: t.name, Description: "test", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (t *testTool) Risk() tool.Risk { return tool.RiskLow }
func (t *testTool) Execute(context.Context, json.RawMessage) (*tool.Result, error) {
	if t.nilRes {
		return nil, nil
	}
	return &tool.Result{Output: append(json.RawMessage(nil), t.output...), IsError: t.err}, nil
}

func TestDispatchToolCall_ReturnsStructuredOutput(t *testing.T) {
	reg := tool.NewMemRegistry()
	if err := reg.Register(&testTool{name: "test.echo", output: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatalf("register: %v", err)
	}

	resp, err := DispatchToolCall(context.Background(), json.RawMessage(`{"name":"test.echo","arguments":{"x":1}}`), reg, nil)
	if err != nil {
		t.Fatalf("DispatchToolCall: %v", err)
	}
	result, ok := resp.(*protocol.ToolCallResult)
	if !ok {
		t.Fatalf("response type=%T, want *protocol.ToolCallResult", resp)
	}
	if result.Tool != "test.echo" {
		t.Fatalf("result.Tool=%q, want test.echo", result.Tool)
	}
	if string(result.Output) != `{"ok":true}` {
		t.Fatalf("result.Output=%s, want {\"ok\":true}", result.Output)
	}
	if len(result.Content) != 1 || result.Content[0].Text != `{"ok":true}` {
		t.Fatalf("result.Content=%+v, want compatibility text content", result.Content)
	}
}

func TestDispatchToolCall_ReturnsStructuredError(t *testing.T) {
	reg := tool.NewMemRegistry()

	resp, err := DispatchToolCall(context.Background(), json.RawMessage(`{"name":"missing.tool"}`), reg, nil)
	if err != nil {
		t.Fatalf("DispatchToolCall: %v", err)
	}
	result, ok := resp.(*protocol.ToolCallResult)
	if !ok {
		t.Fatalf("response type=%T, want *protocol.ToolCallResult", resp)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
	if result.Error == nil || result.Error.Code != "tool_not_found" {
		t.Fatalf("result.Error=%+v, want code tool_not_found", result.Error)
	}
}

func TestDispatchToolCall_RejectsEmptyToolName(t *testing.T) {
	resp, err := DispatchToolCall(context.Background(), json.RawMessage(`{"name":"   "}`), tool.NewMemRegistry(), nil)
	if err != nil {
		t.Fatalf("DispatchToolCall: %v", err)
	}
	result, ok := resp.(*protocol.ToolCallResult)
	if !ok {
		t.Fatalf("response type=%T, want *protocol.ToolCallResult", resp)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
	if result.Error == nil || result.Error.Code != "invalid_params" {
		t.Fatalf("result.Error=%+v, want code invalid_params", result.Error)
	}
}

func TestDispatchToolCall_DefendsNilResult(t *testing.T) {
	reg := tool.NewMemRegistry()
	if err := reg.Register(&testTool{name: "test.nil", nilRes: true}); err != nil {
		t.Fatalf("register: %v", err)
	}

	resp, err := DispatchToolCall(context.Background(), json.RawMessage(`{"name":"test.nil"}`), reg, nil)
	if err != nil {
		t.Fatalf("DispatchToolCall: %v", err)
	}
	result, ok := resp.(*protocol.ToolCallResult)
	if !ok {
		t.Fatalf("response type=%T, want *protocol.ToolCallResult", resp)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
	if result.Error == nil || result.Error.Code != "tool_execution_failed" {
		t.Fatalf("result.Error=%+v, want code tool_execution_failed", result.Error)
	}
	if result.Error.Message != "tool test.nil returned nil result" {
		t.Fatalf("result.Error.Message=%q, want nil-result guard", result.Error.Message)
	}
}
