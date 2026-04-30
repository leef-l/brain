package kernel

import (
	"context"
	"time"

	"github.com/leef-l/brain/sdk/diaglog"
)

// PlanOrchestrator 是 Orchestrator 的智能化编排层扩展。
// 它组合了 Orchestrator（底层委派能力）和 MACCS v2 的新组件
//（TaskPlan、ProjectProgress、DynamicBudget、ReviewLoop、MetaCognitive）。
type PlanOrchestrator struct {
	// 底层编排器
	Orchestrator *Orchestrator

	// MACCS v2 组件
	Memory          ProjectMemory
	Estimator       *ComplexityEstimator
	BudgetPool      *DynamicBudgetPool
	ReviewLoop      *ReviewLoopController
	MetaCognitive   *MetaCognitiveEngine
	ModelRouter     *ModelRouter
	ProgressStore   ProgressStore
	TransferLearner TransferLearner
	ExperienceStore ExperienceStore
}

// PlanOrchestratorConfig 配置。
type PlanOrchestratorConfig struct {
	Memory          ProjectMemory
	Learner         *LearningEngine
	TotalBudget     int // 总 turn 预算，默认 200
	ReviewConfig    ReviewLoopConfig
	ModelRouter     *ModelRouter
	ProgressStore   ProgressStore
	TransferLearner TransferLearner // 可选：未提供时自动构建 DefaultTransferLearner
	ExperienceStore ExperienceStore // 可选：用于跨会话持久化项目经验
}

// NewPlanOrchestrator 创建智能化编排器。
func NewPlanOrchestrator(orch *Orchestrator, cfg PlanOrchestratorConfig) *PlanOrchestrator {
	totalBudget := cfg.TotalBudget
	if totalBudget <= 0 {
		totalBudget = 200
	}

	// 默认构建一个内存版迁移学习器，保证冷启动路径始终可用
	transfer := cfg.TransferLearner
	if transfer == nil {
		transfer = NewTransferLearner()
	}

	po := &PlanOrchestrator{
		Orchestrator:    orch,
		Memory:          cfg.Memory,
		Estimator:       NewComplexityEstimatorWithTransfer(cfg.Learner, transfer),
		BudgetPool:      NewDynamicBudgetPool(totalBudget),
		MetaCognitive:   NewMetaCognitiveEngine(cfg.Learner),
		ModelRouter:     cfg.ModelRouter,
		ProgressStore:   cfg.ProgressStore,
		TransferLearner: transfer,
		ExperienceStore: cfg.ExperienceStore,
	}

	// 如果配置了持久化存储，尝试加载历史经验到迁移学习器
	if po.ExperienceStore != nil {
		if exps, err := po.ExperienceStore.List(context.Background()); err == nil {
			for _, exp := range exps {
				if exp != nil {
					po.TransferLearner.RecordExperience(*exp)
				}
			}
		}
	}

	if orch != nil {
		po.ReviewLoop = NewReviewLoopController(orch, cfg.ReviewConfig)
	}

	return po
}

// ExecuteProject 执行完整的项目流程：
// 1. 预估复杂度 + 分配预算
// 2. 执行 TaskPlan
// 3. 元认知反思
// 4. 持久化进度
func (po *PlanOrchestrator) ExecuteProject(ctx context.Context, plan *TaskPlan) (*ProjectExecutionResult, error) {
	start := time.Now()

	// 1. 创建 ProjectProgress
	progress := NewProjectProgress(plan.ProjectID, plan.PlanID)
	progress.SetPhase(PhaseExecuting)

	// 2. 预估复杂度并分配预算
	if po.Estimator != nil {
		for i := range plan.SubTasks {
			est := po.Estimator.Estimate(plan.SubTasks[i])
			plan.SubTasks[i].EstimatedTurns = est.EstimatedTurns
		}
	}
	if po.BudgetPool != nil {
		po.BudgetPool.AllocateForPlan(plan)
	}

	// 3. 执行 TaskPlan
	var planResult *TaskPlanResult
	var execErr error
	if po.Orchestrator != nil {
		reporter := func(eventType, taskID, status, detail string) {
			diaglog.Info("plan_orchestrator", eventType,
				"task_id", taskID,
				"status", status,
			)
			// 持久化进度
			if po.ProgressStore != nil {
				_ = po.ProgressStore.SaveProgress(ctx, progress)
			}
		}
		planResult, execErr = po.Orchestrator.ExecuteTaskPlan(ctx, plan, progress, reporter)
	}

	// 4. 元认知反思
	var reflection *ReflectionReport
	if po.MetaCognitive != nil {
		reflection = po.MetaCognitive.Reflect(plan, progress)
		po.MetaCognitive.FeedbackToLearner(reflection)

		// 将经验教训存入项目记忆
		if po.Memory != nil && reflection != nil {
			for _, lesson := range reflection.Lessons {
				if lesson.Importance >= 0.5 {
					_ = po.Memory.Store(ctx, MemoryEntry{
						ProjectID:  plan.ProjectID,
						Type:       MemoryLesson,
						Content:    lesson.Description,
						Summary:    lesson.Category + ": " + lesson.Description,
						Tags:       []string{lesson.Category, "reflection"},
						Importance: lesson.Importance,
					})
				}
			}
		}
	}

	// 5. 最终持久化
	progress.SetPhase(PhaseDelivered)
	if po.ProgressStore != nil {
		_ = po.ProgressStore.SaveProgress(ctx, progress)
	}

	return &ProjectExecutionResult{
		PlanResult: planResult,
		Progress:   progress.Snapshot(),
		Reflection: reflection,
		Duration:   time.Since(start),
		ExecError:  execErr,
	}, nil
}

// ProjectExecutionResult 项目执行最终结果。
type ProjectExecutionResult struct {
	PlanResult *TaskPlanResult   `json:"plan_result"`
	Progress   ProjectProgress   `json:"progress"`
	Reflection *ReflectionReport `json:"reflection,omitempty"`
	Duration   time.Duration     `json:"duration"`
	ExecError  error             `json:"-"`
}
