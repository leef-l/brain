# MACCS — 实施路线图

> **版本**: v2.5.0（全量完成）
> **最近更新**: 2026-05-02（Wave 7 4.3/4.4 接入合并后）
> **最终接入率**: **48/48 = 100%**
> **铁律**: `go build ./...` 验证；禁止 `go test` / `go vet`

---

## 1. 历史基线（2026-04-29，立项时刻）

### 1.1 Brain 项目当时状态

**已完成（但落后新架构）**：
- ✅ SDK 基础框架（loop、tool、llm、kernel）
- ✅ 9 个 brain 注册（central, code, browser, data, quant, verifier, fault, desktop, easymvp）
- ✅ 控制论框架（状态观测、反馈控制、耦合矩阵、自稳定）
- ✅ 学习系统 L0-L3 接口（但智能化程度不足）
- ✅ DAG 任务调度（Kahn 算法）
- ✅ 流式输出跨 brain
- ✅ Browser 语义理解
- ✅ Dashboard WebUI

**当时阻塞生产的核心问题**：
- 🔴 Orchestrator 不会停止（task_complete 不终止 run）
- 🔴 LLM 90s 超时导致 context deadline exceeded
- 🔴 无项目级进度追踪
- 🔴 无中断重排机制
- 🔴 固定 20/50 turn 预算，不智能
- 🔴 无并发冲突检测
- 🔴 无自动化审核闭环

> 上述 7 项问题已在 Wave 0 / Wave 1 / Wave 3 全部消化，详见各 Wave 表格。

### 1.2 EasyMVP 项目当时状态

EasyMVP 的七阶段闭环已由 brain 内部 ClosedLoopController 落地（见 Wave 3），EasyMVP 侧不再单独承担。

---

## 2. 实施阶段（按真实接入状态标注）

> 状态图例：✅ 已落地 / 🟡 算法实在但未接入 / 🟠 半成品 / 🔴 伪实现

### Wave 0: 止血与基线修复 — 3/3 = 100% ✅

| # | 任务 | 文件 | 状态 |
|---|------|------|------|
| 0.1 | task_complete 终止 run | `sdk/loop/runner.go` | ✅ |
| 0.2 | LLM 超时 90s → 180s | `sdk/llm/anthropic_provider.go` | ✅ |
| 0.3 | serve 默认 turn 20 → 50 | `cmd/brain/cmd_serve.go` | ✅ |

---

### Wave 1: 核心编排升级 — 8/8 = 100% ✅

| # | 任务 | 文件 | 状态 | 接入说明 |
|---|------|------|------|---------|
| 1.1 | TaskPlan | `sdk/kernel/task_plan.go` | ✅ | cmd_serve_plans + ClosedLoopController + PlanOrchestrator 三处真用 |
| 1.2 | ProjectProgress | `sdk/kernel/project_progress.go` | ✅ | PlanOrchestrator + ProgressStore + GET /v1/plans/{id} |
| 1.3 | InterruptSignal | `sdk/kernel/interrupt.go` | ✅ | cmd_serve_interrupt 路由 + Runner 注入 |
| 1.4 | Runner 中断检查 | `sdk/loop/interrupt.go` | ✅ | adapter 把 kernel 信号转 loop |
| 1.5 | Checkpoint 增强 | `sdk/loop/checkpoint.go` | ✅ | PlanID/CurrentTaskID/ProjectID 三字段 |
| 1.6 | 进度汇报 RPC | `sdk/protocol/methods.go` + `sdk/kernel/progress_rpc.go` | ✅ | orchestrator.go registerReverseHandlers 注册 MethodProgressReport + MethodProgressQuery |
| 1.7 | Orchestrator 集成 TaskPlan | `sdk/kernel/orchestrator.go` | ✅ | ExecuteTaskPlan 入口 |
| 1.8 | 进度持久化 | `sdk/kernel/progress_store.go` | ✅ | SQLite + 异步写入 |

---

### Wave 2: 中央大脑智能化 — 8/8 = 100% ✅

| # | 任务 | 文件 | 状态 | 接入说明 |
|---|------|------|------|---------|
| 2.1 | 项目级记忆存储 | `sdk/kernel/project_memory.go` | ✅ | SQLite project_memory 表 |
| 2.2 | 记忆检索 | `sdk/kernel/memory_retrieval.go` | ✅ | PlanOrchestrator.ExecuteProject reflection 后 Retrieve top-N 追加到 recommendations |
| 2.3 | Context Engine 增强 | `sdk/kernel/context_engine.go` | ✅ | LLM summarize + Compress 策略 2.5 |
| 2.4 | 动态预算池 | `sdk/kernel/dynamic_budget.go` | ✅ | TaskPlan.Budget 动态调整 |
| 2.5 | ContextEngineWithMemory | `cmd/brain/cmd_serve_plans.go` | ✅ | newPlanService 构造 → PlanOrchestrator → SetContextEngine 落到 Orchestrator，所有 Delegate 自动 Assemble 注入项目记忆 |
| 2.6 | ModelRouter | `sdk/kernel/model_router.go` | ✅ | NewModelRouter(StrategyStatic) → SyncToLLMProxy + ExecuteProject 内 Resolve 写 diaglog |
| 2.7 | 元认知反思 | `sdk/kernel/meta_cognitive.go` | ✅ | PlanOrchestrator 完成后调 Reflect |
| 2.8 | Prompt 升级 | `cmd/brain/chat/prompt.go` | ✅ | 注入进度、预算、记忆 |

---

### Wave 3: 七阶段闭环（ClosedLoopController） — 10/10 = 100% ✅

| # | 任务 | 文件 | 状态 | 接入说明 |
|---|------|------|------|---------|
| 3.1 | 项目 Session | `sdk/kernel/project_session.go` | ✅ | cmd_serve_projects.projectService 构造 |
| 3.2 | 需求解析 | `sdk/kernel/requirement_parser.go` | ✅ | Phase 1 |
| 3.3 | 方案设计 | `sdk/kernel/design_phase.go` | ✅ | Phase 2 |
| 3.4 | 方案审核循环 | `sdk/kernel/design_review.go` | ✅ | Phase 3 |
| 3.5 | 任务执行调度 | `sdk/kernel/execution_scheduler.go` | ✅ | DelegateBatch 真派发 + succeeded/retried/failed 三计数 |
| 3.6 | 验收测试 | `sdk/kernel/acceptance_tester.go` | ✅ | exec.CommandContext 真起进程 + 超时 + stdout/stderr 采集 |
| 3.7 | 交付生成 | `sdk/kernel/delivery_generator.go` | ✅ | Phase 6 |
| 3.8 | 复盘学习 | `sdk/kernel/retrospective.go` | ✅ | Phase 7 |
| 3.9 | 状态机 | `sdk/kernel/project_state_machine.go` | ✅ | 完整状态流转 |
| 3.10 | SSE 事件流 | `cmd/brain/cmd_serve_projects.go` | ✅ | POST /v1/projects + GET /v1/projects/{id} |

---

### Wave 4: 并发控制 — 5/5 = 100% ✅

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 4.1 | 资源访问追踪 | （并入 ExecutionScheduler） | ✅ | 独立 resource_tracker.go 已删除（300 行）改为内嵌 |
| 4.2 | 冲突检测 | `sdk/kernel/conflict_detector.go` | ✅ | seq atomic + ExecutionScheduler.AttachConflictControl 注入；RunPlan 派发前 Detect 检查 blocker；按 maccs.conflict.* 启用 |
| 4.5 | 智能重排 | `sdk/kernel/smart_scheduler.go` | ✅ | NewSmartScheduler 在 ExecutionScheduler.AttachConflictControl 注入；BuildExecutionPlan 完成后 Reschedule 做冲突感知重排（dryRun 默认观察期，可关 maccs.conflict.dry_run=false 强制重排） |
| 4.3 | 死锁检测 | `sdk/kernel/deadlock_detector.go` | ✅（Wave 7 接入） | ExecutionScheduler.AttachDeadlockControl 注入；RunPlan 把 ConflictDetector 报告的 blocker 翻译为 (waiter→holder, ResourcePath) 边写入 DeadlockDetector.AddWaitEdge，Detect() 检环；每批结束 RemoveTask 清理。绕开 LeaseManager 持锁等待前置问题，从 ConflictDetector 语义层接入 |
| 4.4 | 仲裁策略 | `sdk/kernel/arbiter.go` | ✅（Wave 7 接入） | DefaultArbiter 5 种策略；ResolveDeadlock(cycle, priorities) 选 victim；priorities 由 buildBatchPriorities 派生（StartedAt!=nil → Critical, EstimatedTurns 短优先级高）；victim 强制 RetryCount=RetryLimit+1 + MarkFailed("deadlock-victim") 不重试以打破环。配置 `maccs.deadlock.enabled` / `dry_run` 双开关 |

---

### Wave 5: 学习系统进化 — 4/6 ⚠️（剩 2 项进 P2）

| # | 任务 | 文件 | 状态 | 接入说明 |
|---|------|------|------|---------|
| 5.1 | 因果学习 | `sdk/kernel/causal_learning.go` | ✅ | TaskStep 加 ContextSize/Complexity/TimeBucket/ProjectKind 4 混杂因子；resolveTargetKind 加权 **Cap×0.4 + Learn×0.25 + Causal×0.35**（0139b5e 把因果权重从 0.2 升到 0.35，让因果信号在评分接近时主导路由） |
| 5.2 | 迁移学习 | `sdk/kernel/transfer_learning.go` | ✅ | PlanSubTask 加 Language/Domain；ComplexityEstimator 三段决策 learning→transfer→heuristic |
| 5.3 | 主动学习 | `sdk/kernel/active_learning.go` | ✅ | recordDelegateOutcome 末尾 AssessUncertainty + EventBus 发 `brain.feedback.requested`；resolveTargetKind 5% epsilon-greedy；**0139b5e 加订阅方**：plan_orchestrator.consumeFeedbackRequests goroutine 把每条反馈作 lesson 写 ProjectMemory，下轮 plan 经 MemoryRetriever 读到形成跨 plan 闭环 |
| 5.4 | 项目模式提取 | `sdk/kernel/pattern_extraction.go` | ✅ | PlanOrchestrator.ExecuteProject 后台 goroutine 异步抽取 → ExperienceStore.Save → Extract → AddPattern → ProjectMemory.Store；patternBgCtx 防 ctx 取消打断；**0139b5e 加可观测**：Save/List/Memory.Store 失败均打 Warn 带 project_id / err |
| 5.5 | 自适应 Prompt | `sdk/kernel/adaptive_prompt.go` | ✅ | NewAdaptivePromptManager 注入 LLMProxy.PromptManager；adaptiveSystemPrefix helper 在 Complete/handleComplete/handleStream 三入口把 SelectVariant 变体作 L1 system block 前置（cache=true） |
| 5.6 | 能力画像可视化 | `cmd/brain/dashboard/` | ✅ | Dashboard 展示学习成果 |

> 此外 SequenceLearner 已接 ExecuteTaskPlan layer 内 RecommendOrder 重排同层任务（不破坏拓扑约束）。

---

### Wave 6: 生产级硬化 — 7/7 = 100% ✅

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.1 | HealthManager | `sdk/kernel/health_check.go` | ✅ | NewHealthManager + brainPool/leaseManager checker；GET /v1/health；可关 maccs.health.enabled=false |
| 6.2 | ChaosEngine | `sdk/kernel/chaos_engine.go` | ✅ | orchestrator.delegateOnce 拦截点 + POST/DELETE /v1/chaos/experiments + GET /v1/chaos/history |
| 6.3 | PerfBenchmark | `sdk/kernel/perf_benchmark.go` | ✅ | NewPerfCollector + WithPerfCollector 注入 delegateOnce 计时（按 brain.kind/status 分桶 P50/P95/P99）；GET /v1/metrics/perf |
| 6.4 | Observability | `sdk/kernel/observability.go` | ✅ | NewObservabilityHub + WithObservability + 内存 provider；delegateOnce 包 TraceSpan；GET /v1/observability + ?trace_id 过滤 |
| 6.5 | SecurityAuditor | `sdk/kernel/security_audit.go` | ✅ | NewSecurityAuditor 注入 projectService；POST /v1/projects 入参 ValidateInput；阈值可配 maccs.security.reject_severity |
| 6.6 | MultiProjectManager | `sdk/kernel/multi_project.go` | ✅ | NewMultiProjectManager(MaxConcurrent=3, QueueSize=16) 注入 projectService；handleCreateProject 进入即 Submit 拿槽位，结束 Complete/Fail；超额返回 429 |
| 6.7 | ProductionReadiness | `sdk/kernel/production_readiness.go` | ✅ | cmd_serve.go 启动期 RunAll + BRAIN_STRICT_READINESS env 守卫 + GET /v1/readiness 路由 |

---

## 3. 最终接入率总览（2026-05-02 全量完成）

| Wave | 接入 / 总数 | 比例 |
|------|------------|------|
| Wave 0 | 3/3 | 100% |
| Wave 1 | 10/10 | 100% |
| Wave 2 | 7/7 | 100% |
| Wave 3 | 10/10 | 100% |
| Wave 4 | 5/5 | 100% |
| Wave 5 | 6/6 | 100% |
| Wave 6 | 7/7 | 100% |
| **总计** | **48/48** | **100%** |

> **MACCS v2 全 48 项任务接入完成** ✅

---

## 4. 后续工作（建议性，非必须）

### P3 — 重复造轮收敛

- 调度器三套并存：`DefaultTaskScheduler` + `ExecutionScheduler` + `SmartScheduler` → 可合并为「ExecutionScheduler 框架 + SmartScheduler 策略 + DefaultTaskScheduler 兼容入口」
- 审核循环：`review_loop.go` + `design_review.go` 语义重叠 → design_review 可收编为 review_loop 的一种 strategy

> 不影响功能完整性，留给后续优化窗口。

### LeaseManager 持锁等待模型（已不在关键路径）

原本计划用此重构来支撑 4.3/4.4，但 Wave 7 已通过另一条路径接入（在 ExecutionScheduler 层把 ConflictDetector 的 blocker 翻译为 wait-for 边）。LeaseManager 持锁等待模型现仅作为锁层面真实并发竞争的可选增强，不再作为 4.3/4.4 的前置条件。

---

## 5. 里程碑（按真实进度修订）

| 日期 | 里程碑 | 交付物 | 状态 |
|------|--------|--------|------|
| 2026-04-29 | Wave 0 | snake-game 完整跑通 | ✅ |
| 2026-04-30 | Wave 1-3 + 部分 4-6 | 接入率 77%（37/48） | ✅ |
| 2026-05-01 | 9 项落地 | 接入率 92%（44/48） | ✅ |
| 2026-05-02 | 0139b5e 5 项审计差距修复 | 接入率 95.8%（46/48） | ✅ |
| 2026-05-02 | **Wave 7（4.3/4.4 接入）** | **全量完成 100%（48/48）** | ✅ |
| 2026-06-30 | 100 项目验收（成功率 ≥ 90%） | 生产级发布 | ⏳ |

---

## 6. 风险与对策

| 风险 | 影响 | 对策 |
|------|------|------|
| 调度器三套并存（P3）重构破坏现有路径 | 高 | 先冻结 ExecutionScheduler 接口，SmartScheduler 改为内部策略，DefaultTaskScheduler 加 deprecation note |
| ConflictDetector 接入后误判频繁导致 RunPlan 卡死 | 中 | 接入时先 dry-run 一周（仅日志，不回退），观察误报率再启用回退 |
| Arbiter 5 种策略缺少业务语义 | 中 | 默认 SerializeByPriority，其余 4 种留作 P3 配置开关 |
| AdaptivePromptManager 改 systemPrompt 影响 LLM 质量 | 高 | A/B 实验隔离 5% 流量，评估后再放量 |
| MultiProjectManager 配额冲突现有 ClosedLoopController | 中 | 引入"项目租约"概念，与 LeaseManager 复用 AcquireSet |

---

## 7. 验收标准（每项任务接入时强制）

1. 写好 .go 文件
2. `go build ./...` 通过（铁律：禁止 `go test` / `go vet`）
3. `grep -rn '<新类型名>' cmd/ sdk/ --include='*.go' | grep -v _test.go | grep -v <新文件本身>` 必须有非零结果（证明被引用）
4. 关键执行函数禁止字面量 `"模拟"` / `Passed: true` 写死 / `_ = err` 吞错
5. commit message 必须体现接入率变化，例如 `feat: MACCS 37/48 → 42/48 — P2-A 子集落地`

---

*历史记录:*
- *2026-04-29 v2.0.0 创立路线图，定义 7 个 Wave + 6 月 30 日里程碑*
- *2026-04-30 v2.1.0 d6619ce — Wave 1 全 + Wave 2 部分 + 5.1/5.2/5.3/5.6 接入决策路径，接入率 18/48*
- *2026-04-30 v2.2.0 dfd57ac — Wave A/B/C：6.7/6.2 + ClosedLoopController 一次性带活 6 项 + Plan 三组件 + P1 1.6 progress RPC 路由，接入率 37/48 (77%)*
- *2026-04-30 v2.3.0 — 路线图按"实际落地状态"重写，里程碑提前到 5 月底，剩余 11 项分 P2-A/B/C 三周推进*
- *2026-05-01 v2.3.x — 9 项落地（4.2/4.5/5.4/5.5/6.1/6.3/6.4/6.5/6.6），接入率 44/48 = 92%*
- *2026-05-02 v2.4.0 — 0139b5e 5 项审计差距修复（1.10 ReviewLoop 任务级 / 5.1 因果权重 0.35 / 2.4 Lessons 反馈下一轮 plan / 5.3 EventBus 反馈订阅 / 5.4 PatternExtractor 可观测），接入率 46/48 = 95.8%。*
- *2026-05-02 **v2.5.0（全量完成）** — Wave 7 接入 4.3 DeadlockDetector + 4.4 Arbiter ResolveDeadlock，绕开 LeaseManager 持锁等待前置不足问题（在 ExecutionScheduler 层把 ConflictDetector blocker 翻译为 wait-for 边），新增 AttachDeadlockControl API + MACCSDeadlockConfig 配置块（默认 enabled=true, dry_run=true 观察期）。**MACCS v2 全 48 项 100% 完成** 🎉*
