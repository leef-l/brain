package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/leef-l/brain/cli"
	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/kernel"
	"github.com/leef-l/brain/llm"
	"github.com/leef-l/brain/loop"
	"github.com/leef-l/brain/persistence"
)

// runRun implements `brain run` for the v0.1.0 reference executable.
//
// In the real kernel this subcommand forks a sidecar and drives a full
// multi-Turn loop through whichever provider the user configured. The
// v0.1.0 executable ships with no sidecar binaries and no live provider,
// so `brain run` instead exercises the whole in-memory stack end-to-end:
//
//  1. build a NewMemKernel (all in-memory stores)
//  2. create a loop.Run with a small budget
//  3. queue a mock assistant reply on llm.MockProvider
//  4. issue one Complete call
//  5. persist a plan delta so PlanStore / ArtifactStore / AuditLogger all
//     record the interaction
//  6. print a JSON summary of the run
//
// The goal is that `brain run --prompt hello` round-trips the full
// Kernel surface without ever touching the network — which is exactly
// what doctor check #7 and #8 (PlanStore RW, ArtifactStore CAS) rely on.
//
// See 27-CLI命令契约.md §6.
func runRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	prompt := fs.String("prompt", "hello from brain run", "user prompt to send to the mock LLM")
	reply := fs.String("reply", "hello from mock provider", "pre-canned assistant reply to emit")
	brainID := fs.String("brain", "central", "brain identifier")
	jsonOut := fs.Bool("json", true, "emit a JSON run summary to stdout")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "brain run: unexpected argument %q\n", fs.Arg(0))
		return cli.ExitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	k := kernel.NewMemKernel(kernel.MemKernelOptions{BrainKind: *brainID})

	// Build a Run with a tiny budget — one Turn is enough for the smoke.
	run := loop.NewRun(
		"run-"+time.Now().UTC().Format("20060102T150405Z"),
		*brainID,
		loop.Budget{
			MaxTurns:     4,
			MaxCostUSD:   1.0,
			MaxLLMCalls:  4,
			MaxToolCalls: 8,
			MaxDuration:  5 * time.Second,
		},
	)
	if err := run.Start(time.Now().UTC()); err != nil {
		return failRun(err, "start run")
	}

	// Provider + minimal ChatRequest.
	mock := llm.NewMockProvider("mock")
	mock.QueueText(*reply)

	turn := loop.NewTurn(run.ID, 1, time.Now().UTC())
	req := &llm.ChatRequest{
		RunID:     run.ID,
		TurnIndex: turn.Index,
		BrainID:   run.BrainID,
		Model:     "mock-model",
		System: []llm.SystemBlock{
			{Text: "You are a helpful brain kernel smoke test.", Cache: true},
		},
		Messages: []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: *prompt}}},
		},
		MaxTokens:       256,
		Stream:          false,
		RemainingBudget: run.Budget.Remaining(),
	}

	resp, err := mock.Complete(ctx, req)
	if err != nil {
		_ = run.Fail(time.Now().UTC())
		return failRun(err, "mock complete")
	}
	turn.LLMCalls++
	turn.End(time.Now().UTC())
	run.CurrentTurn = turn.Index
	run.Budget.UsedTurns++
	run.Budget.UsedLLMCalls++
	run.Budget.UsedCostUSD += resp.Usage.CostUSD
	run.Budget.ElapsedTime = time.Since(run.StartedAt)

	if err := run.Complete(time.Now().UTC()); err != nil {
		return failRun(err, "complete run")
	}

	// --- persist a plan snapshot so PlanStore / ArtifactStore / audit
	// tier all record the interaction ---
	var planID int64
	if k.PlanStore != nil {
		snapshot, _ := json.Marshal(map[string]interface{}{
			"run_id":   run.ID,
			"brain_id": run.BrainID,
			"prompt":   *prompt,
			"reply":    *reply,
		})
		plan := &persistence.BrainPlan{
			BrainID:      run.BrainID,
			Version:      1,
			CurrentState: snapshot,
		}
		id, perr := k.PlanStore.Create(ctx, plan)
		if perr != nil {
			return failRun(perr, "persist plan")
		}
		planID = id
	}

	// --- emit a JSON summary ---
	summary := map[string]interface{}{
		"run_id":        run.ID,
		"brain_id":      run.BrainID,
		"state":         string(run.State),
		"turns":         run.Budget.UsedTurns,
		"llm_calls":     run.Budget.UsedLLMCalls,
		"elapsed_ms":    run.Budget.ElapsedTime.Milliseconds(),
		"stop_reason":   resp.StopReason,
		"reply":         extractText(resp.Content),
		"mock_provider": mock.Name(),
		"plan_id":       planID,
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(summary); err != nil {
			fmt.Fprintf(os.Stderr, "brain run: encode summary: %v\n", err)
			return cli.ExitSoftware
		}
	} else {
		fmt.Printf("run %s %s reply=%q\n", run.ID, run.State, extractText(resp.Content))
	}
	return cli.ExitOK
}

// extractText concatenates every text block in a ContentBlock slice.
// Non-text blocks (tool_use, tool_result) are skipped.
func extractText(blocks []llm.ContentBlock) string {
	out := ""
	for _, b := range blocks {
		if b.Type == "text" {
			out += b.Text
		}
	}
	return out
}

// failRun prints a BrainError-aware error line and returns the CLI exit
// code appropriate for the error class.
func failRun(err error, context string) int {
	var be *brainerrors.BrainError
	if errors.As(err, &be) {
		fmt.Fprintf(os.Stderr, "brain run: %s: [%s] %s\n", context, be.ErrorCode, be.Message)
	} else {
		fmt.Fprintf(os.Stderr, "brain run: %s: %v\n", context, err)
	}
	return cli.ExitFailed
}
