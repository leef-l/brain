package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/leef-l/brain/sdk/cli"
)

// runStatus implements `brain status <run_id>`.
// See 27-CLI命令契约.md §7.
//
// In solo mode, queries the RunCheckpointStore for the latest checkpoint
// of the given run. Since MemKernel stores are ephemeral (in-memory),
// this only works within the same process or with a persistent store
// (SQLite, future).
func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain status <run_id> [--json]")
		return cli.ExitUsage
	}

	runID := fs.Arg(0)

	_, rec, cp, err := loadPersistedRun(runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain status: %v\n", err)
		return cli.ExitNotFound
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		out := map[string]interface{}{
			"run_id":       rec.ID,
			"store_run_id": rec.StoreRunID,
			"brain":        rec.BrainID,
			"state":        rec.Status,
			"created_at":   rec.CreatedAt,
			"updated_at":   rec.UpdatedAt,
			"plan_id":      rec.PlanID,
		}
		if cp != nil {
			out["checkpoint_state"] = cp.State
			out["turn_uuid"] = cp.TurnUUID
			out["turn_index"] = cp.TurnIndex
		}
		enc.Encode(out)
	} else {
		fmt.Fprintf(os.Stdout, "Run %s\n", rec.ID)
		fmt.Fprintf(os.Stdout, "  store_run_id: %d\n", rec.StoreRunID)
		fmt.Fprintf(os.Stdout, "  brain:        %s\n", rec.BrainID)
		fmt.Fprintf(os.Stdout, "  state:        %s\n", rec.Status)
		fmt.Fprintf(os.Stdout, "  created_at:   %s\n", rec.CreatedAt.Format(time.RFC3339))
		fmt.Fprintf(os.Stdout, "  updated_at:   %s\n", rec.UpdatedAt.Format(time.RFC3339))
		if cp != nil {
			fmt.Fprintf(os.Stdout, "  turn_uuid:    %s\n", cp.TurnUUID)
			fmt.Fprintf(os.Stdout, "  turn_index:   %d\n", cp.TurnIndex)
		}
	}
	return cli.ExitOK
}
