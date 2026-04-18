package tool

import "encoding/json"

// Schema is the LLM-facing description of a Tool declared in
// 02-BrainKernel设计.md §6.1. It is handed to the LLM provider as part of
// ChatRequest.Tools so the model knows which tools are callable and what
// arguments each one accepts. The llm/ package defines its own lightweight
// ToolSchema mirror to avoid an import cycle — see
// brain骨架实施计划.md §5.2 note.
type Schema struct {
	// Name is the globally unique tool name, e.g. "code.read_file".
	// MUST match the Tool.Name() return value. See
	// 02-BrainKernel设计.md §6.1 "命名规范铁律".
	Name string `json:"name"`

	// Description is a short human-readable explanation shown to the LLM.
	// See 02-BrainKernel设计.md §6.1.
	Description string `json:"description"`

	// InputSchema is the JSON Schema (draft 2020-12) of the tool's
	// arguments object. Stored as raw JSON so this package does not
	// need a JSON Schema library. See 02-BrainKernel设计.md §6.1.
	InputSchema json.RawMessage `json:"input_schema"`

	// OutputSchema is the optional JSON Schema (draft 2020-12) of the
	// tool's success payload. It is not sent to the LLM, but is used by
	// CLI introspection, sidecar tools/list, and future manifest/package
	// metadata. Empty means "unspecified / dynamic".
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`

	// Brain is the brain_kind that registered this Tool (e.g. "code",
	// "browser", "central"). Used by Registry.ListByBrain. See
	// 02-BrainKernel设计.md §6.1 naming convention.
	Brain string `json:"brain"`

	// Concurrency 声明该工具的并发资源约束。nil 表示无约束（默认串行）。
	// Phase B 的 BatchPlanner 根据此字段推导 LeaseRequest 并构建冲突图。
	Concurrency *ToolConcurrencySpec `json:"concurrency,omitempty"`
}

// ToolConcurrencySpec 描述工具执行时所需的资源锁规格。
type ToolConcurrencySpec struct {
	// Capability 标识资源类别，如 "execution.order"、"data.candle"。
	Capability string `json:"capability"`

	// ResourceKeyTemplate 是从工具参数推导具体 ResourceKey 的模板，
	// 如 "account:{{.account}}"。运行时用 tool_call 参数填充。
	ResourceKeyTemplate string `json:"resource_key_template"`

	// AccessMode 声明访问模式。
	AccessMode string `json:"access_mode"`

	// Scope 声明 lease 的生效范围。
	Scope string `json:"scope"`

	// AcquireTimeout 获取 lease 的超时时间（秒）。0 表示使用默认值。
	AcquireTimeout float64 `json:"acquire_timeout,omitempty"`

	// ApprovalClass 是该工具的语义审批等级（五级：readonly / workspace-write /
	// exec-capable / control-plane / external-network）。
	// 当非空时，SemanticApprover 会优先使用此值而非启发式推断。
	ApprovalClass string `json:"approval_class,omitempty"`
}
