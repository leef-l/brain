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

func RunReplay(args []string, loadRun RunLoader) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	outputDir := fs.String("output-dir", "", "write replay output to directory")
	mockLLM := fs.Bool("mock-llm", true, "use recorded responses (no LLM calls)")
	mockTools := fs.Bool("mock-tools", true, "don't execute tools, show call records only")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain replay <run_id> [--output-dir <path>] [--mock-llm] [--json]")
		return cli.ExitUsage
	}

	runID := fs.Arg(0)

	_ = mockLLM
	_ = mockTools

	runtime, rec, cp, err := loadRun(runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain replay: %v\n", err)
		return cli.ExitNotFound
	}

	var planCount int
	if runtime.Kernel.PlanStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		plans, _ := runtime.Kernel.PlanStore.ListByRun(ctx, rec.StoreRunID)
		planCount = len(plans)
	}

	if *outputDir != "" {
		if err := os.MkdirAll(*outputDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "brain replay: create output dir: %v\n", err)
			if os.IsPermission(err) {
				return cli.ExitNoPerm
			}
			return cli.ExitNoInput
		}
	}

	if *jsonOut {
		out := map[string]interface{}{
			"run_id":       rec.ID,
			"store_run_id": rec.StoreRunID,
			"brain":        rec.BrainID,
			"state":        rec.Status,
			"plans":        planCount,
			"turn_uuid":    rec.TurnUUID,
			"mock_llm":     *mockLLM,
			"mock_tools":   *mockTools,
		}
		if cp != nil {
			out["checkpoint_state"] = cp.State
		}
		if *outputDir != "" {
			out["output_dir"] = *outputDir
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
	} else {
		fmt.Fprintf(os.Stdout, "Replay of run %s\n", rec.ID)
		fmt.Fprintf(os.Stdout, "  brain:      %s\n", rec.BrainID)
		fmt.Fprintf(os.Stdout, "  state:      %s\n", rec.Status)
		fmt.Fprintf(os.Stdout, "  plans:      %d\n", planCount)
		fmt.Fprintf(os.Stdout, "  turn_uuid:  %s\n", rec.TurnUUID)
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, "  (Detailed turn-by-turn replay requires persistent trace storage.)")
		fmt.Fprintln(os.Stdout, "  Current runtime persists run metadata, checkpoints, and plan snapshots.")
	}
	return cli.ExitOK
}
