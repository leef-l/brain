package braintesting

import (
	"context"
	"testing"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// TestComplianceIntegration wires MemComplianceRunner with three dummy test
// cases (pass / fail / skipped) and verifies the aggregated Summary counts.
func TestComplianceIntegration(t *testing.T) {
	runner := NewComplianceRunner().(*MemComplianceRunner)

	runner.Register(ComplianceTest{
		ID:          "C-01",
		Description: "pass test",
		Category:    "protocol",
	}, func(ctx context.Context) error {
		return nil // always passes
	})

	runner.Register(ComplianceTest{
		ID:          "C-02",
		Description: "fail test",
		Category:    "error",
	}, func(ctx context.Context) error {
		return brainerrors.New(brainerrors.CodeBrainTaskFailed,
			brainerrors.WithMessage("intentional failure for compliance test"))
	})

	runner.Register(ComplianceTest{
		ID:          "C-03",
		Description: "skipped test",
		Category:    "loop",
	}, func(ctx context.Context) error {
		return ErrComplianceSkip
	})

	report, err := runner.RunAll(context.Background())
	if err != nil {
		t.Fatalf("RunAll: unexpected error: %v", err)
	}

	// Validate summary counts.
	if report.Summary.Total != 3 {
		t.Errorf("Total: want 3, got %d", report.Summary.Total)
	}
	if report.Summary.Passed != 1 {
		t.Errorf("Passed: want 1, got %d", report.Summary.Passed)
	}
	if report.Summary.Failed != 1 {
		t.Errorf("Failed: want 1, got %d", report.Summary.Failed)
	}
	if report.Summary.Skipped != 1 {
		t.Errorf("Skipped: want 1, got %d", report.Summary.Skipped)
	}

	// Spot-check individual results.
	if r := report.Results["C-01"]; r == nil || r.Status != "pass" {
		t.Errorf("C-01: want status=pass, got %v", r)
	}
	if r := report.Results["C-02"]; r == nil || r.Status != "fail" {
		t.Errorf("C-02: want status=fail, got %v", r)
	}
	if r := report.Results["C-03"]; r == nil || r.Status != "skipped" {
		t.Errorf("C-03: want status=skipped, got %v", r)
	}
}

// TestComplianceIntegration_PassFail tests pass/fail without skipped sentinel,
// ensuring the runner does not stop on the first failure.
func TestComplianceIntegration_PassFail(t *testing.T) {
	runner := NewComplianceRunner().(*MemComplianceRunner)

	runner.Register(ComplianceTest{ID: "P-01", Description: "pass", Category: "protocol"},
		func(ctx context.Context) error { return nil })

	runner.Register(ComplianceTest{ID: "P-02", Description: "fail", Category: "error"},
		func(ctx context.Context) error {
			return brainerrors.New(brainerrors.CodeBrainTaskFailed,
				brainerrors.WithMessage("deliberate"))
		})

	report, err := runner.RunAll(context.Background())
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if report.Summary.Total != 2 {
		t.Errorf("Total: want 2, got %d", report.Summary.Total)
	}
	if report.Summary.Passed != 1 {
		t.Errorf("Passed: want 1, got %d", report.Summary.Passed)
	}
	if report.Summary.Failed != 1 {
		t.Errorf("Failed: want 1, got %d", report.Summary.Failed)
	}
}
