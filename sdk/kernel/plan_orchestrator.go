package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

	// PatternExtractor 可选（MACCS 5.4）。ExecuteProject 完成后异步从历史经验
	// 提取共性模式，把新 pattern 写回 PatternLibrary + 关键摘要写入 ProjectMemory。
	// nil 时跳过；要使用必须同时配置 ExperienceStore 才能拉到历史经验。
	PatternExtractor PatternExtractor
	// patternBgCtx 用于驱动异步抽取的顶层 ctx，请求 ctx 在返回时已 cancel。
	// 通常由 server 注入；nil 时退化为 context.Background。
	patternBgCtx context.Context

	// cachedDesigner / cachedParser 是 Replan 路径(ExecuteProjectWithReplan)需要的
	// 组件,通过 SetReplanComponents 注入。
	// 不进 PlanOrchestratorConfig 是为了不破坏现有 NewPlanOrchestrator 签名,
	// 不需要 replan 的 caller 可以不调 SetReplanComponents,Replan 路径会降级。
	cachedDesigner DesignGenerator
	cachedParser   RequirementParser

	// cachedPlan / cachedProgress 是 ExecuteProjectWithReplan 进入 for 循环时
	// 设置的 "当前正在跑" 引用,出循环(成功完成 / 失败 / 取消)时清除。
	// 提供给外部查询(chat ProgressView / dispatchUserInput.buildRelevanceContext)
	// 真实的 plan.SubTasks 状态 + ProjectProgress.ActiveRuns,而不是猜测。
	//
	// 并发安全:用 currentMu 保护读写,读侧用 CurrentSnapshot 拿快照避免长时持锁。
	cachedPlan     *TaskPlan
	cachedProgress *ProjectProgress
	currentMu      sync.RWMutex
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

	// PatternExtractor 可选（MACCS 5.4）。ExecuteProject 完成后异步从历史经验
	// 提取共性模式。需要同时配置 ExperienceStore 才有历史可用。
	PatternExtractor PatternExtractor
	// PatternBgCtx 用于异步抽取的顶层 ctx；nil 时使用 context.Background。
	PatternBgCtx context.Context
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

	patternBg := cfg.PatternBgCtx
	if patternBg == nil {
		patternBg = context.Background()
	}

	po := &PlanOrchestrator{
		Orchestrator:     orch,
		Memory:           cfg.Memory,
		Estimator:        NewComplexityEstimatorWithTransfer(cfg.Learner, transfer),
		BudgetPool:       NewDynamicBudgetPool(totalBudget),
		MetaCognitive:    NewMetaCognitiveEngine(cfg.Learner),
		ModelRouter:      cfg.ModelRouter,
		ProgressStore:    cfg.ProgressStore,
		TransferLearner:  transfer,
		ExperienceStore:  cfg.ExperienceStore,
		ContextEngine:    cfg.ContextEngine,
		MemoryRetriever:  cfg.MemoryRetriever,
		memoryRetrieveN:  retrieveN,
		PatternExtractor: cfg.PatternExtractor,
		patternBgCtx:     patternBg,
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
		// MACCS 1.10：把 ReviewLoop 注入回 Orchestrator，让 ExecuteTaskPlan
		// 在每个子任务成功后调 SubmitReview 拿审核报告写入 subTask.Result.Review。
		//
		// 用 setReviewLoop 走 engineMu 写锁,与 ExecuteTaskPlan 高频读侧并发安全。
		orch.setReviewLoop(po.ReviewLoop)

		// 接入 ContextEngine：替换底层 Orchestrator 的 contextEngine，使 Delegate
		// 阶段下游 brain prompt 自动带上项目记忆摘要（ContextEngineWithMemory.Assemble
		// 会前置 system 消息）。
		//
		// SetContextEngine 已加 engineMu 写锁,与 Delegate 读端并发安全。
		if cfg.ContextEngine != nil {
			orch.SetContextEngine(cfg.ContextEngine)
		}

		// 接入 ModelRouter：把所有显式配置同步到 LLMProxy.ModelForKind，
		// 使后续 Delegate 自动选用正确模型。Resolve 在 ExecuteProject 中按
		// SubTask.Kind 调用，把决策写入 diaglog 便于审计。
		//
		// SyncToLLMProxy 已改用 SetModelForKind 走 LLMProxy.modelMu 写锁。
		if cfg.ModelRouter != nil {
			cfg.ModelRouter.SyncToLLMProxy(orch.LLMProxy())
		}

		// MACCS 5.3：订阅 ActiveLearner 发布的 brain.feedback.requested 事件，
		// 把高不确定 brain 的反馈请求作为 lesson 存入 ProjectMemory，让下一轮
		// plan 通过 MemoryRetriever 检索到，主动避开易出问题的 brain。
		if orch.EventBus != nil && po.Memory != nil {
			go po.consumeFeedbackRequests(patternBg)
		}
	}

	return po
}

// consumeFeedbackRequests 长期订阅 brain.feedback.requested 事件，把每条
// active-learning 反馈请求作为 lesson 写入 ProjectMemory。退出条件：ctx.Done()。
func (po *PlanOrchestrator) consumeFeedbackRequests(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			diaglog.Error("plan_orchestrator", "feedback subscriber panic", "recover", fmt.Sprint(r))
		}
	}()
	if po.Orchestrator == nil || po.Orchestrator.EventBus == nil {
		return
	}
	ch, unsub := po.Orchestrator.EventBus.Subscribe(ctx, "")
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Type != "brain.feedback.requested" {
				continue
			}
			var payload struct {
				BrainKind string  `json:"brain_kind"`
				Reason    string  `json:"reason"`
				Question  string  `json:"question"`
				Uncertain float64 `json:"uncertainty"`
			}
			if err := json.Unmarshal(ev.Data, &payload); err != nil || payload.BrainKind == "" {
				continue
			}
			summary := fmt.Sprintf("brain=%s 不确定性=%.2f: %s", payload.BrainKind, payload.Uncertain, payload.Reason)
			_ = po.Memory.Store(ctx, MemoryEntry{
				ProjectID:  ev.ExecutionID,
				Type:       MemoryLesson,
				Content:    summary,
				Summary:    summary,
				Tags:       []string{"active_learning", payload.BrainKind},
				Importance: 0.5,
			})
			diaglog.Info("plan_orchestrator", "feedback request stored as lesson",
				"brain", payload.BrainKind, "reason", payload.Reason)
		}
	}
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

		// 将经验教训 + 推荐建议存入项目记忆，下一轮 plan 通过 MemoryRetriever
		// 检索到这些条目并写入 reflection.Recommendations，形成跨 plan 的反馈闭环。
		// 2026-05-01：阈值 0.5→0.3 降低进入门槛（避免低重要度 Lessons 丢失），
		// 同时把 Recommendations 也作为 lesson 存（importance 0.4 默认）。
		if po.Memory != nil && reflection != nil {
			for _, lesson := range reflection.Lessons {
				if lesson.Importance >= 0.3 {
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
			for _, rec := range reflection.Recommendations {
				if rec == "" {
					continue
				}
				// 跳过 MemoryRetriever 上一轮回填的 "[相似经验/...]" 前缀，
				// 避免无限放大同一条记忆。
				if strings.HasPrefix(rec, "[相似经验/") {
					continue
				}
				_ = po.Memory.Store(ctx, MemoryEntry{
					ProjectID:  plan.ProjectID,
					Type:       MemoryLesson,
					Content:    rec,
					Summary:    "recommendation: " + rec,
					Tags:       []string{"recommendation", "reflection"},
					Importance: 0.4,
				})
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

	// 6. MACCS 5.4：异步抽取项目模式（非阻塞）。要求 PatternExtractor + ExperienceStore
	// 同时配置；失败仅写日志不影响主流程。用 patternBgCtx 防止调用方 ctx 取消打断异步任务。
	if po.PatternExtractor != nil && po.ExperienceStore != nil {
		exp := buildProjectExperience(plan, planResult, reflection, time.Since(start))
		go po.runPatternExtraction(exp)
	}

	return &ProjectExecutionResult{
		PlanResult: planResult,
		Progress:   progress.Snapshot(),
		Reflection: reflection,
		Duration:   time.Since(start),
		ExecError:  execErr,
	}, nil
}

// runPatternExtraction 在后台 goroutine 中持久化当前经验 + 抽取共性模式。
// 任何 panic 都被 recover 兜底，避免拖崩主进程。
func (po *PlanOrchestrator) runPatternExtraction(exp *ProjectExperience) {
	defer func() {
		if r := recover(); r != nil {
			diaglog.Error("plan_orchestrator", "pattern extraction panic", "recover", fmt.Sprint(r))
		}
	}()

	ctx := po.patternBgCtx
	if ctx == nil {
		ctx = context.Background()
	}

	// 持久化当前经验，让下次抽取能用到本次结果。失败仅记录，不阻塞。
	if exp != nil {
		if err := po.ExperienceStore.Save(ctx, exp); err != nil {
			diaglog.Warn("plan_orchestrator", "pattern extraction: save experience failed",
				"project_id", exp.ProjectID, "err", err)
		}
	}

	exps, err := po.ExperienceStore.List(ctx)
	if err != nil {
		diaglog.Warn("plan_orchestrator", "pattern extraction: list experiences failed",
			"err", err)
		return
	}
	if len(exps) == 0 {
		diaglog.Info("plan_orchestrator", "pattern extraction: no experiences to extract from")
		return
	}

	// 转 ptr 切片为值切片以匹配 PatternExtractor.Extract 签名
	values := make([]ProjectExperience, 0, len(exps))
	for _, e := range exps {
		if e != nil {
			values = append(values, *e)
		}
	}

	patterns := po.PatternExtractor.Extract(values)
	if len(patterns) == 0 {
		diaglog.Info("plan_orchestrator", "pattern extraction: no patterns extracted",
			"experiences", len(values))
		return
	}

	// 把新模式加入 library + 把摘要写入项目记忆，供下次 reflection 检索
	memStoreFailed := 0
	for _, p := range patterns {
		po.PatternExtractor.AddPattern(p)
		if po.Memory != nil && exp != nil {
			if err := po.Memory.Store(ctx, MemoryEntry{
				ProjectID:  exp.ProjectID,
				Type:       MemoryLesson,
				Content:    fmt.Sprintf("[pattern/%s] %s — confidence=%.2f", p.Category, p.Description, p.Confidence),
				Summary:    p.Name,
				Tags:       []string{"pattern", p.Category},
				Importance: p.Confidence,
			}); err != nil {
				memStoreFailed++
			}
		}
	}
	if memStoreFailed > 0 {
		diaglog.Warn("plan_orchestrator", "pattern extraction: some memory stores failed",
			"failed", memStoreFailed, "total_patterns", len(patterns))
	}

	diaglog.Info("plan_orchestrator", "pattern extraction done",
		"experiences", len(values),
		"new_patterns", len(patterns),
	)
}

// buildProjectExperience 把单次 ExecuteProject 的结果转为可持久化的 ProjectExperience。
// 字段语义参考 transfer_learning.go::ProjectExperience。
func buildProjectExperience(plan *TaskPlan, result *TaskPlanResult, reflection *ReflectionReport, dur time.Duration) *ProjectExperience {
	if plan == nil {
		return nil
	}
	successRate := 0.0
	taskCount := len(plan.SubTasks)
	if result != nil && taskCount > 0 {
		successRate = float64(result.CompletedTasks) / float64(taskCount)
	}
	brainUsage := make(map[string]float64, 4)
	for _, st := range plan.SubTasks {
		if st.Kind != "" {
			brainUsage[string(st.Kind)]++
		}
	}
	if taskCount > 0 {
		for k := range brainUsage {
			brainUsage[k] /= float64(taskCount)
		}
	}
	tags := make([]string, 0, 4)
	// 用 SubTask.Domain（首个非空）作为粗粒度 Category 输入。Pattern 的 architecture
	// 类抽取需要 Category 非空才能分桶（pattern_extraction.go:118）。
	category := ""
	for _, st := range plan.SubTasks {
		if st.Domain != "" {
			category = st.Domain
			break
		}
	}
	if reflection != nil && len(reflection.Lessons) > 0 {
		tags = append(tags, "has_lessons")
	}
	for k := range brainUsage {
		tags = append(tags, "uses:"+k)
	}
	return &ProjectExperience{
		ExperienceID: fmt.Sprintf("exp-%s-%d", plan.ProjectID, time.Now().UnixMilli()),
		ProjectID:    plan.ProjectID,
		Category:     category,
		Tags:         tags,
		TaskCount:    taskCount,
		SuccessRate:  successRate,
		BrainUsage:   brainUsage,
		Duration:     dur,
		CreatedAt:    time.Now(),
	}
}

// ProjectExecutionResult 项目执行最终结果。
type ProjectExecutionResult struct {
	PlanResult *TaskPlanResult   `json:"plan_result"`
	Progress   ProjectProgress   `json:"progress"`
	Reflection *ReflectionReport `json:"reflection,omitempty"`
	Duration   time.Duration     `json:"duration"`
	ExecError  error             `json:"-"`
}
