package command

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/sdk/cli"
)

func RunVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	short := fs.Bool("short", false, "print only the CLI version, no other fields")
	asJSON := fs.Bool("json", false, "emit JSON instead of human text")

	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "brain version: unexpected argument %q\n", fs.Arg(0))
		return cli.ExitUsage
	}
	if *short && *asJSON {
		fmt.Fprintln(os.Stderr, "brain version: --short and --json are mutually exclusive")
		return cli.ExitUsage
	}

	info := cli.VersionInfo{
		CLIVersion:      brain.CLIVersion,
		ProtocolVersion: brain.ProtocolVersion,
		KernelVersion:   brain.KernelVersion,
		SDKLanguage:     brain.SDKLanguage,
		SDKVersion:      brain.SDKVersion,
		Commit:          brain.BuildCommit,
		BuiltAt:         brain.BuildTime,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
	}

	switch {
	case *short:
		fmt.Println(info.CLIVersion)
	case *asJSON:
		out, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain version: marshal: %v\n", err)
			return cli.ExitSoftware
		}
		fmt.Println(string(out))
	default:
		fmt.Printf("brain version %s\n", info.CLIVersion)
		fmt.Printf("  protocol: %s\n", info.ProtocolVersion)
		fmt.Printf("  kernel:   %s\n", info.KernelVersion)
		fmt.Printf("  sdk:      %s/%s\n", info.SDKLanguage, info.SDKVersion)
		fmt.Printf("  commit:   %s\n", info.Commit)
		fmt.Printf("  built:    %s\n", info.BuiltAt)
		fmt.Printf("  os/arch:  %s/%s\n", info.OS, info.Arch)
	}
	return cli.ExitOK
}
