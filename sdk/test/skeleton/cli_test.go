package skeleton

import (
	"testing"

	"github.com/leef-l/brain/sdk/cli"
)

// ---------------------------------------------------------------------------
// 退出码常量
// ---------------------------------------------------------------------------

func TestExitCodeConstants(t *testing.T) {
	if cli.ExitOK != 0 {
		t.Errorf("ExitOK = %d, want 0", cli.ExitOK)
	}
	if cli.ExitFailed != 1 {
		t.Errorf("ExitFailed = %d, want 1", cli.ExitFailed)
	}
	if cli.ExitCanceled != 2 {
		t.Errorf("ExitCanceled = %d, want 2", cli.ExitCanceled)
	}
	if cli.ExitBudgetExhausted != 3 {
		t.Errorf("ExitBudgetExhausted = %d, want 3", cli.ExitBudgetExhausted)
	}
	if cli.ExitUsage != 64 {
		t.Errorf("ExitUsage = %d, want 64", cli.ExitUsage)
	}
	if cli.ExitSoftware != 70 {
		t.Errorf("ExitSoftware = %d, want 70", cli.ExitSoftware)
	}
	if cli.ExitSignalInt != 130 {
		t.Errorf("ExitSignalInt = %d, want 130", cli.ExitSignalInt)
	}
	if cli.ExitSignalTerm != 143 {
		t.Errorf("ExitSignalTerm = %d, want 143", cli.ExitSignalTerm)
	}
}

// ---------------------------------------------------------------------------
// OutputFormat 常量
// ---------------------------------------------------------------------------

func TestOutputFormatConstants(t *testing.T) {
	if cli.FormatHuman != "human" {
		t.Errorf("FormatHuman = %q, want human", cli.FormatHuman)
	}
	if cli.FormatJSON != "json" {
		t.Errorf("FormatJSON = %q, want json", cli.FormatJSON)
	}
}
