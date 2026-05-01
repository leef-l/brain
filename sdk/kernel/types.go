package kernel

import (
	"encoding/json"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/protocol"
)

type DelegateStatus string

const (
	DelegateStatusOK       DelegateStatus = "ok"
	DelegateStatusRejected DelegateStatus = "rejected"
	DelegateStatusFailed   DelegateStatus = "failed"
)

// DelegateRequest is the strongly-typed payload for a subtask delegation.
// It replaces the raw json.RawMessage path in the Orchestrator core.
type DelegateRequest struct {
	TaskID   string `json:"task_id"`
	TargetKind  agent.Kind `json:"target_kind"`
	Instruction string `json:"instruction"`
	Context     json.RawMessage `json:"context,omitempty"`
	RenderMode  string `json:"render_mode,omitempty"`

	// Extended fields carried over from the original DelegateRequest:
	Subtask       *protocol.SubtaskContext         `json:"subtask,omitempty"`
	Budget        *SubtaskBudget                     `json:"budget,omitempty"`
	Execution     *executionpolicy.ExecutionSpec     `json:"execution,omitempty"`
	RequiredCaps  []string                           `json:"required_caps,omitempty"`
	PreferredCaps []string                           `json:"preferred_caps,omitempty"`
	TaskType      string                             `json:"task_type,omitempty"`
	// PipeID 用于 Workflow streaming edge 的跨进程流式传输。
	// 非空时，sidecar 会通过 brain/stream/write 将 tool 输出实时写入 host 的 PipeRegistry。
	PipeID string `json:"pipe_id,omitempty"`

	// ─── Distributed Tracing & Project Memory ─────────────────────────────
	// TraceID 是跨 brain 调用的分布式追踪 ID。整个任务链路（Central →
	// code → verifier → browser）共享同一个 TraceID，实现端到端可观测。
	TraceID string `json:"trace_id,omitempty"`
	// SpanID 是当前 brain 在这个 Trace 中的 span 标识。
	SpanID string `json:"span_id,omitempty"`
	// ProjectID 关联项目级记忆。非空时，ContextEngine 会自动加载该项目的
	// 历史对话和关键决策点作为初始上下文。
	ProjectID string `json:"project_id,omitempty"`
	// ParentSpanID 标识父 brain 的 span，用于构建完整的调用树。
	ParentSpanID string `json:"parent_span_id,omitempty"`

	// Workdir 是中央大脑（host）通知专精大脑（sidecar）的工作目录绝对路径。
	// 设计动机：sidecar 进程启动时只继承父进程的 cwd，不知道用户在哪个目录工作；
	// 而 chat / run / serve 三种模式 host 端都有明确的 workdir（启动 flag 或 req.Workdir），
	// 必须把这个值显式传给 sidecar，让它用相同的 workdir 解析所有相对路径。
	//
	// sidecar SDK 收到后会把它注入工具调用的 sandbox primary 目录，
	// 写文件 `snake.html` → 解析为 `<workdir>/snake.html` 而不是 sidecar 进程的 cwd。
	//
	// 空字符串时 sidecar 退化为继承的 cwd（向后兼容）。
	Workdir string `json:"workdir,omitempty"`
}

// DelegateBatchRequest 将多个无依赖的子任务并行派发给不同的 specialist brain。
// 这是实现"多方审核同时执行"的核心 API。
type DelegateBatchRequest struct {
	// Requests 是要并行执行的委派请求列表。
	// 注意：所有请求必须是互相独立的（无依赖关系），否则应使用 Workflow DAG。
	Requests []*DelegateRequest `json:"requests"`
}

// DelegateBatchResult 是 DelegateBatch 的聚合结果。
type DelegateBatchResult struct {
	// Results 与 Requests 按索引一一对应。
	Results []*DelegateResult `json:"results"`
	// CompletedCount 成功完成的子任务数。
	CompletedCount int `json:"completed_count"`
	// FailedCount 失败或拒绝的子任务数。
	FailedCount int `json:"failed_count"`
}

// DelegateResult is the strongly-typed response for a completed subtask.
type DelegateResult struct {
	TaskID   string         `json:"task_id"`
	Status   string         `json:"status"` // "completed" | "failed" | "rejected"
	Output   json.RawMessage `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Usage    SubtaskUsage   `json:"usage"`
	Metrics  ExecutionMetrics `json:"metrics,omitempty"`
}

type ToolCallRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolCallResult struct {
	Success bool           `json:"success"`
	Data    map[string]any `json:"data,omitempty"`
	Error   *errors.BrainError `json:"error,omitempty"`
}

type ExecutionMetrics struct {
	DurationMs int `json:"duration_ms"`
	TurnCount  int `json:"turn_count"`
}

// SubtaskBudget limits a single delegated subtask.
type SubtaskBudget struct {
	MaxTurns   int           `json:"max_turns,omitempty"`
	MaxCostUSD float64       `json:"max_cost_usd,omitempty"`
	Timeout    time.Duration `json:"timeout,omitempty"`
}

// SubtaskUsage tracks resource consumption of a subtask.
type SubtaskUsage struct {
	Turns    int           `json:"turns"`
	CostUSD  float64       `json:"cost_usd"`
	Duration time.Duration `json:"duration"`
}
