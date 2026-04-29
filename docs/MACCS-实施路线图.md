# MACCS — 实施路线图

> **版本**: v2.0.0  
> **日期**: 2026-04-29  
> **原则**: 激进演进，不停迭代，你说不停我不停

---

## 1. 当前基线（2026-04-29）

### 1.1 Brain 项目现状

**已完成（但落后新架构）**：
- ✅ SDK 基础框架（loop、tool、llm、kernel）
- ✅ 9 个 brain 注册（central, code, browser, data, quant, verifier, fault, desktop, easymvp）
- ✅ 控制论框架（状态观测、反馈控制、耦合矩阵、自稳定）
- ✅ 学习系统 L0-L3 接口（但智能化程度不足）
- ✅ DAG 任务调度（Kahn 算法）
- ✅ 流式输出跨 brain
- ✅ Browser 语义理解
- ✅ Dashboard WebUI

**核心问题（阻塞生产）**：
- 🔴 Orchestrator 不会停止（task_complete 不终止 run）
- 🔴 LLM 90s 超时导致 context deadline exceeded
- 🔴 无项目级进度追踪
- 🔴 无中断重排机制
- 🔴 固定 20/50 turn 预算，不智能
- 🔴 无并发冲突检测
- 🔴 无自动化审核闭环

### 1.2 EasyMVP 项目现状

**已完成**：
- ✅ 基础项目/任务管理
- ✅ DAG 编译与调度
- ✅ Brain Run 绑定与状态同步
- ✅ 任务分层并行执行
- ✅ 事件总线

**核心问题（阻塞生产）**：
- 🔴 编译错误（Timeout 字段不存在）
- 🔴 所有任务 100% 失败（budget.turns_exhausted + timeout）
- 🔴 无方案审核闭环
- 🔴 无项目级 Central Session
- 🔴 验收测试缺失
- 🔴 无学习复盘

---

## 2. 实施阶段

### Wave 0: 止血与基线修复（立即开始）

**目标**: 让 snake-game 能完整跑通一次

| # | 任务 | 文件 | 说明 |
|---|------|------|------|
| 0.1 | 修复编译错误 | `easymvp/.../worker_task_scheduler.go` | 删除不存在的 Timeout 字段 |
| 0.2 | task_complete 终止 run | `brain/sdk/loop/runner.go` | 检测到 task_complete 立即完成 run |
| 0.3 | LLM 超时上调 | `brain/sdk/llm/anthropic_provider.go` | 90s → 180s |
| 0.4 | 默认 turn 预算上调 | `brain/cmd/brain/cmd_serve.go` | 20 → 50 |
| 0.5 | 验证修复 | 端到端测试 snake-game | 确认 6 个任务全部完成 |

**成功标准**: snake-game 项目 6 个任务全部 completed，无 budget.turns_exhausted

---

### Wave 1: 核心编排升级（第 1-2 周）

**目标**: 实现 TaskPlan + ProjectProgress + InterruptSignal

| # | 任务 | 文件 | 说明 |
|---|------|------|------|
| 1.1 | TaskPlan 数据结构 | `brain/sdk/kernel/task_plan.go` | 定义 TaskPlan/SubTask/Budget |
| 1.2 | ProjectProgress 数据结构 | `brain/sdk/kernel/project_progress.go` | 定义全局进度追踪 |
| 1.3 | InterruptSignal 数据结构 | `brain/sdk/kernel/interrupt.go` | 定义中断信号 |
| 1.4 | Runner 支持中断检查 | `brain/sdk/loop/runner.go` | 每 turn 前检查中断 |
| 1.5 | Checkpoint 恢复 | `brain/sdk/loop/checkpoint.go` | 中断后从 checkpoint 恢复 |
| 1.6 | Orchestrator 集成 TaskPlan | `brain/sdk/kernel/orchestrator.go` | Delegate 支持 TaskPlan |
| 1.7 | 进度汇报 RPC | `brain/sdk/protocol/methods.go` | 新增 `brain/progress` 方法 |
| 1.8 | 进度持久化 | `brain/sdk/kernel/progress_store.go` | SQLite 存储进度 |

**成功标准**: Central Brain 能为 snake-game 生成 TaskPlan，执行中能检测进度

---

### Wave 2: 中央大脑智能化（第 2-3 周）

**目标**: 实现全局记忆 + 动态预算 + 复杂度预估

| # | 任务 | 文件 | 说明 |
|---|------|------|------|
| 2.1 | 项目级记忆存储 | `brain/sdk/kernel/project_memory.go` | SQLite 存储项目对话历史 |
| 2.2 | 记忆检索 | `brain/sdk/kernel/memory_retrieval.go` | 语义相似度检索 |
| 2.3 | Context Engine 增强 | `brain/sdk/kernel/context_engine.go` | 自动压缩 + 关键决策保留 |
| 2.4 | 动态预算池 | `brain/sdk/kernel/dynamic_budget.go` | DynamicBudgetPool 实现 |
| 2.5 | 复杂度预估器 | `brain/sdk/kernel/complexity_estimator.go` | 基于学习数据预估 |
| 2.6 | 多模型路由配置 | `brain/cmd/brain/provider/` | 支持 per-brain model 配置 |
| 2.7 | 元认知反思 | `brain/sdk/kernel/meta_cognitive.go` | 项目完成后自动反思 |
| 2.8 | Prompt 升级 | `brain/cmd/brain/chat/prompt.go` | 注入进度、预算、记忆 |

**成功标准**: snake-game 的 TaskPlan 预算从固定 50 → 动态 25-40（基于预估）

---

### Wave 3: EasyMVP 闭环工作流（第 3-4 周）

**目标**: 实现七阶段闭环

| # | 任务 | 文件 | 说明 |
|---|------|------|------|
| 3.1 | EasyMVP 项目 Session | `easymvp/.../project_session.go` | 项目级 Central Session |
| 3.2 | 需求解析 API | `easymvp/.../requirement_handler.go` | Phase 1 接口 |
| 3.3 | 方案设计 API | `easymvp/.../design_handler.go` | Phase 2 接口 |
| 3.4 | 方案审核循环 | `easymvp/.../review_loop.go` | Phase 3 自动化审核 |
| 3.5 | 任务执行调度器 | `easymvp/.../execution_scheduler.go` | Phase 4 基于 TaskPlan 调度 |
| 3.6 | 验收测试层 | `easymvp/.../acceptance_tester.go` | Phase 5 多层验收 |
| 3.7 | 交付生成器 | `easymvp/.../delivery_generator.go` | Phase 6 交付物生成 |
| 3.8 | 复盘学习 | `easymvp/.../retrospective.go` | Phase 7 自动复盘 |
| 3.9 | 项目状态机 | `easymvp/.../project_state_machine.go` | 完整状态流转 |
| 3.10 | SSE 事件流 | `easymvp/.../event_stream.go` | 实时推送给前端 |

**成功标准**: 用户输入"做个贪吃蛇"，系统自动完成全部 7 个阶段，交付可运行游戏

---

### Wave 4: 并发控制与冲突仲裁（第 4-5 周）

**目标**: 真正的智能并发

| # | 任务 | 文件 | 说明 |
|---|------|------|------|
| 4.1 | 资源访问追踪 | `brain/sdk/kernel/resource_tracker.go` | 追踪每个任务的文件访问 |
| 4.2 | 冲突检测 | `brain/sdk/kernel/conflict_detector.go` | 检测文件/端口冲突 |
| 4.3 | 仲裁策略 | `brain/sdk/kernel/arbiter.go` | 串行化/合并/优先级 |
| 4.4 | 智能重排 | `brain/sdk/kernel/smart_scheduler.go` | 动态调整并行层 |
| 4.5 | 死锁检测 | `brain/sdk/kernel/deadlock_detector.go` | 循环依赖检测 |

**成功标准**: 8 个任务同时执行无冲突，或冲突被正确仲裁

---

### Wave 5: 学习系统进化（第 5-7 周）

**目标**: 从统计到智能

| # | 任务 | 文件 | 说明 |
|---|------|------|------|
| 5.1 | 因果学习 | `brain/sdk/kernel/causal_learning.go` | 因果推理而非相关 |
| 5.2 | 迁移学习 | `brain/sdk/kernel/transfer_learning.go` | 跨项目经验复用 |
| 5.3 | 主动学习 | `brain/sdk/kernel/active_learning.go` | 系统主动请求反馈 |
| 5.4 | 项目模式提取 | `brain/sdk/kernel/pattern_extraction.go` | 自动提取最佳实践 |
| 5.5 | 自适应 Prompt | `brain/sdk/kernel/adaptive_prompt.go` | 基于学习数据优化 prompt |
| 5.6 | 能力画像可视化 | `brain/cmd/brain/dashboard/` | Dashboard 展示学习成果 |

**成功标准**: 第 10 个项目的成功率显著高于第 1 个

---

### Wave 6: 生产级硬化（持续）

**目标**: 达到生产级别

| # | 任务 | 说明 |
|---|------|------|
| 6.1 | 全面测试 | 单元测试覆盖率 >= 80%，集成测试覆盖核心链路 |
| 6.2 | 混沌工程 | 随机 kill brain、网络中断、API 降级 |
| 6.3 | 性能基准 | 定义并优化各阶段 latency |
| 6.4 | 可观测性 | Prometheus 指标 + 结构化日志 + 分布式追踪 |
| 6.5 | 安全审计 | 输入验证、沙箱强化、权限最小化 |
| 6.6 | 文档同步 | 代码变更自动更新文档 |
| 6.7 | 多项目并发 | 支持同时执行多个项目 |

**成功标准**: 通过 100 个真实项目测试，成功率 >= 90%

---

## 3. 里程碑

| 日期 | 里程碑 | 交付物 |
|------|--------|--------|
| 2026-04-29 | Wave 0 完成 | snake-game 完整跑通 |
| 2026-05-06 | Wave 1 完成 | TaskPlan + Interrupt 可用 |
| 2026-05-13 | Wave 2 完成 | 动态预算 + 全局记忆 |
| 2026-05-20 | Wave 3 完成 | EasyMVP 七阶段闭环 |
| 2026-05-27 | Wave 4 完成 | 智能并发 |
| 2026-06-10 | Wave 5 完成 | 学习系统进化 |
| 2026-06-30 | Wave 6 完成 | 生产级发布 |

---

## 4. 每日迭代节奏

```
每日循环：
  1. 晨会（自检）：查看昨日完成项、今日计划、阻塞项
  2. 编码：实现当前 Wave 的任务
  3. 测试：跑通 snake-game 验证
  4. 文档：更新相关设计文档
  5. 复盘：记录遇到的问题和决策
  
每周循环：
  1. 周一：规划本周 Wave 任务
  2. 周三：中期检查，调整优先级
  3. 周五：Wave 验收，更新里程碑
  4. 周末：学习新论文/技术，为下周做准备
```

---

## 5. 风险与对策

| 风险 | 影响 | 对策 |
|------|------|------|
| LLM API 不稳定 | 高 | 多 provider  failover，本地模型兜底 |
| 长上下文模型贵 | 中 | 智能压缩，只传关键信息 |
| 并发冲突频繁 | 中 | 保守调度 + 冲突自动仲裁 |
| 学习数据不足 | 中 | 先用启发式，逐步过渡到学习驱动 |
| 代码量爆炸 | 中 | 模块化设计，严格接口边界 |
| 测试覆盖不足 | 高 | 每个功能必须有测试，CI 强制检查 |

---

*本文档是 MACCS 的实施路线图。每完成一个 Wave，必须更新本文档状态。*
