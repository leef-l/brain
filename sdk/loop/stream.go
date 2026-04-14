package loop

import (
	"context"
	"encoding/json"

	"github.com/leef-l/brain/sdk/llm"
)

// StreamConsumer is the callback sink the Agent Loop Runner uses to forward
// llm.StreamReader events (see llm.Provider.Stream) to the host process's
// UI / log / SSE hub / persistence layer in near-real-time. Implementations
// MUST be safe to call from the goroutine that owns the stream, MUST NOT
// block the Runner for longer than the Turn budget allows, and MUST be
// idempotent with respect to identical (run, turn) pairs on resume.
// See 22-Agent-Loop规格.md §7.
type StreamConsumer interface {
	// OnMessageStart fires when the LLM stream emits its
	// llm.EventMessageStart frame, signaling the beginning of a new
	// assistant message for the given Turn.
	// See 22-Agent-Loop规格.md §7.1.
	OnMessageStart(ctx context.Context, run *Run, turn *Turn)

	// OnContentDelta fires for each incremental text chunk arriving on
	// the LLM stream. text is the newly added fragment only — consumers
	// are responsible for any accumulation they need.
	// See 22-Agent-Loop规格.md §7.2.
	OnContentDelta(ctx context.Context, run *Run, turn *Turn, text string)

	// OnToolCallDelta fires when the LLM stream emits a partial
	// tool_use block. toolName is the best-known tool identifier so
	// far (MAY be empty on the very first delta) and argsPartial is
	// the newly arrived JSON fragment of the tool arguments; consumers
	// are responsible for accumulating / parsing when appropriate.
	// See 22-Agent-Loop规格.md §7.3.
	OnToolCallDelta(ctx context.Context, run *Run, turn *Turn, toolName string, argsPartial string)

	// OnMessageDelta fires for llm.EventMessageDelta frames that carry
	// message-level metadata (stop_reason, usage updates, ...). delta
	// is the raw JSON payload as shipped by the provider so that
	// consumers can decode provider-specific fields without losing
	// fidelity. See 22-Agent-Loop规格.md §7.4.
	OnMessageDelta(ctx context.Context, run *Run, turn *Turn, delta json.RawMessage)

	// OnMessageEnd fires once the LLM stream has emitted its
	// llm.EventMessageEnd frame. usage is the final token / cost
	// accounting for the Turn and MUST be forwarded to Run.Budget by
	// the Runner after this callback returns.
	// See 22-Agent-Loop规格.md §7.5.
	OnMessageEnd(ctx context.Context, run *Run, turn *Turn, usage llm.Usage)
}
