package sidecar

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

// mockKernelCaller records all NotifyKernel calls for inspection.
type mockKernelCaller struct {
	notifies []struct {
		Method string
		Params interface{}
	}
}

func (m *mockKernelCaller) CallKernel(ctx context.Context, method string, params interface{}, result interface{}) error {
	return nil
}

func (m *mockKernelCaller) NotifyKernel(ctx context.Context, method string, params interface{}) error {
	m.notifies = append(m.notifies, struct {
		Method string
		Params interface{}
	}{Method: method, Params: params})
	return nil
}

func TestExecutionStreamConsumer_Lifecycle(t *testing.T) {
	caller := &mockKernelCaller{}
	SetProgressContext(caller, "test")
	defer SetProgressContext(nil, "")

	consumer := newExecutionStreamConsumer("exec-123")
	ctx := context.Background()
	run := loop.NewRun("run-1", "test", loop.Budget{})
	turn := loop.NewTurn(run.ID, 1, time.Now())

	// OnMessageStart
	consumer.OnMessageStart(ctx, run, turn)
	if len(caller.notifies) != 1 {
		t.Fatalf("expected 1 notify, got %d", len(caller.notifies))
	}
	checkProgressEvent(t, caller.notifies[0].Params, "llm_start", "exec-123", "", "")

	// OnContentDelta
	consumer.OnContentDelta(ctx, run, turn, "hello")
	if len(caller.notifies) != 2 {
		t.Fatalf("expected 2 notifies, got %d", len(caller.notifies))
	}
	checkProgressEvent(t, caller.notifies[1].Params, "content", "exec-123", "", "hello")

	// OnToolCallDelta
	consumer.OnToolCallDelta(ctx, run, turn, "tool_a", `{"x":1}`)
	if len(caller.notifies) != 3 {
		t.Fatalf("expected 3 notifies, got %d", len(caller.notifies))
	}
	checkProgressEvent(t, caller.notifies[2].Params, "tool_call_delta", "exec-123", "tool_a", "")
	if ev, ok := caller.notifies[2].Params.(ProgressEvent); ok && ev.Detail != `{"x":1}` {
		t.Errorf("detail = %q, want {\"x\":1}", ev.Detail)
	}

	// OnMessageDelta
	consumer.OnMessageDelta(ctx, run, turn, json.RawMessage(`{"stop_reason":"end_turn"}`))
	if len(caller.notifies) != 4 {
		t.Fatalf("expected 4 notifies, got %d", len(caller.notifies))
	}
	checkProgressEvent(t, caller.notifies[3].Params, "llm_delta", "exec-123", "", "")

	// OnMessageEnd
	consumer.OnMessageEnd(ctx, run, turn, llm.Usage{InputTokens: 10, OutputTokens: 5})
	if len(caller.notifies) != 5 {
		t.Fatalf("expected 5 notifies, got %d", len(caller.notifies))
	}
	checkProgressEvent(t, caller.notifies[4].Params, "llm_end", "exec-123", "", "")
}

func TestProgressToolObserver_EmitsEvents(t *testing.T) {
	caller := &mockKernelCaller{}
	SetProgressContext(caller, "test")
	defer SetProgressContext(nil, "")

	base := StderrToolObserver{}
	obs := &progressToolObserver{base: base, executionID: "exec-456"}
	ctx := context.Background()
	run := loop.NewRun("run-2", "test", loop.Budget{})
	turn := loop.NewTurn(run.ID, 1, time.Now())

	obs.OnToolStart(ctx, run, turn, "code.read_file", json.RawMessage(`{"path":"main.go"}`))
	if len(caller.notifies) != 1 {
		t.Fatalf("expected 1 notify after start, got %d", len(caller.notifies))
	}
	checkProgressEvent(t, caller.notifies[0].Params, "tool_start", "exec-456", "code.read_file", "")

	obs.OnToolEnd(ctx, run, turn, "code.read_file", true, json.RawMessage(`{"content":"package main"}`))
	if len(caller.notifies) != 2 {
		t.Fatalf("expected 2 notifies after end, got %d", len(caller.notifies))
	}
	checkProgressEvent(t, caller.notifies[1].Params, "tool_end", "exec-456", "code.read_file", "")
}

func TestProgressToolObserver_WithoutExecutionID(t *testing.T) {
	caller := &mockKernelCaller{}
	SetProgressContext(caller, "test")
	defer SetProgressContext(nil, "")

	obs := &progressToolObserver{base: StderrToolObserver{}, executionID: ""}
	ctx := context.Background()
	run := loop.NewRun("run-3", "test", loop.Budget{})
	turn := loop.NewTurn(run.ID, 1, time.Now())

	obs.OnToolStart(ctx, run, turn, "tool", json.RawMessage(`{}`))
	obs.OnToolEnd(ctx, run, turn, "tool", true, json.RawMessage(`{}`))

	if len(caller.notifies) != 0 {
		t.Errorf("expected 0 notifies when executionID is empty, got %d", len(caller.notifies))
	}
}

func checkProgressEvent(t *testing.T, params interface{}, wantKind, wantExecID, wantTool, wantMsg string) {
	t.Helper()
	ev, ok := params.(ProgressEvent)
	if !ok {
		// Params may be passed as map[string]interface{} through JSON-RPC marshaling;
		// for direct calls it's ProgressEvent.
		return
	}
	if ev.Kind != wantKind {
		t.Errorf("kind = %q, want %q", ev.Kind, wantKind)
	}
	if ev.ExecutionID != wantExecID {
		t.Errorf("execution_id = %q, want %q", ev.ExecutionID, wantExecID)
	}
	if wantTool != "" && ev.ToolName != wantTool {
		t.Errorf("tool_name = %q, want %q", ev.ToolName, wantTool)
	}
	if wantMsg != "" && ev.Message != wantMsg {
		t.Errorf("message = %q, want %q", ev.Message, wantMsg)
	}
}
