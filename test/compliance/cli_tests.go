package compliance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/cli"
	brainerrors "github.com/leef-l/brain/errors"
	braintesting "github.com/leef-l/brain/testing"
)

func registerCLITests(r *braintesting.MemComplianceRunner) {
	// C-CLI-01: ExitOK is 0.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-01", Description: "ExitOK is 0", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitOK != 0 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-01: ExitOK != 0"))
		}
		return nil
	})

	// C-CLI-02: ExitFailed is 1.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-02", Description: "ExitFailed is 1", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitFailed != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-02: ExitFailed != 1"))
		}
		return nil
	})

	// C-CLI-03: ExitCanceled is 2.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-03", Description: "ExitCanceled is 2", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitCanceled != 2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-03: ExitCanceled != 2"))
		}
		return nil
	})

	// C-CLI-04: ExitBudgetExhausted is 3.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-04", Description: "ExitBudgetExhausted is 3", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitBudgetExhausted != 3 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-04: ExitBudgetExhausted != 3"))
		}
		return nil
	})

	// C-CLI-05: ExitUsage is 64.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-05", Description: "ExitUsage is 64", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitUsage != 64 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-05: ExitUsage != 64"))
		}
		return nil
	})

	// C-CLI-06: ExitSoftware is 70.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-06", Description: "ExitSoftware is 70", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitSoftware != 70 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-06: ExitSoftware != 70"))
		}
		return nil
	})

	// C-CLI-07: ExitSignalInt is 130.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-07", Description: "ExitSignalInt is 130", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitSignalInt != 130 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-07: ExitSignalInt != 130"))
		}
		return nil
	})

	// C-CLI-08: ExitSignalTerm is 143.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-08", Description: "ExitSignalTerm is 143", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitSignalTerm != 143 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-08: ExitSignalTerm != 143"))
		}
		return nil
	})

	// C-CLI-09: ExitNotFound is 4.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-09", Description: "ExitNotFound is 4", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitNotFound != 4 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-09: ExitNotFound != 4"))
		}
		return nil
	})

	// C-CLI-10: ExitInvalidState is 5.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-10", Description: "ExitInvalidState is 5", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitInvalidState != 5 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-10: ExitInvalidState != 5"))
		}
		return nil
	})

	// C-CLI-11: OutputFormat "human" and "json".
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-11", Description: "OutputFormat constants", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.FormatHuman != "human" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-11: FormatHuman wrong"))
		}
		if cli.FormatJSON != "json" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-11: FormatJSON wrong"))
		}
		return nil
	})

	// C-CLI-12: VersionInfo JSON schema.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-12", Description: "VersionInfo JSON schema", Category: "cli",
	}, func(ctx context.Context) error {
		vi := cli.VersionInfo{
			CLIVersion:      "0.1.0",
			ProtocolVersion: "1.0",
			KernelVersion:   "0.1.0",
			SDKLanguage:     "go",
			SDKVersion:      "0.1.0",
			Commit:          "abc123",
			BuiltAt:         "2026-01-01T00:00:00Z",
			OS:              "linux",
			Arch:            "amd64",
		}
		data, err := json.Marshal(vi)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-CLI-12: marshal: %v", err)))
		}
		var m map[string]interface{}
		json.Unmarshal(data, &m)
		required := []string{"cli_version", "protocol_version", "kernel_version", "sdk_language", "sdk_version"}
		for _, key := range required {
			if _, ok := m[key]; !ok {
				return brainerrors.New(brainerrors.CodeAssertionFailed,
					brainerrors.WithMessage(fmt.Sprintf("C-CLI-12: missing key %q", key)))
			}
		}
		return nil
	})

	// C-CLI-13: VersionInfo round-trip.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-13", Description: "VersionInfo JSON round-trip", Category: "cli",
	}, func(ctx context.Context) error {
		vi := cli.VersionInfo{CLIVersion: "1.0.0", SDKLanguage: "go"}
		data, _ := json.Marshal(vi)
		var decoded cli.VersionInfo
		if err := json.Unmarshal(data, &decoded); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-CLI-13: unmarshal: %v", err)))
		}
		if decoded.CLIVersion != "1.0.0" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-13: CLIVersion mismatch"))
		}
		return nil
	})

	// C-CLI-14: ExitDataErr is 65.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-14", Description: "ExitDataErr is 65", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitDataErr != 65 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-14: ExitDataErr != 65"))
		}
		return nil
	})

	// C-CLI-15: ExitNoInput is 66.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-15", Description: "ExitNoInput is 66", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitNoInput != 66 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-15: ExitNoInput != 66"))
		}
		return nil
	})

	// C-CLI-16: ExitNoPerm is 67.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-16", Description: "ExitNoPerm is 67", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitNoPerm != 67 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-16: ExitNoPerm != 67"))
		}
		return nil
	})

	// C-CLI-17: ExitOSErr is 71.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-17", Description: "ExitOSErr is 71", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitOSErr != 71 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-17: ExitOSErr != 71"))
		}
		return nil
	})

	// C-CLI-18: ExitCredMissing is 77.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-18", Description: "ExitCredMissing is 77", Category: "cli",
	}, func(ctx context.Context) error {
		if cli.ExitCredMissing != 77 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-18: ExitCredMissing != 77"))
		}
		return nil
	})

	// C-CLI-19: 15 exit codes defined.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-19", Description: "15 exit codes defined", Category: "cli",
	}, func(ctx context.Context) error {
		codes := []int{
			cli.ExitOK, cli.ExitFailed, cli.ExitCanceled,
			cli.ExitBudgetExhausted, cli.ExitNotFound, cli.ExitInvalidState,
			cli.ExitUsage, cli.ExitDataErr, cli.ExitNoInput, cli.ExitNoPerm,
			cli.ExitSoftware, cli.ExitOSErr, cli.ExitCredMissing,
			cli.ExitSignalInt, cli.ExitSignalTerm,
		}
		seen := make(map[int]bool)
		for _, c := range codes {
			seen[c] = true
		}
		if len(seen) != 15 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-CLI-19: unique codes=%d, want 15", len(seen))))
		}
		return nil
	})

	// C-CLI-20: VersionInfo OS and Arch fields present.
	r.Register(braintesting.ComplianceTest{
		ID: "C-CLI-20", Description: "VersionInfo OS/Arch fields", Category: "cli",
	}, func(ctx context.Context) error {
		vi := cli.VersionInfo{OS: "linux", Arch: "amd64"}
		data, _ := json.Marshal(vi)
		var m map[string]interface{}
		json.Unmarshal(data, &m)
		if _, ok := m["os"]; !ok {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-20: missing os"))
		}
		if _, ok := m["arch"]; !ok {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-CLI-20: missing arch"))
		}
		return nil
	})
}
