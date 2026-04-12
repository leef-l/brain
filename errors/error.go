package errors

import (
	"encoding/json"
	"time"
)

// BrainError is the v1 frozen carrier struct defined in 21-错误模型.md §3.1.
//
// BrainError is the only error type allowed to cross Runner/Kernel/sidecar
// boundaries inside BrainKernel. Business code MUST return *BrainError and
// MUST NOT let bare fmt.Errorf / errors.New values leak past those
// boundaries. Construction goes through the New / Wrap builders in new.go
// (see 21 §3.3). Fields are exported so that sidecar SDKs in other
// languages can encode/decode wire frames symmetrically (21 §3.4), but
// callers SHOULD still prefer the constructors.
//
// JSON encoding preserves snake_case for cross-language equivalence. The
// InternalOnly field is tagged json:"-" and is additionally filtered by the
// custom MarshalJSON implementation so it MUST NEVER appear on the wire or
// in an HTTP body — see 21 §7.4.
type BrainError struct {
	// Class is the six-value taxonomy that drives the retry/circuit-breaker
	// matrix. MUST be one of the ClassXxx constants from class.go. See
	// 21 §3.2 and §4.
	Class ErrorClass `json:"class"`

	// ErrorCode is a snake_case identifier from the reserved registry in
	// codes.go. MUST be <= 64 chars and MUST exist in the v1 clause before
	// being emitted on the wire. See 21 §5.
	ErrorCode string `json:"error_code"`

	// Retryable is the boolean hint the scheduler consults. It MUST be
	// consistent with Class (see the matrix in 21 appendix B); e.g.
	// ClassTransient + Retryable=false is a protocol violation.
	Retryable bool `json:"retryable"`

	// Fingerprint is the 16-hex-char aggregation key produced by the
	// Fingerprint function. See 21 §6. The field is set by the constructor
	// (New/Wrap) so callers rarely need to touch it.
	Fingerprint string `json:"fingerprint"`

	// TraceID is the W3C Trace Context trace ID. It is allowed on internal
	// JSON but MUST be stripped before reaching HTTP bodies — it may only be
	// surfaced in a response header. See 21 §7.4.
	TraceID string `json:"trace_id,omitempty"`

	// SpanID is the W3C Trace Context span ID companion to TraceID.
	SpanID string `json:"span_id,omitempty"`

	// Message is a single human-readable sentence describing the failure.
	// MUST be non-empty and SHOULD be <= 200 chars. This is the string
	// surfaced in logs and UI after sanitization.
	Message string `json:"message"`

	// Hint is an optional self-service troubleshooting note. Still public —
	// do not put secrets here.
	Hint string `json:"hint,omitempty"`

	// Cause is the upstream BrainError that produced this failure. Each
	// propagation layer MUST wrap rather than swallow (21 §7.1 rule 1). Use
	// Unwrap() to walk the chain with errors.Is / errors.As.
	Cause *BrainError `json:"cause,omitempty"`

	// InternalOnly carries host-side debug information that MUST NOT be
	// serialized. Both the struct tag and the custom MarshalJSON enforce
	// this — see 21 §3.2 / §7.4.
	InternalOnly *InternalDetail `json:"-"`

	// BrainID is the identity of the brain that raised the error (central,
	// code, browser, ...). Used as fingerprint input and for per-brain
	// error budgets.
	BrainID string `json:"brain_id,omitempty"`

	// SidecarPID is the OS pid of the sidecar process at failure time. Not
	// used in fingerprints (pids change across restarts).
	SidecarPID int `json:"sidecar_pid,omitempty"`

	// OccurredAt is the UTC timestamp at which the error was constructed.
	// MUST be set by the constructor.
	OccurredAt time.Time `json:"occurred_at"`

	// Attempt is the 1-based retry counter attached by the scheduler. Not
	// used in fingerprints (21 §6.3).
	Attempt int `json:"attempt,omitempty"`

	// Suggestions is a list of actionable remediation steps shown to the
	// user. MUST be executable advice, not "please check the config".
	Suggestions []string `json:"suggestions,omitempty"`
}

// InternalDetail carries fields that live only inside the host process and
// MUST NOT be serialized to any wire or HTTP body. See 21 §3.1 and §7.4.
type InternalDetail struct {
	// Stack is the full Go stack (or the sidecar's language-native stack) at
	// the point of failure.
	Stack string

	// RawStderr is the sidecar's un-redacted stderr tail. Host-side logging
	// sinks may record it but MUST NOT forward it to end users.
	RawStderr string

	// DebugFlags is a free-form bag of structured debugging fields. It is
	// intentionally string->string so it can be safely dumped to log lines.
	DebugFlags map[string]string
}

// Error implements the Go error interface. It returns the Message field so
// `err.Error()` surfaces the human-readable sentence, matching the contract
// in 21 §3.1. Callers that need structured access MUST type-assert back to
// *BrainError rather than parse this string.
func (e *BrainError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap returns the upstream BrainError so standard library errors.Is and
// errors.As can walk the chain. Returning a typed nil would break errors.Is
// so we return untyped nil when Cause is absent.
//
// Example:
//
//	if errors.Is(err, otherBrainErr) { ... }
//	var be *BrainError
//	if errors.As(err, &be) { ... }
func (e *BrainError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return nil
	}
	return e.Cause
}

// Is enables errors.Is matching by ErrorCode. Two BrainErrors match when
// they have the same ErrorCode — Class alone is too coarse and Fingerprint
// is too fine. This is the middle ground most callers want.
func (e *BrainError) Is(target error) bool {
	if e == nil || target == nil {
		return false
	}
	other, ok := target.(*BrainError)
	if !ok {
		return false
	}
	return e.ErrorCode == other.ErrorCode
}

// MarshalJSON implements json.Marshaler to guarantee InternalOnly is never
// emitted. The struct tag `json:"-"` already does this for top-level field
// traversal, but the custom marshaler is a defense-in-depth check that also
// recursively cleans Cause chains — a manually crafted BrainError tree with
// an InternalOnly planted deep in the cause list still goes out clean.
//
// See 21 §3.2 field-level MUST list and §7.4 HTTP sanitization rule.
func (e *BrainError) MarshalJSON() ([]byte, error) {
	if e == nil {
		return []byte("null"), nil
	}
	// Use a shadow alias so json.Marshal doesn't call back into MarshalJSON
	// infinitely. The alias has the same layout minus the InternalOnly field
	// (which is already json:"-") and with Cause pointing to a shadow copy
	// for recursive sanitization.
	type alias BrainError
	shadow := alias(*e)
	shadow.InternalOnly = nil // redundant but explicit
	if shadow.Cause != nil {
		// Recursively sanitize — copy so we don't mutate the caller's tree.
		clean := *shadow.Cause
		clean.InternalOnly = nil
		shadow.Cause = &clean
	}
	return json.Marshal(shadow)
}

// ToSpanAttrs returns the OTel-compatible attribute set for this error, per
// 21 §13 (audit + observability). The returned map uses the semantic
// convention keys from the OpenTelemetry spec so tracing backends can index
// errors without per-vendor mapping code.
//
// This method intentionally returns a plain map[string]string so the
// observability package can consume it without importing the errors package
// back — see otel_hook.go for the hook indirection that prevents a cycle.
//
// Keys produced:
//
//	error.type            — always "brain"
//	error.code            — ErrorCode
//	error.class           — Class
//	error.retryable       — "true" / "false"
//	error.fingerprint     — Fingerprint
//	brain.id              — BrainID (omitted when empty)
//	brain.attempt         — Attempt (omitted when zero)
//	error.cause.code      — Cause.ErrorCode (omitted when no cause)
//	error.cause.class     — Cause.Class (omitted when no cause)
func (e *BrainError) ToSpanAttrs() map[string]string {
	if e == nil {
		return nil
	}
	attrs := map[string]string{
		"error.type":        "brain",
		"error.code":        e.ErrorCode,
		"error.class":       string(e.Class),
		"error.retryable":   boolString(e.Retryable),
		"error.fingerprint": e.Fingerprint,
	}
	if e.BrainID != "" {
		attrs["brain.id"] = e.BrainID
	}
	if e.Attempt > 0 {
		attrs["brain.attempt"] = itoa(e.Attempt)
	}
	if e.Cause != nil {
		attrs["error.cause.code"] = e.Cause.ErrorCode
		attrs["error.cause.class"] = string(e.Cause.Class)
	}
	return attrs
}

// boolString is a tiny helper that stringifies booleans without importing
// strconv. Keeps the errors package close to zero dependencies.
func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// itoa converts a small non-negative int to a decimal string without
// importing strconv. Only used by ToSpanAttrs for Attempt counts, which
// realistically stay below a few thousand.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
