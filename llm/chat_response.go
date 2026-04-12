package llm

import "time"

// ChatResponse is the decoded non-streaming response returned by a
// Provider.Complete call. See 22-Agent-Loop规格.md §5 and §6 for the
// frozen v1 shape.
type ChatResponse struct {
	// ID is the provider-assigned response identifier.
	ID string
	// Model echoes the model name used to produce the response.
	Model string
	// StopReason is the normalized stop reason (e.g. "end_turn",
	// "tool_use", "max_tokens"). Provider adapters MUST normalize.
	StopReason string
	// Content is the ordered list of content blocks returned by the model.
	Content []ContentBlock
	// Usage is the token and cost accounting for this call.
	Usage Usage
	// FinishedAt is the wall-clock time the response completed.
	FinishedAt time.Time
}
