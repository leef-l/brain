package loop

import "github.com/leef-l/brain/llm"

// MemCacheBuilder is the in-process implementation of CacheBuilder that
// assembles the three-layer Prompt Cache control markers defined in
// 22-Agent-Loop规格.md §3. All three methods are pure functions of their
// inputs: identical inputs always produce byte-identical CachePoint slices,
// guaranteeing stable prompt-cache hit rates on the upstream LLM provider.
//
// See 22-Agent-Loop规格.md §3.1 / §3.2 / §3.3.
type MemCacheBuilder struct{}

// NewMemCacheBuilder returns a ready-to-use MemCacheBuilder. Construction is
// cheap; callers MAY share a single instance across all Runs.
//
// See 22-Agent-Loop规格.md §3.
func NewMemCacheBuilder() *MemCacheBuilder {
	return &MemCacheBuilder{}
}

// BuildL1System produces a CachePoint for every SystemBlock that has
// Cache==true. The Layer field is always "L1_system" and Index is the
// zero-based position of the block within the system slice.
//
// This is a pure function: the same system slice always yields the same
// CachePoint slice. No internal state is read or written.
//
// See 22-Agent-Loop规格.md §3.1.
func (b *MemCacheBuilder) BuildL1System(system []llm.SystemBlock) []llm.CachePoint {
	if len(system) == 0 {
		return nil
	}
	var out []llm.CachePoint
	for i, blk := range system {
		if blk.Cache {
			out = append(out, llm.CachePoint{
				Layer: "L1_system",
				Index: i,
			})
		}
	}
	return out
}

// BuildL2Task produces a single CachePoint at taskBoundary (inclusive),
// covering the frozen task context that spans multiple Turns. If taskBoundary
// is out of range for messages (< 0 or >= len(messages)) the method returns
// nil so callers do not have to guard the index themselves.
//
// This is a pure function: the same (messages, taskBoundary) pair always
// yields the same result. The messages slice is not inspected beyond its
// length.
//
// See 22-Agent-Loop规格.md §3.2.
func (b *MemCacheBuilder) BuildL2Task(messages []llm.Message, taskBoundary int) []llm.CachePoint {
	if taskBoundary < 0 || taskBoundary >= len(messages) {
		return nil
	}
	return []llm.CachePoint{
		{
			Layer: "L2_task",
			Index: taskBoundary,
		},
	}
}

// BuildL3History produces a single CachePoint at the last stable-message
// boundary — defined as the highest index where Role is "user" or "tool".
// The Runner emits this marker only at the most-recent stable position so the
// per-Turn rolling conversation benefits from provider cache reuse.
//
// If messages is empty, or no "user" / "tool" message exists, the method
// returns nil (no L3 cache point is warranted).
//
// This is a pure function. See 22-Agent-Loop规格.md §3.3.
func (b *MemCacheBuilder) BuildL3History(messages []llm.Message) []llm.CachePoint {
	lastStable := -1
	for i := len(messages) - 1; i >= 0; i-- {
		r := messages[i].Role
		if r == "user" || r == "tool" {
			lastStable = i
			break
		}
	}
	if lastStable < 0 {
		return nil
	}
	return []llm.CachePoint{
		{
			Layer: "L3_history",
			Index: lastStable,
		},
	}
}
