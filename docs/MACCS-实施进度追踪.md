# MACCS 实施进度追踪

> **版本**: v2.0.0  
> **启动日期**: 2026-04-29  
> **跟踪粒度**: 每完成一项即更新  
> **编译验证**: 每批完成后 `go build ./...`  
> **铁律**: 禁止 `go test` / `go vet`，只用 `go build ./...`

---

## Wave 0: 止血与基线修复

**目标**: 让系统能完整跑通一次，修复致命阻塞

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 0.1 | task_complete 终止 run | `sdk/loop/runner.go` | ✅ 完成 | runner 主循环 tool dispatch 后检测 task_complete，goto done |
| 0.2 | LLM 超时 90s → 180s | `sdk/llm/anthropic_provider.go` | ✅ 完成 | newDefaultHTTPClient() Timeout/ResponseHeader/Idle 三项 |
| 0.3 | serve 默认 turn 20 → 50 | `cmd/brain/cmd_serve.go:1213` | ✅ 完成 | req.MaxTurns = 50 |

**依赖**: 无，三项可并行  
**验证**: `go build ./...` 通过

---

## Wave 1: 核心编排升级

**目标**: TaskPlan + ProjectProgress + InterruptSignal + 动态预算 + 审核闭环

### Batch 1 — 纯新增数据结构（无依赖，可并行）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 1.1 | TaskPlan 数据结构 | `sdk/kernel/task_plan.go` (新) | ✅ 完成 | TaskPlan/PlanSubTask/PlanBudget/PlanCheckpoint/ComplexityEstimation + Kahn 拓扑分层 |
| 1.2 | ProjectProgress 数据结构 | `sdk/kernel/project_progress.go` (新) | ✅ 完成 | ProjectProgress(带 mutex)/RunProgress/BlockedInfo/QualityGate/ResourceUsage + 8 个方法 |
| 1.3 | InterruptSignal 数据结构 | `sdk/kernel/interrupt.go` (新) | ✅ 完成 | InterruptSignal/InterruptType/InterruptAction/InterruptChecker + MemInterruptChecker |

**依赖**: 无  
**验证**: `go build ./...` 通过

### Batch 2 — 基础设施接入（依赖 Batch 1）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 1.4 | Runner 中断检查 | `sdk/loop/interrupt.go` (新) + `sdk/loop/runner.go` (改) | ✅ 完成 | RunInterruptChecker slim 接口 + 每 turn 前 stop/pause/restart 分支 |
| 1.5 | Checkpoint 增强 | `sdk/loop/checkpoint.go` (改) | ✅ 完成 | 添加 PlanID/CurrentTaskID/ProjectID 三字段，omitempty 向后兼容 |
| 1.6 | 进度汇报 RPC | `sdk/protocol/methods.go` (改) + `sdk/kernel/progress_rpc.go` (新) | ✅ 完成 | 3 个 Method 常量 + ProgressHandler(HandleReport/HandleQuery) |
| 1.7 | 进度持久化 | `sdk/kernel/progress_store.go` (新) | ✅ 完成 | ProgressStore 接口 + MemoryProgressStore + FileProgressStore |

**依赖**: 1.4 依赖 1.3，其余独立  
**验证**: `go build ./...` 通过

### Batch 3 — 编排集成（依赖 Batch 1+2）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 1.8 | Orchestrator 集成 TaskPlan | `sdk/kernel/orchestrator_plan.go` (新) | ✅ 完成 | ExecuteTaskPlan + TaskPlanResult + 拓扑分层并行 + 重试 |
| 1.9 | 动态预算池 | `sdk/kernel/dynamic_budget.go` (新) | ✅ 完成 | DynamicBudgetPool(Allocate/Reclaim/Emergency) + 启发式估算 |
| 1.10 | ReviewLoop 审核闭环 | `sdk/kernel/review_loop.go` (新) | ✅ 完成 | ReviewLoopController + ReviewReport + 自动修复 + 收敛检查 |

**依赖**: 1.8 依赖 1.1+1.2，1.9 依赖 1.1，1.10 依赖 1.1+1.2+1.8  
**验证**: `go build ./...` 通过

---

## Wave 2: 中央大脑智能化

**目标**: 项目级记忆 + 复杂度预估 + 元认知反思 + 多模型路由

### Batch 1 — 纯新增（无依赖，可并行）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 2.1 | 项目级记忆存储 | `sdk/kernel/project_memory.go` (新) | ✅ 完成 | ProjectMemory 接口 + MemProjectMemory(Store/Query/Get/Delete/Summarize) |
| 2.2 | 记忆检索 | `sdk/kernel/memory_retrieval.go` (新) | ✅ 完成 | MemoryRetriever 多因子排序(keyword+tag+recency+importance) |
| 2.3 | 复杂度预估器 | `sdk/kernel/complexity_estimator.go` (新) | ✅ 完成 | 学习数据优先 + 启发式 fallback + SuggestBudget |
| 2.4 | 元认知反思引擎 | `sdk/kernel/meta_cognitive.go` (新) | ✅ 完成 | Reflect + GenerateLessons + FeedbackToLearner + FormatReport |

**依赖**: 无  
**验证**: `go build ./...` 通过

### Batch 2 — 接入集成（依赖 Batch 1）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 2.5 | Context Engine 增强 | `sdk/kernel/context_engine_memory.go` (新) | ✅ 完成 | ContextEngineWithMemory 包装器，自动注入项目记忆 |
| 2.6 | 多模型路由 | `sdk/kernel/model_router.go` (新) | ✅ 完成 | ModelRouter + 4 种路由策略(fixed/round_robin/adaptive/cost_optimized) |
| 2.7 | Orchestrator 智能化 | `sdk/kernel/plan_orchestrator.go` (新) | ✅ 完成 | PlanOrchestrator 组合包装器，集成全部 MACCS v2 组件 |

**依赖**: 2.5 依赖 2.1，2.7 依赖 2.1+2.4  
**验证**: `go build ./...` 通过

---

## Wave 3: EasyMVP 闭环工作流

**目标**: 七阶段闭环 — 需求→设计→审核→执行→验收→交付→复盘

### Batch 1 — 基础数据结构（无依赖，可并行）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 3.1 | 项目 Session 管理 | `sdk/kernel/project_session.go` (新) | ✅ 完成 | ProjectSession + 7 阶段 PhaseRecord + MemProjectSessionStore |
| 3.2 | 需求解析器 | `sdk/kernel/requirement_parser.go` (新) | ✅ 完成 | RequirementSpec + DefaultRequirementParser 启发式解析 |
| 3.3 | 方案设计接口 | `sdk/kernel/design_api.go` (新) | ✅ 完成 | DesignProposal + DefaultDesignGenerator + ToTaskPlan 转换 |
| 3.4 | 项目状态机 | `sdk/kernel/project_state_machine.go` (新) | ✅ 完成 | ProjectStateMachine + 转移表 + 守卫条件 + 回退/重试 |

### Batch 2 — 执行与验收（依赖 Batch 1）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 3.5 | 方案审核循环 | `sdk/kernel/design_review.go` (新) | ✅ 完成 | DesignReviewer + DesignReviewLoop 闭环 + 7 项启发式检查 + AutoFix |
| 3.6 | 执行调度器 | `sdk/kernel/execution_scheduler.go` (新) | ✅ 完成 | ExecutionScheduler + 拓扑分层调度 + 动态预算 + 重试 |
| 3.7 | 验收测试层 | `sdk/kernel/acceptance_tester.go` (新) | ✅ 完成 | AcceptanceTester + 4 类测试生成 + 自动/手动分流 + 验收报告 |

### Batch 3 — 闭环集成（依赖 Batch 1+2）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 3.8 | 交付生成器 | `sdk/kernel/delivery_generator.go` (新) | ✅ 完成 | DeliveryManifest + README/CHANGELOG 自动生成 |
| 3.9 | 复盘引擎 | `sdk/kernel/retrospective.go` (新) | ✅ 完成 | RetroReport + 4 类教训提取 + 效率评分 |
| 3.10 | 闭环控制器 | `sdk/kernel/closed_loop_controller.go` (新) | ✅ 完成 | ClosedLoopController 7 阶段串联 + rollback + slim 接口解耦 |

**依赖**: 3.5 依赖 3.1+3.3，3.6 依赖 3.1+3.4，3.7 依赖 3.4，3.10 依赖全部  
**验证**: 每批完成后 `go build ./...` 通过

---

## Wave 4: 并发控制与冲突仲裁

**目标**: 资源追踪 + 冲突检测 + 仲裁策略 + 智能重排 + 死锁检测

### Batch 1 — 基础设施（无依赖，可并行）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 4.1 | 资源访问追踪 | `sdk/kernel/resource_tracker.go` (新) | ✅ 完成 | ResourceTracker + 读共享/写独占锁 + 过期清理 |
| 4.2 | 冲突检测器 | `sdk/kernel/conflict_detector.go` (新) | ✅ 完成 | DefaultConflictDetector + 路径前缀匹配 + 循环依赖检测 |
| 4.3 | 死锁检测器 | `sdk/kernel/deadlock_detector.go` (新) | ✅ 完成 | DFS 环检测 + WouldDeadlock 预检 + SuggestVictim |

### Batch 2 — 策略与调度（依赖 Batch 1）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 4.4 | 仲裁策略 | `sdk/kernel/arbiter.go` (新) | ✅ 完成 | DefaultArbiter + 5 种策略 + 死锁 victim 选择 |
| 4.5 | 智能重排调度 | `sdk/kernel/smart_scheduler.go` (新) | ✅ 完成 | SmartScheduler + 贪心冲突分离 + 并行度建议 |

**依赖**: 4.4 依赖 4.1+4.2，4.5 依赖 4.1+4.2+4.3  
**验证**: 每批完成后 `go build ./...` 通过

---

## Wave 5: 学习系统进化

**目标**: 因果学习 + 迁移学习 + 主动学习 + 模式提取 + 自适应 Prompt + 能力画像

### Batch 1 — 基础学习能力（无依赖，可并行）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 5.1 | 因果学习引擎 | `sdk/kernel/causal_learning.go` (新) | ✅ 完成 | CausalLearner + 因果效应计算 + 反事实推理 |
| 5.2 | 迁移学习引擎 | `sdk/kernel/transfer_learning.go` (新) | ✅ 完成 | TransferLearner + 余弦/Jaccard相似度 + 经验迁移 |
| 5.3 | 主动学习引擎 | `sdk/kernel/active_learning.go` (新) | ✅ 完成 | ActiveLearner + 不确定性评估 + 自动问题生成 |

### Batch 2 — 高级能力（依赖 Batch 1）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 5.4 | 项目模式提取 | `sdk/kernel/pattern_extraction.go` (新) | ✅ 完成 | PatternExtractor + 4 类模式提取 + 条件匹配引擎 |
| 5.5 | 自适应 Prompt | `sdk/kernel/adaptive_prompt.go` (新) | ✅ 完成 | AdaptivePromptManager + A/B 测试 + 流量分流 |
| 5.6 | 能力画像 | `sdk/kernel/capability_profile.go` (新) | ✅ 完成 | BrainCapabilityRadar + EWMA 评分 + 成长追踪 |

**依赖**: 5.4 依赖 5.1+5.2，5.5 依赖 5.2+5.3，5.6 依赖全部  
**验证**: 每批完成后 `go build ./...` 通过

---

## Wave 6: 生产级硬化

**目标**: 健康检查 + 混沌注入 + 性能基准 + 可观测性 + 安全审计 + 多项目并发  
**注意**: 铁律禁止 `go test`，本 Wave 只写框架代码和接口，不执行测试

### Batch 1 — 基础设施（无依赖，可并行）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.1 | 健康检查框架 | `sdk/kernel/health_check.go` (新) | ✅ 完成 | HealthManager + 聚合报告 + 自愈回调 |
| 6.2 | 混沌注入框架 | `sdk/kernel/chaos_engine.go` (新) | ✅ 完成 | ChaosEngine + 7 种故障类型 + MemFaultInjector |
| 6.3 | 性能基准框架 | `sdk/kernel/perf_benchmark.go` (新) | ✅ 完成 | PerfCollector + P50/P95/P99 + 吞吐量统计 |
| 6.4 | 可观测性框架 | `sdk/kernel/observability.go` (新) | ✅ 完成 | ObservabilityHub + 多 Provider + Span 追踪 |

### Batch 2 — 安全与并发（依赖 Batch 1）

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.5 | 安全审计框架 | `sdk/kernel/security_audit.go` (新) | ✅ 完成 | 输入验证 + 沙箱检查 + 权限审计 + 报告 (318行) |
| 6.6 | 多项目并发管理 | `sdk/kernel/multi_project.go` (新) | ✅ 完成 | 项目隔离 + 资源配额 + 并发控制 (389行) |
| 6.7 | 生产就绪检查 | `sdk/kernel/production_readiness.go` (新) | ✅ 完成 | 上线前检查清单 + 依赖验证 + 配置审计 (333行) |

**依赖**: 6.5 依赖 6.1，6.6 依赖 6.1+6.3，6.7 依赖全部  
**验证**: 每批完成后 `go build ./...` 通过

---

## 实施规范

### 文件命名与位置
- 纯新增文件放 `sdk/kernel/` 或 `sdk/loop/`
- 遵循包内已有风格：类型定义 + 构造函数 + 方法
- 不引入新的外部依赖

### 依赖方向（严格单向）
```
sdk/kernel/ → sdk/loop/ → sdk/llm/
sdk/kernel/ → sdk/tool/
sdk/kernel/ → sdk/events/
sdk/kernel/ → sdk/protocol/
cmd/brain/ → sdk/*
```
- loop 包不能 import kernel 包
- 需要 kernel 概念时在 loop 包定义 slim 接口

### 代码风格
- 参考 `sdk/kernel/execution.go`（状态机）、`sdk/kernel/scheduler.go`（调度器）
- 类型名首字母大写，JSON tag 用 snake_case
- 构造函数命名 `NewXxx(cfg XxxConfig)` 或 `NewXxx(deps...)`

### 每个 Agent 的交付物
1. 写好的 .go 文件
2. `go build ./...` 编译通过
3. 不要跑 `go test` 或 `go vet`

---

*每完成一项，状态从 ⬜ 改为 ✅，并标注完成时间。*
