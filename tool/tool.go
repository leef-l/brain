package tool

import (
	"context"
	"encoding/json"
)

// Tool is the interface every callable Brain capability must implement,
// as declared in 02-BrainKernel设计.md §6.1. Tools are registered into a
// Registry at Brain bootstrap time; the Kernel's LLM loop then resolves
// each tool_use block emitted by the model to a Tool and invokes Execute.
//
// Runtime concerns — per-tool timeout, JSON-Schema argument validation,
// tool_result sanitizer pipeline, untrusted-content <tool_output> tagging,
// death-loop detection — are the Kernel's responsibility and live in
// 22-Agent-Loop规格.md §7/§8/§10/§11. A Tool implementation only has to
// be correct, idempotent where possible, and honor ctx cancellation.
type Tool interface {
	// Name returns the globally unique tool name. See
	// 02-BrainKernel设计.md §6.1 "命名规范铁律".
	Name() string

	// Schema returns the LLM-facing schema for this tool. The returned
	// Schema.Name MUST equal Name(). See 02-BrainKernel设计.md §6.1.
	Schema() Schema

	// Risk returns the risk classification used by Guardrail and the
	// fault_policy escalation matrix. See 02-BrainKernel设计.md §6
	// and §6.5.
	Risk() Risk

	// Execute runs the tool. ctx carries cancellation and deadline;
	// implementations MUST abort promptly when ctx is cancelled. args
	// is the raw JSON object supplied by the LLM — the Kernel has
	// already validated it against Schema.InputSchema before calling
	// Execute. A non-nil error means the invocation itself failed to
	// run (infrastructure fault); a Result with IsError=true means the
	// tool ran to completion but the operation the LLM requested
	// failed. See 02-BrainKernel设计.md §6.1 and
	// 22-Agent-Loop规格.md §7.
	Execute(ctx context.Context, args json.RawMessage) (*Result, error)
}

// Result is the outcome of a single Tool.Execute invocation. It is the
// payload that becomes the tool_result ContentBlock in the next LLM turn.
// See 02-BrainKernel设计.md §6.1 and 22-Agent-Loop规格.md §7.
type Result struct {
	// Output is the raw JSON payload handed back to the LLM. It is
	// treated as untrusted content and wrapped in <tool_output> by the
	// sanitizer pipeline (22-Agent-Loop规格.md §11).
	Output json.RawMessage `json:"output"`

	// IsError signals that the tool ran to completion but the operation
	// the LLM asked for failed (e.g. file not found, permission denied).
	// A non-nil error from Execute means something else — the Kernel
	// could not run the tool at all.
	IsError bool `json:"is_error,omitempty"`
}
