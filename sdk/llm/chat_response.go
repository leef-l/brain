package llm

import "time"

// ChatResponse is the decoded non-streaming response returned by a
// Provider.Complete call. See 22-Agent-Loop规格.md §5 and §6 for the
// frozen v1 shape.
type ChatResponse struct {
	// ID is the provider-assigned response identifier.
	ID string `json:"id,omitempty"`
	// Model echoes the model name used to produce the response.
	Model string `json:"model,omitempty"`
	// StopReason is the normalized stop reason (e.g. "end_turn",
	// "tool_use", "max_tokens"). Provider adapters MUST normalize.
	StopReason string `json:"stop_reason,omitempty"`
	// Content is the ordered list of content blocks returned by the model.
	Content []ContentBlock `json:"content,omitempty"`
	// Usage is the token and cost accounting for this call.
	Usage Usage `json:"usage,omitempty"`
	// FinishedAt is the wall-clock time the response completed.
	FinishedAt time.Time `json:"finished_at,omitempty"`
}
