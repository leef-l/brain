package command

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/tool"
)

type DemoDeps struct {
	OpenStores     func() (*persistence.ClosableStores, error)
	NewLibrary     func() (*tool.PatternLibrary, error)
	ConvertAndSave func(ctx context.Context, lib *tool.PatternLibrary, seq *persistence.HumanDemoSequence) error
}

func DefaultDemoDeps() DemoDeps {
	return DemoDeps{
		OpenStores: func() (*persistence.ClosableStores, error) {
			return persistence.Open("sqlite", "")
		},
		NewLibrary: func() (*tool.PatternLibrary, error) {
			return tool.NewPatternLibrary("")
		},
		ConvertAndSave: func(ctx context.Context, lib *tool.PatternLibrary, seq *persistence.HumanDemoSequence) error {
			var actions []tool.RecordedAction
			if err := json.Unmarshal(seq.Actions, &actions); err != nil {
				return fmt.Errorf("parse actions: %w", err)
			}
			p := tool.ConvertDemoToPattern(seq, actions)
			if p == nil {
				return fmt.Errorf("conversion produced no pattern (insufficient data)")
			}
			return lib.Upsert(ctx, p)
		},
	}
}

func RunDemo(args []string, deps DemoDeps) int {
	if len(args) == 0 {
		printDemoUsage()
		return cli.ExitUsage
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return runDemoList(rest, deps)
	case "approve":
		return runDemoApprove(rest, deps)
	case "delete":
		return runDemoDelete(rest, deps)
	case "purge":
		return runDemoPurge(rest, deps)
	case "-h", "--help", "help":
		printDemoUsage()
		return cli.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "brain demo: unknown subcommand %q\n", sub)
		printDemoUsage()
		return cli.ExitUsage
	}
}

func printDemoUsage() {
	fmt.Fprintln(os.Stderr, "Usage: brain demo <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list      List recorded human demo sequences")
	fmt.Fprintln(os.Stderr, "  approve   Approve a demo and convert it to a UI pattern")
	fmt.Fprintln(os.Stderr, "  delete    Delete a demo sequence by ID")
	fmt.Fprintln(os.Stderr, "  purge     Delete demos older than a duration (e.g. 30d)")
}

func runDemoList(args []string, deps DemoDeps) int {
	fs := flag.NewFlagSet("demo list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	approvedOnly := fs.Bool("approved", false, "show only approved demos")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: brain demo list [--approved]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	stores, err := deps.OpenStores()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain demo list: %v\n", err)
		return cli.ExitSoftware
	}
	defer stores.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	demos, err := stores.LearningStore.ListHumanDemoSequences(ctx, *approvedOnly)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain demo list: %v\n", err)
		return cli.ExitSoftware
	}

	if len(demos) == 0 {
		fmt.Fprintln(os.Stderr, "No demo sequences found.")
		return cli.ExitOK
	}

	for _, d := range demos {
		status := "pending"
		if d.Approved {
			status = "approved"
		}
		var actionCount int
		var actions []json.RawMessage
		_ = json.Unmarshal(d.Actions, &actions)
		actionCount = len(actions)

		fmt.Printf("ID:%-4d  %-8s  brain=%-8s  actions=%-3d  site=%-30s  goal=%s\n",
			d.ID, status, d.BrainKind, actionCount, truncate(d.URL, 30), truncate(d.Goal, 50))
	}
	fmt.Fprintf(os.Stderr, "\nTotal: %d demo(s)\n", len(demos))
	return cli.ExitOK
}

func runDemoApprove(args []string, deps DemoDeps) int {
	fs := flag.NewFlagSet("demo approve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: brain demo approve <id> [<id>...]")
	}
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return cli.ExitUsage
	}

	stores, err := deps.OpenStores()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain demo approve: %v\n", err)
		return cli.ExitSoftware
	}
	defer stores.Close()

	lib, err := deps.NewLibrary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain demo approve: open pattern library: %v\n", err)
		return cli.ExitSoftware
	}
	defer lib.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exitCode := cli.ExitOK
	for _, arg := range fs.Args() {
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain demo approve: invalid id %q\n", arg)
			exitCode = cli.ExitDataErr
			continue
		}
		if err := stores.LearningStore.ApproveHumanDemoSequence(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "brain demo approve %d: %v\n", id, err)
			exitCode = cli.ExitSoftware
			continue
		}
		seq, err := stores.LearningStore.GetHumanDemoSequence(ctx, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain demo approve %d: approved but failed to read back: %v\n", id, err)
			continue
		}
		if err := deps.ConvertAndSave(ctx, lib, seq); err != nil {
			fmt.Fprintf(os.Stderr, "brain demo approve %d: approved but pattern conversion failed: %v\n", id, err)
			continue
		}
		fmt.Printf("Approved demo %d → converted to UI pattern\n", id)
	}
	return exitCode
}

func runDemoDelete(args []string, deps DemoDeps) int {
	fs := flag.NewFlagSet("demo delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: brain demo delete <id> [<id>...]")
	}
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return cli.ExitUsage
	}

	stores, err := deps.OpenStores()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain demo delete: %v\n", err)
		return cli.ExitSoftware
	}
	defer stores.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	exitCode := cli.ExitOK
	for _, arg := range fs.Args() {
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain demo delete: invalid id %q\n", arg)
			exitCode = cli.ExitDataErr
			continue
		}
		if err := stores.LearningStore.DeleteHumanDemoSequence(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "brain demo delete %d: %v\n", id, err)
			exitCode = cli.ExitSoftware
			continue
		}
		fmt.Printf("Deleted demo %d\n", id)
	}
	return exitCode
}

func runDemoPurge(args []string, deps DemoDeps) int {
	fs := flag.NewFlagSet("demo purge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	olderThan := fs.String("older-than", "", "delete demos older than this duration (e.g. 7d, 30d, 24h)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: brain demo purge --older-than <duration>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if *olderThan == "" {
		fs.Usage()
		return cli.ExitUsage
	}

	dur, err := parseDuration(*olderThan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain demo purge: invalid duration %q: %v\n", *olderThan, err)
		return cli.ExitDataErr
	}

	stores, err := deps.OpenStores()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain demo purge: %v\n", err)
		return cli.ExitSoftware
	}
	defer stores.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cutoff := time.Now().UTC().Add(-dur)
	n, err := stores.LearningStore.PurgeHumanDemoSequences(ctx, cutoff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain demo purge: %v\n", err)
		return cli.ExitSoftware
	}
	fmt.Printf("Purged %d demo(s) older than %s\n", n, *olderThan)
	return cli.ExitOK
}

func parseDuration(s string) (time.Duration, error) {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		days, err := strconv.Atoi(s[:len(s)-1])
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-3]) + "..."
}
