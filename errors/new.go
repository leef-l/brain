package errors

import (
	stderrors "errors"
	"time"
)

// Option configures a BrainError during construction. Options are applied in
// order by New / Wrap, so later options override earlier ones. They exist as
// a functional-options bag rather than a big struct because most call sites
// set only two or three fields (Message + one hint) and a struct would make
// the call sites verbose.
//
// Options MUST be side-effect free with respect to anything other than the
// BrainError pointer they receive — they run before Fingerprint, so
// mutating shared state would break the determinism guarantee in 21 §6.1.
type Option func(*BrainError)

// now is the clock used by New / Wrap. It is a package variable so tests can
// swap it out for a deterministic stub without threading a clock interface
// through every constructor. Production code MUST NOT touch this; the
// default reads the real UTC time.
var now = func() time.Time { return time.Now().UTC() }

// New constructs a fresh BrainError for code and applies opts. The Class
// and Retryable defaults are read from the registry (21 §5.1); if code is
// not registered, New falls back to CodeUnknown + ClassInternalBug and
// records the original code in the InternalOnly.DebugFlags map so the
// alerting layer can surface the registry violation.
//
// OccurredAt is always stamped with now() so callers don't have to wire a
// clock. Fingerprint is computed after all options have run so that options
// that mutate Message / BrainID / Cause contribute to the hash. The
// returned pointer is never nil — a failed Lookup still yields a valid
// BrainError whose ErrorCode is CodeUnknown.
//
// Example:
//
//	err := errors.New(errors.CodeSidecarHung,
//	    errors.WithMessage("sidecar central missed 3 heartbeats"),
//	    errors.WithBrainID("central"),
//	    errors.WithAttempt(2),
//	)
func New(code string, opts ...Option) *BrainError {
	be := newShell(code)
	for _, opt := range opts {
		if opt != nil {
			opt(be)
		}
	}
	be.OccurredAt = now()
	be.Fingerprint = Fingerprint(be)
	DispatchError(be)
	return be
}

// Wrap builds a BrainError for code whose Cause is derived from err. The
// three input shapes are:
//
//  1. err == nil             → equivalent to New(code, opts...). See C-E-19.
//  2. err is *BrainError     → Cause is set to a deep copy of err so the
//                              receiver owns the chain and upstream mutations
//                              cannot bleed through.
//  3. err is any other error → Cause is a synthetic BrainError built with
//                              CodeUnknown + ClassInternalBug carrying the
//                              Go error's Error() text as its Message. This
//                              is the §4.3 fallback: unclassified Go errors
//                              MUST NOT leak past the boundary, but they
//                              MUST preserve the original string for debug.
//
// When err is a non-BrainError Go error and FromGoError maps it to a
// reserved code, Wrap uses that mapped code instead of CodeUnknown so the
// taxonomy stays accurate. This is the primary enforcement point for 21
// §4.3 "Go 标准错误映射".
//
// Example:
//
//	if _, err := os.Open(path); err != nil {
//	    return errors.Wrap(err, errors.CodeSidecarStdoutEOF,
//	        errors.WithMessage("sidecar central stdout closed early"),
//	        errors.WithBrainID("central"),
//	    )
//	}
func Wrap(err error, code string, opts ...Option) *BrainError {
	be := newShell(code)
	if err != nil {
		be.Cause = causeFrom(err)
	}
	for _, opt := range opts {
		if opt != nil {
			opt(be)
		}
	}
	be.OccurredAt = now()
	be.Fingerprint = Fingerprint(be)
	DispatchError(be)
	return be
}

// newShell is the shared bootstrap between New and Wrap. It stamps the code,
// looks up Class / Retryable in the registry, and falls back to
// CodeUnknown + ClassInternalBug when the code is not registered. The
// caller fills OccurredAt and Fingerprint after options run.
func newShell(code string) *BrainError {
	md, ok := Lookup(code)
	if !ok {
		// Registry miss — degrade to CodeUnknown per 21 §5.1 / §7.2 and
		// stash the original code in DebugFlags for post-mortem.
		unknown, _ := Lookup(CodeUnknown)
		return &BrainError{
			ErrorCode: unknown.Code,
			Class:     unknown.Class,
			Retryable: unknown.Retryable,
			Message:   "unregistered error code: " + code,
			InternalOnly: &InternalDetail{
				DebugFlags: map[string]string{
					"requested_code":     code,
					"registry_violation": "true",
				},
			},
		}
	}
	return &BrainError{
		ErrorCode: md.Code,
		Class:     md.Class,
		Retryable: md.Retryable,
	}
}

// causeFrom converts an arbitrary Go error into the *BrainError used for the
// Cause chain. BrainError values are deep-copied so upstream mutations do
// not bleed through the chain boundary. Non-BrainError values go through
// FromGoError first — if the stdlib mapper recognizes the error the cause
// is built with the mapped code and class; otherwise it lands in
// CodeUnknown + ClassInternalBug and carries the original Error() text so
// the debug trail survives.
func causeFrom(err error) *BrainError {
	// Prefer *BrainError — errors.As walks wrapped chains so callers can
	// `Wrap(fmt.Errorf("stage failed: %w", be), ...)` and still preserve
	// the BrainError tree.
	var be *BrainError
	if stderrors.As(err, &be) && be != nil {
		clone := *be
		return &clone
	}

	mappedCode, mapped := FromGoError(err)
	code := CodeUnknown
	if mapped {
		code = mappedCode
	}
	md, ok := Lookup(code)
	cause := &BrainError{
		ErrorCode: code,
		Message:   err.Error(),
	}
	if ok {
		cause.Class = md.Class
		cause.Retryable = md.Retryable
	} else {
		cause.Class = ClassInternalBug
	}
	cause.OccurredAt = now()
	cause.Fingerprint = Fingerprint(cause)
	return cause
}

// --- Options ----------------------------------------------------------------
//
// The Option setters below cover every field on BrainError that a caller is
// allowed to populate. Fields that are derived (Fingerprint, OccurredAt) or
// set by the constructor (Class, Retryable, ErrorCode) do not have public
// options. Exceptions:
//
//   - WithClass exists because a host-level extension that registers a
//     custom code may want to stamp a non-default class for a specific
//     call site. Using this is a smell — prefer registering the code
//     properly — and the option exists only for the extension API in
//     registry.go.
//   - WithRetryable lets callers override the static default, which is
//     legitimate for ClassQuotaExceeded "retry after N seconds" cases
//     that want Retryable=true only when the host supplies a backoff hint.
//     The Class invariant check in decide.go still applies.
//
// Options intentionally do not return error — malformed inputs are silently
// ignored. The constructor contract is that Options always succeed so call
// sites stay terse.

// WithMessage sets the single human-readable sentence surfaced in logs and
// UI. MUST be non-empty per 21 §3.2; empty strings are ignored so the
// option does not clobber a previously-set message.
func WithMessage(msg string) Option {
	return func(e *BrainError) {
		if msg != "" {
			e.Message = msg
		}
	}
}

// WithHint sets the self-service troubleshooting hint. Treated as public —
// callers MUST NOT put secrets here.
func WithHint(hint string) Option {
	return func(e *BrainError) { e.Hint = hint }
}

// WithBrainID stamps the identity of the brain that raised the error. This
// feeds the fingerprint (21 §6.1), so setting it after the fact via field
// assignment would break the hash — always pass via this option.
func WithBrainID(id string) Option {
	return func(e *BrainError) { e.BrainID = id }
}

// WithSidecarPID records the OS pid of the sidecar at failure time. Not
// part of the fingerprint (pids change across restarts).
func WithSidecarPID(pid int) Option {
	return func(e *BrainError) { e.SidecarPID = pid }
}

// WithAttempt sets the 1-based retry counter. Typically stamped by the
// scheduler when it re-submits a failed task; business code rarely sets it.
func WithAttempt(attempt int) Option {
	return func(e *BrainError) {
		if attempt > 0 {
			e.Attempt = attempt
		}
	}
}

// WithSuggestions attaches actionable remediation steps shown to the user.
// Per 21 §3.2 these MUST be executable advice, not vague prompts. The
// option replaces any previously-set list rather than appending so
// duplicate calls do not compound.
func WithSuggestions(suggestions ...string) Option {
	return func(e *BrainError) {
		if len(suggestions) == 0 {
			return
		}
		copied := make([]string, len(suggestions))
		copy(copied, suggestions)
		e.Suggestions = copied
	}
}

// WithTraceID stamps the W3C trace ID. Still ends up on internal JSON (via
// the struct tag) but MUST be stripped before leaving the HTTP boundary.
// The otel_hook.go sanitization pass enforces that on response.
func WithTraceID(traceID string) Option {
	return func(e *BrainError) { e.TraceID = traceID }
}

// WithSpanID stamps the W3C span ID companion to TraceID.
func WithSpanID(spanID string) Option {
	return func(e *BrainError) { e.SpanID = spanID }
}

// WithRetryable overrides the registry's static Retryable default. Use
// sparingly — for most codes the Class dictates Retryable and an override
// that contradicts the Class invariant is a protocol violation (see 21
// §3.2 field-level MUST list). The decide engine double-checks the
// invariant at scheduling time.
func WithRetryable(retryable bool) Option {
	return func(e *BrainError) { e.Retryable = retryable }
}

// WithClass overrides the registry's static Class default. This is the
// extension API escape hatch called out in 21 §5.2 for host-specific codes
// that cannot land in the frozen appendix A clause. Prefer registering the
// code via errors.Register instead — overriding here leaves the registry
// in a state where Lookup and the actual emitted error disagree.
func WithClass(class ErrorClass) Option {
	return func(e *BrainError) {
		if isKnownClass(class) {
			e.Class = class
		}
	}
}

// WithOccurredAt overrides the constructor's clock stamp. Reserved for
// tests that need deterministic timestamps — production code MUST let the
// constructor call now() so the ordering invariant in 21 §3.2 holds.
func WithOccurredAt(ts time.Time) Option {
	return func(e *BrainError) { e.OccurredAt = ts }
}

// WithCause sets the Cause chain explicitly. Mostly useful when manually
// reconstructing a BrainError tree from a persisted frame — the usual path
// is Wrap(cause, code, opts...).
func WithCause(cause *BrainError) Option {
	return func(e *BrainError) {
		if cause == nil {
			return
		}
		clone := *cause
		e.Cause = &clone
	}
}

// --- Internal-only options --------------------------------------------------
//
// The With*Internal* options populate BrainError.InternalOnly, which is the
// host-side-only debug bag (21 §3.1). The struct tag `json:"-"` and the
// MarshalJSON override in error.go both keep these fields off the wire, so
// setting them is always safe; end users will never see them.

// WithStack attaches the Go stack (or sidecar language stack) captured at
// the point of failure. Intended for panicked / assertion_failed call
// sites — ordinary control-flow errors don't need a stack.
func WithStack(stack string) Option {
	return func(e *BrainError) {
		if stack == "" {
			return
		}
		e.ensureInternal().Stack = stack
	}
}

// WithRawStderr attaches the sidecar's tail stderr. Host-side logging sinks
// may record this, but the HTTP sanitizer in otel_hook.go MUST strip it
// from any response body.
func WithRawStderr(stderr string) Option {
	return func(e *BrainError) {
		if stderr == "" {
			return
		}
		e.ensureInternal().RawStderr = stderr
	}
}

// WithDebugFlag sets a single key/value pair on the InternalDetail debug
// bag. Safe to call multiple times — later calls overwrite earlier ones.
func WithDebugFlag(key, value string) Option {
	return func(e *BrainError) {
		if key == "" {
			return
		}
		d := e.ensureInternal()
		if d.DebugFlags == nil {
			d.DebugFlags = make(map[string]string)
		}
		d.DebugFlags[key] = value
	}
}

// WithDebugFlags merges a map of debug flags into InternalDetail. The input
// map is copied defensively so caller mutations after construction cannot
// bleed through. Later merges overwrite earlier keys.
func WithDebugFlags(flags map[string]string) Option {
	return func(e *BrainError) {
		if len(flags) == 0 {
			return
		}
		d := e.ensureInternal()
		if d.DebugFlags == nil {
			d.DebugFlags = make(map[string]string, len(flags))
		}
		for k, v := range flags {
			d.DebugFlags[k] = v
		}
	}
}

// ensureInternal lazily allocates the InternalDetail struct so the common
// case (no internal-only fields set) does not pay for the allocation.
func (e *BrainError) ensureInternal() *InternalDetail {
	if e.InternalOnly == nil {
		e.InternalOnly = &InternalDetail{}
	}
	return e.InternalOnly
}
