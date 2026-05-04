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

	// Forget releases all per-Run bookkeeping for runID. Runner MUST call
	// this when a Run reaches a terminal state, otherwise long-lived
	// detectors (e.g. chat session 共享 detector 跨 turn 复用)会让 state
	// map 单调增长 — 每 turn 用新 runID 永远不释放,内存泄漏。
	// 实现应为多次调用、未知 runID 安全。
	Forget(runID string)
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

	// TurnIndex 当前 Turn 序号(0-based),用于区分跨 turn 重复 vs turn 内多 block。
	// 同一 turn 内 LLM 输出多个相同 tool_use block 不算 loop(LLM 单次响应);
	// 跨 turn 出现相同 sig 才计入 repeatCount。
	// 实现方未填(0)时回退到原行为(每次 Observe 都 +1)。
	TurnIndex int
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
