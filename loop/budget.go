// Package loop implements the Agent Loop Runner contract defined in
// 22-Agent-Loop规格.md.
//
// The Agent Loop Runner is the main-process component that drives a BrainKernel
// Run through a sequence of Turns: each Turn assembles a three-layer-cached
// ChatRequest, invokes the llm.Provider (non-streaming or streaming), dispatches
// any resulting tool.Tool calls, sanitizes tool results, updates the Run-level
// Budget, checks for stuck-loop patterns, and decides the next State
// transition. See 22 §2 (Architecture), §3 (Prompt Cache), §5 (Budget), §6
// (Turn anatomy), §7 (Streaming), §8 (Tool Result Sanitizer), and §9
// (LoopDetector) for normative behavior.
//
// Everything exported here is a frozen v1 contract — the concrete Runner,
// cache builder, stream consumer, sanitizer, and loop detector implementations
// will be filled in by the main-line team; the skeleton only declares the
// shape so the rest of BrainKernel can compile against it.
package loop

import (
	"fmt"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/llm"
)

// Budget is the Run-level resource envelope enforced by the Agent Loop Runner.
// Every Turn, before calling the llm.Provider, the Runner MUST update the
// Used* / Elapsed* counters and call CheckTurn / CheckCost to decide whether
// to proceed. See 22-Agent-Loop规格.md §5.
type Budget struct {
	// MaxTurns is the hard upper bound on Turn count for the whole Run.
	// A Runner MUST fail the Run with a budget.exhausted BrainError once
	// UsedTurns reaches MaxTurns. See 22 §5.1.
	MaxTurns int

	// MaxCostUSD is the hard upper bound on accumulated LLM + tool cost
	// for the whole Run, in US dollars. See 22 §5.2.
	MaxCostUSD float64

	// MaxToolCalls is the hard upper bound on the number of tool
	// invocations allowed across all Turns of the Run. See 22 §5.3.
	MaxToolCalls int

	// MaxLLMCalls is the hard upper bound on the number of llm.Provider
	// Complete / Stream calls allowed across all Turns of the Run.
	// See 22 §5.3.
	MaxLLMCalls int

	// MaxDuration is the wall-clock upper bound on the whole Run. The
	// Runner MUST abort with a budget.timeout BrainError once ElapsedTime
	// exceeds MaxDuration. See 22 §5.4.
	MaxDuration time.Duration

	// UsedTurns is the monotonically increasing count of Turns executed so
	// far. It MUST be updated exactly once per Turn, after the Turn has
	// been persisted. See 22 §5.1 and §6.4.
	UsedTurns int

	// UsedCostUSD is the monotonically increasing accumulated cost of the
	// Run, in US dollars, summed over llm.Usage.CostUSD on every Turn.
	// See 22 §5.2.
	UsedCostUSD float64

	// UsedToolCalls is the monotonically increasing count of tool
	// invocations across all Turns of the Run. See 22 §5.3.
	UsedToolCalls int

	// UsedLLMCalls is the monotonically increasing count of llm.Provider
	// Complete / Stream calls across all Turns of the Run. See 22 §5.3.
	UsedLLMCalls int

	// ElapsedTime is the wall-clock time consumed so far by the Run,
	// measured as time.Now().Sub(Run.StartedAt) at check time. See 22 §5.4.
	ElapsedTime time.Duration
}

// CheckTurn validates that a new Turn is allowed to start under the current
// Budget counters. The check runs all five dimensions from §5 and returns
// the first exhausted axis as a ClassPermanent BrainError. Runners MUST
// call CheckTurn at the top of every Turn loop iteration — the first
// failure aborts the Run with the exact budget_* code that tripped.
//
// The dimension order (turns → cost → llm calls → tool calls → timeout)
// is stable so tests and alert rules can rely on the first-fire code.
// See 22-Agent-Loop规格.md §5.1 / §5.2 / §5.3 / §5.4.
func (b *Budget) CheckTurn() error {
	if b == nil {
		return nil
	}
	if b.MaxTurns > 0 && b.UsedTurns >= b.MaxTurns {
		return brainerrors.New(brainerrors.CodeBudgetTurnsExhausted,
			brainerrors.WithMessage(fmt.Sprintf(
				"budget.turns_exhausted: used=%d max=%d",
				b.UsedTurns, b.MaxTurns,
			)),
		)
	}
	if b.MaxCostUSD > 0 && b.UsedCostUSD >= b.MaxCostUSD {
		return brainerrors.New(brainerrors.CodeBudgetCostExhausted,
			brainerrors.WithMessage(fmt.Sprintf(
				"budget.cost_exhausted: used=%.4f max=%.4f",
				b.UsedCostUSD, b.MaxCostUSD,
			)),
		)
	}
	if b.MaxLLMCalls > 0 && b.UsedLLMCalls >= b.MaxLLMCalls {
		return brainerrors.New(brainerrors.CodeBudgetLLMCallsExhausted,
			brainerrors.WithMessage(fmt.Sprintf(
				"budget.llm_calls_exhausted: used=%d max=%d",
				b.UsedLLMCalls, b.MaxLLMCalls,
			)),
		)
	}
	if b.MaxToolCalls > 0 && b.UsedToolCalls >= b.MaxToolCalls {
		return brainerrors.New(brainerrors.CodeBudgetToolCallsExhausted,
			brainerrors.WithMessage(fmt.Sprintf(
				"budget.tool_calls_exhausted: used=%d max=%d",
				b.UsedToolCalls, b.MaxToolCalls,
			)),
		)
	}
	if b.MaxDuration > 0 && b.ElapsedTime >= b.MaxDuration {
		return brainerrors.New(brainerrors.CodeBudgetTimeoutExhausted,
			brainerrors.WithMessage(fmt.Sprintf(
				"budget.timeout_exhausted: elapsed=%s max=%s",
				b.ElapsedTime, b.MaxDuration,
			)),
		)
	}
	return nil
}

// CheckCost validates that the Budget still has headroom for another LLM
// or tool call. It is the narrower dimension check that Runners call in
// the middle of a Turn (after receiving a Usage snapshot but before
// issuing a follow-up call) — it exits early on the two cost-shaped
// dimensions (cost and LLM calls) and leaves the remaining dimensions to
// the next CheckTurn.
//
// See 22-Agent-Loop规格.md §5.2.
func (b *Budget) CheckCost() error {
	if b == nil {
		return nil
	}
	if b.MaxCostUSD > 0 && b.UsedCostUSD >= b.MaxCostUSD {
		return brainerrors.New(brainerrors.CodeBudgetCostExhausted,
			brainerrors.WithMessage(fmt.Sprintf(
				"budget.cost_exhausted: used=%.4f max=%.4f",
				b.UsedCostUSD, b.MaxCostUSD,
			)),
		)
	}
	if b.MaxLLMCalls > 0 && b.UsedLLMCalls >= b.MaxLLMCalls {
		return brainerrors.New(brainerrors.CodeBudgetLLMCallsExhausted,
			brainerrors.WithMessage(fmt.Sprintf(
				"budget.llm_calls_exhausted: used=%d max=%d",
				b.UsedLLMCalls, b.MaxLLMCalls,
			)),
		)
	}
	return nil
}

// Remaining returns a point-in-time snapshot of the still-available
// budget in the shape consumed by llm.ChatRequest.RemainingBudget.
// TokensRemaining is derived as (MaxLLMCalls - UsedLLMCalls) × 1 because
// v1 does not track a per-Run token envelope separately from LLM calls;
// future specs that add one will extend BudgetSnapshot and this method.
//
// The snapshot is a value copy — callers can mutate the returned struct
// without racing the Budget receiver, matching the "MUST NOT share
// mutable state" clause from 22 §6.3.
func (b *Budget) Remaining() llm.BudgetSnapshot {
	if b == nil {
		return llm.BudgetSnapshot{}
	}
	turnsLeft := b.MaxTurns - b.UsedTurns
	if turnsLeft < 0 {
		turnsLeft = 0
	}
	costLeft := b.MaxCostUSD - b.UsedCostUSD
	if costLeft < 0 {
		costLeft = 0
	}
	tokensLeft := b.MaxLLMCalls - b.UsedLLMCalls
	if tokensLeft < 0 {
		tokensLeft = 0
	}
	return llm.BudgetSnapshot{
		TurnsRemaining:   turnsLeft,
		CostUSDRemaining: costLeft,
		TokensRemaining:  tokensLeft,
	}
}
