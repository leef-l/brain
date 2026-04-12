// Package braintesting provides the test harness primitives specified in
// 25-测试策略.md: a compliance-test runner for the ~150 frozen v1 conformance
// cases, a cassette recorder/player pair backing the LLM Replay layer from
// 25 §11, and the FakeSidecar script engine from 25 Appendix B used by Unit
// and Contract tests.
//
// The package is imported at the test path github.com/leef-l/brain/testing but declares
// the package name braintesting to avoid colliding with the Go standard
// library "testing" package inside consumers that dot-import both. This is
// intentional and documented in 骨架实施计划.md §5.10.
//
// This package has zero external dependencies. It may depend on other brain
// subpackages (errors, llm, agent, protocol, tool) but never the reverse —
// no production code outside cmd/brain and integration tests should import
// braintesting.
package braintesting

import (
	"context"
	"encoding/json"
	"time"
)

// CassetteEvent is the atomic record stored inside a cassette file as
// specified in 25-测试策略.md §11.1. Each event captures one direction of a
// real LLM or tool interaction so that it can later be replayed byte-for-byte
// by the CassettePlayer during REPLAY-mode tests.
//
// The Payload field is intentionally json.RawMessage so that encoding is
// deferred to the recorder/player implementation and no schema coupling to
// llm.ChatRequest / llm.ChatResponse leaks into the cassette format.
type CassetteEvent struct {
	// Type is one of "llm_request", "llm_response", "tool_call" or
	// "tool_result" as enumerated in 25-测试策略.md §11.1.
	Type string `json:"type"`

	// Timestamp is the wall-clock time at which the event was recorded.
	// Implementations MUST scrub non-deterministic fields from Payload
	// before hashing (25 §11.2) but Timestamp itself is preserved as
	// diagnostic metadata only.
	Timestamp time.Time `json:"timestamp"`

	// Payload is the opaque JSON body of the event (request, response or
	// tool I/O). See 25 §11.1 for the canonical request/response schema.
	Payload json.RawMessage `json:"payload"`
}

// CassettePlayer replays a previously recorded cassette file deterministically,
// as specified in 25-测试策略.md §11. Tests that want hermetic, zero-network
// LLM behaviour obtain a player via Load and then drive it with Next, which
// walks the recorded event stream in order. Rewind resets the cursor so the
// same cassette can be replayed multiple times within a single test.
//
// Implementations MUST fail loudly when a request cannot be matched against
// the loaded cassette (25 §11.2): silently falling back to a live provider
// would defeat the purpose of the Replay layer.
type CassettePlayer interface {
	// Load opens the named cassette and positions the cursor at the first
	// event. Name resolution is implementation-defined but SHOULD honour
	// tests/cassettes/ as documented in 25 §11.3.
	Load(ctx context.Context, name string) error

	// Next returns the next recorded event in cassette order and advances
	// the cursor. It MUST return a BrainError classified as Permanent when
	// the cassette is exhausted (see 25 §11.2).
	Next(ctx context.Context) (CassetteEvent, error)

	// Rewind resets the cursor to the first event so the same cassette can
	// be replayed again. It MUST NOT reload from disk.
	Rewind(ctx context.Context) error
}
