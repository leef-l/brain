package main

import (
	"fmt"
	"os"

	"github.com/leef-l/brain/cli"
)

// runStub returns a placeholder Run function for the subcommands that are
// registered but not yet implemented in the v0.1.0 skeleton. It prints a
// clear explanation and returns cli.ExitSoftware (70) per 27-CLI命令契约.md
// §18 (`EX_SOFTWARE` = kernel/internal failure to complete).
//
// The message format is deliberately stable so shell users and CI grep can
// detect the stub state without parsing: the literal prefix is
// `brain <name>: unimplemented in v0.1.0 skeleton`.
func runStub(name string) func(args []string) int {
	return func(args []string) int {
		fmt.Fprintf(os.Stderr, "brain %s: unimplemented in v0.1.0 skeleton\n", name)
		fmt.Fprintln(os.Stderr, "  see docs/next-gen-executor/27-CLI命令契约.md for the full contract")
		return cli.ExitSoftware
	}
}
