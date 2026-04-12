package loop

import "github.com/leef-l/brain/llm"

// CacheBuilder assembles the three-layer Prompt Cache control markers that
// the Agent Loop Runner attaches to every llm.ChatRequest. The three layers
// are L1 (system: agent contract + tool schemas), L2 (task: frozen task
// context that spans multiple Turns), and L3 (history: per-Turn rolling
// conversation). See 22-Agent-Loop规格.md §3.
//
// Implementations MUST be pure functions of their inputs: given the same
// messages and boundaries, they MUST produce byte-identical CachePoint
// slices so that the upstream LLM provider's prompt-cache hit rate is stable.
type CacheBuilder interface {
	// BuildL1System produces the CachePoint markers for the L1 (system)
	// layer, covering the agent contract, role prompt, and registered
	// tool schemas. See 22-Agent-Loop规格.md §3.1.
	BuildL1System(system []llm.SystemBlock) []llm.CachePoint

	// BuildL2Task produces the CachePoint markers for the L2 (task) layer.
	// taskBoundary is the index in messages where the current task's
	// frozen context ends and the rolling history begins; everything up to
	// and including taskBoundary is considered task-level and MUST be
	// cached as a single stable block. See 22-Agent-Loop规格.md §3.2.
	BuildL2Task(messages []llm.Message, taskBoundary int) []llm.CachePoint

	// BuildL3History produces the CachePoint markers for the L3 (history)
	// layer, covering the rolling per-Turn conversation. The Runner is
	// expected to emit this cache marker only at the most recent
	// stable-message boundary. See 22-Agent-Loop规格.md §3.3.
	BuildL3History(messages []llm.Message) []llm.CachePoint
}
