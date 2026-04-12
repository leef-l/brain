package loop

import (
	"context"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/llm"
)

// Turn is a single iteration of the Agent Loop: assemble a ChatRequest,
// invoke llm.Provider, dispatch any returned tool calls, update the Budget,
// and decide the next Run.State. Every Turn is persisted (see
// persistence.PlanStore) so that a resumed host process can replay the Run
// deterministically using the UUID field as the idempotency key.
// See 22-Agent-Loop规格.md §6.
type Turn struct {
	// Index is the 1-based ordinal of the Turn within its Run.
	// See 22-Agent-Loop规格.md §6.1.
	Index int

	// RunID is the parent Run.ID. See 22-Agent-Loop规格.md §6.1.
	RunID string

	// UUID is a globally unique idempotency key generated before the
	// LLM call and persisted before any external side effect. On resume
	// the Runner MUST reject a duplicate Turn with the same UUID, so
	// partial crashes cannot cause double-billing or double-tool-call.
	// See 22-Agent-Loop规格.md §6.1 and §12.
	UUID string

	// StartedAt is the wall-clock timestamp when the Turn began
	// assembling its ChatRequest. See 22-Agent-Loop规格.md §6.1.
	StartedAt time.Time

	// EndedAt is the wall-clock timestamp when the Turn reached a
	// terminal TurnResult (success or error). nil while in progress.
	// See 22-Agent-Loop规格.md §6.1.
	EndedAt *time.Time

	// LLMCalls is the number of llm.Provider.Complete / Stream calls
	// executed within this Turn. Normally 1, but MAY be >1 if the
	// Runner retried a transient error inside the Turn.
	// See 22-Agent-Loop规格.md §6.4.
	LLMCalls int

	// ToolCalls is the number of tool.Tool.Execute calls executed
	// within this Turn. See 22-Agent-Loop规格.md §6.4.
	ToolCalls int
}

// Executor is the abstraction that drives a single Turn to completion. A
// concrete Executor is responsible for assembling the final ChatRequest
// (including CachePoint layers and RemainingBudget), invoking the
// llm.Provider, dispatching tool calls, and producing a TurnResult. The
// Agent Loop Runner owns the Run-level State machine and delegates each
// Turn to an Executor. See 22-Agent-Loop规格.md §6.2.
type Executor interface {
	// Execute drives exactly one Turn of the Run. Implementations MUST
	// respect ctx cancellation, MUST update run.Budget before returning,
	// and MUST return a TurnResult whose Turn field is fully populated
	// even on error. See 22-Agent-Loop规格.md §6.2.
	Execute(ctx context.Context, run *Run, req *llm.ChatRequest) (*TurnResult, error)
}

// TurnResult is the value produced by Executor.Execute. It carries the
// persisted Turn record, the decoded llm.ChatResponse (nil on hard
// failure), the next Run.State the Runner should transition to, and a
// *brainerrors.BrainError when the Turn failed. See 22-Agent-Loop规格.md §6.3.
type TurnResult struct {
	// Turn is the persisted Turn record for this iteration. MUST be
	// non-nil. See 22-Agent-Loop规格.md §6.3.
	Turn *Turn

	// Response is the decoded llm.ChatResponse from the LLM call. MAY be
	// nil when Error is non-nil. See 22-Agent-Loop规格.md §6.3.
	Response *llm.ChatResponse

	// NextState is the Run.State the Agent Loop Runner should transition
	// to after consuming this TurnResult. See 22-Agent-Loop规格.md §4.2
	// and §6.3.
	NextState State

	// Error is the *brainerrors.BrainError produced by this Turn, or nil
	// on success. The Runner MUST NOT wrap this into a bare Go error on
	// its way to persistence; see 21-错误模型.md §3.
	Error *brainerrors.BrainError
}
