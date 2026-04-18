package command

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/provider"
	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

type ConfigLoader func() (*config.Config, error)

func RunResume(args []string, loadRun RunLoader, loadCfg ConfigLoader) int {
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

	runtime, rec, cp, err := loadRun(runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain resume: %v\n", err)
		return cli.ExitNotFound
	}
	if cp == nil {
		fmt.Fprintf(os.Stderr, "brain resume: checkpoint for %s not found\n", runID)
		return cli.ExitNotFound
	}

	switch cp.State {
	case "paused", "crashed":
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

	cfg, _ := loadCfg()
	session, provErr := provider.OpenConfigured(cfg, cp.BrainID, nil, "", "", "", "")
	var prov llm.Provider
	providerModel := "mock-model"
	if provErr != nil {
		mock := llm.NewMockProvider("mock")
		mock.QueueText(fmt.Sprintf("Resumed from checkpoint (run %s, turn %s)", runID, cp.TurnUUID))
		prov = mock
		fmt.Fprintf(os.Stderr, "brain resume: no API key configured, using mock provider\n")
	} else {
		prov = session.Provider
		providerModel = session.Model
	}

	runner := &loop.Runner{
		Provider:     prov,
		ToolRegistry: runtime.Kernel.ToolRegistry,
		Sanitizer:    loop.NewMemSanitizer(),
		LoopDetector: loop.NewMemLoopDetector(),
		CacheBuilder: loop.NewMemCacheBuilder(),
	}

	messages := []llm.Message{
		{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "Resume execution"}}},
	}
	opts := loop.RunOptions{
		System:    []llm.SystemBlock{{Text: "You are resuming a paused run."}},
		MaxTokens: 4096,
		Model:     providerModel,
		Stream:    *follow,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, run, messages, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain resume: execute: %v\n", err)
		return cli.ExitSoftware
	}

	cp.State = string(result.Run.State)
	_ = runtime.Kernel.RunCheckpoint.Save(ctx, cp)
	_, _ = runtime.RunStore.Finish(rec.ID, string(result.Run.State), rec.Result, "")

	replyText := ExtractText(result.FinalMessages)

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

// ExtractText concatenates every text block from the last assistant message.
func ExtractText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			out := ""
			for _, b := range messages[i].Content {
				if b.Type == "text" {
					out += b.Text
				}
			}
			return out
		}
	}
	return ""
}
