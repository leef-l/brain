# MACCS — 实施路线图

> **版本**: v2.3.0
> **最近更新**: 2026-04-30（按 dfd57ac 实际接入率重写）
> **当前接入率**: 37/48 = 77%
> **里程碑**: 5 月底前消化剩余 11 项（P2-A/B/C 三周计划）
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

### Wave 4: 并发控制 — 1/5 ⚠️（剩 4 项进 P2）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 4.1 | 资源访问追踪 | （并入 ExecutionScheduler） | ✅ | 独立 resource_tracker.go 已删除（300 行）改为内嵌 |
| 4.2 | 冲突检测 | `sdk/kernel/conflict_detector.go` | 🟡 | 算法实在，未注入 ExecutionScheduler — **进 P2-A** |
| 4.3 | 死锁检测 | `sdk/kernel/deadlock_detector.go` | 🟡 | 未接 LeaseManager.AcquireSet — **进 P2-B** |
| 4.4 | 仲裁策略 | `sdk/kernel/arbiter.go` | 🟡 | 5 种策略未接 ConflictDetector — **进 P2-B** |
| 4.5 | 智能重排 | `sdk/kernel/smart_scheduler.go` | 🟡 | 与 ExecutionScheduler 二选一收敛 — **进 P2-A** |

---

### Wave 5: 学习系统进化 — 4/6 ⚠️（剩 2 项进 P2）

| # | 任务 | 文件 | 状态 | 接入说明 |
|---|------|------|------|---------|
| 5.1 | 因果学习 | `sdk/kernel/causal_learning.go` | ✅ | TaskStep 加 ContextSize/Complexity/TimeBucket/ProjectKind 4 混杂因子；resolveTargetKind 加权 Cap*0.5 + Learn*0.3 + Causal*0.2 |
| 5.2 | 迁移学习 | `sdk/kernel/transfer_learning.go` | ✅ | PlanSubTask 加 Language/Domain；ComplexityEstimator 三段决策 learning→transfer→heuristic |
| 5.3 | 主动学习 | `sdk/kernel/active_learning.go` | ✅ | recordDelegateOutcome 末尾 AssessUncertainty + EventBus 发 brain.feedback.requested；resolveTargetKind 5% epsilon-greedy |
| 5.4 | 项目模式提取 | `sdk/kernel/pattern_extraction.go` | 🟡 | PlanOrchestrator 完成后未异步抽取 — **进 P2-B** |
| 5.5 | 自适应 Prompt | `sdk/kernel/adaptive_prompt.go` | 🟡 | 未注入 LLMProxy 的 systemPrompt 装配链 — **进 P2-A** |
| 5.6 | 能力画像可视化 | `cmd/brain/dashboard/` | ✅ | Dashboard 展示学习成果 |

> 此外 SequenceLearner 已接 ExecuteTaskPlan layer 内 RecommendOrder 重排同层任务（不破坏拓扑约束）。

---

### Wave 6: 生产级硬化 — 2/7 ⚠️（剩 5 项进 P2）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.1 | HealthManager | `sdk/kernel/health.go` | 🟡 | 未接 GET /v1/health 路由 — **进 P2-A** |
| 6.2 | ChaosEngine | `sdk/kernel/chaos_engine.go` | ✅ | orchestrator.delegateOnce 拦截点 + POST/DELETE /v1/chaos/experiments + GET /v1/chaos/history |
| 6.3 | PerfBenchmark | `sdk/kernel/perf_benchmark.go` | 🟡 | 未接 GET /v1/metrics/perf — **进 P2-B** |
| 6.4 | Observability | `sdk/kernel/observability.go` | 🟡 | 未注入 Orchestrator.delegateOnce Span — **进 P2-A** |
| 6.5 | SecurityAuditor | `sdk/kernel/security_auditor.go` | 🟡 | 未接 /v1/* 入参校验中间件 — **进 P2-B** |
| 6.6 | MultiProjectManager | `sdk/kernel/multi_project_manager.go` | 🟡 | 未接 cmd_serve_projects 配额 — **进 P2-C** |
| 6.7 | ProductionReadiness | `sdk/kernel/production_readiness.go` | ✅ | cmd_serve.go 启动期 RunAll + BRAIN_STRICT_READINESS env 守卫 + GET /v1/readiness 路由 |

---

## 3. 当前接入率总览（2026-04-30）

| Wave | 接入 / 总数 | 比例 |
|------|------------|------|
| Wave 0 | 3/3 | 100% |
| Wave 1 | 8/8 | 100% |
| Wave 2 | 8/8 | 100% |
| Wave 3 | 10/10 | 100% |
| Wave 4 | 1/5 | 20% |
| Wave 5 | 4/6 | 67% |
| Wave 6 | 2/7 | 29% |
| **总计** | **37/48** | **77%** |

剩余 11 项全部分类到 P2-A / P2-B / P2-C 三周推进（见 §4），未到 P3 重构层面。

---

## 4. 剩余 11 项推进计划（5 月）

> 按"预期收益高 / 改动面小"排序，每周一个批次。详细接入策略见 `MACCS-实施进度追踪.md` §P2。

### P2-A（第 1 周，2026-05-04 → 2026-05-10）— 5 项

| 任务 | 接入主线 |
|------|---------|
| 4.2 ConflictDetector | 注入 ExecutionScheduler，每层 NextBatch 之后 DetectConflicts |
| 4.5 SmartScheduler | 收敛为 ExecutionScheduler 的策略层（贪心冲突分离） |
| 5.5 AdaptivePromptManager | 注入 LLMProxy 的 systemPrompt 装配链 |
| 6.1 HealthManager | 加 GET /v1/health 路由（与 /v1/readiness 区分） |
| 6.4 Observability | 包 Orchestrator.delegateOnce Span，brain/tool 链路上报 |

### P2-B（第 2 周，2026-05-11 → 2026-05-17）— 5 项

| 任务 | 接入主线 |
|------|---------|
| 4.3 DeadlockDetector | 接 LeaseManager.AcquireSet 之前 DetectDeadlock 兜底 |
| 4.4 Arbiter | 接 ConflictDetector 检测出冲突后由 Arbiter 决策 |
| 5.4 PatternExtractor | PlanOrchestrator.ExecuteProject 完成后异步抽 → ProjectMemory |
| 6.3 PerfBenchmark | 加 GET /v1/metrics/perf，按 brain.kind 分桶 P50/P95/P99 |
| 6.5 SecurityAuditor | /v1/* 入参校验中间件统一过 ValidateInput |

### P2-C（第 3 周，2026-05-18 → 2026-05-24）— 1 项

| 任务 | 接入主线 |
|------|---------|
| 6.6 MultiProjectManager | 接 cmd_serve_projects.go，做项目级配额 + 并发上限 |

### P3 — 重复造轮收敛（5 月底之后再议）

- 调度器三套并存：`DefaultTaskScheduler` + `ExecutionScheduler` + `SmartScheduler` → 合并为「ExecutionScheduler 框架 + SmartScheduler 策略 + DefaultTaskScheduler 兼容入口」
- 审核循环：`review_loop.go` + `design_review.go` 语义重叠 → design_review 收编为 review_loop 的一种 strategy

---

## 5. 里程碑（按真实进度修订）

| 日期 | 里程碑 | 交付物 | 状态 |
|------|--------|--------|------|
| 2026-04-29 | Wave 0 | snake-game 完整跑通 | ✅ |
| 2026-04-30 | Wave 1-3 + 部分 4-6 | 接入率 77%（37/48） | ✅ |
| 2026-05-10 | P2-A 完成 | 接入率 86%（41/48） | ⏳ |
| 2026-05-17 | P2-B 完成 | 接入率 96%（46/48） | ⏳ |
| 2026-05-24 | P2-C 完成 | 接入率 98%（47/48） | ⏳ |
| 2026-05-31 | P3 调度器/审核循环收敛 | 接入率 100%（48/48） | ⏳ |
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
