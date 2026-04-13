package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/leef-l/brain/cli"
	"github.com/leef-l/brain/llm"
	"github.com/leef-l/brain/loop"
)

// runResume implements `brain resume <run_id> [--follow]`.
// See 27-CLI命令契约.md §8.
//
// Resumes a paused/crashed Run by loading its checkpoint, rebuilding
// the Runner, and continuing execution. In solo/mem mode, run data is
// ephemeral so the checkpoint must still exist in the current process.
func runResume(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	follow := fs.Bool("follow", false, "stream output (NDJSON)")
	jsonOut := fs.Bool("json", false, "output JSON summary")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain resume <run_id> [--follow] [--json]")
		return cli.ExitUsage
	}

	runID := fs.Arg(0)

	runtime, rec, cp, err := loadPersistedRun(runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain resume: %v\n", err)
		return cli.ExitNotFound
	}
	if cp == nil {
		fmt.Fprintf(os.Stderr, "brain resume: checkpoint for %s not found\n", runID)
		return cli.ExitNotFound
	}

	// Only paused/crashed runs can be resumed.
	switch cp.State {
	case "paused", "crashed":
		// OK
	case "completed":
		fmt.Fprintf(os.Stderr, "brain resume: run %s is already completed\n", runID)
		return cli.ExitInvalidState
	case "failed":
		fmt.Fprintf(os.Stderr, "brain resume: run %s has failed (use --force with cancel to reset)\n", runID)
		return cli.ExitInvalidState
	case "canceled":
		fmt.Fprintf(os.Stderr, "brain resume: run %s was canceled\n", runID)
		return cli.ExitInvalidState
	default:
		fmt.Fprintf(os.Stderr, "brain resume: run %s is in state %q (must be paused or crashed)\n", runID, cp.State)
		return cli.ExitInvalidState
	}

	// Rebuild and continue execution.
	run := loop.NewRun(
		fmt.Sprintf("resume-%s-%s", runID, time.Now().UTC().Format("20060102T150405Z")),
		cp.BrainID,
		loop.Budget{
			MaxTurns:    10,
			MaxCostUSD:  5.0,
			MaxLLMCalls: 20,
			MaxDuration: 60 * time.Second,
		},
	)

	mock := llm.NewMockProvider("mock")
	mock.QueueText(fmt.Sprintf("Resumed from checkpoint (run %s, turn %s)", runID, cp.TurnUUID))

	runner := &loop.Runner{
		Provider:     mock,
		ToolRegistry: runtime.Kernel.ToolRegistry,
	}

	messages := []llm.Message{
		{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "Resume execution"}}},
	}
	opts := loop.RunOptions{
		System:    []llm.SystemBlock{{Text: "You are resuming a paused run."}},
		MaxTokens: 256,
		Model:     "mock-model",
		Stream:    *follow,
	}

	result, err := runner.Execute(bgCtx(), run, messages, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain resume: execute: %v\n", err)
		return cli.ExitSoftware
	}

	// Update checkpoint state.
	cp.State = string(result.Run.State)
	_ = runtime.Kernel.RunCheckpoint.Save(bgCtx(), cp)
	_, _ = runtime.RunStore.finish(rec.ID, string(result.Run.State), rec.Result, "")

	replyText := ""
	for i := len(result.FinalMessages) - 1; i >= 0; i-- {
		if result.FinalMessages[i].Role == "assistant" {
			replyText = extractText(result.FinalMessages[i].Content)
			break
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"run_id":       runID,
			"store_run_id": rec.StoreRunID,
			"resumed_as":   result.Run.ID,
			"state":        string(result.Run.State),
			"turns":        result.Run.Budget.UsedTurns,
			"reply":        replyText,
		})
	} else {
		fmt.Fprintf(os.Stdout, "Resumed run %s as %s\n", runID, result.Run.ID)
		fmt.Fprintf(os.Stdout, "  state: %s\n", result.Run.State)
		fmt.Fprintf(os.Stdout, "  turns: %d\n", result.Run.Budget.UsedTurns)
		if replyText != "" {
			fmt.Fprintf(os.Stdout, "  reply: %s\n", replyText)
		}
	}

	switch result.Run.State {
	case loop.StateCompleted:
		return cli.ExitOK
	case loop.StateFailed:
		return cli.ExitFailed
	case loop.StateCanceled:
		return cli.ExitCanceled
	default:
		return cli.ExitOK
	}
}
