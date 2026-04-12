package braintesting

import (
	"context"
)

// CassetteRecorder captures live LLM and tool interactions into a cassette
// file so that subsequent test runs can replay them without touching the
// network. See 25-测试策略.md §11.1 for the RECORD → REPLAY workflow.
//
// A recording session is bracketed by Start and Finish. Between those calls
// any number of CassetteEvent values may be appended via Record; the events
// MUST be written in the exact order they were observed on the wire so that
// the CassettePlayer can stream them back deterministically.
//
// Implementations MUST run a secret redactor before flushing the cassette to
// disk, as required by 25 §11.3, and SHOULD embed the meta block described
// in 25 §11.1 (recorded_at, provider, model) in the resulting file.
type CassetteRecorder interface {
	// Start begins a new recording session under the given name. The name
	// SHOULD resolve to tests/cassettes/<name>.json per 25 §11.3.
	Start(ctx context.Context, name string) error

	// Record appends a single event to the active session in arrival
	// order. It MUST return a Permanent BrainError if called before Start
	// or after Finish.
	Record(ctx context.Context, event CassetteEvent) error

	// Finish flushes the cassette to persistent storage and closes the
	// session. It MUST be idempotent so that test teardown can call it
	// unconditionally.
	Finish(ctx context.Context) error
}
