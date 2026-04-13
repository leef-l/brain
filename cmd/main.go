// Command brain is the user-facing CLI for the Brain SDK, frozen by
// 27-CLI命令契约.md. All 13 subcommands are implemented:
//
//	run, status, resume, cancel, list, logs, replay, tool, config,
//	serve, doctor, version
//
// The CLI is deliberately built on the standard library `flag` package only;
// Cobra / urfave-cli / kingpin are forbidden per 28-SDK交付规范.md §6.
package main

import (
	"fmt"
	"os"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/cli"
)

func main() {
	// Drop argv[0], keep the subcommand and its args.
	args := os.Args[1:]

	// `brain` with no args enters interactive chat mode (like Claude Code).
	// `brain --help` / `-h` / `help` prints top-level usage.
	if len(args) == 0 {
		os.Exit(runChat(nil))
	}
	switch args[0] {
	case "-h", "--help", "help":
		printTopUsage(os.Stdout)
		os.Exit(cli.ExitOK)
	}

	name := args[0]
	rest := args[1:]

	cmd, ok := lookup(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "brain: unknown command %q\n", name)
		fmt.Fprintln(os.Stderr, "Run `brain --help` for usage.")
		os.Exit(cli.ExitUsage)
	}

	code := cmd.Run(rest)
	os.Exit(code)
}

func printTopUsage(w *os.File) {
	fmt.Fprintf(w, "brain — multi-brain agent executor CLI (v%s)\n", brain.CLIVersion)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  brain <command> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	for _, c := range commandOrder {
		cmd := commands[c]
		fmt.Fprintf(w, "  %-10s  %s\n", c, cmd.Short)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Use `brain <command> --help` for more information about a command.")
	fmt.Fprintln(w, "Spec: docs/27-CLI命令契约.md")
}
