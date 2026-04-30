package kernel

import (
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// ---------------------------------------------------------------------------
// ProjectPhase — 项目阶段枚举
// ---------------------------------------------------------------------------

type ProjectPhase string

const (
	PhaseAnalyzing ProjectPhase = "analyzing"
	PhaseDesigning ProjectPhase = "designing"
	PhaseReviewing ProjectPhase = "reviewing"
	PhaseExecuting ProjectPhase = "executing"
	PhaseAccepting ProjectPhase = "accepting"
	PhaseDelivered ProjectPhase = "delivered"
	PhaseReworking ProjectPhase = "reworking"
)

// ---------------------------------------------------------------------------
// ProjectProgress — 项目级进度追踪
// ---------------------------------------------------------------------------

// ProjectProgress 实时追踪整个项目的执行进度，包括活跃运行、已完成任务、
// 阻塞任务、质量门禁和资源消耗。中央大脑基于此结构做出调度决策。
type ProjectProgress struct {
	mu             sync.RWMutex   `json:"-"`
	ProjectID      string         `json:"project_id"`
	PlanID         string         `json:"plan_id"`
	UpdatedAt      time.Time      `json:"updated_at"`
	OverallPercent float64        `json:"overall_percent"`
	Phase          ProjectPhase   `json:"phase"`
	ActiveRuns     []RunProgress  `json:"active_runs"`
	CompletedTasks []TaskSummary  `json:"completed_tasks"`
	BlockedTasks   []BlockedInfo  `json:"blocked_tasks"`
	QualityGates   []QualityGate  `json:"quality_gates"`
	ResourceUsage  ResourceUsage  `json:"resource_usage"`
}

// ---------------------------------------------------------------------------
// RunProgress — 单次运行的实时进度
// ---------------------------------------------------------------------------

type RunProgress struct {
	RunID        string     `json:"run_id"`
	TaskID       string     `json:"task_id"`
	TaskName     string     `json:"task_name"`
	BrainKind    agent.Kind `json:"brain_kind"`
	Status       string     `json:"status"`
	CurrentTurn  int        `json:"current_turn"`
	MaxTurns     int        `json:"max_turns"`
	TurnUsage    float64    `json:"turn_usage"`
	LastSummary  string     `json:"last_summary"`
	Confidence   float64    `json:"confidence"`
	StartedAt    time.Time  `json:"started_at"`
	EstimatedEnd *time.Time `json:"estimated_end,omitempty"`
}

// ---------------------------------------------------------------------------
// TaskSummary — 已完成任务的摘要
// ---------------------------------------------------------------------------

type TaskSummary struct {
	TaskID      string        `json:"task_id"`
	TaskName    string        `json:"task_name"`
	BrainKind   agent.Kind    `json:"brain_kind"`
	Duration    time.Duration `json:"duration"`
	TurnsUsed   int           `json:"turns_used"`
	Success     bool          `json:"success"`
	CompletedAt time.Time     `json:"completed_at"`
}

// ---------------------------------------------------------------------------
// BlockedInfo — 任务阻塞信息
// ---------------------------------------------------------------------------

type BlockedInfo struct {
	TaskID    string    `json:"task_id"`
	Reason    string    `json:"reason"`
	BlockedBy []string  `json:"blocked_by"`
	Since     time.Time `json:"since"`
}

// ---------------------------------------------------------------------------
// QualityGate — 质量门禁
// ---------------------------------------------------------------------------

type QualityGate struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Status   string `json:"status"` // pending/passed/failed
	Details  string `json:"details"`
}

// ---------------------------------------------------------------------------
// ResourceUsage — 资源消耗统计
// ---------------------------------------------------------------------------

type ResourceUsage struct {
	TotalTurnsUsed  int     `json:"total_turns_used"`
	TotalTokensUsed int     `json:"total_tokens_used"`
	TotalCostUSD    float64 `json:"total_cost_usd"`
	ActiveBrains    int     `json:"active_brains"`
}

// ---------------------------------------------------------------------------
// 构造函数
// ---------------------------------------------------------------------------

// NewProjectProgress 创建一个新的项目进度追踪实例。
func NewProjectProgress(projectID, planID string) *ProjectProgress {
	return &ProjectProgress{
		ProjectID:      projectID,
		PlanID:         planID,
		UpdatedAt:      time.Now(),
		Phase:          PhaseAnalyzing,
		ActiveRuns:     make([]RunProgress, 0),
		CompletedTasks: make([]TaskSummary, 0),
		BlockedTasks:   make([]BlockedInfo, 0),
		QualityGates:   make([]QualityGate, 0),
	}
}

// ---------------------------------------------------------------------------
// 公开方法（均加锁保护）
// ---------------------------------------------------------------------------

// UpdateRun 更新活跃运行状态。如果 RunID 已存在则覆盖，否则追加。
func (p *ProjectProgress) UpdateRun(run RunProgress) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, r := range p.ActiveRuns {
		if r.RunID == run.RunID {
			p.ActiveRuns[i] = run
			p.UpdatedAt = time.Now()
			p.recalcOverall()
			return
		}
	}
	p.ActiveRuns = append(p.ActiveRuns, run)
	p.UpdatedAt = time.Now()
	p.recalcOverall()
}

// CompleteTask 标记任务完成，从 ActiveRuns 移除对应条目并追加到 CompletedTasks。
func (p *ProjectProgress) CompleteTask(taskID string, summary TaskSummary) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 从 ActiveRuns 中移除
	for i, r := range p.ActiveRuns {
		if r.TaskID == taskID {
			p.ActiveRuns = append(p.ActiveRuns[:i], p.ActiveRuns[i+1:]...)
			break
		}
	}
	p.CompletedTasks = append(p.CompletedTasks, summary)
	p.UpdatedAt = time.Now()
	p.recalcOverall()
}

// BlockTask 标记任务阻塞。
func (p *ProjectProgress) BlockTask(info BlockedInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 避免重复添加
	for i, b := range p.BlockedTasks {
		if b.TaskID == info.TaskID {
			p.BlockedTasks[i] = info
			p.UpdatedAt = time.Now()
			p.recalcOverall()
			return
		}
	}
	p.BlockedTasks = append(p.BlockedTasks, info)
	p.UpdatedAt = time.Now()
	p.recalcOverall()
}

// UnblockTask 解除任务阻塞。
func (p *ProjectProgress) UnblockTask(taskID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, b := range p.BlockedTasks {
		if b.TaskID == taskID {
			p.BlockedTasks = append(p.BlockedTasks[:i], p.BlockedTasks[i+1:]...)
			p.UpdatedAt = time.Now()
			p.recalcOverall()
			return
		}
	}
}

// SetPhase 设置项目阶段。
func (p *ProjectProgress) SetPhase(phase ProjectPhase) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.Phase = phase
	p.UpdatedAt = time.Now()
}

// Snapshot 返回不含 mutex 的只读快照，可安全用于 JSON 序列化。
func (p *ProjectProgress) Snapshot() ProjectProgress {
	p.mu.RLock()
	defer p.mu.RUnlock()

	snap := ProjectProgress{
		ProjectID:      p.ProjectID,
		PlanID:         p.PlanID,
		UpdatedAt:      p.UpdatedAt,
		OverallPercent: p.OverallPercent,
		Phase:          p.Phase,
		ResourceUsage:  p.ResourceUsage,
	}

	snap.ActiveRuns = make([]RunProgress, len(p.ActiveRuns))
	copy(snap.ActiveRuns, p.ActiveRuns)

	snap.CompletedTasks = make([]TaskSummary, len(p.CompletedTasks))
	copy(snap.CompletedTasks, p.CompletedTasks)

	snap.BlockedTasks = make([]BlockedInfo, len(p.BlockedTasks))
	copy(snap.BlockedTasks, p.BlockedTasks)

	snap.QualityGates = make([]QualityGate, len(p.QualityGates))
	copy(snap.QualityGates, p.QualityGates)

	return snap
}

// ---------------------------------------------------------------------------
// 内部方法
// ---------------------------------------------------------------------------

// recalcOverall 重新计算 OverallPercent。
// 公式：completed / (completed + active + blocked) * 100
// 调用方必须持有 mu 写锁。
func (p *ProjectProgress) recalcOverall() {
	total := len(p.CompletedTasks) + len(p.ActiveRuns) + len(p.BlockedTasks)
	if total == 0 {
		p.OverallPercent = 0
		return
	}
	p.OverallPercent = float64(len(p.CompletedTasks)) / float64(total) * 100
}
