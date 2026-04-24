package main

// command is one entry in the top-level `brain` subcommand table. Each command
// takes its remaining argv tail (without argv[0] and without the subcommand
// name itself) and returns the process exit code. Exit codes MUST come from
// github.com/leef-l/brain/cli (27-CLI命令契约.md §18).
type command struct {
	// Short is a one-line description shown in `brain --help`.
	Short string

	// Run executes the subcommand and returns its exit code.
	Run func(args []string) int
}

// commands is the full subcommand table. Order is preserved separately in
// commandOrder so `brain --help` prints commands in the documented order from
// 27 §5 rather than random map order.
var commands = map[string]command{
	"chat":    {Short: "Interactive conversation mode (like Claude Code)", Run: runChat},
	"run":     {Short: "Single-shot Run (one prompt, one result)", Run: runRun},
	"status":  {Short: "Query Run status", Run: runStatus},
	"resume":  {Short: "Resume an interrupted Run", Run: runResume},
	"cancel":  {Short: "Cancel a Run", Run: runCancel},
	"list":    {Short: "List Runs", Run: runList},
	"logs":    {Short: "View Run logs", Run: runLogs},
	"replay":  {Short: "Replay a Run for audit", Run: runReplay},
	"tool":    {Short: "Manage the tool registry", Run: runTool},
	"config":  {Short: "Manage configuration", Run: runConfig},
	"serve":   {Short: "Start the Kernel service (cluster mode)", Run: runServe},
	"brain":   {Short: "Manage installed brains (list, install, activate, deactivate)", Run: runBrainManage},
	"pattern": {Short: "Share the UI pattern library (import / export)", Run: runPattern},
	"demo":    {Short: "Manage recorded human demo sequences (list, approve, delete)", Run: runDemo},
	"doctor":  {Short: "Diagnose the local brain environment", Run: runDoctor},
	"version": {Short: "Print version information", Run: runVersion},
}

// commandOrder is the fixed display order for `brain --help`, following the
// table of contents in 27-CLI命令契约.md §5.
var commandOrder = []string{
	"chat",
	"run",
	"status",
	"resume",
	"cancel",
	"list",
	"logs",
	"replay",
	"tool",
	"config",
	"serve",
	"brain",
	"pattern",
	"demo",
	"doctor",
	"version",
}

// lookup returns the command struct for the given subcommand name, or
// ok=false if no such command is registered.
func lookup(name string) (command, bool) {
	c, ok := commands[name]
	return c, ok
}
