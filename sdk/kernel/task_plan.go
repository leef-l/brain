package kernel

import (
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// ─────────────────────────────────────────────────────────────────────────────
// TaskPlan — MACCS v2 结构化任务规划
// ─────────────────────────────────────────────────────────────────────────────

// PlanStatus 表示 TaskPlan 的整体状态。
type PlanStatus string

const (
	PlanDraft     PlanStatus = "draft"
	PlanActive    PlanStatus = "active"
	PlanPaused    PlanStatus = "paused"
	PlanCompleted PlanStatus = "completed"
	PlanFailed    PlanStatus = "failed"
)

// PlanTaskStatus 表示单个子任务的状态。
type PlanTaskStatus string

const (
	PlanTaskPending     PlanTaskStatus = "pending"
	PlanTaskRunning     PlanTaskStatus = "running"
	PlanTaskCompleted   PlanTaskStatus = "completed"
	PlanTaskFailed      PlanTaskStatus = "failed"
	PlanTaskBlocked     PlanTaskStatus = "blocked"
	PlanTaskCancelled   PlanTaskStatus = "cancelled"
	PlanTaskInterrupted PlanTaskStatus = "interrupted"
)

// TaskPlan 是中央大脑制定的结构化执行计划。
// 包含子任务列表、依赖关系、并行分层、预算和检查点。
type TaskPlan struct {
	PlanID         string              `json:"plan_id"`
	Version        int                 `json:"version"`
	ProjectID      string              `json:"project_id"`
	Goal           string              `json:"goal"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	SubTasks       []PlanSubTask       `json:"sub_tasks"`
	Dependencies   map[string][]string `json:"dependencies"`
	ParallelLayers [][]string          `json:"parallel_layers"`
	Budget         PlanBudget          `json:"budget"`
	Checkpoints    []PlanCheckpoint    `json:"checkpoints"`
	Estimation     ComplexityEstimation `json:"estimation"`
	Status         PlanStatus          `json:"status"`
	// Interrupt 字段预留：等 interrupt.go (InterruptSignal) 创建后再接入。
}

// PlanSubTask 是 TaskPlan 中的单个子任务。
type PlanSubTask struct {
	TaskID               string          `json:"task_id"`
	Name                 string          `json:"name"`
	Kind                 agent.Kind      `json:"kind"`
	Instruction          string          `json:"instruction"`
	// Language 与 Domain 用于构造冷启动迁移学习的项目指纹
	// (language, domain, kind) 三元组。可为空（保持向后兼容）。
	Language             string          `json:"language,omitempty"`
	Domain               string          `json:"domain,omitempty"`
	EstimatedTurns       int             `json:"estimated_turns"`
	EstimatedTokens      int             `json:"estimated_tokens"`
	VerificationCriteria []string        `json:"verification_criteria"`
	Status               PlanTaskStatus  `json:"status"`
	RunID                string          `json:"run_id,omitempty"`
	StartedAt            *time.Time      `json:"started_at,omitempty"`
	CompletedAt          *time.Time      `json:"completed_at,omitempty"`
	Result               *PlanTaskResult `json:"result,omitempty"`
	RetryPolicy          RetryPolicy     `json:"retry_policy"`
	RetryCount           int             `json:"retry_count"`
}

// PlanTaskResult 是子任务执行完成后的结果。
type PlanTaskResult struct {
	Output     string      `json:"output"`
	Artifacts  []string    `json:"artifacts"`
	Confidence float64     `json:"confidence"`
	Issues     []PlanIssue `json:"issues"`
}

// PlanIssue 描述子任务执行中发现的问题。
type PlanIssue struct {
	Severity     string `json:"severity"`               // critical/warning/info
	Category     string `json:"category"`                // code/security/performance/style
	Description  string `json:"description"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
	AutoFixable  bool   `json:"auto_fixable"`
}

// RetryPolicy 定义子任务的重试策略。
type RetryPolicy struct {
	MaxRetries   int           `json:"max_retries"`
	BackoffBase  time.Duration `json:"backoff_base"`
	RetryOnTypes []string      `json:"retry_on_types"`
}

// PlanBudget 是计划级的资源预算。
type PlanBudget struct {
	TotalTurns   int                `json:"total_turns"`
	TotalTokens  int                `json:"total_tokens"`
	TotalCostUSD float64            `json:"total_cost_usd"`
	UsedTurns    int                `json:"used_turns"`
	UsedTokens   int                `json:"used_tokens"`
	UsedCostUSD  float64            `json:"used_cost_usd"`
	Adjustments  []BudgetAdjustment `json:"adjustments"`
}

// BudgetAdjustment 记录预算的动态调整。
type BudgetAdjustment struct {
	At        time.Time `json:"at"`
	Reason    string    `json:"reason"`
	TurnDelta int       `json:"turn_delta"`
}

// PlanCheckpoint 是执行计划中的关键检查点。
type PlanCheckpoint struct {
	Name      string `json:"name"`
	Condition string `json:"condition"`
	Required  bool   `json:"required"`
	Passed    bool   `json:"passed"`
}

// ComplexityEstimation 是任务复杂度的预估信息。
type ComplexityEstimation struct {
	Source        string  `json:"source"`          // "learning"/"heuristic"/"llm"
	Confidence    float64 `json:"confidence"`
	HistoricalAvg float64 `json:"historical_avg"`
	HistoricalStd float64 `json:"historical_std"`
}

// ─────────────────────────────────────────────────────────────────────────────
// 构造函数与辅助方法
// ─────────────────────────────────────────────────────────────────────────────

// NewTaskPlan 创建一个新的 TaskPlan。
func NewTaskPlan(projectID, goal string) *TaskPlan {
	now := time.Now()
	return &TaskPlan{
		PlanID:         fmt.Sprintf("plan-%d", now.UnixNano()),
		Version:        1,
		ProjectID:      projectID,
		Goal:           goal,
		CreatedAt:      now,
		UpdatedAt:      now,
		SubTasks:       nil,
		Dependencies:   make(map[string][]string),
		ParallelLayers: nil,
		Budget:         PlanBudget{},
		Checkpoints:    nil,
		Status:         PlanDraft,
	}
}

// AddSubTask 向计划中添加一个子任务。
func (p *TaskPlan) AddSubTask(task PlanSubTask) {
	if task.Status == "" {
		task.Status = PlanTaskPending
	}
	p.SubTasks = append(p.SubTasks, task)
	p.UpdatedAt = time.Now()
}

// UpdateTaskStatus 更新指定子任务的状态。
func (p *TaskPlan) UpdateTaskStatus(taskID string, status PlanTaskStatus) {
	for i := range p.SubTasks {
		if p.SubTasks[i].TaskID == taskID {
			p.SubTasks[i].Status = status
			now := time.Now()
			switch status {
			case PlanTaskRunning:
				p.SubTasks[i].StartedAt = &now
			case PlanTaskCompleted, PlanTaskFailed, PlanTaskCancelled, PlanTaskInterrupted:
				p.SubTasks[i].CompletedAt = &now
			}
			p.UpdatedAt = now
			return
		}
	}
}

// ComputeParallelLayers 基于 Dependencies 使用 Kahn 算法计算拓扑分层。
// 每一层中的任务可以并行执行。
func (p *TaskPlan) ComputeParallelLayers() error {
	if len(p.SubTasks) == 0 {
		p.ParallelLayers = nil
		return nil
	}

	// 构建任务 ID 集合
	taskSet := make(map[string]bool, len(p.SubTasks))
	for _, t := range p.SubTasks {
		taskSet[t.TaskID] = true
	}

	// 验证依赖的合法性
	for id, deps := range p.Dependencies {
		if !taskSet[id] {
			return fmt.Errorf("dependency references unknown task %q", id)
		}
		for _, dep := range deps {
			if !taskSet[dep] {
				return fmt.Errorf("task %q depends on unknown task %q", id, dep)
			}
		}
	}

	// Kahn's algorithm：计算入度
	inDegree := make(map[string]int, len(p.SubTasks))
	downstream := make(map[string][]string)
	for _, t := range p.SubTasks {
		if _, ok := inDegree[t.TaskID]; !ok {
			inDegree[t.TaskID] = 0
		}
	}
	for id, deps := range p.Dependencies {
		inDegree[id] += len(deps)
		for _, dep := range deps {
			downstream[dep] = append(downstream[dep], id)
		}
	}

	var layers [][]string
	remaining := len(p.SubTasks)

	for remaining > 0 {
		// 收集入度为 0 的任务
		var layer []string
		for id, deg := range inDegree {
			if deg == 0 {
				layer = append(layer, id)
			}
		}
		if len(layer) == 0 {
			return fmt.Errorf("cycle detected in task dependencies")
		}

		layers = append(layers, layer)

		// 从图中移除已排入本层的任务
		for _, id := range layer {
			inDegree[id] = -1
			remaining--
			for _, child := range downstream[id] {
				inDegree[child]--
			}
		}
	}

	p.ParallelLayers = layers
	p.UpdatedAt = time.Now()
	return nil
}

// OverallProgress 计算所有子任务的完成进度百分比（0.0 ~ 1.0）。
func (p *TaskPlan) OverallProgress() float64 {
	if len(p.SubTasks) == 0 {
		return 0
	}
	completed := 0
	for _, t := range p.SubTasks {
		if t.Status == PlanTaskCompleted {
			completed++
		}
	}
	return float64(completed) / float64(len(p.SubTasks))
}
