// replan_types.go — 动态重规划(Stop-the-World Replan)的数据契约
//
// 设计动机:
//   chat / run / serve 三模式跑长任务期间,必须支持"用户中途修改"和"子 agent
//   反馈/错误"触发整个执行流的暂停 + 状态收集 + 重新规划 + 重启,而不是被动
//   等到当前 plan 跑完才修。
//
// 工作流:
//   1. user input 或 sub feedback → RelevanceClassifier 判断关联性
//   2. 关联 → EventBus.Publish replan.requested 事件
//   3. PlanOrchestrator 旁路 goroutine 收到事件 → cancel 当前 ExecuteTaskPlan ctx
//   4. ExecuteTaskPlan 收到 ctx done,层间退出,在跑的 SubTask 标记 Interrupted
//   5. PlanOrchestrator 收集 snapshot(progress + checkpoint + 项目记忆 top-N)
//   6. 调 DesignGenerator.GenerateWithModification 生成 newPlan(Version 自增)
//   7. 启动 newPlan,继续 ExecuteTaskPlan loop
//
// 设计文档:Replan-基于现有持久化与EventBus的渐进路线.md

package kernel

import (
	"encoding/json"
	"time"
)

// ReplanTrigger 标识触发重规划的来源。
type ReplanTrigger string

const (
	// TriggerUserModification 用户中途要求修改(改成 / 不要 / 也加 / 等下先做 等)。
	// chat REPL.dispatchUserInput → RelevanceClassifier 命中 Modification 时发布。
	TriggerUserModification ReplanTrigger = "user_modification"

	// TriggerSubFailure 子 agent 执行失败且 RetryPolicy 已耗尽。
	// 由 PlanOrchestrator 内部判断后自动发布,不需要外部触发。
	TriggerSubFailure ReplanTrigger = "sub_failure"

	// TriggerSubFeedback 子 agent 主动反馈"我建议拆成 2 步"等需要重规划的 hint。
	// 复用 MACCS 5.3 的 brain.feedback.requested 事件机制,但 reason 字段
	// 表明"需要 replan"时升级为 replan.requested。
	TriggerSubFeedback ReplanTrigger = "sub_feedback"
)

// ReplanRequest 是触发一次重规划的完整上下文。
//
// 通过 EventBus.Publish(events.Event{Type: EventReplanRequested, Data: marshalled})
// 发送给 PlanOrchestrator;ExecutionID 设为 plan.ProjectID 让订阅者可按项目过滤。
type ReplanRequest struct {
	// PlanID 当前正在执行的 plan ID。
	PlanID string `json:"plan_id"`

	// ProjectID 关联项目 ID。EventBus.Subscribe(ctx, projectID) 用此过滤。
	ProjectID string `json:"project_id"`

	// Trigger 触发原因。
	Trigger ReplanTrigger `json:"trigger"`

	// UserInput 用户修改文本。仅 Trigger=TriggerUserModification 时填。
	// 原文保留(不做截断/摘要),交给 ReplanLLM 自己理解。
	UserInput string `json:"user_input,omitempty"`

	// SubTaskID 触发 replan 的子任务 ID。Trigger=TriggerSubFailure / TriggerSubFeedback 时填。
	SubTaskID string `json:"sub_task_id,omitempty"`

	// SubError 子任务错误信息。Trigger=TriggerSubFailure 时填。
	SubError string `json:"sub_error,omitempty"`

	// Hint 子任务提供的"建议"。例如 "我建议把这步拆成 2 步" / "用户的目标看起来需要先做 X"。
	// Trigger=TriggerSubFeedback 时填。
	Hint string `json:"hint,omitempty"`

	// At 事件时间戳。
	At time.Time `json:"at"`
}

// MarshalReplanRequest 把 ReplanRequest 序列化为 JSON,供 EventBus.Event.Data 使用。
// 失败返回 nil(不会发生,struct 全是基本字段)。
func MarshalReplanRequest(req *ReplanRequest) json.RawMessage {
	if req == nil {
		return nil
	}
	if req.At.IsZero() {
		req.At = time.Now()
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil
	}
	return b
}

// UnmarshalReplanRequest 从 EventBus.Event.Data 还原 ReplanRequest。
func UnmarshalReplanRequest(data json.RawMessage) (*ReplanRequest, error) {
	var req ReplanRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// EventBus 事件类型常量(host → 订阅方)。
//
// 命名遵循 EventBus 现有约定:小写点分(brain.feedback.requested / task.state.completed)。
const (
	// EventReplanRequested chat REPL / sub agent 发布,触发 PlanOrchestrator 重规划。
	// Data: ReplanRequest JSON。
	EventReplanRequested = "replan.requested"

	// EventReplanStarted PlanOrchestrator 收到请求并开始 abort + snapshot 时发布。
	// Data: {plan_id, project_id, trigger, version_before}
	EventReplanStarted = "replan.started"

	// EventReplanCompleted 新 plan 生成成功并启动时发布。
	// Data: {plan_id, project_id, version_after, sub_tasks_added, sub_tasks_modified, sub_tasks_kept}
	EventReplanCompleted = "replan.completed"

	// EventReplanAborted Replan 失败(LLM 错误 / 连续 2 次失败上限 / 等)时发布。
	// Data: {plan_id, project_id, error}
	EventReplanAborted = "replan.aborted"
)

// ReplanSnapshot 是 PlanOrchestrator 在触发 replan 前收集的项目当前状态。
//
// 传给 DesignGenerator.GenerateWithModification 作为重规划的输入上下文。
// 设计上**不包含**完整的 LLM 历史(那个走 Checkpoint.MessagesRef CAS 引用),
// 只含决策需要的"轮廓信息"。
type ReplanSnapshot struct {
	// CompletedTasks 已完成的子任务摘要。重规划时这些任务保留 Status=Completed,
	// 不重做。
	CompletedTasks []SubTaskSnapshot `json:"completed_tasks"`

	// InterruptedTasks 触发 replan 时正在跑的子任务。包含 partial files / progress
	// / 最近一次 Checkpoint 摘要。重规划时这些任务可能被改写 instruction / 换 brain。
	InterruptedTasks []SubTaskSnapshot `json:"interrupted_tasks"`

	// PendingTasks 未启动的子任务。重规划时根据新方案保留 / 删除 / 修改。
	PendingTasks []SubTaskSnapshot `json:"pending_tasks"`

	// MemoryHints 项目记忆 top-N 摘要(decision / lesson / pattern 类型)。
	// 由 MemoryRetriever 按当前 goal 检索,作为 ReplanLLM 的"前情记忆"。
	MemoryHints []string `json:"memory_hints,omitempty"`

	// CapturedAt snapshot 创建时间。
	CapturedAt time.Time `json:"captured_at"`
}

// SubTaskSnapshot 是单个子任务在 replan 触发时的轮廓信息。
type SubTaskSnapshot struct {
	TaskID         string         `json:"task_id"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	Instruction    string         `json:"instruction"`
	Status         PlanTaskStatus `json:"status"`
	OutputSummary  string         `json:"output_summary,omitempty"` // 已完成时:Result.Output 摘要
	PartialFiles   []string       `json:"partial_files,omitempty"`  // 中断时:已写入但未完成的文件
	AbortReason    string         `json:"abort_reason,omitempty"`   // 中断原因
	TurnsUsed      int            `json:"turns_used,omitempty"`     // 已消耗 turn(从 progress 拿)
	Confidence     float64        `json:"confidence,omitempty"`     // 已完成时的产出置信度
}
