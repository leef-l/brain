package loop

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leef-l/brain/llm"
	"github.com/leef-l/brain/tool"
)

// ---------------------------------------------------------------------------
// Stream pipeline tests — verify stream.start/chunk/end lifecycle
// ---------------------------------------------------------------------------

func TestStream_TextMessage_EventOrder(t *testing.T) {
	// Verify stream events fire in correct order:
	// message_start → content_delta(s) → message_delta → message_end
	mp := llm.NewMockProvider("stream-test", llm.WithMockStreamChunkSize(5))
	mp.QueueText("Hello World from streaming!")

	reg := tool.NewMemRegistry()
	consumer := NewMemStreamConsumer()

	runner := &Runner{
		Provider:       mp,
		ToolRegistry:   reg,
		StreamConsumer: consumer,
		Now:            fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	run := NewRun("stream-1", "test", defaultBudget())
	opts := defaultOpts()
	opts.Stream = true

	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("hello"),
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State=%q, want completed", result.Run.State)
	}

	snap, ok := consumer.Snapshot(run.ID, 1)
	if !ok {
		t.Fatal("no snapshot for turn 1")
	}

	// Verify all event types fired.
	if snap.EventCounts["message_start"] != 1 {
		t.Errorf("message_start=%d, want 1", snap.EventCounts["message_start"])
	}
	if snap.EventCounts["content_delta"] < 1 {
		t.Error("expected at least 1 content_delta event")
	}
	if snap.EventCounts["message_delta"] != 1 {
		t.Errorf("message_delta=%d, want 1", snap.EventCounts["message_delta"])
	}
	if snap.EventCounts["message_end"] != 1 {
		t.Errorf("message_end=%d, want 1", snap.EventCounts["message_end"])
	}

	// Verify accumulated text.
	if snap.Content.String() != "Hello World from streaming!" {
		t.Errorf("Content=%q, want 'Hello World from streaming!'", snap.Content.String())
	}

	// Verify finished flag.
	if !snap.Finished {
		t.Error("snapshot should be Finished")
	}
}

func TestStream_ToolCallMessage_Events(t *testing.T) {
	// Verify streaming with tool_use emits tool_call_delta events.
	mp := llm.NewMockProvider("stream-test")
	mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"streamed"}`))
	mp.QueueText("Done after tool!")

	reg := tool.NewMemRegistry()
	reg.Register(tool.NewEchoTool("test"))
	consumer := NewMemStreamConsumer()

	runner := &Runner{
		Provider:       mp,
		ToolRegistry:   reg,
		StreamConsumer: consumer,
		Now:            fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	run := NewRun("stream-2", "test", defaultBudget())
	opts := defaultOpts()
	opts.Stream = true

	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("echo streamed"),
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State=%q, want completed", result.Run.State)
	}

	// Turn 1: tool_use via stream.
	snap1, ok := consumer.Snapshot(run.ID, 1)
	if !ok {
		t.Fatal("no snapshot for turn 1")
	}
	if snap1.EventCounts["message_start"] < 1 {
		t.Error("turn 1 missing message_start")
	}
	if snap1.EventCounts["tool_call_delta"] < 1 {
		t.Error("turn 1 missing tool_call_delta")
	}
	if snap1.EventCounts["message_end"] < 1 {
		t.Error("turn 1 missing message_end")
	}
	if len(snap1.ToolCalls) == 0 {
		t.Error("turn 1 should have tool calls")
	} else if snap1.ToolCalls[0].ToolName != "test.echo" {
		t.Errorf("tool name=%q, want test.echo", snap1.ToolCalls[0].ToolName)
	}

	// Turn 2: text response via stream.
	snap2, ok := consumer.Snapshot(run.ID, 2)
	if !ok {
		t.Fatal("no snapshot for turn 2")
	}
	if snap2.Content.String() != "Done after tool!" {
		t.Errorf("turn 2 content=%q, want 'Done after tool!'", snap2.Content.String())
	}
}

func TestStream_MessageDeltas_Captured(t *testing.T) {
	// Verify that message_delta events (carrying stop_reason) are captured.
	mp := llm.NewMockProvider("stream-test")
	mp.QueueText("With metadata!")

	reg := tool.NewMemRegistry()
	consumer := NewMemStreamConsumer()

	runner := &Runner{
		Provider:       mp,
		ToolRegistry:   reg,
		StreamConsumer: consumer,
		Now:            fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	run := NewRun("stream-3", "test", defaultBudget())
	opts := defaultOpts()
	opts.Stream = true

	runner.Execute(context.Background(), run, []llm.Message{
		userMessage("meta"),
	}, opts)

	snap, ok := consumer.Snapshot(run.ID, 1)
	if !ok {
		t.Fatal("no snapshot")
	}

	if len(snap.MessageDeltas) == 0 {
		t.Fatal("expected at least 1 message delta")
	}

	// Parse the delta to verify it contains stop_reason.
	var delta struct {
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(snap.MessageDeltas[0], &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if delta.StopReason != "end_turn" {
		t.Errorf("stop_reason=%q, want end_turn", delta.StopReason)
	}
}

func TestStream_FinalUsage_Recorded(t *testing.T) {
	// Verify that OnMessageEnd captures the final usage.
	mp := llm.NewMockProvider("stream-test")
	mp.Queue(&llm.ChatResponse{
		ID:         "usage-test",
		Model:      "test",
		StopReason: "end_turn",
		Content:    []llm.ContentBlock{{Type: "text", Text: "ok"}},
		Usage:      llm.Usage{InputTokens: 100, OutputTokens: 50},
	})

	reg := tool.NewMemRegistry()
	consumer := NewMemStreamConsumer()

	runner := &Runner{
		Provider:       mp,
		ToolRegistry:   reg,
		StreamConsumer: consumer,
		Now:            fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	run := NewRun("stream-4", "test", defaultBudget())
	opts := defaultOpts()
	opts.Stream = true

	runner.Execute(context.Background(), run, []llm.Message{
		userMessage("usage test"),
	}, opts)

	snap, ok := consumer.Snapshot(run.ID, 1)
	if !ok {
		t.Fatal("no snapshot")
	}

	if !snap.Finished {
		t.Error("should be Finished after message_end")
	}
	if snap.FinalUsage.InputTokens != 100 {
		t.Errorf("FinalUsage.InputTokens=%d, want 100", snap.FinalUsage.InputTokens)
	}
	if snap.FinalUsage.OutputTokens != 50 {
		t.Errorf("FinalUsage.OutputTokens=%d, want 50", snap.FinalUsage.OutputTokens)
	}
}

func TestStream_NilConsumer_NoError(t *testing.T) {
	// Stream path with nil consumer should work without error.
	mp := llm.NewMockProvider("stream-test")
	mp.QueueText("works without consumer")

	reg := tool.NewMemRegistry()

	runner := &Runner{
		Provider:       mp,
		ToolRegistry:   reg,
		StreamConsumer: nil, // explicitly nil
		Now:            fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	run := NewRun("stream-5", "test", defaultBudget())
	opts := defaultOpts()
	opts.Stream = true

	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("nil consumer"),
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State=%q, want completed", result.Run.State)
	}
}

func TestStream_MultiTurn_IndependentBuffers(t *testing.T) {
	// Verify that each turn gets its own independent stream buffer.
	mp := llm.NewMockProvider("stream-test")
	mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"turn1"}`))
	mp.QueueText("final answer")

	reg := tool.NewMemRegistry()
	reg.Register(tool.NewEchoTool("test"))
	consumer := NewMemStreamConsumer()

	runner := &Runner{
		Provider:       mp,
		ToolRegistry:   reg,
		StreamConsumer: consumer,
		Now:            fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	run := NewRun("stream-6", "test", defaultBudget())
	opts := defaultOpts()
	opts.Stream = true

	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("multi turn stream"),
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Turns) != 2 {
		t.Fatalf("Turns=%d, want 2", len(result.Turns))
	}

	// Both turns should have independent snapshots.
	snap1, ok1 := consumer.Snapshot(run.ID, 1)
	snap2, ok2 := consumer.Snapshot(run.ID, 2)
	if !ok1 || !ok2 {
		t.Fatal("missing snapshot for one of the turns")
	}

	// Turn 1 has tool_call, turn 2 has text.
	if len(snap1.ToolCalls) == 0 {
		t.Error("turn 1 should have tool calls")
	}
	if snap2.Content.String() != "final answer" {
		t.Errorf("turn 2 content=%q, want 'final answer'", snap2.Content.String())
	}

	// Both should be finished.
	if !snap1.Finished || !snap2.Finished {
		t.Error("both turns should be Finished")
	}
}
