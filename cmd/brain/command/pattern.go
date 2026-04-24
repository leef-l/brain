package command

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/tool"
)

// `brain pattern` — manage the UIPattern library (share / import / export).
//
// Sub-commands:
//   brain pattern export [<category>] [--ids a,b] [--source user,seed]
//                        [--include-disabled] [--include-learned]
//                        [-o file.json] [--origin LABEL]
//   brain pattern import <file> [--mode=merge|overwrite|dry-run]
//                        [--allow-overwrite-builtin]
//                        [--category auth,form]
//
// All operations go through PatternLibrary's public Export/Import API — we
// never hit SQLite directly here.

// PatternDeps is what main wires in. NewLibrary lets the entry point decide
// the DSN (real runs use the default ~/.brain/ui_patterns.db; tests can pass
// a temp path).
type PatternDeps struct {
	NewLibrary func() (*tool.PatternLibrary, error)
}

// DefaultPatternDeps constructs a PatternDeps bound to the real default DSN.
func DefaultPatternDeps() PatternDeps {
	return PatternDeps{
		NewLibrary: func() (*tool.PatternLibrary, error) {
			return tool.NewPatternLibrary("")
		},
	}
}

// RunPattern dispatches `brain pattern <sub>`.
func RunPattern(args []string, deps PatternDeps) int {
	if len(args) == 0 {
		printPatternUsage()
		return cli.ExitUsage
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return runPatternList(rest, deps)
	case "delete":
		return runPatternDelete(rest, deps)
	case "export":
		return runPatternExport(rest, deps)
	case "import":
		return runPatternImport(rest, deps)
	case "-h", "--help", "help":
		printPatternUsage()
		return cli.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "brain pattern: unknown subcommand %q\n", sub)
		printPatternUsage()
		return cli.ExitUsage
	}
}

func printPatternUsage() {
	fmt.Fprintln(os.Stderr, "Usage: brain pattern <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list     List UI patterns")
	fmt.Fprintln(os.Stderr, "  delete   Delete a UI pattern by ID")
	fmt.Fprintln(os.Stderr, "  export   Export UI patterns to a JSON file")
	fmt.Fprintln(os.Stderr, "  import   Import UI patterns from a JSON file")
}

func runPatternList(args []string, deps PatternDeps) int {
	fs := flag.NewFlagSet("pattern list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	category := fs.String("category", "", "filter by category (e.g. auth, admin)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: brain pattern list [--category <cat>]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	lib, err := deps.NewLibrary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain pattern list: %v\n", err)
		return cli.ExitSoftware
	}
	defer lib.Close()

	patterns := lib.ListAll(*category)
	if len(patterns) == 0 {
		fmt.Fprintln(os.Stderr, "No patterns found.")
		return cli.ExitOK
	}
	for _, p := range patterns {
		enabled := "on "
		if !p.Enabled {
			enabled = "off"
		}
		pending := ""
		if p.Pending {
			pending = " [pending]"
		}
		url := p.AppliesWhen.URLPattern
		if url == "" {
			url = p.AppliesWhen.SiteHost
		}
		fmt.Printf("%-30s  %s  src=%-8s  cat=%-10s  steps=%-3d  match=%-4d  succ=%-4d  url=%s%s\n",
			p.ID, enabled, p.Source, p.Category, len(p.ActionSequence),
			p.Stats.MatchCount, p.Stats.SuccessCount, url, pending)
	}
	fmt.Fprintf(os.Stderr, "\nTotal: %d pattern(s)\n", len(patterns))
	return cli.ExitOK
}

func runPatternDelete(args []string, deps PatternDeps) int {
	fs := flag.NewFlagSet("pattern delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: brain pattern delete <id> [<id>...]")
	}
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return cli.ExitUsage
	}

	lib, err := deps.NewLibrary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain pattern delete: %v\n", err)
		return cli.ExitSoftware
	}
	defer lib.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	exitCode := cli.ExitOK
	for _, id := range fs.Args() {
		if err := lib.Delete(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "brain pattern delete %s: %v\n", id, err)
			exitCode = cli.ExitSoftware
			continue
		}
		fmt.Printf("Deleted pattern %s\n", id)
	}
	return exitCode
}

func runPatternExport(args []string, deps PatternDeps) int {
	fs := flag.NewFlagSet("pattern export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	output := fs.String("o", "", "output file path (default: stdout)")
	idsCSV := fs.String("ids", "", "comma-separated pattern IDs to include")
	srcCSV := fs.String("source", "", "comma-separated sources (e.g. seed,user)")
	origin := fs.String("origin", "", "origin label stamped into the envelope")
	includeDisabled := fs.Bool("include-disabled", false, "include disabled patterns")
	includeLearned := fs.Bool("include-learned", false, "include learned (private) patterns")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: brain pattern export [<category>] [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	filter := tool.ExportFilter{
		IDs:             splitCSV(*idsCSV),
		Sources:         splitCSV(*srcCSV),
		Origin:          *origin,
		IncludeDisabled: *includeDisabled,
		IncludeLearned:  *includeLearned,
	}
	if fs.NArg() > 0 {
		filter.Categories = fs.Args()
	}

	lib, err := deps.NewLibrary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain pattern export: %v\n", err)
		return cli.ExitSoftware
	}
	defer lib.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	blob, err := lib.Export(ctx, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain pattern export: %v\n", err)
		return cli.ExitSoftware
	}
	if *output == "" {
		os.Stdout.Write(blob)
		os.Stdout.Write([]byte("\n"))
	} else {
		if err := os.WriteFile(*output, blob, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "brain pattern export: write %s: %v\n", *output, err)
			return cli.ExitSoftware
		}
		fmt.Fprintf(os.Stderr, "Wrote %d bytes to %s\n", len(blob), *output)
	}
	return cli.ExitOK
}

func runPatternImport(args []string, deps PatternDeps) int {
	fs := flag.NewFlagSet("pattern import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("mode", "merge", "import mode: merge | overwrite | dry-run")
	allowBuiltin := fs.Bool("allow-overwrite-builtin", false, "permit overwriting seed (builtin) patterns")
	catCSV := fs.String("category", "", "restrict import to these categories (comma-separated)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: brain pattern import <file> [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return cli.ExitUsage
	}
	path := fs.Arg(0)

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain pattern import: read %s: %v\n", path, err)
		return cli.ExitDataErr
	}

	opts := tool.ImportOptions{
		Mode:                  tool.ImportMode(*mode),
		AllowOverwriteBuiltin: *allowBuiltin,
		CategoryFilter:        splitCSV(*catCSV),
	}

	lib, err := deps.NewLibrary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain pattern import: %v\n", err)
		return cli.ExitSoftware
	}
	defer lib.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	report, err := lib.Import(ctx, data, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain pattern import: %v\n", err)
		return cli.ExitDataErr
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	// Non-zero exit if nothing landed AND nothing was merely skipped-by-choice,
	// so CI can detect a bad pack. Dry-run is always ExitOK.
	if report.Mode != tool.ImportModeDryRun && report.Written == 0 && report.Rejected > 0 && report.Skipped == 0 {
		return cli.ExitSoftware
	}
	return cli.ExitOK
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
