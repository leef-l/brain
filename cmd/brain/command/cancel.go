package command

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/leef-l/brain/sdk/cli"
)

func RunCancel(args []string, loadRun RunLoader) int {
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

	runtime, rec, cp, err := loadRun(runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain cancel: %v\n", err)
		return cli.ExitNotFound
	}

	currentState := rec.Status
	if cp != nil && cp.State != "" {
		currentState = cp.State
	}
	switch currentState {
	case "completed", "failed", "canceled":
		fmt.Fprintf(os.Stderr, "brain cancel: run %s is already %s\n", runID, currentState)
		return cli.ExitInvalidState
	}

	newState := "canceled"
	if *force {
		newState = "crashed"
	}
	if cp != nil {
		cp.State = newState
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runtime.Kernel.RunCheckpoint.Save(ctx, cp); err != nil {
			fmt.Fprintf(os.Stderr, "brain cancel: save checkpoint: %v\n", err)
			return cli.ExitSoftware
		}
	}
	if _, err := runtime.RunStore.Finish(runID, newState, rec.Result, ""); err != nil {
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
