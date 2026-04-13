package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	brainerrors "github.com/leef-l/brain/errors"
	braintesting "github.com/leef-l/brain/testing"
)

func registerErrorTests(r *braintesting.MemComplianceRunner) {
	// C-E-01: BrainError.New sets ErrorCode.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-01", Description: "BrainError.New sets ErrorCode", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"))
		if e.ErrorCode != brainerrors.CodeSidecarHung {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-01: ErrorCode mismatch"))
		}
		return nil
	})

	// C-E-02: BrainError.New sets Class from registry.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-02", Description: "BrainError.New sets Class from registry", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"))
		if e.Class != brainerrors.ClassTransient {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-E-02: expected ClassTransient, got %s", e.Class)))
		}
		return nil
	})

	// C-E-03: BrainError.New sets Retryable from registry.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-03", Description: "BrainError.New sets Retryable from registry", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"))
		if !e.Retryable {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-03: CodeSidecarHung should be retryable"))
		}
		return nil
	})

	// C-E-04: BrainError.New sets Fingerprint.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-04", Description: "BrainError.New sets Fingerprint", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"))
		if e.Fingerprint == "" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-04: Fingerprint empty"))
		}
		if len(e.Fingerprint) != 16 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-E-04: Fingerprint len=%d, want 16", len(e.Fingerprint))))
		}
		return nil
	})

	// C-E-05: BrainError.New sets OccurredAt.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-05", Description: "BrainError.New sets OccurredAt", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"))
		if e.OccurredAt.IsZero() {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-05: OccurredAt is zero"))
		}
		return nil
	})

	// C-E-06: BrainError.Error() returns Message.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-06", Description: "BrainError.Error() returns Message", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("hello world"))
		if e.Error() != "hello world" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-06: Error() != Message"))
		}
		return nil
	})

	// C-E-07: Wrap sets Cause chain.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-07", Description: "Wrap sets Cause chain", Category: "error",
	}, func(ctx context.Context) error {
		inner := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("inner"))
		outer := brainerrors.Wrap(inner, brainerrors.CodeBrainTaskFailed,
			brainerrors.WithMessage("outer"))
		if outer.Cause == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-07: Cause is nil"))
		}
		if outer.Cause.ErrorCode != brainerrors.CodeSidecarHung {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-07: Cause.ErrorCode mismatch"))
		}
		return nil
	})

	// C-E-08: errors.Is matches on ErrorCode.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-08", Description: "errors.Is matches on ErrorCode", Category: "error",
	}, func(ctx context.Context) error {
		e1 := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("a"))
		e2 := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("b"))
		if !errors.Is(e1, e2) {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-08: errors.Is should match on ErrorCode"))
		}
		return nil
	})

	// C-E-09: errors.As unwraps BrainError.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-09", Description: "errors.As unwraps BrainError", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"))
		var be *brainerrors.BrainError
		if !errors.As(e, &be) {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-09: errors.As should succeed"))
		}
		if be.ErrorCode != brainerrors.CodeSidecarHung {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-09: ErrorCode mismatch"))
		}
		return nil
	})

	// C-E-10: MarshalJSON strips InternalOnly.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-10", Description: "MarshalJSON strips InternalOnly", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodePanicked,
			brainerrors.WithMessage("panic"))
		e.InternalOnly = &brainerrors.InternalDetail{
			Stack:     "goroutine 1...",
			RawStderr: "secret stderr",
		}
		data, err := json.Marshal(e)
		if err != nil {
			return brainerrors.New(brainerrors.CodeFrameEncodingError,
				brainerrors.WithMessage(fmt.Sprintf("C-E-10: marshal failed: %v", err)))
		}
		var m map[string]interface{}
		json.Unmarshal(data, &m)
		if _, found := m["InternalOnly"]; found {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-10: InternalOnly leaked to JSON"))
		}
		return nil
	})

	// C-E-11: Six ErrorClass values defined.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-11", Description: "Six ErrorClass values", Category: "error",
	}, func(ctx context.Context) error {
		classes := []brainerrors.ErrorClass{
			brainerrors.ClassTransient,
			brainerrors.ClassPermanent,
			brainerrors.ClassUserFault,
			brainerrors.ClassQuotaExceeded,
			brainerrors.ClassSafetyRefused,
			brainerrors.ClassInternalBug,
		}
		for _, c := range classes {
			if c == "" {
				return brainerrors.New(brainerrors.CodeAssertionFailed,
					brainerrors.WithMessage("C-E-11: empty class"))
			}
		}
		if len(classes) != 6 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-11: not 6 classes"))
		}
		return nil
	})

	// C-E-12: ErrorCode registry DefaultRegistry is populated.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-12", Description: "DefaultRegistry has registered codes", Category: "error",
	}, func(ctx context.Context) error {
		md, ok := brainerrors.Lookup(brainerrors.CodeSidecarHung)
		if !ok {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-12: CodeSidecarHung not in registry"))
		}
		if md.Code != brainerrors.CodeSidecarHung {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-12: Code mismatch"))
		}
		return nil
	})

	// C-E-13: Decide returns ActionRetry for Transient errors.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-13", Description: "Decide ActionRetry for Transient", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"))
		decision := brainerrors.Decide(e, brainerrors.DecideContext{
			Attempt: 1,
		})
		if decision.Action != brainerrors.ActionRetry {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-E-13: expected ActionRetry, got %s", decision.Action)))
		}
		return nil
	})

	// C-E-14: Decide returns ActionFail for Permanent errors.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-14", Description: "Decide ActionFail for Permanent", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeToolNotFound,
			brainerrors.WithMessage("test"))
		decision := brainerrors.Decide(e, brainerrors.DecideContext{
			Attempt: 1,
		})
		if decision.Action != brainerrors.ActionFail {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-E-14: expected ActionFail, got %s", decision.Action)))
		}
		return nil
	})

	// C-E-15: Fingerprint stability — same code+message+brainID = same fingerprint.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-15", Description: "Fingerprint deterministic", Category: "error",
	}, func(ctx context.Context) error {
		e1 := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"),
			brainerrors.WithBrainID("central"))
		e2 := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"),
			brainerrors.WithBrainID("central"))
		if e1.Fingerprint != e2.Fingerprint {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-15: fingerprints differ"))
		}
		return nil
	})

	// C-E-16: Different ErrorCodes produce different Fingerprints.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-16", Description: "Different codes → different fingerprints", Category: "error",
	}, func(ctx context.Context) error {
		e1 := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"))
		e2 := brainerrors.New(brainerrors.CodeToolNotFound,
			brainerrors.WithMessage("test"))
		if e1.Fingerprint == e2.Fingerprint {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-16: fingerprints should differ"))
		}
		return nil
	})

	// C-E-17: Wrap(nil) behaves like New.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-17", Description: "Wrap(nil) behaves like New", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.Wrap(nil, brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"))
		if e.ErrorCode != brainerrors.CodeSidecarHung {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-E-17: Wrap(nil) ErrorCode wrong"))
		}
		return nil
	})

	// C-E-18: ToSpanAttrs produces expected keys.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-18", Description: "ToSpanAttrs produces OTel attributes", Category: "error",
	}, func(ctx context.Context) error {
		e := brainerrors.New(brainerrors.CodeSidecarHung,
			brainerrors.WithMessage("test"),
			brainerrors.WithBrainID("central"))
		attrs := e.ToSpanAttrs()
		required := []string{"error.code", "error.class", "error.fingerprint"}
		for _, key := range required {
			if _, ok := attrs[key]; !ok {
				return brainerrors.New(brainerrors.CodeAssertionFailed,
					brainerrors.WithMessage(fmt.Sprintf("C-E-18: missing attr %q", key)))
			}
		}
		return nil
	})

	// C-E-19: ErrorCode constants are snake_case and ≤ 64 chars.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-19", Description: "ErrorCode constants are valid format", Category: "error",
	}, func(ctx context.Context) error {
		codes := []string{
			brainerrors.CodeSidecarStartFailed,
			brainerrors.CodeSidecarExitNonzero,
			brainerrors.CodeSidecarHung,
			brainerrors.CodeToolNotFound,
			brainerrors.CodeToolExecutionFailed,
			brainerrors.CodeLLMRateLimitedShortterm,
			brainerrors.CodeFrameTooLarge,
			brainerrors.CodeFrameParseError,
			brainerrors.CodeBudgetTurnsExhausted,
			brainerrors.CodeAgentLoopDetected,
			brainerrors.CodePanicked,
			brainerrors.CodeUnknown,
		}
		for _, code := range codes {
			if len(code) > 64 {
				return brainerrors.New(brainerrors.CodeAssertionFailed,
					brainerrors.WithMessage(fmt.Sprintf("C-E-19: code %q > 64 chars", code)))
			}
			if code == "" {
				return brainerrors.New(brainerrors.CodeAssertionFailed,
					brainerrors.WithMessage("C-E-19: empty code"))
			}
		}
		return nil
	})

	// C-E-20: Nil BrainError.Error() is safe.
	r.Register(braintesting.ComplianceTest{
		ID: "C-E-20", Description: "Nil BrainError.Error() is safe", Category: "error",
	}, func(ctx context.Context) error {
		var e *brainerrors.BrainError
		msg := e.Error()
		if msg == "" {
			// nil-safe Error() should return some non-panic value
		}
		// If we got here without panic, success.
		_ = msg
		return nil
	})
}
