// Package compliance registers and runs the full 150-test compliance suite
// defined in 28-SDK交付规范.md §8 and 25-测试策略.md §3.
//
// Each test is registered with MemComplianceRunner and exercised through
// the standard Go testing framework. The test file is the canonical entry
// point for `go test ./test/compliance/...`.
package compliance

import (
	"context"
	"testing"

	braintesting "github.com/leef-l/brain/sdk/testing"
)

// TestComplianceSuite registers all 150 compliance tests and runs them.
func TestComplianceSuite(t *testing.T) {
	runner := braintesting.NewComplianceRunner().(*braintesting.MemComplianceRunner)

	// Register all test categories.
	registerProtocolTests(runner)    // C-01 ~ C-20
	registerErrorTests(runner)       // C-E-01 ~ C-E-20
	registerLoopTests(runner)        // C-L-01 ~ C-L-20
	registerSecurityTests(runner)    // C-S-01 ~ C-S-20
	registerObservabilityTests(runner) // C-O-01 ~ C-O-15
	registerPersistenceTests(runner) // C-P-01 ~ C-P-15
	registerCLITests(runner)         // C-CLI-01 ~ C-CLI-20
	registerSDKTests(runner)         // C-SDK-01 ~ C-SDK-20

	// Verify we registered exactly 150 tests.
	tests := runner.List()
	if len(tests) != 150 {
		t.Fatalf("expected 150 compliance tests, got %d", len(tests))
	}

	// Run all tests via the compliance framework.
	report, err := runner.RunAll(context.Background())
	if err != nil {
		t.Fatalf("RunAll failed: %v", err)
	}

	// Report individual failures as Go test failures.
	for _, test := range tests {
		result := report.Results[test.ID]
		if result == nil {
			t.Errorf("%s: no result", test.ID)
			continue
		}
		t.Run(test.ID+"_"+test.Category, func(t *testing.T) {
			switch result.Status {
			case "fail":
				t.Errorf("%s FAILED: %s", test.ID, result.Error)
			case "skipped":
				t.Skipf("%s skipped", test.ID)
			}
		})
	}

	// Summary
	t.Logf("Compliance: %d passed, %d failed, %d skipped / %d total",
		report.Summary.Passed, report.Summary.Failed,
		report.Summary.Skipped, report.Summary.Total)
}
