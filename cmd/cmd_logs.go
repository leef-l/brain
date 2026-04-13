package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/leef-l/brain/cli"
)

// runLogs implements `brain logs <run_id> [--type <type>] [--json]`.
// See 27-CLI命令契约.md §11.
//
// Outputs structured trace/audit logs for a Run. In solo/mem mode,
// queries the in-memory AuditLogger for audit events. With a persistent
// store, would read from trace.jsonl / audit.jsonl files.
func runLogs(args []string) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	logType := fs.String("type", "all", "filter: llm|tool|trace|audit|all")
	jsonOut := fs.Bool("json", false, "output JSON")
	_ = fs.Bool("follow", false, "stream tail (requires running Run)")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain logs <run_id> [--type <type>] [--follow] [--json]")
		return cli.ExitUsage
	}

	runID := fs.Arg(0)

	_, rec, _, err := loadPersistedRun(runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain logs: %v\n", err)
		return cli.ExitNotFound
	}

	if *jsonOut {
		events := filterRunEvents(rec.Events, *logType)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]interface{}{
			"run_id": runID,
			"events": events,
		})
	} else {
		fmt.Fprintf(os.Stdout, "Logs for run %s:\n", runID)
		events := filterRunEvents(rec.Events, *logType)
		if len(events) == 0 {
			fmt.Fprintln(os.Stdout, "  (No persisted events recorded.)")
			return cli.ExitOK
		}
		for _, ev := range events {
			line := fmt.Sprintf("  %s  %-18s %s", ev.At.Format("2006-01-02T15:04:05Z07:00"), ev.Type, ev.Message)
			if len(ev.Data) > 0 {
				line += " " + string(ev.Data)
			}
			fmt.Fprintln(os.Stdout, line)
		}
	}
	return cli.ExitOK
}

func filterRunEvents(events []persistedRunEvent, kind string) []persistedRunEvent {
	kind = strings.TrimSpace(kind)
	if kind == "" || kind == "all" {
		return events
	}
	filtered := make([]persistedRunEvent, 0, len(events))
	for _, ev := range events {
		switch kind {
		case "tool":
			if strings.HasPrefix(ev.Type, "tool.") {
				filtered = append(filtered, ev)
			}
		case "trace", "audit":
			if strings.HasPrefix(ev.Type, kind+".") {
				filtered = append(filtered, ev)
			}
		case "llm":
			if strings.HasPrefix(ev.Type, "llm.") {
				filtered = append(filtered, ev)
			}
		default:
			if ev.Type == kind {
				filtered = append(filtered, ev)
			}
		}
	}
	return filtered
}
