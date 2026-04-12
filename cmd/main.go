// Command brain is the user-facing CLI for the Brain SDK, frozen by
// 27-CLI命令契约.md. In the v0.1.0 skeleton stage only `version` and `doctor`
// are fully implemented — the other 11 subcommands register with the
// dispatcher but return an "unimplemented" BrainError with exit code 70.
//
// The CLI is deliberately built on the standard library `flag` package only;
// Cobra / urfave-cli / kingpin are forbidden per 28-SDK交付规范.md §6.
package main

import (
	"fmt"
	"os"

	"github.com/leef-l/brain/cli"
)

func main() {
	// Drop argv[0], keep the subcommand and its args.
	args := os.Args[1:]

	// `brain` with no args or `--help` / `-h` / `help` prints top-level usage.
	if len(args) == 0 {
		printTopUsage(os.Stdout)
		os.Exit(cli.ExitOK)
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
	fmt.Fprintln(w, "brain — next-generation executor CLI (v0.1.0 skeleton)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  brain <command> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	for _, c := range commandOrder {
		cmd := commands[c]
		marker := "  "
		if cmd.stub {
			marker = "* "
		}
		fmt.Fprintf(w, "  %s%-10s  %s\n", marker, c, cmd.Short)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "* = stub in v0.1.0 skeleton (returns exit code 70).")
	fmt.Fprintln(w, "Spec: docs/next-gen-executor/27-CLI命令契约.md")
}
