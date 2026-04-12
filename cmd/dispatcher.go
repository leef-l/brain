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

	// stub marks commands that are registered but not yet implemented in
	// the v0.1.0 skeleton. These still dispatch (so the user gets a useful
	// error) but return cli.ExitSoftware (70).
	stub bool
}

// commands is the full subcommand table. Order is preserved separately in
// commandOrder so `brain --help` prints commands in the documented order from
// 27 §5 rather than random map order.
var commands = map[string]command{
	"run":     {Short: "Start a new Run", Run: runRun},
	"status":  {Short: "Query Run status", Run: runStub("status"), stub: true},
	"resume":  {Short: "Resume an interrupted Run", Run: runStub("resume"), stub: true},
	"cancel":  {Short: "Cancel a Run", Run: runStub("cancel"), stub: true},
	"list":    {Short: "List Runs", Run: runStub("list"), stub: true},
	"logs":    {Short: "View Run logs", Run: runStub("logs"), stub: true},
	"replay":  {Short: "Replay a Run for audit", Run: runStub("replay"), stub: true},
	"tool":    {Short: "Manage the tool registry", Run: runStub("tool"), stub: true},
	"config":  {Short: "Manage configuration", Run: runStub("config"), stub: true},
	"serve":   {Short: "Start the Kernel service (cluster mode)", Run: runStub("serve"), stub: true},
	"doctor":  {Short: "Diagnose the local brain environment", Run: runDoctor},
	"version": {Short: "Print version information", Run: runVersion},
}

// commandOrder is the fixed display order for `brain --help`, following the
// table of contents in 27-CLI命令契约.md §5.
var commandOrder = []string{
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
	"doctor",
	"version",
}

// lookup returns the command struct for the given subcommand name, or
// ok=false if no such command is registered.
func lookup(name string) (command, bool) {
	c, ok := commands[name]
	return c, ok
}
