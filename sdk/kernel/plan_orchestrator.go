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
	Memory           ProjectMemory
	Estimator        *ComplexityEstimator
	BudgetPool       *DynamicBudgetPool
	ReviewLoop       *ReviewLoopController
	MetaCognitive    *MetaCognitiveEngine
	ModelRouter      *ModelRouter
	ProgressStore    ProgressStore
	TransferLearner  TransferLearner
	ExperienceStore  ExperienceStore
	ContextEngine    ContextEngine    // 可选：带项目记忆的 ContextEngine（接入下游 brain prompt）
	MemoryRetriever  *MemoryRetriever // 可选：用于 reflection 后检索相似历史经验
	memoryRetrieveN  int              // MemoryRetriever 取 top-N
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

	// ContextEngine 可选：通常是 ContextEngineWithMemory（包装默认 ContextEngine
	// + ProjectMemory）。NewPlanOrchestrator 会在构造时调用 orch.SetContextEngine
	// 把它注入底层 Orchestrator，使 Delegate 阶段下游 brain prompt 自动带上项目
	// 记忆摘要。
	ContextEngine ContextEngine

	// MemoryRetriever 可选：多因子排序记忆检索器。NewPlanOrchestrator 会保存它，
	// ExecuteProject 在 reflection 之后用它从 Memory 中检索与 plan.Goal 相似的
	// 历史 entries，把 top-N 摘要追加到 reflection.Recommendations。
	MemoryRetriever *MemoryRetriever

	// MemoryRetrieveLimit MemoryRetriever 取 top-N，默认 5。
	MemoryRetrieveLimit int
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

	retrieveN := cfg.MemoryRetrieveLimit
	if retrieveN <= 0 {
		retrieveN = 5
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
		ContextEngine:   cfg.ContextEngine,
		MemoryRetriever: cfg.MemoryRetriever,
		memoryRetrieveN: retrieveN,
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

		// 接入 ContextEngine：替换底层 Orchestrator 的 contextEngine，使 Delegate
		// 阶段下游 brain prompt 自动带上项目记忆摘要（ContextEngineWithMemory.Assemble
		// 会前置 system 消息）。
		if cfg.ContextEngine != nil {
			orch.SetContextEngine(cfg.ContextEngine)
		}

		// 接入 ModelRouter：把所有显式配置同步到 LLMProxy.ModelForKind，
		// 使后续 Delegate 自动选用正确模型。Resolve 在 ExecuteProject 中按
		// SubTask.Kind 调用，把决策写入 diaglog 便于审计。
		if cfg.ModelRouter != nil {
			cfg.ModelRouter.SyncToLLMProxy(orch.LLMProxy())
		}
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

	// 2.5 ModelRouter：按 SubTask.Kind 解析推荐模型，写入 diaglog 便于审计。
	// SyncToLLMProxy 已在 NewPlanOrchestrator 完成「显式配置 → ModelForKind」同步；
	// 这里的 Resolve 是「按任务类型选模型」的二次决策，主要用于审计/可观测。
	if po.ModelRouter != nil {
		for i := range plan.SubTasks {
			st := &plan.SubTasks[i]
			model := po.ModelRouter.Resolve(st.Kind, st.Name)
			if model != "" {
				diaglog.Info("plan_orchestrator", "model_router_resolve",
					"plan_id", plan.PlanID,
					"task_id", st.TaskID,
					"kind", string(st.Kind),
					"model", model,
				)
			}
		}
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

		// 4.5 MemoryRetriever：从项目记忆中按 plan.Goal 多因子检索 top-N 相关
		// entries（关键词 + tag + 时间衰减 + 重要度 4 维加权），把摘要追加到
		// reflection.Recommendations，形成"借鉴历史经验"的可读输出。
		if po.MemoryRetriever != nil && po.Memory != nil && reflection != nil {
			entries, err := po.Memory.Query(ctx, MemoryQuery{
				ProjectID: plan.ProjectID,
				Limit:     200,
			})
			if err == nil && len(entries) > 0 {
				results := po.MemoryRetriever.Retrieve(entries, plan.Goal, nil, po.memoryRetrieveN)
				for _, r := range results {
					if r.Score <= 0 {
						continue
					}
					summary := r.Entry.Summary
					if summary == "" {
						summary = r.Entry.Content
					}
					reflection.Recommendations = append(reflection.Recommendations,
						"[相似经验/"+r.MatchType+"] "+summary,
					)
				}
				diaglog.Info("plan_orchestrator", "memory_retriever_top",
					"plan_id", plan.PlanID,
					"project_id", plan.ProjectID,
					"matched", len(results),
				)
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
