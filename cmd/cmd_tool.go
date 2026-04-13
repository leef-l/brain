package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/leef-l/brain/cli"
	"github.com/leef-l/brain/tool"
	"github.com/leef-l/brain/toolpolicy"
)

// runTool implements `brain tool` with subcommands: list, describe, test.
// See 27-CLI命令契约.md §13.
func runTool(args []string) int {
	if len(args) == 0 {
		printToolUsage()
		return cli.ExitUsage
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "list":
		return runToolList(rest)
	case "describe":
		return runToolDescribe(rest)
	case "test":
		return runToolTest(rest)
	case "-h", "--help", "help":
		printToolUsage()
		return cli.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "brain tool: unknown subcommand %q\n", sub)
		printToolUsage()
		return cli.ExitUsage
	}
}

func printToolUsage() {
	fmt.Fprintln(os.Stderr, "Usage: brain tool <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list       List runtime tools (optionally filtered by --scope)")
	fmt.Fprintln(os.Stderr, "  describe   Show full schema of a tool")
	fmt.Fprintln(os.Stderr, "  test       Execute a tool directly (for debugging)")
}

func buildToolCommandRegistry(cfg *brainConfig) tool.Registry {
	reg := newBaseToolRegistry(cfg)

	orch := buildOrchestrator(orchestratorConfig{cfg: cfg})
	registerDelegateToolIfAvailable(reg, orch, nil)
	return reg
}

func applyToolScope(reg tool.Registry, cfg *brainConfig, scope string) (tool.Registry, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return reg, nil
	}
	if err := toolpolicy.ValidateScope(scope); err != nil {
		return nil, err
	}
	return filterRegistryWithConfig(reg, cfg, scope), nil
}

// runToolList implements `brain tool list [--brain <brain>] [--scope <scope>] [--json]`.
func runToolList(args []string) int {
	fs := flag.NewFlagSet("tool list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	brainFilter := fs.String("brain", "", "filter by brain kind")
	scope := fs.String("scope", "", "apply active_tools for a runtime scope (e.g. chat.central.default, run.code)")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain tool list: %v\n", err)
		return cli.ExitSoftware
	}

	reg, err := applyToolScope(buildToolCommandRegistry(cfg), cfg, *scope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain tool list: %v\n", err)
		return cli.ExitDataErr
	}

	var tools []toolInfo
	if *brainFilter != "" {
		for _, t := range reg.ListByBrain(*brainFilter) {
			tools = append(tools, toolInfo{
				Name:        t.Name(),
				Brain:       t.Schema().Brain,
				Risk:        string(t.Risk()),
				Description: t.Schema().Description,
			})
		}
	} else {
		for _, t := range reg.List() {
			tools = append(tools, toolInfo{
				Name:        t.Name(),
				Brain:       t.Schema().Brain,
				Risk:        string(t.Risk()),
				Description: t.Schema().Description,
			})
		}
	}

	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	if *jsonOut {
		out := map[string]interface{}{
			"tools": tools,
			"total": len(tools),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
	} else {
		fmt.Fprintf(os.Stdout, "%-25s %-12s %-8s %s\n", "NAME", "BRAIN", "RISK", "DESCRIPTION")
		for _, t := range tools {
			fmt.Fprintf(os.Stdout, "%-25s %-12s %-8s %s\n", t.Name, t.Brain, t.Risk, t.Description)
		}
		fmt.Fprintf(os.Stdout, "\n%d tool(s) registered.\n", len(tools))
	}
	return cli.ExitOK
}

type toolInfo struct {
	Name        string `json:"name"`
	Brain       string `json:"brain"`
	Risk        string `json:"risk"`
	Description string `json:"description"`
}

// runToolDescribe implements `brain tool describe <tool_name> [--scope <scope>]`.
func runToolDescribe(args []string) int {
	fs := flag.NewFlagSet("tool describe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	scope := fs.String("scope", "", "apply active_tools for a runtime scope")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain tool describe [--scope <scope>] <tool_name>")
		return cli.ExitUsage
	}

	toolName := fs.Arg(0)
	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain tool describe: %v\n", err)
		return cli.ExitSoftware
	}

	reg, err := applyToolScope(buildToolCommandRegistry(cfg), cfg, *scope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain tool describe: %v\n", err)
		return cli.ExitDataErr
	}

	t, ok := reg.Lookup(toolName)
	if !ok {
		fmt.Fprintf(os.Stderr, "brain tool describe: tool %q not found\n", toolName)
		return cli.ExitNotFound
	}

	schema := t.Schema()
	out := map[string]interface{}{
		"name":        schema.Name,
		"brain":       schema.Brain,
		"risk":        string(t.Risk()),
		"description": schema.Description,
	}
	if len(schema.InputSchema) > 0 {
		var raw interface{}
		json.Unmarshal(schema.InputSchema, &raw)
		out["input_schema"] = raw
	}
	if len(schema.OutputSchema) > 0 {
		var raw interface{}
		json.Unmarshal(schema.OutputSchema, &raw)
		out["output_schema"] = raw
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	return cli.ExitOK
}

// runToolTest implements `brain tool test <tool_name> [--args-json <json>] [--scope <scope>]`.
func runToolTest(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: brain tool test <tool_name> [--scope <scope>] [--args-json '{...}']")
		return cli.ExitUsage
	}

	// First positional argument is the tool name.
	toolName := args[0]

	fs := flag.NewFlagSet("tool test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	argsJSON := fs.String("args-json", "{}", "tool arguments as JSON")
	scope := fs.String("scope", "", "apply active_tools for a runtime scope")
	if err := fs.Parse(args[1:]); err != nil {
		return cli.ExitUsage
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain tool test: %v\n", err)
		return cli.ExitSoftware
	}

	reg, err := applyToolScope(buildToolCommandRegistry(cfg), cfg, *scope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain tool test: %v\n", err)
		return cli.ExitDataErr
	}

	t, ok := reg.Lookup(toolName)
	if !ok {
		fmt.Fprintf(os.Stderr, "brain tool test: tool %q not found\n", toolName)
		return cli.ExitNotFound
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	result, err := t.Execute(ctx, json.RawMessage(*argsJSON))
	dur := time.Since(start)

	if err != nil {
		fmt.Fprintf(os.Stderr, "brain tool test: execution error: %v\n", err)
		return cli.ExitSoftware
	}

	out := map[string]interface{}{
		"tool":        toolName,
		"status":      "ok",
		"duration_ms": dur.Milliseconds(),
		"is_error":    result.IsError,
	}
	if result.IsError {
		out["status"] = "error"
	}
	if len(result.Output) > 0 {
		var raw interface{}
		json.Unmarshal(result.Output, &raw)
		out["result"] = raw
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	return cli.ExitOK
}
