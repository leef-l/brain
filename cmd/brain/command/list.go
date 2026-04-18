package command

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/sdk/cli"
)

type RuntimeFactory func(brainKind string) (*cliruntime.Runtime, error)

func RunList(args []string, newRuntime RuntimeFactory) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stateFilter := fs.String("state", "all", "filter by state (running|completed|failed|all)")
	limit := fs.Int("limit", 50, "maximum number of results")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	runtime, err := newRuntime("central")
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain list: runtime: %v\n", err)
		return cli.ExitSoftware
	}
	runs := runtime.RunStore.List(*limit, *stateFilter)

	if *jsonOut {
		out := map[string]interface{}{
			"runs":     runs,
			"total":    len(runs),
			"returned": len(runs),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
	} else {
		if len(runs) == 0 {
			fmt.Fprintln(os.Stdout, "No runs found.")
			fmt.Fprintln(os.Stdout, "  Use `brain run --prompt '...'` to create a run.")
			return cli.ExitOK
		}
		fmt.Fprintf(os.Stdout, "%-24s %-12s %-12s %s\n", "RUN ID", "BRAIN", "STATE", "STORE ID")
		for _, r := range runs {
			fmt.Fprintf(os.Stdout, "%-24s %-12s %-12s %d\n", r.ID, r.BrainID, r.Status, r.StoreRunID)
		}
		fmt.Fprintf(os.Stdout, "\n%d run(s) found.\n", len(runs))
	}
	return cli.ExitOK
}
