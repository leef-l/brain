package protocol

// SubtaskContext carries immutable caller intent that must not be rewritten by
// the delegating LLM. It travels alongside the delegated instruction so the
// target brain can distinguish coordinator prose from the user's original ask.
type SubtaskContext struct {
	UserUtterance string `json:"user_utterance,omitempty"`
	RenderMode    string `json:"render_mode,omitempty"` // "headed" | "headless" | ""
	ParentRunID   string `json:"parent_run_id,omitempty"`
	TurnIndex     int    `json:"turn_index,omitempty"`
	// ProjectID 是当前 chat/run 的项目 ID(MACCS Wave 7+)。
	// chat 在选了项目后,把 CurrentProject.ID 通过此字段透传给 bridge/delegate,
	// 让 bridge/delegate 在构造 DelegateRequest 时填 req.ProjectID,
	// 进而让 Orchestrator.delegateOnce 的 Assemble 自动加载项目历史 + 记忆。
	ProjectID string `json:"project_id,omitempty"`
}
