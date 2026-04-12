package loop

import "context"

// LoopDetector observes a stream of intra-Run events and decides whether the
// Agent Loop Runner is stuck in a degenerate pattern (repeated identical tool
// calls, empty streaming deltas, the same traceparent replayed, etc.). When a
// loop is detected the Runner MUST abort the Run with a
// "loop.detected" BrainError and transition to StateFailed. See
// 22-Agent-Loop规格.md §9.
type LoopDetector interface {
	// Observe ingests a single LoopEvent from the current Turn and returns
	// a LoopVerdict describing whether a stuck-loop pattern has been
	// identified. Implementations MUST be safe to call concurrently across
	// multiple Runs, but a single Run's events SHOULD arrive in order.
	// See 22-Agent-Loop规格.md §9.2.
	Observe(ctx context.Context, run *Run, event LoopEvent) (LoopVerdict, error)
}

// LoopEvent is a single observation fed into LoopDetector.Observe. Type
// distinguishes the source (tool_call / llm_call / content); ToolName and
// ContentHash identify the observed artifact; TraceID is the W3C trace
// parent used to detect replay loops. See 22-Agent-Loop规格.md §9.1.
type LoopEvent struct {
	// Type is one of "tool_call", "llm_call", or "content".
	// See 22-Agent-Loop规格.md §9.1.
	Type string

	// ToolName is the tool.Tool.Name for Type=="tool_call", empty
	// otherwise. See 22-Agent-Loop规格.md §9.1.
	ToolName string

	// ContentHash is the stable fingerprint of the observed content or
	// tool-call arguments, used to detect exact-repetition loops.
	// See 22-Agent-Loop规格.md §9.1.
	ContentHash string

	// TraceID is the W3C Trace Context trace ID of the current Turn.
	// See 22-Agent-Loop规格.md §9.1.
	TraceID string
}

// LoopVerdict is the decision returned by LoopDetector.Observe.
// When IsLoop is true the Runner MUST abort the Run.
// See 22-Agent-Loop规格.md §9.2.
type LoopVerdict struct {
	// IsLoop is true when the detector believes the Run is stuck.
	// See 22-Agent-Loop规格.md §9.2.
	IsLoop bool

	// Pattern is a short machine-readable label for the detected pattern,
	// e.g. "repeated_tool_call", "empty_delta", "same_trace_id".
	// See 22-Agent-Loop规格.md §9.2.
	Pattern string

	// Confidence is the detector's confidence in [0.0, 1.0].
	// See 22-Agent-Loop规格.md §9.2.
	Confidence float64
}
