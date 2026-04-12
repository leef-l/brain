// Package llm defines the internal LLM contract used by the Agent Loop
// Runner, as specified in 02-核心架构.md §5 and 22-Agent-Loop规格.md §6.
//
// The ChatRequest/ChatResponse pair, the Provider/StreamReader interfaces,
// the three-layer CachePoint model, and the Usage accounting struct are all
// frozen v1 contracts — see 22-Agent-Loop规格.md for normative behavior.
//
// This package intentionally defines its own lightweight ToolSchema instead
// of importing github.com/leef-l/brain/tool, to keep llm/ and tool/ independent and
// avoid an import cycle (see 骨架实施计划.md §5.2 note).
package llm

import (
	"encoding/json"
	"time"
)

// ChatRequest is the main-process Agent Loop Runner's internal request
// object, carrying all inputs for a single llm.complete / llm.stream call.
// See 22-Agent-Loop规格.md §6 for the frozen v1 field schema.
type ChatRequest struct {
	// --- Identity ---
	RunID     string // see 22-Agent-Loop规格.md §6.2 run_id
	TurnIndex int    // current Turn index (0-based), §6.2
	BrainID   string // which brain issued this request, §6.2

	// --- Layered prompt (L1/L2 system, L3 history) ---
	System   []SystemBlock // §6.2 system — L1 + L2
	Messages []Message     // §6.2 messages — L3

	// --- Tool definitions (frozen within a Run, §3.3) ---
	Tools      []ToolSchema // available tools, lightweight copy, §5.2 note
	ToolChoice string       // "auto" | "required" | "none" | tool name, §6.4

	// --- Model config ---
	Model     string // §6.2 model
	MaxTokens int    // §6.2 max_tokens
	Stream    bool   // §6.2 stream

	// --- Prompt Cache control ---
	CacheControl []CachePoint // three-layer cache_control, §3 + §5.2

	// --- Budget snapshot (aligned with Run Budget) ---
	TurnTimeout     time.Duration  // §6.2 turn_timeout
	RemainingBudget BudgetSnapshot // §6.2 remaining_budget, real-time snapshot
}

// SystemBlock is a single system-prompt block, representing one layer (L1
// or L2) of the three-layer Prompt Cache model defined in
// 22-Agent-Loop规格.md §3.
type SystemBlock struct {
	// Text is the rendered prompt text for this block.
	Text string
	// Cache indicates whether this block should be marked cache_control:
	// ephemeral by the provider adapter. See 22-Agent-Loop规格.md §3.
	Cache bool
}

// Message is a single conversational message (user / assistant / tool) in
// the L3 history layer. See 22-Agent-Loop规格.md §6.2.
type Message struct {
	// Role is one of "user", "assistant", or "tool".
	Role string
	// Content is the ordered list of content blocks for this message.
	Content []ContentBlock
}

// ContentBlock is a single piece of content inside a Message. It models
// the union of text, tool_use, and tool_result blocks as defined in
// 22-Agent-Loop规格.md §6.2 and §7.
type ContentBlock struct {
	// Type is one of "text", "tool_use", or "tool_result".
	Type string
	// Text carries the text payload for type == "text".
	Text string `json:",omitempty"`
	// ToolUseID correlates a tool_result with its originating tool_use.
	ToolUseID string `json:",omitempty"`
	// ToolName is the tool name for tool_use blocks.
	ToolName string `json:",omitempty"`
	// Input is the JSON argument payload for tool_use blocks.
	Input json.RawMessage `json:",omitempty"`
	// Output is the JSON result payload for tool_result blocks.
	Output json.RawMessage `json:",omitempty"`
	// IsError marks a tool_result as an error result.
	IsError bool `json:",omitempty"`
}

// CachePoint marks a cache boundary in the three-layer Prompt Cache model.
// See 22-Agent-Loop规格.md §3 for the L1/L2/L3 taxonomy.
type CachePoint struct {
	// Layer identifies which layer this point belongs to: "L1_system",
	// "L2_task", or "L3_history".
	Layer string
	// Index is the zero-based position of the cache point within its layer.
	Index int
}

// BudgetSnapshot is the real-time snapshot of the remaining Run-level
// budget, attached to every ChatRequest. See 22-Agent-Loop规格.md §6.3
// ("MUST NOT be cached").
type BudgetSnapshot struct {
	// TurnsRemaining is the number of Turns still available in the Run.
	TurnsRemaining int
	// CostUSDRemaining is the remaining cost budget in USD.
	CostUSDRemaining float64
	// TokensRemaining is the remaining token budget.
	TokensRemaining int
}

// ToolSchema is a lightweight description of a tool exposed to the LLM.
// It is intentionally defined here (rather than imported from
// github.com/leef-l/brain/tool) to avoid an import cycle between llm/ and tool/.
// See 骨架实施计划.md §5.2 note and 22-Agent-Loop规格.md §6.2 tools.
type ToolSchema struct {
	// Name is the tool's stable identifier (e.g. "code.run_tests").
	Name string
	// Description is the natural-language description shown to the LLM.
	Description string
	// InputSchema is the JSON Schema for the tool's input arguments.
	InputSchema json.RawMessage
}
