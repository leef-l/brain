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
	MaxParallel      int           `json:"max_parallel"`      // 最大并行任务数，默认 3
	DefaultTimeout   time.Duration `json:"default_timeout"`   // 单任务默认超时，默认 10min
	RetryLimit       int           `json:"retry_limit"`       // 失败重试次数，默认 2
	EnableBudget     bool          `json:"enable_budget"`     // 是否启用动态预算
	ProgressInterval time.Duration `json:"progress_interval"` // 进度汇报间隔，默认 30s
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
	// Workdir 从 TaskPlan.Workdir 复制过来，传递给每个 DelegateRequest。
	Workdir string `json:"workdir,omitempty"`
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
//
// 接入：
//   - conflictDetector + smartScheduler 共同提供"冲突感知重排"能力。
//     当两者非空时，BuildExecutionPlan 完成后会用 SmartScheduler.Reschedule
//     在拓扑分层基础上做贪心冲突分离（同层资源冲突挤到下一层）；RunPlan
//     在派发当前层之前再调用 ValidateSchedule，发现仍存在 blocker 冲突
//     的任务会被推迟（dryRun 模式下仅 diaglog 警告，不实际推迟）。
//   - deadlockDetector + arbiter 共同提供"死锁检测 + 仲裁"能力（MACCS Wave 7）。
//     当两者非空时，RunPlan 派发前若 ConflictDetector 报告 blocker，会把冲突
//     转换为 wait-for 边写入 DeadlockDetector，命中环时由 Arbiter.ResolveDeadlock
//     选 victim，victim 直接 MarkFailed("deadlock-victim") 不重试以打破环。
//     dryRunDeadlock=true 时仅 diaglog 警告，不实际中止 victim（生产观察期）。
//   - dryRunConflict=true 时只记录日志不重排，用于灰度观察误报率（路线图
//     §6 风险对策）。
type ExecutionScheduler struct {
	mu               sync.RWMutex
	config           ExecutionSchedulerConfig
	budget           *DynamicBudgetPool                   // 可选
	progress         *ProjectProgress                     // 可选
	orchestrator     *Orchestrator                        // 可选；非空时 Run/RunPlan 通过 DelegateBatch 真实派发
	conflictDetector ConflictDetector                     // 可选：每层派发前做冲突检测
	smartScheduler   *SmartScheduler                      // 可选：基于检测结果做冲突感知重排
	dryRunConflict   bool                                 // 灰度开关：true 时仅日志，不实际重排/推迟
	resourceProvider func(taskID string) TaskResourceDecl // 可选：从外部解析资源声明
	deadlockDetector *DeadlockDetector                    // 可选（Wave 7）：wait-for graph
	arbiter          Arbiter                              // 可选（Wave 7）：死锁/冲突仲裁
	dryRunDeadlock   bool                                 // Wave 7 灰度开关：true 时仅日志，不实际中止 victim
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

// AttachConflictControl 注入冲突检测 + 智能重排能力。
// detector 与 smart 任一为 nil 视为关闭冲突感知；dryRun=true 时仅记录日志，
// 不实际重排（给生产环境一周观察期，再切换到强制重排，路线图风险对策）。
// resolver 可选：从外部（如 PlanSubTask 元数据）解析每个任务的 TaskResourceDecl；
// 为 nil 时仅根据 Kind 生成最小声明。
func (s *ExecutionScheduler) AttachConflictControl(detector ConflictDetector, smart *SmartScheduler, dryRun bool, resolver func(taskID string) TaskResourceDecl) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conflictDetector = detector
	s.smartScheduler = smart
	s.dryRunConflict = dryRun
	s.resourceProvider = resolver
}

// AttachDeadlockControl 注入 wait-for graph 死锁检测 + 仲裁能力（MACCS Wave 7）。
// detector 与 arbiter 任一为 nil 视为关闭。dryRun=true 时仅 diaglog 警告，
// 不实际中止 victim 任务（生产观察期开关）。
//
// 接入点：RunPlan 派发当前层之前，如果 ConflictDetector 报告 blocker 冲突，
// 会把每个 blocker 的 TaskIDs 转换为 wait-for 边（按 PlanSubTask.TaskID
// 字典序，排在前者为 holder，后者为 waiter——稳定可重现）。然后调
// DeadlockDetector.Detect 检环，命中环交给 Arbiter.ResolveDeadlock 选
// victim，将其 MarkFailed("deadlock-victim") 强制不可重试以打破环。
func (s *ExecutionScheduler) AttachDeadlockControl(detector *DeadlockDetector, arbiter Arbiter, dryRun bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadlockDetector = detector
	s.arbiter = arbiter
	s.dryRunDeadlock = dryRun
}

// resourceDecls 为 ExecutionPlan 内所有任务生成资源声明。
// 优先用 resourceProvider 注入的解析器；缺失时返回最小声明（仅 BrainKind）。
func (s *ExecutionScheduler) resourceDecls(execPlan *ExecutionPlan) map[string]TaskResourceDecl {
	s.mu.RLock()
	provider := s.resourceProvider
	s.mu.RUnlock()
	out := make(map[string]TaskResourceDecl, len(execPlan.Tasks))
	for tid, st := range execPlan.Tasks {
		if provider != nil {
			d := provider(tid)
			if d.TaskID == "" {
				d.TaskID = tid
			}
			if d.BrainKind == "" {
				d.BrainKind = string(st.Task.Kind)
			}
			out[tid] = d
			continue
		}
		out[tid] = TaskResourceDecl{TaskID: tid, BrainKind: string(st.Task.Kind)}
	}
	return out
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
		Workdir:      plan.Workdir,
	}

	// 冲突感知重排：如果注入了 SmartScheduler + ConflictDetector，则在拓扑分层
	// 之上做贪心冲突分离。dryRun 模式下仅 diaglog 警告，不改变 ep.Layers。
	s.mu.RLock()
	smart := s.smartScheduler
	dryRun := s.dryRunConflict
	s.mu.RUnlock()
	if smart != nil {
		decls := s.resourceDecls(ep)
		result := smart.Reschedule(ep.Layers, decls)
		if result != nil && result.ConflictsAvoided > 0 {
			diaglog.Info("execution_scheduler", "smart reschedule applied",
				"plan_id", ep.PlanID,
				"conflicts_avoided", result.ConflictsAvoided,
				"layers_delta", result.LayersDelta,
				"dry_run", dryRun,
				"explanation", result.Explanation,
			)
			if !dryRun && len(result.OptimizedLayers) > 0 {
				ep.Layers = result.OptimizedLayers
			}
		}
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

		// 派发前再次校验：拿到当前层的实际任务集（可能包含被推迟的） →
		// 调 ConflictDetector 过一遍。命中 blocker 就在 dryRun=false 下记录
		// 警告（重排在 Build 时已经做过；此处是安全网）。
		s.mu.RLock()
		detector := s.conflictDetector
		dryRun := s.dryRunConflict
		ddet := s.deadlockDetector
		arb := s.arbiter
		ddDryRun := s.dryRunDeadlock
		s.mu.RUnlock()
		victims := map[string]bool{} // Wave 7 死锁仲裁选出的 victim 集合
		if detector != nil {
			declMap := s.resourceDecls(execPlan)
			batchDecls := make([]TaskResourceDecl, 0, len(batch))
			for _, t := range batch {
				if d, ok := declMap[t.Task.TaskID]; ok {
					batchDecls = append(batchDecls, d)
				}
			}
			report := detector.Detect(batchDecls)
			if report != nil && report.HasBlockers {
				diaglog.Warn("execution_scheduler", "blocker conflicts detected at dispatch",
					"plan_id", execPlan.PlanID,
					"layer", execPlan.CurrentLayer,
					"blocker_count", report.BlockerCount,
					"summary", report.Summary(),
					"dry_run", dryRun,
				)

				// MACCS Wave 7（4.3 + 4.4）：把 blocker 冲突转为 wait-for 边喂给
				// DeadlockDetector，命中环时由 Arbiter.ResolveDeadlock 选 victim。
				if ddet != nil && arb != nil {
					victims = s.resolveDeadlocksFromConflicts(execPlan, batch, report, ddet, arb, ddDryRun)
				}
			}
		}

		// 过滤掉 victim：MarkFailed 但 RetryCount 强制不重试以打破环。
		liveBatch := make([]*ScheduledTask, 0, len(batch))
		for _, t := range batch {
			if victims[t.Task.TaskID] {
				// dryRun 模式下不真正中止，仅日志（resolveDeadlocksFromConflicts 已记录）
				continue
			}
			liveBatch = append(liveBatch, t)
		}
		batch = liveBatch
		if len(batch) == 0 {
			// 整批都是 victim，推进到下一层避免空转
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
				Workdir:     execPlan.Workdir, // workdir 端到端贯穿
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

// resolveDeadlocksFromConflicts 把 ConflictDetector 报告的 blocker 冲突翻译为
// DeadlockDetector 的 wait-for 边，检环后由 Arbiter 选 victim。返回 victim
// taskID 集合：调用方据此把这些任务从派发列表中剔除（dryRun 时返回空集，仅日志）。
//
// 翻译规则：每个 blocker 冲突涉及若干 TaskID，按字典序排序后，下一个等待前一个
// （形成 task[i+1] → task[i] 的边）。这样若多个冲突循环引用同一资源链，环会
// 被检测出来；同时排序保证可重现，避免本调度器自身引入不确定性。
//
// 注意：单条 blocker 冲突 TaskIDs=[a, b] 只产生一条边 b→a，不构成环。
// 真正触发死锁仲裁需要多条 blocker 冲突跨资源相互引用（如 a→b、b→c、c→a），
// 这正是设计意图——绝大多数真冲突由 SmartScheduler 重排即可解决，仅在多任务
// 多资源相互锁死时由本路径介入仲裁。
//
// 副作用：把本批次写入的 wait-for 边在调用结束时全部清除（按 task 维度），
// 让下一批不携带本批次的死锁状态。
func (s *ExecutionScheduler) resolveDeadlocksFromConflicts(
	execPlan *ExecutionPlan,
	batch []*ScheduledTask,
	report *ConflictReport,
	ddet *DeadlockDetector,
	arb Arbiter,
	dryRun bool,
) map[string]bool {
	victims := map[string]bool{}
	if report == nil || len(report.Conflicts) == 0 {
		return victims
	}

	// 收集本批次所有写入的 task ID，便于结束后清理 wait-for graph。
	touched := map[string]bool{}
	for _, c := range report.Conflicts {
		if c.Severity != SeverityBlocker {
			continue
		}
		ids := append([]string(nil), c.TaskIDs...)
		// 字典序稳定，避免随机性
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				if ids[j] < ids[i] {
					ids[i], ids[j] = ids[j], ids[i]
				}
			}
		}
		// task[k+1] 等待 task[k] 释放资源
		for k := 0; k+1 < len(ids); k++ {
			holder, waiter := ids[k], ids[k+1]
			ddet.AddWaitEdge(waiter, holder, c.ResourcePath)
			touched[holder] = true
			touched[waiter] = true
		}
	}

	defer func() {
		for tid := range touched {
			ddet.RemoveTask(tid)
		}
	}()

	deadReport := ddet.Detect()
	if deadReport == nil || !deadReport.HasDeadlock {
		return victims
	}

	priorities := buildBatchPriorities(execPlan, batch)

	// 把 RetryLimit 在循环外取一次（带锁），避免每个 victim 都裸读 s.config。
	s.mu.RLock()
	retryCap := s.config.RetryLimit
	s.mu.RUnlock()

	for _, cycle := range deadReport.Cycles {
		decision := arb.ResolveDeadlock(cycle, priorities)
		if decision == nil {
			continue
		}
		diaglog.Warn("execution_scheduler", "deadlock cycle resolved by arbiter",
			"plan_id", execPlan.PlanID,
			"layer", execPlan.CurrentLayer,
			"cycle_id", cycle.CycleID,
			"cycle_tasks", cycle.TaskIDs,
			"strategy", string(decision.Strategy),
			"victims", decision.LoserTasks,
			"reason", decision.Reason,
			"dry_run", dryRun,
		)
		if dryRun {
			continue
		}
		for _, vtid := range decision.LoserTasks {
			victims[vtid] = true
			st, ok := execPlan.Tasks[vtid]
			if !ok {
				continue
			}
			// 强制不可重试：把 RetryCount 推到上限再标失败。
			// 注意：MarkFailed 内部会取 task.mu.Lock，所以这里在释放后再调。
			st.mu.Lock()
			st.RetryCount = retryCap + 1
			st.mu.Unlock()
			_ = s.MarkFailed(st, fmt.Sprintf("deadlock-victim: cycle=%s", cycle.CycleID))
		}
	}
	return victims
}

// buildBatchPriorities 为本批次的任务推导 TaskPriorityInfo，
// 供 Arbiter 选择 victim 时参考。
//
// 派生规则（无显式 Priority 字段时的默认）：
//   - 已开始的任务（StartedAt != nil）置 Critical=true，避免被中止丢失进度
//   - Priority = EstimatedTurns（turns 越短数值越小→优先级越高，让短任务先跑完释放锁）
//   - 在批次外的任务（如旧层未清理的 holder）也注册一份信息，避免 arbiter 误选
func buildBatchPriorities(execPlan *ExecutionPlan, batch []*ScheduledTask) map[string]TaskPriorityInfo {
	out := make(map[string]TaskPriorityInfo, len(execPlan.Tasks))
	for tid, st := range execPlan.Tasks {
		st.mu.RLock()
		startedAt := st.StartedAt
		prio := st.Task.EstimatedTurns
		kind := string(st.Task.Kind)
		st.mu.RUnlock()
		critical := startedAt != nil
		if prio <= 0 {
			prio = 100
		}
		out[tid] = TaskPriorityInfo{
			TaskID:    tid,
			Priority:  prio,
			BrainKind: kind,
			StartedAt: startedAt,
			Critical:  critical,
		}
	}
	// batch 中的任务即使未启动也应在 priority 表，确保 arbiter 能找到
	for _, t := range batch {
		if _, ok := out[t.Task.TaskID]; !ok {
			out[t.Task.TaskID] = TaskPriorityInfo{
				TaskID:    t.Task.TaskID,
				Priority:  100,
				BrainKind: string(t.Task.Kind),
			}
		}
	}
	return out
}
