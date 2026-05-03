// plan_runner.go — 项目级需求(IntentProject)的执行入口。
//
// 把 chat/slash_plan.go::handlePlanCreate 的核心流程抽成 PlanRunner,
// 三模式(chat/run/serve)在 IntentClassifier 判定项目级时统一调用此 PlanRunner.Execute。
//
// 流程:
//   1. RequirementParser.Parse(input) → ProjectSpec
//   2. DesignGenerator.Generate(spec) → DesignProposal
//   3. designer.ToTaskPlan(proposal) → TaskPlan
//   4. PlanOrchestrator.ExecuteProject(plan) → ProjectExecutionResult
//      内含 ReviewLoop / Reflection / Lessons / FeedbackRequest 等 MACCS 反馈循环。
//
// 解析失败时降级到 fallbackPlan(单 central 子任务,等价于直接 Runner.Execute)。

package agentpipe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/loop"
)

// ErrPlanFallback 是 ExecuteWithInput 在 Parser/Designer 失败时返回的 sentinel,
// 提示调用方"项目级解析失败,应该改走 Invocation 走 simple 路径"。这样可以
// 避免 fallbackPlan 单 task 仍跑七阶段闭环(ReviewLoop/Reflection)的浪费。
//
// 三模式调用方应 errors.Is(err, ErrPlanFallback) 检测后改走 Invocation。
var ErrPlanFallback = errors.New("agentpipe: plan parse failed, fallback to simple invocation")

// PlanRunner 把用户的项目级需求 → TaskPlan → ExecuteProject 的全链路打包。
//
// 三模式各自维护一个 PlanRunner 实例(每个 chat session / 每个 run / serve mgr 一个),
// 复用 Orchestrator + Memory + Learner 等长期组件。
type PlanRunner struct {
	Orchestrator *kernel.Orchestrator

	// 长期组件(由调用方注入或 NewPlanRunner 自动创建)
	Memory          kernel.ProjectMemory
	ProgressStore   kernel.ProgressStore
	Parser          kernel.RequirementParser
	Designer        kernel.DesignGenerator
	MemoryRetriever *kernel.MemoryRetriever
	ModelRouter     *kernel.ModelRouter
	ContextEngine   *kernel.ContextEngineWithMemory
	ExperienceStore kernel.ExperienceStore
	PatternExtract  kernel.PatternExtractor

	// 实际编排器(懒构造)
	planOrch     *kernel.PlanOrchestrator
	planOrchOnce sync.Once

	// TotalBudget 是 PlanOrchestrator 的总 turn 预算,默认 200。
	TotalBudget int

	// MemoryRetrieveLimit 检索时返回的记忆条数,默认 5。
	MemoryRetrieveLimit int

	// bgCtx + bgCancel 给 PlanOrchestrator 的 consumeFeedbackRequests goroutine
	// 用,Close() 时 cancel,避免 run/serve 每个 run 漏一个永不退出的 goroutine。
	bgCtx    context.Context
	bgCancel context.CancelFunc
}

// NewPlanRunner 用 Orchestrator 构造 PlanRunner,长期组件如未传则用 in-memory 默认实现。
// 推荐调用者持有 PlanRunner 实例长生命周期,避免每次重新构造 ProgressStore / PatternExtractor。
//
// ProjectMemory 复用策略:
//   - 如果 orch 当前 ContextEngine 是 *ContextEngineWithMemory(chat 项目模式),
//     复用其内部 Memory(可能是 PersistentProjectMemory),保证 plan 路径写
//     的 lessons / patterns 能持久化到 SQLite,session 间不丢。
//   - 否则用 NewMemProjectMemory()(run/serve simple 场景默认行为)。
//
// 注意:用完必须调 Close() 释放后台 goroutine(consumeFeedbackRequests),
// 否则 run/serve 每个 run 都会漏一个永不退出的订阅 goroutine。
func NewPlanRunner(orch *kernel.Orchestrator) *PlanRunner {
	bgCtx, bgCancel := context.WithCancel(context.Background())

	// 复用 orch 已配置的 Memory(避免覆盖 chat 注入的 PersistentProjectMemory)
	var memory kernel.ProjectMemory
	if orch != nil {
		if cew, ok := orch.ContextEngine().(*kernel.ContextEngineWithMemory); ok {
			if m := cew.Memory(); m != nil {
				memory = m
			}
		}
	}
	if memory == nil {
		memory = kernel.NewMemProjectMemory()
	}

	pr := &PlanRunner{
		Orchestrator:        orch,
		Memory:              memory,
		ProgressStore:       kernel.NewMemoryProgressStore(),
		Parser:              kernel.NewDefaultRequirementParser(),
		Designer:            kernel.NewDefaultDesignGenerator(),
		MemoryRetriever:     kernel.NewMemoryRetriever(),
		ExperienceStore:     kernel.NewMemExperienceStore(),
		PatternExtract:      kernel.NewPatternExtractor(),
		TotalBudget:         200,
		MemoryRetrieveLimit: 5,
		bgCtx:               bgCtx,
		bgCancel:            bgCancel,
	}

	// ModelRouter 静态策略 + 复用 Orchestrator learner
	router := kernel.NewModelRouter(kernel.StrategyStatic)
	if orch != nil {
		if learner := orch.Learner(); learner != nil {
			router.SetLearner(learner)
		}
	}
	pr.ModelRouter = router

	// ContextEngine:复用 Orchestrator 已配置的(含 LLM Summarizer + SharedStore),
	// 包一层 ProjectMemory。chat 模式 project_apply.go 启动时已经把 orch 的
	// ContextEngine 换成 *ContextEngineWithMemory,这里要先解包再重包,否则
	// type assert 失败 → 退化到空 DefaultContextEngine,丢 Summarizer/SharedStore,
	// Compress 退化为字符串截断,SharedMessages 不再持久化。
	var baseCtx *kernel.DefaultContextEngine
	if orch != nil {
		switch ce := orch.ContextEngine().(type) {
		case *kernel.DefaultContextEngine:
			baseCtx = ce
		case *kernel.ContextEngineWithMemory:
			// 已经是 wrapper(chat 项目模式),取出内层 DefaultContextEngine 复用,
			// 然后下面会重包一层带 pr.Memory 的 wrapper。
			baseCtx = ce.Engine()
		}
	}
	if baseCtx == nil {
		baseCtx = kernel.NewDefaultContextEngine()
	}
	pr.ContextEngine = kernel.NewContextEngineWithMemory(baseCtx, pr.Memory)

	return pr
}

// PlanInput 是 Execute 的扩展入参,允许调用方注入额外 system 约束。
//
// 设计动机:chat PLAN/RESTRICTED mode 下用户输入项目级需求,host 侧的权限
// 上下文(BuildSystemPrompt 输出)必须贯穿到下游 sidecar,否则 PlanOrchestrator
// 派发的 code/browser 子任务完全不知道"只读模式",会击穿权限边界直接调
// fs_write/shell。修复:把权限约束作为 ExtraInstruction 注入到每个 SubTask
// 的 instruction 前缀,sidecar 看到后会在 system prompt 之前优先遵守。
type PlanInput struct {
	// ProjectID 项目 ID(空时自动生成)。
	ProjectID string
	// Goal 项目级需求文本。
	Goal string
	// ExtraInstruction 是额外的指令前缀,会被注入到每个 SubTask 的 Instruction
	// 之前,确保下游 sidecar 在执行任何动作之前先读到权限/上下文约束。
	// 典型内容:chat 的 BuildSystemPrompt(mode, sandbox) 输出。
	ExtraInstruction string
}

// Execute 用项目级需求 input 跑全流程 (parse → design → plan → execute)。
//
// projectID 可选 — 空时自动生成。建议传项目持久化层的 ID,让 ProjectMemory
// 跨 session 复用同项目的 lessons / patterns。
//
// 返回 ProjectExecutionResult 包含:
//   - Plan: 生成的 TaskPlan
//   - Progress: 执行过程中的进度(各 task 状态 / 整体百分比 / 阶段)
//   - PlanResult: TaskPlanResult,各 SubTask 的执行结果
//   - Reflection: 复盘报告(包含 lessons / recommendations,会被写回 Memory)
//   - ExecError: 执行错误(任意阶段失败)
func (p *PlanRunner) Execute(ctx context.Context, projectID, input string) (*kernel.ProjectExecutionResult, error) {
	return p.ExecuteWithInput(ctx, PlanInput{ProjectID: projectID, Goal: input})
}

// ExecuteWithInput 是 Execute 的扩展版本,支持 ExtraInstruction 注入。
func (p *PlanRunner) ExecuteWithInput(ctx context.Context, in PlanInput) (*kernel.ProjectExecutionResult, error) {
	if p == nil || p.Orchestrator == nil {
		return nil, fmt.Errorf("agentpipe: PlanRunner.Orchestrator is nil — cannot execute")
	}
	projectID := in.ProjectID
	if projectID == "" {
		projectID = fmt.Sprintf("proj-%d", time.Now().UnixNano())
	}

	if err := p.ensurePlanOrch(); err != nil {
		return nil, err
	}

	plan, err := p.buildPlan(ctx, projectID, in.Goal)
	if err != nil {
		// ErrPlanFallback 直接透传(不 wrap),让调用方 errors.Is 检测后
		// 退化到 Invocation 简单路径。其他错误才 wrap。
		if errors.Is(err, ErrPlanFallback) {
			return nil, err
		}
		return nil, fmt.Errorf("agentpipe: build plan: %w", err)
	}

	// 把 ExtraInstruction 前缀注入到每个 SubTask.Instruction,确保下游 sidecar
	// 在 brain/execute 层就看到权限约束。
	// 双语分隔符照顾英文 sidecar — 中文 LLM 也能识别 "USER TASK" 不必加纯中文版。
	if extra := in.ExtraInstruction; extra != "" {
		for i := range plan.SubTasks {
			plan.SubTasks[i].Instruction = extra + "\n\n--- USER TASK / 用户任务 ---\n" + plan.SubTasks[i].Instruction
		}
	}

	return p.planOrch.ExecuteProject(ctx, plan)
}

// ensurePlanOrch 懒构造 PlanOrchestrator(可重复调,只构造一次)。
//
// 用 sync.Once 避免 chat 多 turn 并发触发 IntentProject 时双重构造 race
// (两个 goroutine 同时见到 planOrch==nil → 各自 NewPlanOrchestrator → 各起
// 一个 consumeFeedbackRequests goroutine + race detector fatal)。
//
// PatternBgCtx 用 PlanRunner 的 bgCtx,Close() 时 cancel,
// PlanOrchestrator.consumeFeedbackRequests goroutine 会随之退出,
// 避免 run/serve 每 run 漏一个 EventBus 订阅 goroutine。
func (p *PlanRunner) ensurePlanOrch() error {
	if p.Orchestrator == nil {
		return fmt.Errorf("Orchestrator is nil")
	}
	p.planOrchOnce.Do(func() {
		p.planOrch = kernel.NewPlanOrchestrator(p.Orchestrator, kernel.PlanOrchestratorConfig{
			Memory:              p.Memory,
			Learner:             p.Orchestrator.Learner(),
			TotalBudget:         p.TotalBudget,
			ProgressStore:       p.ProgressStore,
			ModelRouter:         p.ModelRouter,
			ContextEngine:       p.ContextEngine,
			MemoryRetriever:     p.MemoryRetriever,
			MemoryRetrieveLimit: p.MemoryRetrieveLimit,
			ExperienceStore:     p.ExperienceStore,
			PatternExtractor:    p.PatternExtract,
			PatternBgCtx:        p.bgCtx,
		})
	})
	return nil
}

// Close 释放 PlanRunner 持有的后台资源。
//
// run/serve 每个 IntentProject run 都会 NewPlanRunner,Execute 完后必须
// Close,否则 PlanOrchestrator 的 consumeFeedbackRequests goroutine 永不退出。
// chat 模式 PlanRunner 跟 session 同生命周期,session 退出时调一次。
//
// 多次调用安全(bgCancel 是幂等的)。
func (p *PlanRunner) Close() {
	if p == nil || p.bgCancel == nil {
		return
	}
	p.bgCancel()
}

// buildPlan 把自然语言 input 转成 TaskPlan。
//
// 失败语义改造(2026-05-03):Parser/Designer 失败不再造单 task fallbackPlan
// 走七阶段闭环(浪费 ReviewLoop/Reflection),而是返回 ErrPlanFallback 让
// 调用方退化到 Invocation 简单路径。
func (p *PlanRunner) buildPlan(ctx context.Context, projectID, input string) (*kernel.TaskPlan, error) {
	spec, err := p.Parser.Parse(ctx, input)
	if err != nil || spec == nil {
		return nil, ErrPlanFallback
	}
	proposal, err := p.Designer.Generate(ctx, spec)
	if err != nil || proposal == nil {
		return nil, ErrPlanFallback
	}
	plan := p.Designer.ToTaskPlan(proposal)
	if plan == nil || len(plan.SubTasks) == 0 {
		return nil, ErrPlanFallback
	}
	if projectID != "" {
		plan.ProjectID = projectID
	}
	return plan, nil
}

// PlanOrch 暴露内部 PlanOrchestrator(用于已有 chat /plan slash 命令兼容)。
func (p *PlanRunner) PlanOrch() *kernel.PlanOrchestrator {
	_ = p.ensurePlanOrch()
	return p.planOrch
}

// AggregatePlanBudget 把 ProjectExecutionResult 各 SubTask 的 Usage 累加成
// loop.Budget,供 chat / run / serve 项目级路径构造 RunResult.Run.Budget,
// 避免持久化层写 turns=0/cost=0 的伪数据污染 Dashboard 和 L2 学习。
//
// Notes:
//   - SubtaskUsage 只含 Turns / CostUSD / Duration,没有独立的 LLMCalls / ToolCalls。
//   - **不**把 Turns 当 LLMCalls 写脏数据 — 项目级路径下每个 SubTask 是一次完整
//     委派(包含多次 LLM 调用 + 工具调用),host 拿不到细粒度计数。
//     UsedLLMCalls / UsedToolCalls 留 0 比假数值更诚实,Dashboard 应当读
//     UsedTurns + UsedCostUSD 即可,LLMCalls=0 表示 "细粒度未知",不是 "未发生"。
//   - projResult 为 nil 时返回 zero Budget(已记录 ExecError 的失败路径)。
func AggregatePlanBudget(projResult *kernel.ProjectExecutionResult) loop.Budget {
	if projResult == nil {
		return loop.Budget{}
	}
	var b loop.Budget
	if projResult.PlanResult != nil {
		for _, dr := range projResult.PlanResult.Results {
			if dr == nil {
				continue
			}
			b.UsedTurns += dr.Usage.Turns
			b.UsedCostUSD += dr.Usage.CostUSD
			// UsedLLMCalls / UsedToolCalls 故意留 0 — 见函数文档说明。
		}
	}
	b.ElapsedTime = projResult.Duration
	return b
}
