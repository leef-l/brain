package kernel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/diaglog"
)

// ─────────────────────────────────────────────────────────────────────────────
// ExecutionScheduler — MACCS Wave 3 执行调度器
//
// 方案审核通过后进入执行阶段，将 TaskPlan 调度到各个 brain 执行。
// 支持拓扑分层并行、动态预算、失败重试和进度追踪。
// ─────────────────────────────────────────────────────────────────────────────

// ExecutionSchedulerConfig 调度器配置。
type ExecutionSchedulerConfig struct {
	MaxParallel      int           `json:"max_parallel"`       // 最大并行任务数，默认 3
	DefaultTimeout   time.Duration `json:"default_timeout"`    // 单任务默认超时，默认 10min
	RetryLimit       int           `json:"retry_limit"`        // 失败重试次数，默认 2
	EnableBudget     bool          `json:"enable_budget"`      // 是否启用动态预算
	ProgressInterval time.Duration `json:"progress_interval"`  // 进度汇报间隔，默认 30s
}

func defaultExecutionSchedulerConfig() ExecutionSchedulerConfig {
	return ExecutionSchedulerConfig{
		MaxParallel:      3,
		DefaultTimeout:   10 * time.Minute,
		RetryLimit:       2,
		EnableBudget:     false,
		ProgressInterval: 30 * time.Second,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ScheduledTask — 调度中的任务
// ─────────────────────────────────────────────────────────────────────────────

// ScheduledTask 表示执行调度器中的单个任务及其运行时状态。
// Status: queued/running/completed/failed/retrying/skipped
type ScheduledTask struct {
	mu            sync.RWMutex
	Task          PlanSubTask `json:"task"`
	Status        string      `json:"status"`
	StartedAt     *time.Time  `json:"started_at,omitempty"`
	CompletedAt   *time.Time  `json:"completed_at,omitempty"`
	Result        string      `json:"result,omitempty"`
	Error         string      `json:"error,omitempty"`
	RetryCount    int         `json:"retry_count"`
	AssignedBrain string      `json:"assigned_brain,omitempty"`
	TurnsUsed     int         `json:"turns_used"`
	TurnsAlloc    int         `json:"turns_alloc"`
}

// ─────────────────────────────────────────────────────────────────────────────
// ExecutionPlan — 从 TaskPlan 转换而来的执行计划
// ─────────────────────────────────────────────────────────────────────────────

// ExecutionPlan 包含拓扑分层和所有调度任务，驱动逐层并行执行。
type ExecutionPlan struct {
	PlanID       string                    `json:"plan_id"`
	Layers       [][]string                `json:"layers"`
	Tasks        map[string]*ScheduledTask `json:"tasks"`
	TotalTasks   int                       `json:"total_tasks"`
	CurrentLayer int                       `json:"current_layer"`
}

// ─────────────────────────────────────────────────────────────────────────────
// ExecutionProgress — 进度快照
// ─────────────────────────────────────────────────────────────────────────────

// ExecutionProgress 是执行计划的进度快照，不含锁，可安全序列化。
type ExecutionProgress struct {
	TotalTasks     int     `json:"total_tasks"`
	CompletedTasks int     `json:"completed_tasks"`
	FailedTasks    int     `json:"failed_tasks"`
	RunningTasks   int     `json:"running_tasks"`
	QueuedTasks    int     `json:"queued_tasks"`
	CurrentLayer   int     `json:"current_layer"`
	TotalLayers    int     `json:"total_layers"`
	Percentage     float64 `json:"percentage"`
	TotalTurnsUsed int     `json:"total_turns_used"`
}

// ─────────────────────────────────────────────────────────────────────────────
// ExecutionScheduler — 执行调度器
// ─────────────────────────────────────────────────────────────────────────────

// ExecutionScheduler 将审核通过的 TaskPlan 调度为可执行的分层并行计划，
// 并提供任务生命周期管理（启动、完成、失败、重试）和进度追踪。
type ExecutionScheduler struct {
	mu           sync.RWMutex
	config       ExecutionSchedulerConfig
	budget       *DynamicBudgetPool // 可选
	progress     *ProjectProgress   // 可选
	orchestrator *Orchestrator      // 可选；非空时 Run/RunPlan 通过 DelegateBatch 真实派发
}

// ─────────────────────────────────────────────────────────────────────────────
// 构造函数
// ─────────────────────────────────────────────────────────────────────────────

// NewExecutionScheduler 创建使用默认配置的执行调度器。
func NewExecutionScheduler(config ExecutionSchedulerConfig) *ExecutionScheduler {
	cfg := applyExecDefaults(config)
	return &ExecutionScheduler{config: cfg}
}

// NewExecutionSchedulerWithDeps 创建带动态预算和进度追踪的执行调度器。
func NewExecutionSchedulerWithDeps(config ExecutionSchedulerConfig, budget *DynamicBudgetPool, progress *ProjectProgress) *ExecutionScheduler {
	cfg := applyExecDefaults(config)
	return &ExecutionScheduler{
		config:   cfg,
		budget:   budget,
		progress: progress,
	}
}

// NewExecutionSchedulerWithOrchestrator 创建可真实派发任务的执行调度器。
// 注入 Orchestrator 后 Run/RunPlan 会通过 DelegateBatch 把 ScheduledTask
// 派发到 brain pool。
func NewExecutionSchedulerWithOrchestrator(config ExecutionSchedulerConfig, orch *Orchestrator) *ExecutionScheduler {
	cfg := applyExecDefaults(config)
	return &ExecutionScheduler{config: cfg, orchestrator: orch}
}

// AttachOrchestrator 在已构造的 Scheduler 上注入 Orchestrator 引用。
func (s *ExecutionScheduler) AttachOrchestrator(orch *Orchestrator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orchestrator = orch
}

func applyExecDefaults(cfg ExecutionSchedulerConfig) ExecutionSchedulerConfig {
	defaults := defaultExecutionSchedulerConfig()
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = defaults.MaxParallel
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = defaults.DefaultTimeout
	}
	if cfg.RetryLimit < 0 {
		cfg.RetryLimit = defaults.RetryLimit
	}
	if cfg.ProgressInterval <= 0 {
		cfg.ProgressInterval = defaults.ProgressInterval
	}
	return cfg
}

// ─────────────────────────────────────────────────────────────────────────────
// BuildExecutionPlan — 将 TaskPlan 转为 ExecutionPlan
// ─────────────────────────────────────────────────────────────────────────────

// BuildExecutionPlan 基于 TaskPlan 的拓扑分层构建 ExecutionPlan。
// 调用 plan.ComputeParallelLayers() 获取分层，为每个 PlanSubTask 创建
// ScheduledTask，若启用 budget 则从 DynamicBudgetPool 分配预算。
func (s *ExecutionScheduler) BuildExecutionPlan(plan *TaskPlan) (*ExecutionPlan, error) {
	if plan == nil {
		return nil, fmt.Errorf("plan is nil")
	}
	if len(plan.SubTasks) == 0 {
		return nil, fmt.Errorf("plan has no sub-tasks")
	}

	// 计算拓扑分层
	if err := plan.ComputeParallelLayers(); err != nil {
		return nil, fmt.Errorf("compute parallel layers: %w", err)
	}

	// 构建任务索引
	taskMap := make(map[string]*PlanSubTask, len(plan.SubTasks))
	for i := range plan.SubTasks {
		taskMap[plan.SubTasks[i].TaskID] = &plan.SubTasks[i]
	}

	// 创建 ScheduledTask
	s.mu.RLock()
	enableBudget := s.config.EnableBudget
	s.mu.RUnlock()

	tasks := make(map[string]*ScheduledTask, len(plan.SubTasks))
	for _, sub := range plan.SubTasks {
		st := &ScheduledTask{
			Task:   sub,
			Status: "queued",
		}

		// 如果启用 budget 且 pool 可用，分配预算
		if enableBudget && s.budget != nil {
			alloc := s.budget.Allocate(sub.TaskID, sub.EstimatedTurns)
			st.TurnsAlloc = alloc
		}

		tasks[sub.TaskID] = st
	}

	ep := &ExecutionPlan{
		PlanID:       plan.PlanID,
		Layers:       plan.ParallelLayers,
		Tasks:        tasks,
		TotalTasks:   len(plan.SubTasks),
		CurrentLayer: 0,
	}

	return ep, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 批次与分层推进
// ─────────────────────────────────────────────────────────────────────────────

// NextBatch 返回当前层中 status=queued 的任务，最多 MaxParallel 个。
func (s *ExecutionScheduler) NextBatch(execPlan *ExecutionPlan) []*ScheduledTask {
	s.mu.RLock()
	maxP := s.config.MaxParallel
	s.mu.RUnlock()

	if execPlan.CurrentLayer >= len(execPlan.Layers) {
		return nil
	}

	layer := execPlan.Layers[execPlan.CurrentLayer]
	var batch []*ScheduledTask

	for _, taskID := range layer {
		if len(batch) >= maxP {
			break
		}
		st, ok := execPlan.Tasks[taskID]
		if !ok {
			continue
		}
		st.mu.RLock()
		status := st.Status
		st.mu.RUnlock()
		if status == "queued" {
			batch = append(batch, st)
		}
	}

	return batch
}

// AdvanceLayer 当前层全部完成（或跳过/失败）时推进到下一层。
// 返回 true 表示还有后续层待执行，false 表示全部完成。
func (s *ExecutionScheduler) AdvanceLayer(execPlan *ExecutionPlan) bool {
	if execPlan.CurrentLayer >= len(execPlan.Layers) {
		return false
	}

	// 检查当前层是否全部终结
	layer := execPlan.Layers[execPlan.CurrentLayer]
	for _, taskID := range layer {
		st, ok := execPlan.Tasks[taskID]
		if !ok {
			continue
		}
		st.mu.RLock()
		status := st.Status
		st.mu.RUnlock()
		switch status {
		case "completed", "failed", "skipped":
			// 已终结，继续检查
		default:
			return true // 还有未终结的任务
		}
	}

	execPlan.CurrentLayer++
	return execPlan.CurrentLayer < len(execPlan.Layers)
}

// ─────────────────────────────────────────────────────────────────────────────
// 任务生命周期管理
// ─────────────────────────────────────────────────────────────────────────────

// MarkRunning 标记任务为运行中，记录分配的 brain 类型。
func (s *ExecutionScheduler) MarkRunning(task *ScheduledTask, brainKind string) {
	task.mu.Lock()
	defer task.mu.Unlock()

	now := time.Now()
	task.Status = "running"
	task.StartedAt = &now
	task.AssignedBrain = brainKind
}

// MarkCompleted 标记任务完成，记录结果和 turns 消耗。
// 若启用 budget，回收多余预算。
func (s *ExecutionScheduler) MarkCompleted(task *ScheduledTask, result string, turnsUsed int) {
	task.mu.Lock()
	taskID := task.Task.TaskID
	now := time.Now()
	task.Status = "completed"
	task.CompletedAt = &now
	task.Result = result
	task.TurnsUsed = turnsUsed
	alloced := task.TurnsAlloc
	task.mu.Unlock()

	// 回收多余预算
	s.mu.RLock()
	enableBudget := s.config.EnableBudget
	s.mu.RUnlock()

	if enableBudget && s.budget != nil && alloced > 0 {
		s.budget.Reclaim(taskID, turnsUsed)
	}
}

// MarkFailed 标记任务失败，返回是否还能重试。
func (s *ExecutionScheduler) MarkFailed(task *ScheduledTask, errMsg string) bool {
	task.mu.Lock()
	defer task.mu.Unlock()

	now := time.Now()
	task.Status = "failed"
	task.CompletedAt = &now
	task.Error = errMsg

	s.mu.RLock()
	retryLimit := s.config.RetryLimit
	s.mu.RUnlock()

	return task.RetryCount < retryLimit
}

// CanRetry 检查任务是否可重试。
func (s *ExecutionScheduler) CanRetry(task *ScheduledTask) bool {
	task.mu.RLock()
	defer task.mu.RUnlock()

	s.mu.RLock()
	retryLimit := s.config.RetryLimit
	s.mu.RUnlock()

	return task.RetryCount < retryLimit
}

// RetryTask 重置任务状态为 queued，重试计数+1。
func (s *ExecutionScheduler) RetryTask(task *ScheduledTask) {
	task.mu.Lock()
	defer task.mu.Unlock()

	task.Status = "queued"
	task.RetryCount++
	task.StartedAt = nil
	task.CompletedAt = nil
	task.Error = ""
	task.Result = ""
	task.AssignedBrain = ""
	task.TurnsUsed = 0
}

// ─────────────────────────────────────────────────────────────────────────────
// 进度查询
// ─────────────────────────────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────────
// RunPlan / Run — 通过 Orchestrator 真实派发任务
// ─────────────────────────────────────────────────────────────────────────────

// RunPlan 通过 Orchestrator.DelegateBatch 按拓扑层并行执行 ExecutionPlan。
// 每一层拾取 NextBatch → 调用 DelegateBatch → 处理结果 → 失败按 RetryLimit 重试 →
// 推进下一层。必须先注入 Orchestrator（NewExecutionSchedulerWithOrchestrator
// 或 AttachOrchestrator），否则返回错误。
func (s *ExecutionScheduler) RunPlan(ctx context.Context, execPlan *ExecutionPlan) error {
	if execPlan == nil {
		return fmt.Errorf("execution plan is nil")
	}
	s.mu.RLock()
	orch := s.orchestrator
	retryLimit := s.config.RetryLimit
	s.mu.RUnlock()
	if orch == nil {
		return fmt.Errorf("execution_scheduler: 未注入 Orchestrator，无法真实派发任务")
	}

	for execPlan.CurrentLayer < len(execPlan.Layers) {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		batch := s.NextBatch(execPlan)
		if len(batch) == 0 {
			if !s.AdvanceLayer(execPlan) {
				break
			}
			continue
		}

		batchReq := &DelegateBatchRequest{Requests: make([]*DelegateRequest, 0, len(batch))}
		for _, t := range batch {
			s.MarkRunning(t, string(t.Task.Kind))
			req := &DelegateRequest{
				TaskID:      t.Task.TaskID,
				TargetKind:  t.Task.Kind,
				Instruction: t.Task.Instruction,
			}
			if t.Task.EstimatedTurns > 0 {
				req.Budget = &SubtaskBudget{MaxTurns: t.Task.EstimatedTurns}
			}
			batchReq.Requests = append(batchReq.Requests, req)
		}

		diaglog.Info("execution_scheduler", "dispatching batch via Orchestrator.DelegateBatch",
			"plan_id", execPlan.PlanID,
			"layer", execPlan.CurrentLayer,
			"size", len(batch))

		batchResult, err := orch.DelegateBatch(ctx, batchReq)
		if err != nil {
			// 整批返回错误：按 retryLimit 决定是否还给本层一次机会
			for _, t := range batch {
				if !s.MarkFailed(t, err.Error()) {
					continue
				}
				if retryLimit > 0 {
					s.RetryTask(t)
				}
			}
			if !s.AdvanceLayer(execPlan) {
				break
			}
			continue
		}

		// succeeded：本批次成功数；retried：可重试的失败数；failed：不可重试的失败数。
		// retried==0 有两种来源：全成功 或 全部失败且超 RetryLimit。
		// 只有 failed==0 时才能推进下一层，否则整层已不可恢复。
		var succeeded, retried, failed int
		for i, t := range batch {
			if i >= len(batchResult.Results) {
				if s.MarkFailed(t, "no result returned") {
					s.RetryTask(t)
					retried++
				} else {
					failed++
				}
				continue
			}
			r := batchResult.Results[i]
			if r != nil && r.Status == "completed" {
				s.MarkCompleted(t, string(r.Output), r.Usage.Turns)
				succeeded++
				continue
			}
			errMsg := "unknown failure"
			if r != nil && r.Error != "" {
				errMsg = r.Error
			}
			if s.MarkFailed(t, errMsg) {
				s.RetryTask(t)
				retried++
			} else {
				failed++
			}
		}
		_ = succeeded // 记录成功数，保留用于后续扩展（如进度上报）

		if failed > 0 && retried == 0 {
			// 整层存在不可重试的失败任务，终止计划执行
			return fmt.Errorf("execution plan %s: layer %d 有 %d 个任务彻底失败（超过重试上限），终止执行",
				execPlan.PlanID, execPlan.CurrentLayer, failed)
		}
		if retried == 0 {
			// 本批次全部成功，推进下一层
			if !s.AdvanceLayer(execPlan) {
				break
			}
		}
	}

	return nil
}

// Run 一站式执行：BuildExecutionPlan + RunPlan。
// 当 Orchestrator 未注入时返回错误。
func (s *ExecutionScheduler) Run(ctx context.Context, plan *TaskPlan) (*ExecutionPlan, error) {
	execPlan, err := s.BuildExecutionPlan(plan)
	if err != nil {
		return nil, err
	}
	if err := s.RunPlan(ctx, execPlan); err != nil {
		return execPlan, err
	}
	return execPlan, nil
}

// Progress 返回当前 ExecutionPlan 的进度快照。
func (s *ExecutionScheduler) Progress(execPlan *ExecutionPlan) ExecutionProgress {
	var completed, failed, running, queued, turnsUsed int

	for _, st := range execPlan.Tasks {
		st.mu.RLock()
		switch st.Status {
		case "completed":
			completed++
		case "failed":
			failed++
		case "running":
			running++
		case "queued", "retrying":
			queued++
		case "skipped":
			completed++ // skipped 算入已处理
		}
		turnsUsed += st.TurnsUsed
		st.mu.RUnlock()
	}

	total := execPlan.TotalTasks
	var pct float64
	if total > 0 {
		pct = float64(completed) / float64(total) * 100
	}

	return ExecutionProgress{
		TotalTasks:     total,
		CompletedTasks: completed,
		FailedTasks:    failed,
		RunningTasks:   running,
		QueuedTasks:    queued,
		CurrentLayer:   execPlan.CurrentLayer,
		TotalLayers:    len(execPlan.Layers),
		Percentage:     pct,
		TotalTurnsUsed: turnsUsed,
	}
}
