# MACCS 实施进度追踪

> **版本**: v2.0.0
> **启动日期**: 2026-04-29
> **最近核查**: 2026-04-30 全量代码审查
> **编译验证**: `./scripts/build.sh ./...` (1.5G 内存上限) 通过
> **铁律**: 禁止 `go test` / `go vet`，只用 `go build ./...`

---

## ⚠️ 状态图例（重要）

之前所有任务粗暴标 ✅，经审查发现绝大多数文件存在但未接入主线，文档与代码现状不符。现采用四级状态：

| 标记 | 含义 |
|------|------|
| ✅ **已落地** | 代码完整 + 接入主线被实际调用 + 算法真实可执行 |
| 🟡 **算法实在但未接入** | 代码逻辑实在、算法可用，但 `cmd/brain` / orchestrator 中**零调用方**，等于孤岛 |
| 🟠 **半成品** | 代码框架在，但关键路径有"模拟"字符串 / 协议无路由 / 接口签名不互通 |
| 🔴 **伪实现** | 默认全 true / map 查找当真实检查 / 空 Inject — 接口完整但内部空跑 |

---

## Wave 0: 止血与基线修复

**目标**: 让系统能完整跑通一次，修复致命阻塞

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 0.1 | task_complete 终止 run | `sdk/loop/runner.go` | ✅ 已落地 | runner 主循环 tool dispatch 后检测 task_complete，goto done |
| 0.2 | LLM 超时 90s → 180s | `sdk/llm/anthropic_provider.go` | ✅ 已落地 | newDefaultHTTPClient() Timeout/ResponseHeader/Idle 三项 |
| 0.3 | serve 默认 turn 20 → 50 | `cmd/brain/cmd_serve.go:1213` | ✅ 已落地 | req.MaxTurns = 50 |

**Wave 0 真实接入率：3/3 = 100%**

---

## Wave 1: 核心编排升级

**目标**: TaskPlan + ProjectProgress + InterruptSignal + 动态预算 + 审核闭环

### Batch 1 — 纯新增数据结构

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 1.1 | TaskPlan 数据结构 | `sdk/kernel/task_plan.go` | 🟡 算法实在但未接入 | Kahn 拓扑分层完整；仅被 1.8 使用，cmd/brain 0 引用 |
| 1.2 | ProjectProgress 数据结构 | `sdk/kernel/project_progress.go` | 🟡 算法实在但未接入 | 带锁 + 8 个方法完整；无任何调用方 |
| 1.3 | InterruptSignal 数据结构 | `sdk/kernel/interrupt.go` | 🟡 算法实在但未接入 | MemInterruptChecker 可用；无路由 |

### Batch 2 — 基础设施接入

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 1.4 | Runner 中断检查 | `sdk/loop/interrupt.go` + `sdk/loop/runner.go` | 🟠 半成品 | **loop 侧 `RunInterruptChecker.CheckInterrupt(ctx, runID)` 与 kernel 侧 `InterruptChecker.Check` 签名不互通，无 adapter；`Runner.InterruptChecker` 字段全代码库 0 注入点，中断功能在生产路径完全失效** |
| 1.5 | Checkpoint 增强 | `sdk/loop/checkpoint.go` | ✅ 已落地 | 添加 PlanID/CurrentTaskID/ProjectID 三字段，omitempty 向后兼容 |
| 1.6 | 进度汇报 RPC | `sdk/protocol/methods.go` + `sdk/kernel/progress_rpc.go` | 🟠 半成品 | **协议常量定义了但无 dispatcher 注册，sidecar 发了 `progress/report` host 端认不出来** |
| 1.7 | 进度持久化 | `sdk/kernel/progress_store.go` | 🟡 算法实在但未接入 | MemoryProgressStore + FileProgressStore 完整；无调用方 |

### Batch 3 — 编排集成

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 1.8 | Orchestrator 集成 TaskPlan | `sdk/kernel/orchestrator_plan.go` | 🟡 算法实在但未接入 | **`Orchestrator.ExecuteTaskPlan` 真接 DelegateBatch，逻辑完整可跑**；但 cmd/brain 无入口、无 HTTP 路由、无 chat slash 命令暴露 |
| 1.9 | 动态预算池 | `sdk/kernel/dynamic_budget.go` | 🟡 算法实在但未接入 | Allocate/Reclaim/Emergency 完整；无调用方 |
| 1.10 | ReviewLoop 审核闭环 | `sdk/kernel/review_loop.go` | 🟡 算法实在但未接入 | 与已有 `design_review.go::DesignReviewLoop` 重复造轮 |

**Wave 1 真实接入率：1/10 = 10%（仅 Checkpoint）**

---

## Wave 2: 中央大脑智能化

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 2.1 | 项目级记忆存储 | `sdk/kernel/project_memory.go` | 🟡 算法实在但未接入 | MemProjectMemory CRUD 完整；无调用方 |
| 2.2 | 记忆检索 | `sdk/kernel/memory_retrieval.go` | 🟡 算法实在但未接入 | 多因子排序算法实在；无调用方 |
| 2.3 | 复杂度预估器 | `sdk/kernel/complexity_estimator.go` | 🟡 算法实在但未接入 | 学习数据优先 + 启发式 fallback；无调用方 |
| 2.4 | 元认知反思引擎 | `sdk/kernel/meta_cognitive.go` | 🟡 算法实在但未接入 | 694 行，Reflect/Lessons/Feedback 完整；无调用方 |
| 2.5 | Context Engine 增强 | `sdk/kernel/context_engine_memory.go` | 🟡 算法实在但未接入 | ContextEngineWithMemory 包装器；无调用方 |
| 2.6 | 多模型路由 | `sdk/kernel/model_router.go` | 🟡 算法实在但未接入 | 4 种策略；无调用方 |
| 2.7 | Orchestrator 智能化 | `sdk/kernel/plan_orchestrator.go` | 🟡 算法实在但未接入 | **`PlanOrchestrator.ExecuteProject` 串联所有 Wave 2 组件，可作为入口**；cmd/brain 0 引用 |

**Wave 2 真实接入率：0/7 = 0%**

---

## Wave 3: EasyMVP 闭环工作流

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 3.1 | 项目 Session 管理 | `sdk/kernel/project_session.go` | 🟡 算法实在但未接入 | 7 阶段 PhaseRecord + MemStore 完整；无调用方 |
| 3.2 | 需求解析器 | `sdk/kernel/requirement_parser.go` | 🟡 算法实在但未接入 | 启发式解析；无调用方 |
| 3.3 | 方案设计接口 | `sdk/kernel/design_api.go` | 🟡 算法实在但未接入 | DesignProposal + ToTaskPlan 转换；无调用方 |
| 3.4 | 项目状态机 | `sdk/kernel/project_state_machine.go` | 🟡 算法实在但未接入 | 转移表 + 守卫条件 + 回退/重试；无调用方 |
| 3.5 | 方案审核循环 | `sdk/kernel/design_review.go` | 🟡 算法实在但未接入 | 7 项启发式检查 + AutoFix；无调用方 |
| 3.6 | 执行调度器 | `sdk/kernel/execution_scheduler.go` | 🔴 伪实现 | **整文件不调度任何执行：只有 MarkRunning/MarkCompleted 状态机，没调用 Orchestrator/DelegateBatch；与已有 `scheduler.go::DefaultTaskScheduler` 重叠且互不连通** |
| 3.7 | 验收测试层 | `sdk/kernel/acceptance_tester.go` | 🔴 伪实现 | **`RunTests` 不执行 `Command` 字段（一行 shell 都不跑），只 `if val, ok := artifacts[test.TestID]; ok { Passed = true }`，通过率取决于上游有没有把 testID 当 key 塞 map** |
| 3.8 | 交付生成器 | `sdk/kernel/delivery_generator.go` | 🟡 算法实在但未接入 | README/CHANGELOG 模板可用；无调用方 |
| 3.9 | 复盘引擎 | `sdk/kernel/retrospective.go` | 🟡 算法实在但未接入 | 4 类教训提取 + 效率评分；无调用方 |
| 3.10 | 闭环控制器 | `sdk/kernel/closed_loop_controller.go` | 🔴 伪实现 | **Phase 4 执行阶段循环调 `MarkCompleted("模拟执行完成")`；Phase 5 `artifacts[t.TestID] = "模拟交付物"`；所谓"七阶段闭环"的核心阶段是空跑** |

**Wave 3 真实接入率：0/10 = 0%（且 3 个伪实现）**

**关键缺陷**: easymvp 的 `contracts.go` / `contracts_review.go` / `contracts_solution.go` 是真接入了 brain handler 的（用户侧能调），但与 Wave 3 的这一堆 `sdk/kernel/*` 文件**没有任何连接关系**。Wave 3 等于平行宇宙。

---

## Wave 4: 并发控制与冲突仲裁

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 4.1 | 资源访问追踪 | `sdk/kernel/resource_tracker.go` | 🟡 算法实在但未接入 | 读共享/写独占完整；与已有 `lease.go::MemLeaseManager` 重叠；无调用方 |
| 4.2 | 冲突检测器 | `sdk/kernel/conflict_detector.go` | 🟡 算法实在但未接入 | 路径前缀 + 循环依赖检测；`seq` 字段无锁自增（潜在 race）；无调用方 |
| 4.3 | 死锁检测器 | `sdk/kernel/deadlock_detector.go` | 🟡 算法实在但未接入 | DFS 环检测；无调用方 |
| 4.4 | 仲裁策略 | `sdk/kernel/arbiter.go` | 🟡 算法实在但未接入 | 5 种策略；无调用方 |
| 4.5 | 智能重排调度 | `sdk/kernel/smart_scheduler.go` | 🟡 算法实在但未接入 | 贪心冲突分离；与 `scheduler.go` + `execution_scheduler.go` **三套调度器并存**；无调用方 |

**Wave 4 真实接入率：0/5 = 0%**

---

## Wave 5: 学习系统进化

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 5.1 | 因果学习引擎 | `sdk/kernel/causal_learning.go` | 🟡 算法实在但未接入 | `parseCausalStatKey` 用首/末 `:` 分隔脆弱（factor 含 `:` 会错位）；无调用方 |
| 5.2 | 迁移学习引擎 | `sdk/kernel/transfer_learning.go` | 🟡 算法实在但未接入 | 余弦/Jaccard 相似度真实；无调用方 |
| 5.3 | 主动学习引擎 | `sdk/kernel/active_learning.go` | 🟡 算法实在但未接入 | 不确定性评估 + 自动问题生成；无调用方 |
| 5.4 | 项目模式提取 | `sdk/kernel/pattern_extraction.go` | 🟡 算法实在但未接入 | 4 类模式 + 条件匹配；无调用方 |
| 5.5 | 自适应 Prompt | `sdk/kernel/adaptive_prompt.go` | 🟡 算法实在但未接入 | A/B 测试 + 流量分流；无调用方 |
| 5.6 | 能力画像 | `sdk/kernel/capability_profile.go` | 🟡 算法实在但未接入 | EWMA 评分；与已有 `capability.go` 类似；无调用方 |

**Wave 5 真实接入率：0/6 = 0%**

---

## Wave 6: 生产级硬化

### Batch 1 — 基础设施

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.1 | 健康检查框架 | `sdk/kernel/health_check.go` | 🟡 算法实在但未接入 | HealthManager 聚合报告完整；无调用方 |
| 6.2 | 混沌注入框架 | `sdk/kernel/chaos_engine.go` | 🔴 伪实现 | **`MemFaultInjector.Inject` = `m.active[id] = exp`，`Remove` = `delete(m.active, id)`；零真实故障注入** |
| 6.3 | 性能基准框架 | `sdk/kernel/perf_benchmark.go` | 🟡 算法实在但未接入 | P50/P95/P99 + 吞吐量统计真实；无调用方 |
| 6.4 | 可观测性框架 | `sdk/kernel/observability.go` | 🟡 算法实在但未接入 | 多 Provider + Span 追踪；无调用方 |

### Batch 2 — 安全与并发

| # | 任务 | 文件 | 状态 | 备注 |
|---|------|------|------|------|
| 6.5 | 安全审计框架 | `sdk/kernel/security_audit.go` | 🟡 算法实在但未接入 | 输入验证规则可用；无调用方 |
| 6.6 | 多项目并发管理 | `sdk/kernel/multi_project.go` | 🟡 算法实在但未接入 | 项目隔离 + 资源配额 + 并发控制；无调用方 |
| 6.7 | 生产就绪检查 | `sdk/kernel/production_readiness.go` | 🔴 伪实现 | **7 项默认 ReadinessCheck 全部 hardcoded `Passed: true`，没有任何真实探测，`IsReady()` 永远 true** |

**Wave 6 真实接入率：0/7 = 0%（且 2 个伪实现）**

---

## 总览表

| Wave | 任务数 | ✅ 已落地 | 🟡 算法实在但未接入 | 🟠 半成品 | 🔴 伪实现 |
|------|-------|----------|-------------------|----------|---------|
| 0 | 3 | 3 | 0 | 0 | 0 |
| 1 | 10 | 1 | 7 | 2 | 0 |
| 2 | 7 | 0 | 7 | 0 | 0 |
| 3 | 10 | 0 | 7 | 0 | 3 |
| 4 | 5 | 0 | 5 | 0 | 0 |
| 5 | 6 | 0 | 6 | 0 | 0 |
| 6 | 7 | 0 | 5 | 0 | 2 |
| **合计** | **48** | **4 (8%)** | **37 (77%)** | **2 (4%)** | **5 (11%)** |

---

## 修复优先级（下一阶段）

### 🔥 P0 — 修复伪实现（5 项）

| 任务 | 修复策略 |
|------|----------|
| 3.6 execution_scheduler | 删除或改造为接 Orchestrator.DelegateBatch，与 scheduler.go 二选一 |
| 3.7 acceptance_tester | 真正执行 `Command` 字段（exec.Command + 超时）；或明确改成"由上游 brain 跑测试再回填" |
| 3.10 closed_loop_controller | Phase 4/5 删除"模拟"，调用 Orchestrator.ExecuteTaskPlan + DeliveryGenerator |
| 6.2 chaos_engine | 给 `MemFaultInjector` 加真实 hook 点（HTTP middleware / RPC 拦截） |
| 6.7 production_readiness | 7 项检查接真实探测：BrainPool.Size > 0、LLMProxy.Ping、Metrics.Enabled 等 |

### 🟠 P1 — 修复半成品（2 项）

| 任务 | 修复策略 |
|------|----------|
| 1.4 Runner 中断 | 在 cmd_serve 注入 `RunInterruptChecker`；或写 adapter 把 kernel.InterruptChecker 转 loop.RunInterruptChecker |
| 1.6 进度 RPC | 在 BrainPool / ProcessRunner 的 dispatcher 注册 `progress/report` 路由 |

### 🟡 P2 — 把孤岛接入主线（37 项 → 选其中 1-2 个最关键的）

**建议先接入 1.8 + 2.7**：把 `PlanOrchestrator.ExecuteProject` 接到 chat 的 `/plan` slash 命令 或 HTTP `POST /v1/plans`，跑通一个最小闭环。

剩余 35 项孤岛：要么真正接入（每接入一个写一段集成代码），要么标 `experimental` 并从主路径移除引用。

### ⚠️ P3 — 重复造轮收敛（3 组）

- 调度器三套：`scheduler.go` + `execution_scheduler.go` + `smart_scheduler.go` → 收敛为一套
- 资源租约两套：`lease.go` + `resource_tracker.go` → 二选一
- 审核循环两套：`review_loop.go` + `design_review.go::DesignReviewLoop` → 合并

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

### 验收标准（强化）
原来：写好 .go 文件 + `go build ./...` 通过
现在：
1. 写好 .go 文件
2. `./scripts/build.sh ./...` 通过（1.5G 内存上限）
3. **`grep -rn '<新类型名>' cmd/ sdk/ --include='*.go' | grep -v _test.go | grep -v <新文件本身>` 必须有非零结果**（证明被引用）
4. 关键执行函数禁止字面量 `"模拟"` / `Passed: true` 写死 / `_ = err` 吞错

---

*历史记录: 2026-04-30 全量代码审查后，从"45/45 全部 ✅"修正为"4/48 真接入"。*
