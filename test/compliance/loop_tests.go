package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/llm"
	"github.com/leef-l/brain/loop"
	braintesting "github.com/leef-l/brain/testing"
	"github.com/leef-l/brain/tool"
)

func registerLoopTests(r *braintesting.MemComplianceRunner) {
	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fixedClock := func() time.Time { return fixedTime }

	// C-L-01: Run state machine pending → running → completed.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-01", Description: "Run state: pending→running→completed", Category: "loop",
	}, func(ctx context.Context) error {
		run := loop.NewRun("test-1", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		if run.State != loop.StatePending {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-01: initial state not pending"))
		}
		if err := run.Start(fixedTime); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-01: Start: %v", err)))
		}
		if run.State != loop.StateRunning {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-01: state not running after Start"))
		}
		if err := run.Complete(fixedTime); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-01: Complete: %v", err)))
		}
		if run.State != loop.StateCompleted {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-01: state not completed"))
		}
		return nil
	})

	// C-L-02: Run state machine pending → running → failed.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-02", Description: "Run state: pending→running→failed", Category: "loop",
	}, func(ctx context.Context) error {
		run := loop.NewRun("test-2", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		run.Start(fixedTime)
		if err := run.Fail(fixedTime); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-02: Fail: %v", err)))
		}
		if run.State != loop.StateFailed {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-02: state not failed"))
		}
		return nil
	})

	// C-L-03: Run state machine canceled.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-03", Description: "Run state: running→canceled", Category: "loop",
	}, func(ctx context.Context) error {
		run := loop.NewRun("test-3", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		run.Start(fixedTime)
		if err := run.Cancel(fixedTime); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-03: Cancel: %v", err)))
		}
		if run.State != loop.StateCanceled {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-03: state not canceled"))
		}
		return nil
	})

	// C-L-04: Budget.CheckTurn returns nil when under limits.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-04", Description: "Budget.CheckTurn passes under limits", Category: "loop",
	}, func(ctx context.Context) error {
		b := loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute}
		b.UsedTurns = 5
		if err := b.CheckTurn(); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-04: unexpected error: %v", err)))
		}
		return nil
	})

	// C-L-05: Budget.CheckTurn returns error on turns exhausted.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-05", Description: "Budget.CheckTurn fails on turns exhausted", Category: "loop",
	}, func(ctx context.Context) error {
		b := loop.Budget{MaxTurns: 5, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute}
		b.UsedTurns = 5
		err := b.CheckTurn()
		if err == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-05: expected error"))
		}
		return nil
	})

	// C-L-06: Budget.CheckCost returns error on cost exhausted.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-06", Description: "Budget.CheckCost fails on cost exhausted", Category: "loop",
	}, func(ctx context.Context) error {
		b := loop.Budget{MaxTurns: 10, MaxCostUSD: 1.0, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute}
		b.UsedCostUSD = 1.5
		err := b.CheckCost()
		if err == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-06: expected error"))
		}
		return nil
	})

	// C-L-07: Budget.Remaining returns snapshot.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-07", Description: "Budget.Remaining returns snapshot", Category: "loop",
	}, func(ctx context.Context) error {
		b := loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 20, MaxToolCalls: 10, MaxDuration: time.Minute}
		b.UsedLLMCalls = 5
		snap := b.Remaining()
		if snap.TokensRemaining != 15 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-07: TokensRemaining=%d, want 15", snap.TokensRemaining)))
		}
		return nil
	})

	// C-L-08: Turn creation and End.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-08", Description: "Turn creation and End", Category: "loop",
	}, func(ctx context.Context) error {
		turn := loop.NewTurn("run-1", 1, fixedTime)
		if turn.Index != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-08: Index wrong"))
		}
		if turn.RunID != "run-1" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-08: RunID wrong"))
		}
		turn.End(fixedTime)
		if turn.EndedAt == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-08: EndedAt nil after End"))
		}
		return nil
	})

	// C-L-09: Runner.Execute single text turn.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-09", Description: "Runner single text turn", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueText("Hello!")
		reg := tool.NewMemRegistry()
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, Now: fixedClock}
		run := loop.NewRun("cl09", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		result, err := runner.Execute(ctx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hi"}}},
		}, loop.RunOptions{MaxTokens: 256})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-09: Execute: %v", err)))
		}
		if result.Run.State != loop.StateCompleted {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-09: state=%s, want completed", result.Run.State)))
		}
		return nil
	})

	// C-L-10: Runner.Execute with tool call then text.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-10", Description: "Runner tool call then text", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"ping"}`))
		mp.QueueText("Done!")
		reg := tool.NewMemRegistry()
		reg.Register(tool.NewEchoTool("test"))
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, Now: fixedClock}
		run := loop.NewRun("cl10", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		result, err := runner.Execute(ctx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "echo"}}},
		}, loop.RunOptions{MaxTokens: 256})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-10: Execute: %v", err)))
		}
		if result.Run.State != loop.StateCompleted {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-10: not completed"))
		}
		if result.Run.Budget.UsedToolCalls != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-10: UsedToolCalls=%d, want 1", result.Run.Budget.UsedToolCalls)))
		}
		return nil
	})

	// C-L-11: Runner budget turns exhaustion → StateFailed.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-11", Description: "Runner budget turns exhausted → failed", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		for i := 0; i < 5; i++ {
			mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"loop"}`))
		}
		reg := tool.NewMemRegistry()
		reg.Register(tool.NewEchoTool("test"))
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, Now: fixedClock}
		run := loop.NewRun("cl11", "brain", loop.Budget{MaxTurns: 2, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		result, err := runner.Execute(ctx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "loop"}}},
		}, loop.RunOptions{MaxTokens: 256})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-11: Execute: %v", err)))
		}
		if result.Run.State != loop.StateFailed {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-11: state=%s, want failed", result.Run.State)))
		}
		return nil
	})

	// C-L-12: Runner context canceled → StateCanceled.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-12", Description: "Runner context canceled", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueText("should not reach")
		reg := tool.NewMemRegistry()
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, Now: fixedClock}
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		run := loop.NewRun("cl12", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		result, err := runner.Execute(cancelCtx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hi"}}},
		}, loop.RunOptions{MaxTokens: 256})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-12: unexpected error: %v", err)))
		}
		if result.Run.State != loop.StateCanceled {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-12: state=%s, want canceled", result.Run.State)))
		}
		return nil
	})

	// C-L-13: Runner tool not found → error tool_result.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-13", Description: "Runner tool not found → error result", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueToolUse("nonexistent", json.RawMessage(`{}`))
		mp.QueueText("Done.")
		reg := tool.NewMemRegistry()
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, Now: fixedClock}
		run := loop.NewRun("cl13", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		result, err := runner.Execute(ctx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "call"}}},
		}, loop.RunOptions{MaxTokens: 256})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-13: Execute: %v", err)))
		}
		if result.Run.State != loop.StateCompleted {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-13: should complete after error tool_result"))
		}
		return nil
	})

	// C-L-14: LoopDetector detects repeated tool calls.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-14", Description: "LoopDetector detects repeated calls", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		for i := 0; i < 5; i++ {
			mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"same"}`))
		}
		reg := tool.NewMemRegistry()
		reg.Register(tool.NewEchoTool("test"))
		detector := loop.NewMemLoopDetector()
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, LoopDetector: detector, Now: fixedClock}
		run := loop.NewRun("cl14", "brain", loop.Budget{MaxTurns: 100, MaxCostUSD: 10, MaxLLMCalls: 100, MaxToolCalls: 100, MaxDuration: time.Minute})
		result, err := runner.Execute(ctx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "loop"}}},
		}, loop.RunOptions{MaxTokens: 256})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-14: Execute: %v", err)))
		}
		if result.Run.State != loop.StateFailed {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-14: should fail on loop detection"))
		}
		return nil
	})

	// C-L-15: StreamConsumer receives events.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-15", Description: "StreamConsumer receives events", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueText("streamed")
		reg := tool.NewMemRegistry()
		consumer := loop.NewMemStreamConsumer()
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, StreamConsumer: consumer, Now: fixedClock}
		run := loop.NewRun("cl15", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		result, err := runner.Execute(ctx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "stream"}}},
		}, loop.RunOptions{MaxTokens: 256, Stream: true})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-15: Execute: %v", err)))
		}
		if result.Run.State != loop.StateCompleted {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-15: not completed"))
		}
		snap, ok := consumer.Snapshot(run.ID, 1)
		if !ok {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-15: no stream snapshot"))
		}
		if snap.EventCounts["message_start"] < 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-15: no message_start event"))
		}
		return nil
	})

	// C-L-16: Sanitizer integration — tool_result wrapped.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-16", Description: "Sanitizer wraps tool_result", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueToolUse("test.echo", json.RawMessage(`{"message":"hello"}`))
		mp.QueueText("Done.")
		reg := tool.NewMemRegistry()
		reg.Register(tool.NewEchoTool("test"))
		sanitizer := loop.NewMemSanitizer()
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, Sanitizer: sanitizer, Now: fixedClock}
		run := loop.NewRun("cl16", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		result, err := runner.Execute(ctx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "echo"}}},
		}, loop.RunOptions{MaxTokens: 256})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-16: Execute: %v", err)))
		}
		if result.Run.State != loop.StateCompleted {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-16: not completed"))
		}
		return nil
	})

	// C-L-17: Multiple tool calls in one response.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-17", Description: "Multiple tool calls in one response", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.Queue(&llm.ChatResponse{
			ID: "multi", Model: "test", StopReason: "tool_use",
			Content: []llm.ContentBlock{
				{Type: "tool_use", ToolUseID: "t1", ToolName: "test.echo", Input: json.RawMessage(`{"message":"a"}`)},
				{Type: "tool_use", ToolUseID: "t2", ToolName: "test.echo", Input: json.RawMessage(`{"message":"b"}`)},
			},
			Usage: llm.Usage{InputTokens: 10, OutputTokens: 5},
		})
		mp.QueueText("Both done!")
		reg := tool.NewMemRegistry()
		reg.Register(tool.NewEchoTool("test"))
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, Now: fixedClock}
		run := loop.NewRun("cl17", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		result, err := runner.Execute(ctx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "both"}}},
		}, loop.RunOptions{MaxTokens: 256})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-17: Execute: %v", err)))
		}
		if result.Run.Budget.UsedToolCalls != 2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-17: UsedToolCalls=%d, want 2", result.Run.Budget.UsedToolCalls)))
		}
		return nil
	})

	// C-L-18: TurnExecutor implements Executor interface.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-18", Description: "TurnExecutor implements Executor", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueText("Hello")
		reg := tool.NewMemRegistry()
		exec := &loop.TurnExecutor{Provider: mp, ToolRegistry: reg, Now: fixedClock}
		run := loop.NewRun("cl18", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		run.Start(fixedTime)
		run.CurrentTurn = 1
		req := &llm.ChatRequest{
			RunID: run.ID, TurnIndex: 1, BrainID: "brain",
			Messages:  []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hi"}}}},
			MaxTokens: 256,
		}
		tr, err := exec.Execute(ctx, run, req)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-18: Execute: %v", err)))
		}
		if tr.NextState != loop.StateCompleted {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-18: NextState not completed"))
		}
		return nil
	})

	// C-L-19: FinalMessages contain full conversation.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-19", Description: "FinalMessages contain full conversation", Category: "loop",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueText("Reply!")
		reg := tool.NewMemRegistry()
		runner := &loop.Runner{Provider: mp, ToolRegistry: reg, Now: fixedClock}
		run := loop.NewRun("cl19", "brain", loop.Budget{MaxTurns: 10, MaxCostUSD: 10, MaxLLMCalls: 10, MaxToolCalls: 10, MaxDuration: time.Minute})
		result, _ := runner.Execute(ctx, run, []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hi"}}},
		}, loop.RunOptions{MaxTokens: 256})
		// Should have: [user, assistant]
		if len(result.FinalMessages) != 2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-19: FinalMessages len=%d, want 2", len(result.FinalMessages))))
		}
		if result.FinalMessages[0].Role != "user" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-19: first message not user"))
		}
		if result.FinalMessages[1].Role != "assistant" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-L-19: second message not assistant"))
		}
		return nil
	})

	// C-L-20: Eight Run states are defined.
	r.Register(braintesting.ComplianceTest{
		ID: "C-L-20", Description: "Eight Run states defined", Category: "loop",
	}, func(ctx context.Context) error {
		states := []loop.State{
			loop.StatePending, loop.StateRunning, loop.StateWaitingTool,
			loop.StatePaused, loop.StateCompleted, loop.StateFailed,
			loop.StateCanceled, loop.StateCrashed,
		}
		seen := make(map[loop.State]bool)
		for _, s := range states {
			if s == "" {
				return brainerrors.New(brainerrors.CodeAssertionFailed,
					brainerrors.WithMessage("C-L-20: empty state"))
			}
			seen[s] = true
		}
		if len(seen) != 8 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-L-20: unique states=%d, want 8", len(seen))))
		}
		return nil
	})
}
