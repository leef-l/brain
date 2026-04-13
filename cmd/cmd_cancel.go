package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/leef-l/brain/cli"
)

// runCancel implements `brain cancel <run_id> [--force] [--json]`.
// See 27-CLI命令契约.md §9.
//
// In solo mode, cancellation is done by looking up the run's checkpoint
// and marking it as canceled. With ephemeral MemKernel stores this
// only works within the same process lifetime.
func runCancel(args []string) int {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "force kill (may lose data)")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain cancel <run_id> [--force] [--json]")
		return cli.ExitUsage
	}

	runID := fs.Arg(0)

	runtime, rec, cp, err := loadPersistedRun(runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain cancel: %v\n", err)
		return cli.ExitNotFound
	}

	// Check state — only running/paused/waiting_tool can be canceled.
	currentState := rec.Status
	if cp != nil && cp.State != "" {
		currentState = cp.State
	}
	switch currentState {
	case "completed", "failed", "canceled":
		fmt.Fprintf(os.Stderr, "brain cancel: run %s is already %s\n", runID, currentState)
		return cli.ExitInvalidState
	}

	// Update checkpoint to canceled (or crashed if --force).
	newState := "canceled"
	if *force {
		newState = "crashed"
	}
	if cp != nil {
		cp.State = newState
		ctx, cancel := bgCtx()
		defer cancel()
		if err := runtime.Kernel.RunCheckpoint.Save(ctx, cp); err != nil {
			fmt.Fprintf(os.Stderr, "brain cancel: save checkpoint: %v\n", err)
			return cli.ExitSoftware
		}
	}
	if _, err := runtime.RunStore.finish(runID, newState, rec.Result, ""); err != nil {
		fmt.Fprintf(os.Stderr, "brain cancel: save run record: %v\n", err)
		return cli.ExitSoftware
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"run_id":    runID,
			"state":     newState,
			"forced":    *force,
			"persisted": true,
		})
	} else {
		action := "canceled gracefully"
		if *force {
			action = "force-killed (checkpoint may be incomplete)"
		}
		fmt.Fprintf(os.Stdout, "Run %s %s\n", runID, action)
	}
	return cli.ExitOK
}
