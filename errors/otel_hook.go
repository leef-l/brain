package errors

import "sync"

// ObservabilityHook is the indirection the errors package exposes so that
// metric / trace / log sinks can observe every BrainError without the
// errors package having to import brain/observability. That reverse import
// would create a cycle — observability depends on errors to report its own
// internal failures, and errors wants to push per-error samples into
// observability — so the two are glued together through this interface at
// runtime. See 21-错误模型.md §7.2 / §13.
//
// Implementations MUST be concurrency-safe and non-blocking: every New /
// Wrap call fans into every registered hook and any blocking hook would
// serialize error construction across the whole Kernel. Hooks SHOULD also
// be side-effect free with respect to the BrainError pointer — mutating it
// after construction would invalidate the Fingerprint the constructor
// already computed.
type ObservabilityHook interface {
	// OnError is invoked exactly once per constructed BrainError, after
	// Fingerprint has been stamped and OccurredAt populated. The err
	// argument is owned by the caller; hooks MUST NOT retain it past the
	// call or publish it without first sanitizing InternalOnly.
	OnError(err *BrainError)
}

// HookFunc is a function adapter so callers can register ad-hoc hooks
// without defining a new type.
type HookFunc func(*BrainError)

// OnError satisfies ObservabilityHook for HookFunc values.
func (f HookFunc) OnError(err *BrainError) {
	if f != nil {
		f(err)
	}
}

// hookRegistry holds the currently-installed hooks. It is a slice rather
// than a single pointer so multiple independent observers (metrics + trace
// + audit log) can coexist — the brain/observability package registers its
// own three hooks in the kernel init path.
//
// Access is guarded by a RWMutex because RegisterHook can run at any time
// (plugins, tests) while Dispatch runs on every error construction.
type hookRegistry struct {
	mu    sync.RWMutex
	hooks []ObservabilityHook
}

var globalHooks = &hookRegistry{}

// RegisterObservabilityHook installs a hook. Returns an unregister function
// so hosts can tear the hook down during shutdown or tests. Registering nil
// is a no-op that still returns a non-nil unregister closure for caller
// ergonomics.
//
// Example:
//
//	undo := errors.RegisterObservabilityHook(errors.HookFunc(func(e *errors.BrainError) {
//	    metrics.IncCounter("brain_error_total", e.ToSpanAttrs())
//	}))
//	defer undo()
func RegisterObservabilityHook(hook ObservabilityHook) (unregister func()) {
	if hook == nil {
		return func() {}
	}
	globalHooks.mu.Lock()
	globalHooks.hooks = append(globalHooks.hooks, hook)
	idx := len(globalHooks.hooks) - 1
	globalHooks.mu.Unlock()
	return func() {
		globalHooks.mu.Lock()
		defer globalHooks.mu.Unlock()
		if idx < len(globalHooks.hooks) {
			// Clear the slot rather than re-slicing so other hooks keep
			// their indices stable across unregisters.
			globalHooks.hooks[idx] = nil
		}
	}
}

// ResetObservabilityHooks clears every registered hook. Reserved for tests
// that need a clean slate between cases — production code MUST NOT touch
// this.
func ResetObservabilityHooks() {
	globalHooks.mu.Lock()
	defer globalHooks.mu.Unlock()
	globalHooks.hooks = nil
}

// DispatchError fans a BrainError out to every registered hook. Called by
// New / Wrap after Fingerprint and OccurredAt have been stamped. The
// dispatch is synchronous but each hook contract forbids blocking, so the
// total overhead is bounded by the number of hooks × O(1) work per hook.
//
// A hook that panics is caught and silently dropped — we cannot let an
// observability bug kill error reporting, and the alerting layer has its
// own independent path for "something is catastrophically broken".
func DispatchError(err *BrainError) {
	if err == nil {
		return
	}
	globalHooks.mu.RLock()
	// Take a snapshot under the read lock so we can release it before
	// invoking hooks — a hook that tries to register another hook would
	// otherwise deadlock on the write lock.
	snapshot := make([]ObservabilityHook, 0, len(globalHooks.hooks))
	for _, h := range globalHooks.hooks {
		if h != nil {
			snapshot = append(snapshot, h)
		}
	}
	globalHooks.mu.RUnlock()

	for _, h := range snapshot {
		safeDispatch(h, err)
	}
}

// safeDispatch isolates a single hook invocation so one misbehaving hook
// cannot nuke the whole fanout. The recover is intentionally silent — a
// log line from here would recurse into the error system itself, which is
// where we just came from.
func safeDispatch(h ObservabilityHook, err *BrainError) {
	defer func() { _ = recover() }()
	h.OnError(err)
}

// SanitizeForWire returns a shallow copy of err with every field that MUST
// NOT cross the HTTP boundary stripped. This is the 21 §7.4 sanitizer that
// the transport layer runs on outbound error bodies: InternalOnly is
// wiped, TraceID / SpanID move to headers (the transport reads them before
// calling this), and the Cause chain is sanitized recursively.
//
// It is a method on the package rather than on BrainError because the
// caller MUST NOT mutate the original — the returned pointer is a fresh
// allocation so the original stays intact for logging / audit sinks.
//
// Returns nil when err is nil so callers can chain it unconditionally.
func SanitizeForWire(err *BrainError) *BrainError {
	if err == nil {
		return nil
	}
	clean := *err
	clean.InternalOnly = nil
	clean.TraceID = ""
	clean.SpanID = ""
	if clean.Cause != nil {
		clean.Cause = SanitizeForWire(clean.Cause)
	}
	return &clean
}
