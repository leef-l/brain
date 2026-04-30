# MACCS 实施进度追踪

> **版本**: v2.2.0
> **启动日期**: 2026-04-29
> **最近核查**: 2026-04-30 d6619ce + 本轮 Wave A/B/C 接入审查
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
| 1.10 | ReviewLoop 审核闭环 | `sdk/kernel/review_loop.go` | ✅ 已落地 | PlanOrchestrator.ReviewLoop 在 reflection 后真实跑 |

**Wave 1 真实接入率：10/10 = 100%**

---

## Wave 2: 中央大脑智能化

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 2.1 | 项目级记忆存储 | `sdk/kernel/project_memory.go` | ✅ 已落地 | MemProjectMemory 由 cmd_serve_plans + chat/slash_plan 注入 PlanOrchestrator |
| 2.2 | 记忆检索 | `sdk/kernel/memory_retrieval.go` | ✅ 已落地 | NewMemoryRetriever 注入 PlanOrchestrator.MemoryRetriever，ExecuteProject reflection 后调 Retrieve top-N |
| 2.3 | 复杂度预估器 | `sdk/kernel/complexity_estimator.go` | ✅ 已落地 | NewComplexityEstimatorWithTransfer 在 PlanOrchestrator 构造时真用 |
| 2.4 | 元认知反思引擎 | `sdk/kernel/meta_cognitive.go` | ✅ 已落地 | PlanOrchestrator 内部 reflection 阶段真实调用 |
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
| 4.2 | 冲突检测器 | `sdk/kernel/conflict_detector.go` | 🟡 算法实在但未接入 | 路径前缀 + 循环依赖检测；`seq` 字段仍无锁；无调用方 |
| 4.3 | 死锁检测器 | `sdk/kernel/deadlock_detector.go` | 🟡 算法实在但未接入 | DFS 环检测；无调用方 |
| 4.4 | 仲裁策略 | `sdk/kernel/arbiter.go` | 🟡 算法实在但未接入 | 5 种策略；无调用方 |
| 4.5 | 智能重排调度 | `sdk/kernel/smart_scheduler.go` | 🟡 算法实在但未接入 | 贪心冲突分离；调度器仍三套并存（scheduler.go + execution_scheduler.go + smart_scheduler.go） |

**Wave 4 真实接入率：1/5 = 20%**（仅 4.1 收敛）

---

## Wave 5: 学习系统进化

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 5.1 | 因果学习引擎 | `sdk/kernel/causal_learning.go` | ✅ 已落地（d6619ce 接入） | learning.go:709 RecordSequence 时 causal.Observe + :718 LearnRelations；orchestrator.go:1509 brainCausalScore 真参与 resolveTargetKind 加权 0.2 |
| 5.2 | 迁移学习引擎 | `sdk/kernel/transfer_learning.go` | ✅ 已落地（d6619ce 接入） | DefaultTransferLearner 在 PlanOrchestrator 构造时注入 ComplexityEstimator |
| 5.3 | 主动学习引擎 | `sdk/kernel/active_learning.go` | ✅ 已落地（d6619ce 接入） | orchestrator.go:1488 exploreCandidate 5% epsilon-greedy；:1668 assessActiveLearning 调 AssessUncertainty 后发 EventBus brain.feedback.requested |
| 5.4 | 项目模式提取 | `sdk/kernel/pattern_extraction.go` | 🟡 算法实在但未接入 | 4 类模式；无调用方 |
| 5.5 | 自适应 Prompt | `sdk/kernel/adaptive_prompt.go` | 🟡 算法实在但未接入 | A/B 测试；无调用方 |
| 5.6 | 能力画像 | `sdk/kernel/capability_profile.go` | ✅ 已落地（d6619ce 接入） | learning.go RecommendOrder 通过 SequenceLearner + orchestrator_plan.go:108 在 ExecuteTaskPlan layer 内重排同层任务（不破坏拓扑） |

**Wave 5 真实接入率：4/6 ≈ 67%**

---

## Wave 6: 生产级硬化

### Batch 1 — 基础设施

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.1 | 健康检查框架 | `sdk/kernel/health_check.go` | 🟡 算法实在但未接入 | HealthManager 完整；无调用方 |
| 6.2 | 混沌注入框架 | `sdk/kernel/chaos_engine.go` | ✅ 已落地（本轮接入） | DelayInjector + ErrorInjector 真实拦截；orchestrator.go:847 delegateOnce 内 `chaos.IsEnabled()` + `GetActiveDelayInjector/GetActiveErrorInjector` 守卫；cmd_serve_chaos.go + /v1/chaos/experiments(POST/DELETE) + /v1/chaos/history 三路由 |
| 6.3 | 性能基准框架 | `sdk/kernel/perf_benchmark.go` | 🟡 算法实在但未接入 | P50/P95/P99 真实；无调用方 |
| 6.4 | 可观测性框架 | `sdk/kernel/observability.go` | 🟡 算法实在但未接入 | 多 Provider；无调用方 |

### Batch 2 — 安全与并发

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.5 | 安全审计框架 | `sdk/kernel/security_audit.go` | 🟡 算法实在但未接入 | 输入验证；无调用方 |
| 6.6 | 多项目并发管理 | `sdk/kernel/multi_project.go` | 🟡 算法实在但未接入 | 项目隔离；无调用方 |
| 6.7 | 生产就绪检查 | `sdk/kernel/production_readiness.go` | ✅ 已落地（d6619ce 修复 + 本轮接入） | 7 项 check 真实探测（BrainPool.AvailableKinds、LLMProxy 等）；cmd_serve.go:865 NewReadinessCheckerWithConfig + 启动期 RunAll + BRAIN_STRICT_READINESS 守卫 + GET /v1/readiness 暴露报告 |

**Wave 6 真实接入率：2/7 ≈ 29%**

---

## 总览表

| Wave | 任务数 | ✅ 已落地 | 🟡 算法实在但未接入 | 🟠 半成品 | 🔴 伪实现 |
|------|-------|----------|-------------------|----------|---------|
| 0 | 3 | 3 | 0 | 0 | 0 |
| 1 | 10 | 10 | 0 | 0 | 0 |
| 2 | 7 | 7 | 0 | 0 | 0 |
| 3 | 10 | 10 | 0 | 0 | 0 |
| 4 | 5 | 1 | 4 | 0 | 0 |
| 5 | 6 | 4 | 2 | 0 | 0 |
| 6 | 7 | 2 | 5 | 0 | 0 |
| **合计** | **48** | **37 (77%)** | **11 (23%)** | **0** | **0** |

> **零伪实现 + 零半成品** — d6619ce 修复了 6.7/6.2/3.6/3.7/3.10 五项伪实现 + 把 5.1/5.2/5.3/5.6 四个高级 Learner 接入决策路径；本轮 Wave A/B/C 把 6.2/6.7 真接入主线，把 ClosedLoopController + 三组件（MemoryRetriever/ContextEngineWithMemory/ModelRouter）一次性接入主线，加上 P1 1.6 progress/report 路由注册到 dispatcher。

---

## 下一阶段优先级

### 🟡 P2 — 把剩余孤岛接入主线（14 项）

按"预期收益高 / 改动面小"排序。

#### Wave 4 — 并发控制（4 项）
Wave 3 的 ExecutionScheduler 已经走 DelegateBatch 真实派发，下一步需要接入冲突检测：

| 优先级 | 任务 | 接入策略 |
|--------|------|---------|
| P2-A | 4.2 ConflictDetector | 注入 ExecutionScheduler，每层 NextBatch 之后调一次 DetectConflicts，发现就回退 / 重排；先修 `seq` 字段加 atomic |
| P2-A | 4.5 SmartScheduler | 与 ExecutionScheduler 二选一收敛或合并：让 SmartScheduler 作为 ExecutionScheduler 的策略层（贪心冲突分离） |
| P2-B | 4.3 DeadlockDetector | 接入 LeaseManager.AcquireSet 之前调 DetectDeadlock 兜底 |
| P2-B | 4.4 Arbiter | 接入 ConflictDetector 检测出冲突后，由 Arbiter 决策保留哪一方 |

#### Wave 5 — 学习系统（剩 2 项孤岛）
LearningEngine + 4 个高级 Learner（Causal/Transfer/Active/Sequence）已全部接入决策路径，剩余两项：

| 优先级 | 任务 | 接入策略 |
|--------|------|---------|
| P2-A | 5.5 AdaptivePromptManager | 注入 LLMProxy 的 systemPrompt 装配链，按当前 brain 选 A/B 变体 |
| P2-B | 5.4 PatternExtractor | 在 PlanOrchestrator.ExecuteProject 完成后异步抽取 pattern → 写入 ProjectMemory |

#### Wave 6 — 生产级硬化（5 项）
最值得继续推进的是可观测性和健康检查：

| 优先级 | 任务 | 接入策略 |
|--------|------|---------|
| P2-A | 6.1 HealthManager | 接 cmd_serve.go 加 GET /v1/health 路由（与 /v1/readiness 区分：health 关注 liveness，readiness 关注 dependencies） |
| P2-A | 6.4 Observability | 注入 Orchestrator.delegateOnce 包一层 Span，把 brain/tool 调用链路上报 |
| P2-B | 6.3 PerfBenchmark | 接 cmd_serve.go 加 GET /v1/metrics/perf 路由，按 brain.kind 分桶汇总 P50/P95/P99 |
| P2-B | 6.5 SecurityAuditor | 接 cmd_serve.go 入参校验中间件，所有 /v1/* 路由统一过 ValidateInput |
| P2-C | 6.6 MultiProjectManager | 接 cmd_serve_projects.go，做项目级配额 + 并发上限 |

### ⚠️ P3 — 重复造轮收敛（仍待办）

- 调度器：`scheduler.go::DefaultTaskScheduler` + `execution_scheduler.go::ExecutionScheduler` + `smart_scheduler.go::SmartScheduler` → 仍三套并存，建议合并为「ExecutionScheduler 调度框架 + SmartScheduler 策略 + DefaultTaskScheduler 兼容入口」
- 审核循环：`review_loop.go::ReviewLoop` + `design_review.go::DesignReviewLoop` 两套均已接入主线但语义重叠，可考虑把 design_review 收编为 review_loop 的一种 strategy

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
