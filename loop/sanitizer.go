package loop

import (
	"context"

	"github.com/leef-l/brain/llm"
	"github.com/leef-l/brain/tool"
)

// ToolResultSanitizer converts a raw tool.Result produced by a tool.Tool
// into an llm.ContentBlock that is safe to feed back into the next Turn's
// ChatRequest. Sanitization is mandatory for every tool result, regardless
// of the tool's risk class: the sanitizer strips secrets, truncates
// oversized payloads, redacts prompt-injection attempts, and enforces the
// tool-output schema. See 22-Agent-Loop规格.md §8.
type ToolResultSanitizer interface {
	// Sanitize converts raw into a safe-to-send llm.ContentBlock.
	// meta carries the contextual hints (tool name, risk class, Run ID)
	// the sanitizer needs to apply the correct policy. On unrecoverable
	// policy violations (e.g. secret detected with no redaction rule)
	// implementations MUST return a *errors.BrainError with
	// ErrorCode="tool.sanitize_failed" rather than leaking the raw
	// payload. See 22-Agent-Loop规格.md §8.2.
	Sanitize(ctx context.Context, raw *tool.Result, meta SanitizeMeta) (*llm.ContentBlock, error)
}

// SanitizeMeta is the contextual envelope passed alongside a raw
// tool.Result into ToolResultSanitizer.Sanitize. All fields are advisory
// hints — the sanitizer itself owns the final policy decision.
// See 22-Agent-Loop规格.md §8.1.
type SanitizeMeta struct {
	// ToolName is the tool.Tool.Name that produced the raw Result.
	// See 22-Agent-Loop规格.md §8.1.
	ToolName string

	// Risk is the tool.Risk class of the producing tool. Higher-risk
	// tools (e.g. tool.RiskHigh) SHOULD trigger stricter redaction.
	// See 22-Agent-Loop规格.md §8.1.
	Risk tool.Risk

	// RunID is the parent Run.ID; used for structured-logging
	// correlation and for run-scoped allow-lists. See
	// 22-Agent-Loop规格.md §8.1.
	RunID string
}
