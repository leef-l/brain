package errors

import "sync"

// ErrorRegistry is the runtime index of every error_code known to the
// Kernel. It maps a wire-level code string to the CodeMetadata that the
// constructors need to stamp Class and Retryable defaults onto a new
// BrainError. The registry is the enforcement point for the 21-错误模型.md
// §5.1 rule that every code emitted on the wire MUST be pre-registered —
// New / Wrap will fall back to ClassInternalBug + CodeUnknown when a caller
// passes an unregistered code, and registry_violation is surfaced via a
// metric so the host can alert per 21 §7.2.
//
// An ErrorRegistry is safe for concurrent reads after init. Register is
// guarded by a sync.RWMutex so test hooks and per-host extensions can add
// codes dynamically without racing the hot Lookup path.
type ErrorRegistry struct {
	mu      sync.RWMutex
	entries map[string]CodeMetadata
}

// NewErrorRegistry returns an empty registry. Most callers should use
// DefaultRegistry instead — it is seeded at init time from reservedMetadata
// and is the one Lookup / New / Wrap consult. NewErrorRegistry is exposed
// for tests that want to exercise registry semantics without mutating the
// process-wide default.
func NewErrorRegistry() *ErrorRegistry {
	return &ErrorRegistry{entries: make(map[string]CodeMetadata)}
}

// Register adds or replaces a code in the registry. Re-registering the same
// code is allowed (the last writer wins) so that host-level extensions can
// override defaults during bootstrap, but a production Kernel SHOULD treat
// double-registration as an error and alert. The returned bool is true when
// the code is new and false when it overwrote a prior entry.
//
// Register validates the minimum shape: Code non-empty and Class one of the
// six v1 values. Any other violation is the caller's responsibility; see 21
// §5.1 for the naming rules that the code_naming lint enforces at build
// time.
func (r *ErrorRegistry) Register(md CodeMetadata) bool {
	if md.Code == "" {
		return false
	}
	if !isKnownClass(md.Class) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, existed := r.entries[md.Code]
	r.entries[md.Code] = md
	return !existed
}

// Lookup returns the metadata registered for code. The ok return is false
// when the code has never been registered — callers MUST treat that as a
// protocol violation and fall back to CodeUnknown + ClassInternalBug per
// 21 §5.1 / §7.2.
func (r *ErrorRegistry) Lookup(code string) (CodeMetadata, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	md, ok := r.entries[code]
	return md, ok
}

// Len returns the number of registered codes. Intended for tests that want
// to assert "every appendix A row made it in".
func (r *ErrorRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// All returns a defensive copy of every registered code. Ordering is
// unstable — callers that need a deterministic view MUST sort the result.
// Exported so the doctor CLI and compliance tests can enumerate the
// reserved clause without reaching into reservedMetadata directly.
func (r *ErrorRegistry) All() []CodeMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CodeMetadata, 0, len(r.entries))
	for _, md := range r.entries {
		out = append(out, md)
	}
	return out
}

// DefaultRegistry is the process-wide registry seeded with the v1 appendix A
// clause at init time. All of New / Wrap / Lookup consult this registry.
// Tests that want isolation can use NewErrorRegistry directly.
var DefaultRegistry = NewErrorRegistry()

// Lookup is a package-level convenience that dispatches to DefaultRegistry.
// It is the preferred call site for production code so the default can be
// swapped in tests via a temporary replacement.
func Lookup(code string) (CodeMetadata, bool) {
	return DefaultRegistry.Lookup(code)
}

// Register is a package-level convenience that dispatches to
// DefaultRegistry. Host extensions that add custom codes at boot SHOULD
// call this before the first error is constructed so the Lookup hot path
// sees a stable set.
func Register(md CodeMetadata) bool {
	return DefaultRegistry.Register(md)
}

// init seeds DefaultRegistry from the reservedMetadata slice in class_map.go.
// Running at package init time guarantees the registry is populated before
// any downstream package builds its first BrainError — the errors package
// is at the root of the brain import topology so its init fires first.
//
// If the slice ever grows a duplicate code this init will silently keep
// the last definition. A compliance test (C-E-04) asserts uniqueness so
// the duplicate surfaces as a CI failure rather than a runtime footgun.
func init() {
	for _, md := range reservedMetadata {
		DefaultRegistry.Register(md)
	}
}

// isKnownClass is the allowlist check for the six-value taxonomy. Kept
// private so the v1 frozen set stays authoritative — adding a new class
// requires changing class.go, which the spec freeze process governs.
func isKnownClass(c ErrorClass) bool {
	switch c {
	case ClassTransient, ClassPermanent, ClassUserFault,
		ClassQuotaExceeded, ClassSafetyRefused, ClassInternalBug:
		return true
	default:
		return false
	}
}
