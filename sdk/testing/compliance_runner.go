package braintesting

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// ComplianceTest is a single entry in the frozen v1 conformance suite
// defined in 25-测试策略.md §3 and enumerated in 25 Appendix A. Every SDK
// implementation (Go, TypeScript, Python, Rust) MUST pass the full set of
// ~150 tests to be considered protocol-compliant per 28-SDK交付规范.md.
//
// ID follows the numbering scheme from 25 §3.1 — e.g. "C-01" for protocol
// tests, "C-E-01" for error-model tests, "C-L-01" for Agent-Loop tests.
type ComplianceTest struct {
	// ID is the stable test identifier from 25 Appendix A.
	ID string

	// Description is a short human-readable summary of what the test
	// asserts. It is surfaced in compliance reports.
	Description string

	// Category groups tests for reporting purposes. See 25 §3.1 for the
	// allowed values: protocol / error / loop / security / persistence /
	// observability / cli.
	Category string
}

// TestResult captures the outcome of a single ComplianceTest invocation.
// See 25-测试策略.md §3 for the required result schema used by the CI
// reporter and the cross-language dashboard in 28 §5.
type TestResult struct {
	// TestID matches ComplianceTest.ID.
	TestID string

	// Status is one of "pass", "fail" or "skipped". The three values are
	// frozen v1 — no other statuses are permitted.
	Status string

	// DurationMS is the wall-clock duration of the test in milliseconds.
	DurationMS int64

	// Error is the stringified BrainError.Code:Message for failing tests,
	// empty for passes. Implementations MUST populate this field whenever
	// Status is "fail" so that the cross-SDK diff report in 28 §5 is
	// actionable.
	Error string
}

// Summary aggregates TestResult counts for a single ComplianceReport.
// See 25-测试策略.md §3 for the canonical summary format.
type Summary struct {
	// Total is the number of tests executed (passed + failed + skipped).
	Total int

	// Passed is the number of tests whose Status is "pass".
	Passed int

	// Failed is the number of tests whose Status is "fail".
	Failed int

	// Skipped is the number of tests whose Status is "skipped".
	Skipped int
}

// ComplianceReport is the aggregate output of ComplianceRunner.RunAll and
// the serialised artifact uploaded to the cross-SDK dashboard in
// 28-SDK交付规范.md §5. Fields are stable v1 and MUST NOT be renamed.
type ComplianceReport struct {
	// SDKLanguage is the target SDK identifier, e.g. "go", "ts", "py".
	SDKLanguage string

	// SDKVersion is the semver of the SDK under test.
	SDKVersion string

	// KernelVersion is the semver of the BrainKernel build used to host
	// the tests.
	KernelVersion string

	// ProtocolVersion is the wire protocol version from 20-协议规格.md §2.
	ProtocolVersion string

	// RunAt is the wall-clock time at which RunAll started.
	RunAt time.Time

	// Results maps ComplianceTest.ID → TestResult for every test that was
	// executed. Skipped tests MUST also appear so consumers can
	// distinguish "not implemented" from "not run".
	Results map[string]*TestResult

	// Summary is the pre-aggregated count matching Results.
	Summary Summary
}

// ComplianceRunner executes the frozen v1 compliance suite defined in
// 25-测试策略.md §3. List enumerates the tests registered with this runner,
// Run executes a single test by ID, and RunAll executes the full suite and
// returns an aggregate report suitable for upload to the cross-SDK
// dashboard (28-SDK交付规范.md §5).
//
// Implementations MUST be deterministic: running the same suite against the
// same Kernel build twice MUST produce identical Results maps (barring
// DurationMS). Non-determinism is a compliance failure in its own right.
type ComplianceRunner interface {
	// List returns the full set of ComplianceTest entries this runner
	// knows about, in stable ID order.
	List() []ComplianceTest

	// Run executes a single test by ID and returns its result. It MUST
	// return a Permanent BrainError when testID is unknown.
	Run(ctx context.Context, testID string) (*TestResult, error)

	// RunAll executes every test returned by List and aggregates the
	// outcomes into a ComplianceReport. It MUST NOT stop on the first
	// failure; every test MUST be attempted so the report is complete.
	RunAll(ctx context.Context) (*ComplianceReport, error)
}

// ComplianceTestFunc is the executable side of a registered compliance
// test: it receives the runner's shared context and returns nil on
// success or a *BrainError describing the failure. Returning the
// sentinel ErrComplianceSkip marks the test as "skipped" in the report
// instead of "fail", matching the 25 §3 three-way status scheme.
type ComplianceTestFunc func(ctx context.Context) error

// ErrComplianceSkip is the sentinel a ComplianceTestFunc returns when
// the test cannot run in the current environment (e.g. a network-only
// test on an offline runner). The runner translates it to
// TestResult.Status="skipped".
var ErrComplianceSkip = brainerrors.New(brainerrors.CodeShuttingDown,
	brainerrors.WithMessage("compliance: test skipped"))

// MemComplianceRunner is the in-process ComplianceRunner from
// 25-测试策略.md §3. It keeps a registry of ComplianceTest descriptors
// alongside their executable ComplianceTestFunc bodies and runs them
// deterministically (sorted by ID) in the test binary that imports
// this package.
//
// The concrete compliance cases (C-01..C-P-14..C-L-14..) are NOT
// hard-coded here — they are registered by the individual brain
// sub-packages (brain/errors, brain/protocol, brain/persistence, ...)
// through Register. That keeps the runner decoupled from the data
// model and lets each sub-package own the tests that assert its own
// contract.
type MemComplianceRunner struct {
	// SDKLanguage is copied into every ComplianceReport the runner
	// produces. Defaults to "go".
	SDKLanguage string
	// SDKVersion is copied into every ComplianceReport.
	SDKVersion string
	// KernelVersion is copied into every ComplianceReport.
	KernelVersion string
	// ProtocolVersion is copied into every ComplianceReport.
	ProtocolVersion string

	mu    sync.Mutex
	tests map[string]*registeredTest
}

type registeredTest struct {
	desc ComplianceTest
	fn   ComplianceTestFunc
}

// NewComplianceRunner returns a new ComplianceRunner bound to the current
// Kernel build. The concrete wiring is filled in by cmd/brain compliance
// in wave 3; this skeleton exists so that 25 §3 and 28 §5 consumers can
// compile-time reference the entry point.
func NewComplianceRunner() ComplianceRunner {
	return &MemComplianceRunner{
		SDKLanguage:     "go",
		SDKVersion:      "0.0.0-dev",
		KernelVersion:   "0.0.0-dev",
		ProtocolVersion: "1.0",
		tests:           make(map[string]*registeredTest),
	}
}

// Register adds a test to the runner's catalog. Re-registering an ID
// panics because tests are meant to be statically declared at package
// init time — a duplicate usually signals a copy-paste error.
func (r *MemComplianceRunner) Register(desc ComplianceTest, fn ComplianceTestFunc) {
	if desc.ID == "" {
		panic("braintesting.MemComplianceRunner.Register: ComplianceTest.ID is required")
	}
	if fn == nil {
		panic("braintesting.MemComplianceRunner.Register: test function is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tests[desc.ID]; exists {
		panic(fmt.Sprintf(
			"braintesting.MemComplianceRunner.Register: duplicate test id %q",
			desc.ID,
		))
	}
	r.tests[desc.ID] = &registeredTest{desc: desc, fn: fn}
}

// List returns the registered tests in stable, lexicographic ID order.
// The stable order is a hard requirement of 25 §3 so CI reporters can
// diff two runs deterministically.
func (r *MemComplianceRunner) List() []ComplianceTest {
	r.mu.Lock()
	ids := make([]string, 0, len(r.tests))
	for id := range r.tests {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	sort.Strings(ids)
	out := make([]ComplianceTest, 0, len(ids))
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if t, ok := r.tests[id]; ok {
			out = append(out, t.desc)
		}
	}
	return out
}

// Run executes a single test by ID. An unknown ID trips a
// CodeRecordNotFound BrainError so callers can distinguish "test does
// not exist" from "test exists and failed".
func (r *MemComplianceRunner) Run(ctx context.Context, testID string) (*TestResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	r.mu.Lock()
	t, ok := r.tests[testID]
	r.mu.Unlock()
	if !ok {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf(
				"MemComplianceRunner.Run: unknown test id %q", testID,
			)),
		)
	}
	return r.runOne(ctx, t), nil
}

// RunAll executes every registered test in List order and returns the
// aggregated report. Failures do NOT short-circuit the loop — every
// test is attempted so the report is complete, per the contract in
// 25 §3.
func (r *MemComplianceRunner) RunAll(ctx context.Context) (*ComplianceReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	report := &ComplianceReport{
		SDKLanguage:     r.SDKLanguage,
		SDKVersion:      r.SDKVersion,
		KernelVersion:   r.KernelVersion,
		ProtocolVersion: r.ProtocolVersion,
		RunAt:           time.Now().UTC(),
		Results:         make(map[string]*TestResult),
	}
	for _, desc := range r.List() {
		r.mu.Lock()
		t := r.tests[desc.ID]
		r.mu.Unlock()
		if t == nil {
			continue
		}
		res := r.runOne(ctx, t)
		report.Results[desc.ID] = res
		report.Summary.Total++
		switch res.Status {
		case "pass":
			report.Summary.Passed++
		case "fail":
			report.Summary.Failed++
		case "skipped":
			report.Summary.Skipped++
		}
		// Honour ctx cancellation between tests so a killed CI run
		// does not keep burning compute — but the partial report up
		// to this point is still returned so debuggers can inspect
		// which test tripped the cancel.
		if err := ctx.Err(); err != nil {
			return report, wrapCtxErr(err)
		}
	}
	return report, nil
}

// runOne dispatches a single registered test and formats its outcome.
// A panic inside the test function is converted into a "fail" result
// with a CodePanicked error string — the panic is NOT rethrown because
// doing so would violate the "every test MUST be attempted" rule.
func (r *MemComplianceRunner) runOne(ctx context.Context, t *registeredTest) (res *TestResult) {
	start := time.Now()
	res = &TestResult{TestID: t.desc.ID}
	defer func() {
		if rec := recover(); rec != nil {
			res.Status = "fail"
			res.Error = fmt.Sprintf("%s: panic: %v",
				brainerrors.CodePanicked, rec)
		}
		res.DurationMS = time.Since(start).Milliseconds()
	}()

	err := t.fn(ctx)
	switch {
	case err == nil:
		res.Status = "pass"
	case isSkip(err):
		res.Status = "skipped"
	default:
		res.Status = "fail"
		res.Error = formatComplianceError(err)
	}
	return res
}

// isSkip detects the sentinel returned by ComplianceTestFunc to mark
// a test as skipped. We compare by identity first (fast path) and by
// ErrorCode second (so wrapped sentinels are still recognised).
func isSkip(err error) bool {
	if err == nil {
		return false
	}
	if err == ErrComplianceSkip {
		return true
	}
	if be, ok := err.(*brainerrors.BrainError); ok && be != nil {
		// Tests may construct their own skip errors with a
		// matching code; recognising the code keeps the sentinel
		// optional.
		return be.ErrorCode == brainerrors.CodeShuttingDown &&
			be.Message == "compliance: test skipped"
	}
	return false
}

// formatComplianceError renders err as "code:message" so the report
// is diffable across SDK implementations. Non-BrainError values fall
// back to their Error() string.
func formatComplianceError(err error) string {
	if be, ok := err.(*brainerrors.BrainError); ok && be != nil {
		return fmt.Sprintf("%s: %s", be.ErrorCode, be.Message)
	}
	return err.Error()
}
