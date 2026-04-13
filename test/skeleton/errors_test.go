package skeleton

import (
	"encoding/json"
	"errors"
	"testing"

	brainerrors "github.com/leef-l/brain/errors"
)

// ---------------------------------------------------------------------------
// New / Wrap 构造
// ---------------------------------------------------------------------------

func TestNewSetsClass(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeSidecarHung,
		brainerrors.WithMessage("test hung"),
	)
	if err.Class != brainerrors.ClassTransient {
		t.Errorf("Class = %q, want transient", err.Class)
	}
	if !err.Retryable {
		t.Error("SidecarHung should be retryable")
	}
}

func TestNewUnregisteredFallback(t *testing.T) {
	err := brainerrors.New("this_code_does_not_exist",
		brainerrors.WithMessage("oops"),
	)
	if err.ErrorCode != brainerrors.CodeUnknown {
		t.Errorf("ErrorCode = %q, want %q", err.ErrorCode, brainerrors.CodeUnknown)
	}
	if err.Class != brainerrors.ClassInternalBug {
		t.Errorf("Class = %q, want internal_bug", err.Class)
	}
}

func TestWrapNilEqualsNew(t *testing.T) {
	err := brainerrors.Wrap(nil, brainerrors.CodeToolNotFound,
		brainerrors.WithMessage("not found"),
	)
	if err.Cause != nil {
		t.Error("Wrap(nil, ...) should produce nil Cause")
	}
	if err.ErrorCode != brainerrors.CodeToolNotFound {
		t.Errorf("ErrorCode = %q, want %q", err.ErrorCode, brainerrors.CodeToolNotFound)
	}
}

func TestWrapBrainError(t *testing.T) {
	inner := brainerrors.New(brainerrors.CodeLLMUpstream5xx,
		brainerrors.WithMessage("upstream 500"),
	)
	outer := brainerrors.Wrap(inner, brainerrors.CodeSidecarHung,
		brainerrors.WithMessage("sidecar lost"),
	)
	if outer.Cause == nil {
		t.Fatal("Wrap should set Cause")
	}
	if outer.Cause.ErrorCode != brainerrors.CodeLLMUpstream5xx {
		t.Errorf("Cause.ErrorCode = %q, want %q", outer.Cause.ErrorCode, brainerrors.CodeLLMUpstream5xx)
	}
}

func TestWrapGoError(t *testing.T) {
	goErr := errors.New("raw go error")
	wrapped := brainerrors.Wrap(goErr, brainerrors.CodeToolExecutionFailed,
		brainerrors.WithMessage("tool broke"),
	)
	if wrapped.Cause == nil {
		t.Fatal("Wrap(goErr) should set Cause")
	}
	if wrapped.Cause.Message != "raw go error" {
		t.Errorf("Cause.Message = %q, want %q", wrapped.Cause.Message, "raw go error")
	}
}

// ---------------------------------------------------------------------------
// Error() / Unwrap() / Is()
// ---------------------------------------------------------------------------

func TestErrorInterface(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeToolNotFound,
		brainerrors.WithMessage("tool xyz not found"),
	)
	if err.Error() != "tool xyz not found" {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestUnwrapChain(t *testing.T) {
	cause := brainerrors.New(brainerrors.CodeLLMAuthFailed,
		brainerrors.WithMessage("bad key"),
	)
	outer := brainerrors.Wrap(cause, brainerrors.CodeToolExecutionFailed,
		brainerrors.WithMessage("tool auth failed"),
	)
	var target *brainerrors.BrainError
	if !errors.As(outer, &target) {
		t.Fatal("errors.As should match")
	}
	if target.ErrorCode != brainerrors.CodeToolExecutionFailed {
		t.Errorf("ErrorCode = %q", target.ErrorCode)
	}
}

func TestIsMatchesByErrorCode(t *testing.T) {
	a := brainerrors.New(brainerrors.CodeToolNotFound, brainerrors.WithMessage("a"))
	b := brainerrors.New(brainerrors.CodeToolNotFound, brainerrors.WithMessage("b"))
	if !errors.Is(a, b) {
		t.Error("same ErrorCode should match via Is")
	}
}

// ---------------------------------------------------------------------------
// MarshalJSON — InternalOnly 不泄露
// ---------------------------------------------------------------------------

func TestMarshalJSONOmitsInternalOnly(t *testing.T) {
	err := brainerrors.New(brainerrors.CodePanicked,
		brainerrors.WithMessage("panic recovery"),
		brainerrors.WithStack("goroutine 1 [running]:\nmain.go:42"),
		brainerrors.WithRawStderr("segfault"),
	)
	data, jsonErr := json.Marshal(err)
	if jsonErr != nil {
		t.Fatal(jsonErr)
	}
	var m map[string]interface{}
	if e := json.Unmarshal(data, &m); e != nil {
		t.Fatal(e)
	}
	if _, ok := m["InternalOnly"]; ok {
		t.Error("InternalOnly leaked into JSON")
	}
	if _, ok := m["internal_only"]; ok {
		t.Error("internal_only leaked into JSON")
	}
}

// ---------------------------------------------------------------------------
// Fingerprint 确定性
// ---------------------------------------------------------------------------

func TestFingerprintDeterministic(t *testing.T) {
	a := brainerrors.New(brainerrors.CodeSidecarHung,
		brainerrors.WithMessage("missed 3 heartbeats"),
		brainerrors.WithBrainID("central"),
	)
	b := brainerrors.New(brainerrors.CodeSidecarHung,
		brainerrors.WithMessage("missed 3 heartbeats"),
		brainerrors.WithBrainID("central"),
	)
	if a.Fingerprint != b.Fingerprint {
		t.Errorf("fingerprints differ: %q vs %q", a.Fingerprint, b.Fingerprint)
	}
	if len(a.Fingerprint) != 16 {
		t.Errorf("Fingerprint length = %d, want 16", len(a.Fingerprint))
	}
}

// ---------------------------------------------------------------------------
// ToSpanAttrs
// ---------------------------------------------------------------------------

func TestToSpanAttrs(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeToolTimeout,
		brainerrors.WithMessage("timeout"),
		brainerrors.WithBrainID("code"),
		brainerrors.WithAttempt(2),
	)
	attrs := err.ToSpanAttrs()
	if attrs["error.type"] != "brain" {
		t.Errorf("error.type = %q", attrs["error.type"])
	}
	if attrs["error.code"] != brainerrors.CodeToolTimeout {
		t.Errorf("error.code = %q", attrs["error.code"])
	}
	if attrs["brain.id"] != "code" {
		t.Errorf("brain.id = %q", attrs["brain.id"])
	}
	if attrs["brain.attempt"] != "2" {
		t.Errorf("brain.attempt = %q", attrs["brain.attempt"])
	}
}

// ---------------------------------------------------------------------------
// Options 覆盖
// ---------------------------------------------------------------------------

func TestWithOptions(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeSidecarCrashed,
		brainerrors.WithMessage("crash"),
		brainerrors.WithHint("check stderr"),
		brainerrors.WithBrainID("browser"),
		brainerrors.WithSidecarPID(12345),
		brainerrors.WithAttempt(3),
		brainerrors.WithSuggestions("restart", "check logs"),
		brainerrors.WithTraceID("abc123"),
		brainerrors.WithSpanID("span456"),
	)
	if err.Hint != "check stderr" {
		t.Errorf("Hint = %q", err.Hint)
	}
	if err.BrainID != "browser" {
		t.Errorf("BrainID = %q", err.BrainID)
	}
	if err.SidecarPID != 12345 {
		t.Errorf("SidecarPID = %d", err.SidecarPID)
	}
	if err.Attempt != 3 {
		t.Errorf("Attempt = %d", err.Attempt)
	}
	if len(err.Suggestions) != 2 {
		t.Errorf("Suggestions len = %d", len(err.Suggestions))
	}
	if err.TraceID != "abc123" {
		t.Errorf("TraceID = %q", err.TraceID)
	}
	if err.SpanID != "span456" {
		t.Errorf("SpanID = %q", err.SpanID)
	}
}
