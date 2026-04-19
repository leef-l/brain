package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/tool"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func newTestRunner(provider llm.Provider, registry tool.Registry) *Runner {
	return &Runner{
		Provider:     provider,
		ToolRegistry: registry,
		Now:          fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}
}

func defaultBudget() Budget {
	return Budget{
		MaxTurns:     100,
		MaxCostUSD:   10.0,
		MaxToolCalls: 100,
		MaxLLMCalls:  100,
		MaxDuration:  10 * time.Minute,
	}
}

func userMessage(text string) llm.Message {
	return llm.Message{
		Role: "user",
		Content: []llm.ContentBlock{
			{Type: "text", Text: text},
		},
	}
}

func defaultOpts() RunOptions {
	return RunOptions{
		ToolChoice: "auto",
		MaxTokens:  4096,
	}
}

// failingTool is a tool that returns a Go error from Execute.
type failingTool struct{}

func (f *failingTool) Name() string { return "test.fail" }
func (f *failingTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "test.fail",
		Description: "A tool that always fails",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Brain:       "test",
	}
}
func (f *failingTool) Risk() tool.Risk { return tool.RiskSafe }
func (f *failingTool) Execute(_ context.Context, _ json.RawMessage) (*tool.Result, error) {
	return nil, fmt.Errorf("intentional infrastructure failure")
}

type blockingProvider struct{}

func (p *blockingProvider) Name() string { return "blocking" }
func (p *blockingProvider) Complete(ctx context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *blockingProvider) Stream(ctx context.Context, _ *llm.ChatRequest) (llm.StreamReader, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// ---------------------------------------------------------------------------
// Test 1: Single text turn — no tool calls
// ---------------------------------------------------------------------------

func TestRunner_SingleTextTurn(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueText("Hello, world!")

	reg := tool.NewMemRegistry()
	runner := newTestRunner(mp, reg)

	run := NewRun("run-1", "test-brain", defaultBudget())
	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("Hi"),
	}, defaultOpts())

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State = %q, want %q", result.Run.State, StateCompleted)
	}
	if len(result.Turns) != 1 {
		t.Fatalf("Turns len = %d, want 1", len(result.Turns))
	}
	if result.Run.Budget.UsedTurns != 1 {
		t.Errorf("UsedTurns = %d, want 1", result.Run.Budget.UsedTurns)
	}
	if result.Run.Budget.UsedLLMCalls != 1 {
		t.Errorf("UsedLLMCalls = %d, want 1", result.Run.Budget.UsedLLMCalls)
	}
	// FinalMessages: [user, assistant]
	if len(result.FinalMessages) != 2 {
		t.Errorf("FinalMessages len = %d, want 2", len(result.FinalMessages))
	}
}

// ---------------------------------------------------------------------------
// Test 2: Tool call then text response
// ---------------------------------------------------------------------------

func TestRunner_ToolCallThenText(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"ping"}`))
	mp.QueueText("Done!")

	reg := tool.NewMemRegistry()
	echo := tool.NewEchoTool("test")
	if err := reg.Register(echo); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner(mp, reg)

	run := NewRun("run-2", "test-brain", defaultBudget())
	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("echo ping"),
	}, defaultOpts())

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State = %q, want %q", result.Run.State, StateCompleted)
	}
	if len(result.Turns) != 2 {
		t.Fatalf("Turns len = %d, want 2", len(result.Turns))
	}
	if result.Run.Budget.UsedTurns != 2 {
		t.Errorf("UsedTurns = %d, want 2", result.Run.Budget.UsedTurns)
	}
	if result.Run.Budget.UsedToolCalls != 1 {
		t.Errorf("UsedToolCalls = %d, want 1", result.Run.Budget.UsedToolCalls)
	}
	// FinalMessages: [user, assistant(tool_use), user(tool_result), assistant(text)]
	if len(result.FinalMessages) != 4 {
		t.Errorf("FinalMessages len = %d, want 4", len(result.FinalMessages))
	}
}

// ---------------------------------------------------------------------------
// Test 3: Multiple tool calls in one response
// ---------------------------------------------------------------------------

func TestRunner_MultipleToolCalls(t *testing.T) {
	mp := llm.NewMockProvider("test-model")

	// Queue a response with 2 tool_use blocks.
	mp.Queue(&llm.ChatResponse{
		ID:         "multi-tc",
		Model:      "test-model",
		StopReason: "tool_use",
		Content: []llm.ContentBlock{
			{Type: "tool_use", ToolUseID: "tu-1", ToolName: "test.echo", Input: json.RawMessage(`{"message":"a"}`)},
			{Type: "tool_use", ToolUseID: "tu-2", ToolName: "test.echo", Input: json.RawMessage(`{"message":"b"}`)},
		},
		Usage: llm.Usage{InputTokens: 10, OutputTokens: 5},
	})
	mp.QueueText("Both done!")

	reg := tool.NewMemRegistry()
	echo := tool.NewEchoTool("test")
	reg.Register(echo)

	runner := newTestRunner(mp, reg)

	run := NewRun("run-3", "test-brain", defaultBudget())
	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("echo both"),
	}, defaultOpts())

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State = %q, want %q", result.Run.State, StateCompleted)
	}
	if result.Run.Budget.UsedToolCalls != 2 {
		t.Errorf("UsedToolCalls = %d, want 2", result.Run.Budget.UsedToolCalls)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Budget turns exhausted
// ---------------------------------------------------------------------------

func TestRunner_BudgetTurnsExhausted(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	// Queue enough tool_use responses to exhaust the budget.
	for i := 0; i < 5; i++ {
		mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"loop"}`))
	}

	reg := tool.NewMemRegistry()
	reg.Register(tool.NewEchoTool("test"))

	runner := newTestRunner(mp, reg)

	budget := Budget{
		MaxTurns:     2,
		MaxCostUSD:   10.0,
		MaxToolCalls: 100,
		MaxLLMCalls:  100,
		MaxDuration:  10 * time.Minute,
	}
	run := NewRun("run-4", "test-brain", budget)
	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("loop forever"),
	}, defaultOpts())

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateFailed {
		t.Errorf("State = %q, want %q", result.Run.State, StateFailed)
	}
	// Should have done 2 turns then failed on the 3rd attempt.
	if result.Run.Budget.UsedTurns != 2 {
		t.Errorf("UsedTurns = %d, want 2", result.Run.Budget.UsedTurns)
	}
}

// ---------------------------------------------------------------------------
// Test 5: Context canceled
// ---------------------------------------------------------------------------

func TestRunner_ContextCanceled(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueText("should not reach here")

	reg := tool.NewMemRegistry()
	runner := newTestRunner(mp, reg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	run := NewRun("run-5", "test-brain", defaultBudget())
	result, err := runner.Execute(ctx, run, []llm.Message{
		userMessage("hi"),
	}, defaultOpts())

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCanceled {
		t.Errorf("State = %q, want %q", result.Run.State, StateCanceled)
	}
}

func TestRunner_ContextCanceledDuringProviderCall(t *testing.T) {
	reg := tool.NewMemRegistry()
	runner := newTestRunner(&blockingProvider{}, reg)

	ctx, cancel := context.WithCancel(context.Background())
	run := NewRun("run-5b", "test-brain", defaultBudget())

	done := make(chan struct{})
	var (
		result *RunResult
		err    error
	)
	go func() {
		defer close(done)
		result, err = runner.Execute(ctx, run, []llm.Message{
			userMessage("hi"),
		}, defaultOpts())
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after cancel")
	}

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCanceled {
		t.Errorf("State = %q, want %q", result.Run.State, StateCanceled)
	}
	if got := result.Turns[len(result.Turns)-1].NextState; got != StateCanceled {
		t.Errorf("NextState = %q, want %q", got, StateCanceled)
	}
}

// ---------------------------------------------------------------------------
// Test 6: Tool not found
// ---------------------------------------------------------------------------

func TestRunner_ToolNotFound(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueToolUse("nonexistent.tool", json.RawMessage(`{}`))
	mp.QueueText("Got the error, done.")

	reg := tool.NewMemRegistry() // Empty registry — no tools registered.

	runner := newTestRunner(mp, reg)

	run := NewRun("run-6", "test-brain", defaultBudget())
	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("call missing tool"),
	}, defaultOpts())

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State = %q, want %q", result.Run.State, StateCompleted)
	}
	// The LLM should see the error tool_result and then respond with text.
	if len(result.Turns) != 2 {
		t.Fatalf("Turns len = %d, want 2", len(result.Turns))
	}
	// Verify tool_result is in the messages.
	if len(result.FinalMessages) < 3 {
		t.Fatalf("FinalMessages len = %d, want >= 3", len(result.FinalMessages))
	}
	toolResultMsg := result.FinalMessages[2] // [user, assistant(tool_use), user(tool_result), ...]
	if toolResultMsg.Role != "user" {
		t.Errorf("tool_result message role = %q, want user", toolResultMsg.Role)
	}
	if len(toolResultMsg.Content) == 0 || !toolResultMsg.Content[0].IsError {
		t.Error("tool_result should have IsError=true")
	}
}

// ---------------------------------------------------------------------------
// Test 7: Streaming path
// ---------------------------------------------------------------------------

func TestRunner_StreamingPath(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueText("streamed response")

	reg := tool.NewMemRegistry()
	consumer := NewMemStreamConsumer()

	runner := &Runner{
		Provider:       mp,
		ToolRegistry:   reg,
		StreamConsumer: consumer,
		Now:            fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	run := NewRun("run-7", "test-brain", defaultBudget())
	opts := defaultOpts()
	opts.Stream = true

	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("stream please"),
	}, opts)

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State = %q, want %q", result.Run.State, StateCompleted)
	}

	// Verify stream consumer received events.
	snap, ok := consumer.Snapshot(run.ID, 1)
	if !ok {
		t.Fatal("no stream snapshot for turn 1")
	}
	if snap.EventCounts["message_start"] < 1 {
		t.Error("expected at least 1 message_start event")
	}
	if snap.EventCounts["message_end"] < 1 {
		t.Error("expected at least 1 message_end event")
	}
}

// ---------------------------------------------------------------------------
// Test 8: Loop detected
// ---------------------------------------------------------------------------

func TestRunner_LoopDetected(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	// Queue 5 identical tool_use responses to trigger loop detection.
	for i := 0; i < 5; i++ {
		mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"same"}`))
	}

	reg := tool.NewMemRegistry()
	reg.Register(tool.NewEchoTool("test"))

	detector := NewMemLoopDetector()
	runner := &Runner{
		Provider:     mp,
		ToolRegistry: reg,
		LoopDetector: detector,
		Now:          fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	run := NewRun("run-8", "test-brain", defaultBudget())
	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("loop"),
	}, defaultOpts())

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateFailed {
		t.Errorf("State = %q, want %q", result.Run.State, StateFailed)
	}
	// The last turn should have a loop detection error.
	lastTurn := result.Turns[len(result.Turns)-1]
	if lastTurn.Error == nil {
		t.Fatal("expected loop detection error")
	}
	if lastTurn.Error.ErrorCode != "agent_loop_detected" {
		t.Errorf("ErrorCode = %q, want agent_loop_detected", lastTurn.Error.ErrorCode)
	}
}

// ---------------------------------------------------------------------------
// Test 9: Tool execution error (Go error, not tool_result error)
// ---------------------------------------------------------------------------

func TestRunner_ToolExecutionError(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueToolUse("test.fail", json.RawMessage(`{}`))
	mp.QueueText("I see the error, done.")

	reg := tool.NewMemRegistry()
	reg.Register(&failingTool{})

	runner := newTestRunner(mp, reg)

	run := NewRun("run-9", "test-brain", defaultBudget())
	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("call failing tool"),
	}, defaultOpts())

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State = %q, want %q", result.Run.State, StateCompleted)
	}
	// The tool error should appear as an error tool_result, and the LLM
	// should have continued to produce a text response.
	if len(result.Turns) != 2 {
		t.Fatalf("Turns len = %d, want 2", len(result.Turns))
	}
}

// ---------------------------------------------------------------------------
// Test 10: Sanitizer integration
// ---------------------------------------------------------------------------

func TestRunner_SanitizerIntegration(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"hello"}`))
	mp.QueueText("Sanitized and done.")

	reg := tool.NewMemRegistry()
	reg.Register(tool.NewEchoTool("test"))

	sanitizer := NewMemSanitizer()
	runner := &Runner{
		Provider:     mp,
		ToolRegistry: reg,
		Sanitizer:    sanitizer,
		Now:          fixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	run := NewRun("run-10", "test-brain", defaultBudget())
	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("echo with sanitizer"),
	}, defaultOpts())

	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCompleted {
		t.Errorf("State = %q, want %q", result.Run.State, StateCompleted)
	}
	// Verify the sanitized tool_result is in the conversation.
	// With sanitizer, the tool_result should contain <tool_output> envelope.
	if len(result.FinalMessages) < 3 {
		t.Fatalf("FinalMessages len = %d, want >= 3", len(result.FinalMessages))
	}
	toolResultMsg := result.FinalMessages[2]
	if len(toolResultMsg.Content) == 0 {
		t.Fatal("expected tool_result content")
	}
	block := toolResultMsg.Content[0]
	if block.Type != "tool_result" {
		t.Errorf("block type = %q, want tool_result", block.Type)
	}
	// The MemSanitizer wraps output in <tool_output> envelope.
	if block.Text == "" {
		t.Error("sanitized block should have non-empty Text (envelope)")
	}
}

type countTool struct {
	name  string
	calls *int
}

func (t *countTool) Name() string { return t.name }
func (t *countTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        t.name,
		Description: "counting tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Brain:       "test",
	}
}
func (t *countTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *countTool) Execute(_ context.Context, _ json.RawMessage) (*tool.Result, error) {
	(*t.calls)++
	return &tool.Result{Output: json.RawMessage(`"ok"`)}, nil
}

func TestRunner_PreTurnStateHookOverridesDispatchRegistry(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueToolUse("test.echo", json.RawMessage(`{}`))
	mp.QueueText("done")

	baseReg := tool.NewMemRegistry()
	baseCalls := 0
	baseReg.Register(&countTool{name: "test.echo", calls: &baseCalls})

	turnReg := tool.NewMemRegistry()
	turnCalls := 0
	turnReg.Register(&countTool{name: "test.echo", calls: &turnCalls})

	runner := newTestRunner(mp, baseReg)
	runner.PreTurnStateHook = func(_ context.Context, _ *Run, _ int) (*PreTurnState, error) {
		return &PreTurnState{
			Tools: []llm.ToolSchema{{
				Name:        "test.echo",
				Description: "counting tool",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
			Registry: turnReg,
		}, nil
	}

	run := NewRun("run-preturn-state", "test-brain", defaultBudget())
	result, err := runner.Execute(context.Background(), run, []llm.Message{
		userMessage("call the tool"),
	}, defaultOpts())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Run.State != StateCompleted {
		t.Fatalf("State = %q, want %q", result.Run.State, StateCompleted)
	}
	if baseCalls != 0 {
		t.Fatalf("base registry tool executed %d times, want 0", baseCalls)
	}
	if turnCalls != 1 {
		t.Fatalf("turn registry tool executed %d times, want 1", turnCalls)
	}
}
