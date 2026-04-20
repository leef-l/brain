package protocol

// SubtaskContext carries immutable caller intent that must not be rewritten by
// the delegating LLM. It travels alongside the delegated instruction so the
// target brain can distinguish coordinator prose from the user's original ask.
type SubtaskContext struct {
	UserUtterance string `json:"user_utterance,omitempty"`
	RenderMode    string `json:"render_mode,omitempty"` // "headed" | "headless" | ""
	ParentRunID   string `json:"parent_run_id,omitempty"`
	TurnIndex     int    `json:"turn_index,omitempty"`
}
