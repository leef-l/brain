# MACCS 实施进度追踪

> **版本**: v2.4.0（全量完成）
> **启动日期**: 2026-04-29
> **完成日期**: 2026-05-02（Wave 7 4.3/4.4 接入合并后）
> **最终接入率**: **48/48 = 100%**
> **编译验证**: `go build ./...` 通过
> **铁律**: 禁止 `go test` / `go vet`，只用 `go build ./...`

---

## ⚠️ 状态图例

| 标记 | 含义 |
|------|------|
| ✅ **已落地** | 代码完整 + 接入主线被实际调用 + 算法真实可执行 |
| 🟡 **算法实在但未接入** | 代码逻辑实在、算法可用，但 `cmd/brain` / orchestrator 中**零调用方**，等于孤岛 |
| 🟠 **半成品** | 代码框架在，但关键路径有"模拟"字符串 / 协议无路由 / 接口签名不互通 |
| 🔴 **伪实现** | 默认全 true / map 查找当真实检查 / 空 Inject — 接口完整但内部空跑 |

---

## Wave 0: 止血与基线修复

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 0.1 | task_complete 终止 run | `sdk/loop/runner.go` | ✅ 已落地 | runner 主循环 tool dispatch 后检测 task_complete |
| 0.2 | LLM 超时 90s → 180s | `sdk/llm/anthropic_provider.go` | ✅ 已落地 | newDefaultHTTPClient() 三项 |
| 0.3 | serve 默认 turn 20 → 50 | `cmd/brain/cmd_serve.go` | ✅ 已落地 | req.MaxTurns = 50 |

**Wave 0 真实接入率：3/3 = 100%**

---

## Wave 1: 核心编排升级

### Batch 1 — 数据结构

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 1.1 | TaskPlan | `sdk/kernel/task_plan.go` | ✅ 已落地 | cmd_serve_plans + ClosedLoopController + PlanOrchestrator 三处真用 |
| 1.2 | ProjectProgress | `sdk/kernel/project_progress.go` | ✅ 已落地 | PlanOrchestrator 创建 + ProgressStore 持久化 + GET /v1/plans/{id} 查询 |
| 1.3 | InterruptSignal | `sdk/kernel/interrupt.go` | ✅ 已落地 | cmd_serve_interrupt.go 路由 + Runner 注入 |

### Batch 2 — 基础设施接入

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 1.4 | Runner 中断检查 | `sdk/loop/interrupt.go` | ✅ 已落地 | cmd_serve_interrupt 注入 + adapter 把 kernel 转 loop |
| 1.5 | Checkpoint 增强 | `sdk/loop/checkpoint.go` | ✅ 已落地 | PlanID/CurrentTaskID/ProjectID 三字段 |
| 1.6 | 进度汇报 RPC | `sdk/protocol/methods.go` + `sdk/kernel/progress_rpc.go` | ✅ 已落地 | orchestrator.go:1299/1308 在 registerReverseHandlers 注册 MethodProgressReport + MethodProgressQuery |
| 1.7 | 进度持久化 | `sdk/kernel/progress_store.go` | ✅ 已落地 | MemoryProgressStore 接入 PlanOrchestrator + chat /plan |

### Batch 3 — 编排集成

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 1.8 | Orchestrator 集成 TaskPlan | `sdk/kernel/orchestrator_plan.go` | ✅ 已落地 | ExecuteTaskPlan 由 PlanOrchestrator + ClosedLoopController 双路径调用 |
| 1.9 | 动态预算池 | `sdk/kernel/dynamic_budget.go` | ✅ 已落地 | PlanOrchestrator 内部 BudgetPool 真实分配/回收 |
| 1.10 | ReviewLoop 审核闭环 | `sdk/kernel/review_loop.go` | ✅ 已落地（0139b5e 任务级接入） | 双路径接入：(a) PlanOrchestrator.ReviewLoop 在 reflection 后跑 ExecuteWithReview；(b) Orchestrator.reviewLoop + WithReviewLoop option，ExecuteTaskPlan 每个子任务完成后调 SubmitReview，写 subTask.Result.Review（不重做避免重复 delegate） |

**Wave 1 真实接入率：10/10 = 100%**

---

## Wave 2: 中央大脑智能化

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 2.1 | 项目级记忆存储 | `sdk/kernel/project_memory.go` | ✅ 已落地 | MemProjectMemory 由 cmd_serve_plans + chat/slash_plan 注入 PlanOrchestrator |
| 2.2 | 记忆检索 | `sdk/kernel/memory_retrieval.go` | ✅ 已落地 | NewMemoryRetriever 注入 PlanOrchestrator.MemoryRetriever，ExecuteProject reflection 后调 Retrieve top-N |
| 2.3 | 复杂度预估器 | `sdk/kernel/complexity_estimator.go` | ✅ 已落地 | NewComplexityEstimatorWithTransfer 在 PlanOrchestrator 构造时真用 |
| 2.4 | 元认知反思引擎 | `sdk/kernel/meta_cognitive.go` | ✅ 已落地（0139b5e Lessons 反馈闭环） | PlanOrchestrator reflection 阶段真实调用；**lesson 阈值 0.5→0.3 降低进入门槛**，新增 Recommendations 也作为 lesson 存（importance 0.4），跳过 `[相似经验/` 前缀避免无限放大同条记忆——形成跨 plan 元认知闭环 |
| 2.5 | Context Engine 增强 | `sdk/kernel/context_engine_memory.go` | ✅ 已落地 | NewContextEngineWithMemory 包装注入 PlanOrchestrator → SetContextEngine 落到 Orchestrator |
| 2.6 | 多模型路由 | `sdk/kernel/model_router.go` | ✅ 已落地 | NewModelRouter(StrategyStatic) 注入 PlanOrchestrator，ExecuteProject 内 ModelRouter.Resolve 写 diaglog；NewPlanOrchestrator 调 SyncToLLMProxy |
| 2.7 | Orchestrator 智能化 | `sdk/kernel/plan_orchestrator.go` | ✅ 已落地 | cmd_serve_plans + chat/slash_plan + cmd_serve_projects 三处入口 |

**Wave 2 真实接入率：7/7 = 100%**

---

## Wave 3: EasyMVP 闭环工作流

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 3.1 | 项目 Session 管理 | `sdk/kernel/project_session.go` | ✅ 已落地 | ClosedLoopController.Execute 真实创建 Session + 7 阶段记录，cmd_serve_projects 路由暴露 |
| 3.2 | 需求解析器 | `sdk/kernel/requirement_parser.go` | ✅ 已落地 | cmd_serve_plans + ClosedLoopController 真用 |
| 3.3 | 方案设计接口 | `sdk/kernel/design_api.go` | ✅ 已落地 | DefaultDesignGenerator 在 cmd_serve_plans + ClosedLoopController 真用 |
| 3.4 | 项目状态机 | `sdk/kernel/project_state_machine.go` | ✅ 已落地 | cmd_serve_projects 经 ClosedLoopController 真实推进 7 阶段 |
| 3.5 | 方案审核循环 | `sdk/kernel/design_review.go` | ✅ 已落地 | DesignReviewLoop 由 cmd_serve_projects 注入 ClosedLoopController.deps |
| 3.6 | 执行调度器 | `sdk/kernel/execution_scheduler.go` | ✅ 已落地（d6619ce 修复） | NewExecutionSchedulerWithOrchestrator + RunPlan 调 DelegateBatch；不再是只标 MarkRunning 的伪调度 |
| 3.7 | 验收测试层 | `sdk/kernel/acceptance_tester.go` | ✅ 已落地（d6619ce 修复） | RunTests 用 exec.CommandContext("sh","-c", test.Command) 真跑 shell；ClosedLoopController Phase 5 接入 |
| 3.8 | 交付生成器 | `sdk/kernel/delivery_generator.go` | ✅ 已落地 | ClosedLoopController Phase 6 真实调用生成 README/CHANGELOG |
| 3.9 | 复盘引擎 | `sdk/kernel/retrospective.go` | ✅ 已落地 | ClosedLoopController Phase 7 真实调用 + cmd_serve_projects 暴露报告 |
| 3.10 | 闭环控制器 | `sdk/kernel/closed_loop_controller.go` | ✅ 已落地（d6619ce 修复 + 本轮接入） | Phase 4/5 已删"模拟"，调真实 ExecuteTaskPlan + AcceptanceTester；cmd_serve_projects.go 注册 POST /v1/projects + GET /v1/projects/{id} |

**Wave 3 真实接入率：10/10 = 100%**

---

## Wave 4: 并发控制与冲突仲裁

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 4.1 | 资源访问追踪 | `sdk/kernel/resource_tracker.go` | ✅ 已落地（d6619ce 收敛） | 文件已删除，统一收敛到 `lease.go::MemLeaseManager` |
| 4.2 | 冲突检测器 | `sdk/kernel/conflict_detector.go` | ✅ 已落地（2026-05-01 接入） | seq 改 atomic.Int64.Add 防并发竞争；ExecutionScheduler.AttachConflictControl 注入；RunPlan 派发前 Detect 检查 blocker 冲突；cmd_serve_projects.go 按 maccs.conflict.* 启用 |
| 4.3 | 死锁检测器 | `sdk/kernel/deadlock_detector.go` | ✅ 已落地（Wave 7 接入） | DFS 环检测 + Wait-For Graph；ExecutionScheduler.AttachDeadlockControl 注入；RunPlan 在 ConflictDetector 报 blocker 后调 resolveDeadlocksFromConflicts，把 blocker.TaskIDs 按字典序翻译为 (waiter→holder, ResourcePath) 边写入 DeadlockDetector，Detect() 检环，每批结束时 RemoveTask 清理。绕开"LeaseManager 无持锁等待"前置不足问题：从 ConflictDetector 的语义层面构造 wait-for 边 |
| 4.4 | 仲裁策略 | `sdk/kernel/arbiter.go` | ✅ 已落地（Wave 7 接入） | DefaultArbiter 5 种策略；ResolveDeadlock 选 victim 算法（Critical > 已开始 > Priority 数值小）；ExecutionScheduler 命中环时调 ResolveDeadlock(cycle, priorities)，priorities 由 buildBatchPriorities 从 ScheduledTask 派生（StartedAt!=nil → Critical, EstimatedTurns 越短优先级越高）；victim 强制 RetryCount=RetryLimit+1 → MarkFailed("deadlock-victim") 不重试以打破环 |
| 4.5 | 智能重排调度 | `sdk/kernel/smart_scheduler.go` | ✅ 已落地（2026-05-01 接入） | NewSmartScheduler 在 ExecutionScheduler.AttachConflictControl 注入；BuildExecutionPlan 完成后调 Reschedule 做冲突感知重排（同层资源冲突挤到下一层），dryRun=true 时仅 diaglog 警告（生产观察期），可通过 maccs.conflict.dry_run=false 切到强制重排 |

**Wave 4 真实接入率：5/5 = 100%** ✅（4.3/4.4 经 Wave 7 接入完成）

---

## Wave 5: 学习系统进化

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 5.1 | 因果学习引擎 | `sdk/kernel/causal_learning.go` | ✅ 已落地（d6619ce 接入 + 0139b5e 权重升级） | learning.go RecordSequence 时 causal.Observe + LearnRelations；orchestrator.go resolveTargetKind 加权 **0.4 / 0.25 / 0.35**（capScore/learnScore/causalScore）— 因果权重 0.2→0.35 让因果信号在评分接近时主导路由 |
| 5.2 | 迁移学习引擎 | `sdk/kernel/transfer_learning.go` | ✅ 已落地（d6619ce 接入） | DefaultTransferLearner 在 PlanOrchestrator 构造时注入 ComplexityEstimator |
| 5.3 | 主动学习引擎 | `sdk/kernel/active_learning.go` | ✅ 已落地（d6619ce 接入 + 0139b5e 反馈订阅） | orchestrator.go exploreCandidate 5% epsilon-greedy；assessActiveLearning 调 AssessUncertainty 后发 EventBus `brain.feedback.requested`；plan_orchestrator.go consumeFeedbackRequests goroutine 长期订阅，把每条反馈请求作为 lesson 写入 ProjectMemory，下轮 plan 经 MemoryRetriever 读到形成跨 plan 闭环 |
| 5.4 | 项目模式提取 | `sdk/kernel/pattern_extraction.go` | ✅ 已落地（2026-05-01 接入 + 0139b5e 可观测） | NewPatternExtractor 注入 PlanOrchestrator；ExecuteProject 完成后异步 goroutine 跑 buildProjectExperience → ExperienceStore.Save → Extract → AddPattern → ProjectMemory.Store；用 patternBgCtx 防调用 ctx 取消打断；recover 兜底 panic；**Save / List / Memory.Store 失败均打 Warn 日志带 project_id / err，覆盖原 silent failure** |
| 5.5 | 自适应 Prompt | `sdk/kernel/adaptive_prompt.go` | ✅ 已落地（2026-05-01 接入） | NewAdaptivePromptManager 注入 LLMProxy.PromptManager；adaptiveSystemPrefix helper 在 Complete/handleComplete/handleStream 三入口把 SelectVariant 的变体作为 L1 system block 前置（cache=true），不破坏调用方原 System 列表 |
| 5.6 | 能力画像 | `sdk/kernel/capability_profile.go` | ✅ 已落地（d6619ce 接入） | learning.go RecommendOrder 通过 SequenceLearner + orchestrator_plan.go:108 在 ExecuteTaskPlan layer 内重排同层任务（不破坏拓扑） |

**Wave 5 真实接入率：6/6 = 100%** ✅

---

## Wave 6: 生产级硬化

### Batch 1 — 基础设施

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.1 | 健康检查框架 | `sdk/kernel/health_check.go` | ✅ 已落地（2026-05-01 接入） | NewHealthManager + brainPool/leaseManager checker；GET /v1/health；config 可关 maccs.health.enabled=false |
| 6.2 | 混沌注入框架 | `sdk/kernel/chaos_engine.go` | ✅ 已落地 | DelayInjector + ErrorInjector 真实拦截；orchestrator.go:847 delegateOnce 内 `chaos.IsEnabled()` 守卫；cmd_serve_chaos.go + /v1/chaos/experiments(POST/DELETE) + /v1/chaos/history 三路由 |
| 6.3 | 性能基准框架 | `sdk/kernel/perf_benchmark.go` | ✅ 已落地（2026-05-01 接入） | NewPerfCollector + WithPerfCollector 注入 Orchestrator.delegateOnce 计时（按 brain.kind/status 分桶 P50/P95/P99）；GET /v1/metrics/perf |
| 6.4 | 可观测性框架 | `sdk/kernel/observability.go` | ✅ 已落地（2026-05-01 接入） | NewObservabilityHub + WithObservability + 内存 provider；delegateOnce 包 TraceSpan（trace_id 优先 req.TraceID 回退 task_id，tags=kind/task_id）；GET /v1/observability + ?trace_id 过滤 |

### Batch 2 — 安全与并发

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.5 | 安全审计框架 | `sdk/kernel/security_audit.go` | ✅ 已落地（2026-05-01 接入） | NewSecurityAuditor 注入 projectService；POST /v1/projects 入参 ValidateInput；阈值可配 maccs.security.reject_severity（critical/high/medium/low） |
| 6.6 | 多项目并发管理 | `sdk/kernel/multi_project.go` | ✅ 已落地（2026-05-01 接入） | NewMultiProjectManager(MaxConcurrent=3, QueueSize=16) 注入 projectService；handleCreateProject 进入即 Submit 拿槽位，结束 Complete/Fail；超额返回 429 |
| 6.7 | 生产就绪检查 | `sdk/kernel/production_readiness.go` | ✅ 已落地（d6619ce 修复 + 本轮接入） | 7 项 check 真实探测（BrainPool.AvailableKinds、LLMProxy 等）；cmd_serve.go:865 NewReadinessCheckerWithConfig + 启动期 RunAll + BRAIN_STRICT_READINESS 守卫 + GET /v1/readiness 暴露报告 |

**Wave 6 真实接入率：7/7 = 100%** ✅

---

## 总览表

| Wave | 任务数 | ✅ 已落地 | 🟠 半成品 | 🔴 伪实现 |
|------|-------|----------|----------|---------|
| 0 | 3 | 3 | 0 | 0 |
| 1 | 10 | 10 | 0 | 0 |
| 2 | 7 | 7 | 0 | 0 |
| 3 | 10 | 10 | 0 | 0 |
| 4 | 5 | 5 | 0 | 0 |
| 5 | 6 | 6 | 0 | 0 |
| 6 | 7 | 7 | 0 | 0 |
| **合计** | **48** | **48 (100%)** | **0** | **0** |

> **2026-05-02 全量完成里程碑**：MACCS v2 全 48 项任务接入完成 ✅
>
> 0139b5e — 5 项审计差距全部修复:
> 1. **1.10 ReviewLoop 任务级接入** — Orchestrator.reviewLoop + WithReviewLoop option，每个子任务完成后调 SubmitReview 写入 subTask.Result.Review（不重做避免重复 delegate）
> 2. **5.1 因果权重 0.2 → 0.35** — combined 公式 0.5/0.3/0.2 → 0.4/0.25/0.35，因果信号在评分接近时主导路由
> 3. **2.4 MetaCognitive Lessons 反馈下一轮 plan** — 阈值 0.5→0.3、Recommendations 作 lesson 存（importance 0.4）、跳过 `[相似经验/` 前缀防无限放大
> 4. **5.3 ActiveLearning brain.feedback.requested 订阅** — consumeFeedbackRequests goroutine 长期订阅 EventBus，把高不确定 brain 反馈作 lesson 写入 ProjectMemory，下轮 plan 经 MemoryRetriever 读到形成跨 plan 闭环
> 5. **5.4 PatternExtractor 失败可观测** — Save / List / Memory.Store 失败现在都打 Warn 带 project_id / err，覆盖原 silent failure
>
> Wave 7 — 4.3 / 4.4 接入完成（绕开 LeaseManager 持锁等待前置不足问题）:
> - **4.3 DeadlockDetector** — ExecutionScheduler.AttachDeadlockControl 注入；RunPlan 把 ConflictDetector 报告的 blocker 翻译为 wait-for 边写入 DeadlockDetector.AddWaitEdge，Detect() 检环，每批结束 RemoveTask 清理
> - **4.4 Arbiter ResolveDeadlock** — 命中环时调 DefaultArbiter.ResolveDeadlock(cycle, priorities) 选 victim；priorities 由 buildBatchPriorities 派生（StartedAt!=nil → Critical, EstimatedTurns 短 → 优先级高）；victim RetryCount 推到 RetryLimit+1 → MarkFailed 不重试打破环
> - 配置：`maccs.deadlock.enabled`（默认 true）+ `maccs.deadlock.dry_run`（默认 true，首周观察期；切 false 启用强制中止）

---

## 后续工作（建议性，非必须）

### P3 — 重复造轮收敛

- 调度器：~~三套并存可合并~~ **决策：已自然收敛，不需重构**（2026-05-04 复审）
  - `DefaultTaskScheduler` (scheduler.go) — 任务级 Plan + L1 SelectBrain（学习+优先级），是任务级入口
  - `ExecutionScheduler` (execution_scheduler.go) — 项目级 plan 落地框架，已是「框架」：
    - 持有 `smartScheduler *SmartScheduler` 作为可选注入（line 117）
    - `AttachConflictControl(detector, smart, ...)` 把 SmartScheduler 作为策略注入（line 165）
    - `AttachDeadlockControl(detector, arbiter, ...)` 是 Wave 7 死锁检测钩子（line 183）
  - `SmartScheduler` (smart_scheduler.go) — 拓扑层之上的冲突感知重排策略，文件头已显式自述「可选辅助/增强组件，不是第三套调度器」
  - 三者职责正交（任务级路由 / 项目级落地 / 冲突重排），不是重复造。强行合并会破坏 Wave 7 死锁检测的接入路径
- 审核循环：~~`review_loop.go::ReviewLoop` + `design_review.go::DesignReviewLoop` 收编~~ **决策：不收编**（2026-05-04 复审）
  - 输入对象本质不同：`DelegateResult/Artifact`（运行时产物） vs `DesignProposal`（规格图）
  - AutoFix 语义不同：生成"修复任务"重新执行 vs 修改 Proposal 本身
  - 字段几乎不重叠：`File/Line/AutoFixable` vs `ProposalID/Round/Category=architecture`
  - 真共享代码仅 4 行 for 控制流，强行抽 strategy 需泛型化 `Issue/Result` 类型，收益小于 churn
  - `review_loop.go:1-14` 与 `design_review.go:1-15` 已显式文档化"刻意保持独立"

> 两项均已 close — 复审后确认当前结构合理，不需重构。

### LeaseManager 持锁等待模型（已不在关键路径）

原本计划用此重构来支撑 4.3/4.4，但 Wave 7 已通过另一条路径接入（在 ExecutionScheduler 层把 ConflictDetector 的 blocker 翻译为 wait-for 边）。LeaseManager 持锁等待模型现仅作为锁层面真实并发竞争的可选增强，不再作为 4.3/4.4 的前置条件。

---

## 实施规范

### 文件命名与位置
- 纯新增文件放 `sdk/kernel/` 或 `sdk/loop/`
- 遵循包内已有风格

### 依赖方向（严格单向）
```
sdk/kernel/ → sdk/loop/ → sdk/llm/
sdk/kernel/ → sdk/tool/ / sdk/events/ / sdk/protocol/
cmd/brain/ → sdk/*
```
- loop 包不能 import kernel 包

### 验收标准
1. 写好 .go 文件
2. `go build ./...` 通过
3. `grep -rn '<新类型名>' cmd/ sdk/ --include='*.go' | grep -v _test.go | grep -v <新文件本身>` 必须有非零结果（证明被引用）
4. 关键执行函数禁止字面量 `"模拟"` / `Passed: true` 写死 / `_ = err` 吞错

---

*历史记录:*
- *2026-04-30 全量代码审查后，从"45/45 全部 ✅"修正为"4/48 真接入"*
- *2026-04-30 d6619ce 修复 5 项伪实现（3.6/3.7/3.10/6.2/6.7） + 接入 Wave 1 全 + Wave 2 部分 + Wave 5.2，接入率提升至 18/48*
- *2026-04-30 本轮 Wave A/B/C 接入：A 接 6.7 readiness 路由 + 6.2 chaos delegateOnce 拦截；B 接 ClosedLoopController + cmd_serve_projects 一次落地 3.1/3.4/3.5/3.8/3.9/3.10；C 接 PlanOrchestrator 三组件 2.2/2.5/2.6 + cmd_serve_plans + chat/slash_plan；P1 1.6 progress RPC 双方法路由注册。最终 34/48 = 71%*
- *2026-05-01 本轮接入 9 项（4.2 / 4.5 / 5.4 / 5.5 / 6.1 / 6.3 / 6.4 / 6.5 / 6.6）→ **44/48 = 92%**：4.2 ConflictDetector seq 改 atomic + 接入 ExecutionScheduler；4.5 SmartScheduler 在 BuildExecutionPlan 后做冲突感知重排；5.4 PatternExtractor 异步抽取（PlanOrchestrator.ExecuteProject 后台 goroutine，写 ProjectMemory）；5.5 AdaptivePromptManager 注入 LLMProxy.PromptManager（A/B 变体作 L1 system block，cache=true）；6.1 HealthManager + brainPool/leaseManager checker + GET /v1/health；6.3 PerfCollector 注入 delegateOnce 计时 + GET /v1/metrics/perf；6.4 ObservabilityHub 注入 delegateOnce TraceSpan + GET /v1/observability；6.5 SecurityAuditor POST /v1/projects 入参审计（阈值可配 maccs.security.reject_severity）；6.6 MultiProjectManager 项目级配额（默认 max_concurrent=3, queue_size=16）。剩 2 项（4.3/4.4）前置条件不足。*
- *2026-05-01 配置同步：新增 `MACCSConfig` 8 个配置块（health/perf/observability/security/multi_project/adaptive_prompt/conflict/pattern_extractor）+ 11 个 nil-safe 默认值访问器；examples.go 同步带 MACCS 段；`brain config init` 输出新版配置参考。*
- *2026-05-01 修两个 v2 遗留 bug：(1) startupOrch 用 `&kernel.LLMProxy{}` 空壳（缺 ProviderFactory），导致 POST /v1/projects 走 ClosedLoopController 派发到 sidecar 反向调 LLM 时报 "no ProviderFactory configured"；改为复用 runs 路径同款 ProviderFactory，七阶段闭环全过。(2) AcceptanceTester 默认 spec 用 `verify_<sanitized_name>` 模板生成 Command 实际 PATH 不存在 → 走 `sh -c verify_xxx` 全 exit 127；改为 Command 留空走 artifacts fallback，AcceptanceCriteria 加可选 Command 字段供用户自定义命令。*
- *2026-05-01 完整重写 `central/docs/38-中央大脑核心职责.md` 为 v3 MACCS 视角，列出 6 大反馈源 + 7 条动态调整路径 + 工具白名单（编排 / 只读 / 禁用三类）。明确"中央大脑只动嘴不动手"边界。*
- *2026-05-02 c4fe85b — MACCS 学习闭环持久化（L1/L2/L3 自动 Save/Load）+ chat 流式渲染修复 + 多实例并发验证*
- *2026-05-02 0139b5e — 5 项审计差距全部修复 → **46/48 = 95.8%**：1.10 ReviewLoop 任务级接入（SubmitReview 写 subTask.Result.Review）；5.1 因果权重 0.2→0.35（combined 改 0.4/0.25/0.35）；2.4 MetaCognitive Lessons 反馈下一轮 plan（阈值降到 0.3 + Recommendations 入 lesson + 跳 `[相似经验/` 前缀）；5.3 ActiveLearning EventBus `brain.feedback.requested` 订阅 goroutine（写 ProjectMemory）；5.4 PatternExtractor Save/List/Memory.Store 失败 Warn 日志（覆盖 silent failure）。*
- *2026-05-02 Wave 7（全量完成里程碑）— 4.3 / 4.4 接入完成 → **48/48 = 100%**。绕开 LeaseManager 持锁等待前置不足问题：在 ExecutionScheduler 层把 ConflictDetector 报告的 blocker 翻译为 wait-for 边写入 DeadlockDetector，Detect() 检环；命中环时由 DefaultArbiter.ResolveDeadlock 选 victim（基于 buildBatchPriorities 派生的 Critical/StartedAt/EstimatedTurns 优先级），victim RetryCount=RetryLimit+1 + MarkFailed("deadlock-victim") 不重试打破环；每批结束 RemoveTask 清理。配置 `maccs.deadlock.enabled`/`dry_run` 双开关（默认 enabled=true, dry_run=true 首周观察期）。新增 `AttachDeadlockControl` API + `MACCSDeadlockConfig` 配置块 + 2 个访问器。*
