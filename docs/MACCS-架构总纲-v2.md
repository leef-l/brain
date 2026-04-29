# MACCS — Multi-Agent Cognitive Collaboration System 架构总纲 v2.0

> **命名**: MACCS（Multi-Agent Cognitive Collaboration System，多智能体认知协作系统）  
> **代号**: Project Prometheus（普罗米修斯）  
> **版本**: v2.0.0  
> **日期**: 2026-04-29  
> **目标**: 超越 Kimi 2.6 Agent 集群模式，打造世界最前沿的分层认知多智能体协作系统  
> **范围**: Brain SDK + EasyMVP 全链路

---

## 1. 核心理念

### 1.1 什么是 MACCS？

MACCS 不是简单的"多个 AI 各干各的"，而是一个**分层认知架构（Hierarchical Cognitive Architecture, HCA）**：

- **L3 战略层（中央大脑 / Central Cortex）**: 长上下文记忆、全局状态感知、战略编排、冲突仲裁、元认知反思
- **L2 战术层（专精大脑集群 / Specialist Corps）**: 领域执行、专业判断、质量控制、故障修复
- **L1 反射层（工具与环境交互 / Reflex Layer）**: 直接操作文件系统、浏览器、代码、数据库、API
- **L0 元认知层（学习与进化 / Meta-Cognitive Layer）**: 能力画像、任务评分、自适应调度、经验累积、策略进化

### 1.2 与 Kimi 2.6 Agent 集群的本质区别

| 维度 | Kimi 2.6 Agent | MACCS |
|------|---------------|-------|
| **上下文** | 单会话级，无跨会话持久化 | 项目级长期记忆，全对话历史持久化 |
| **编排** | 预设工作流或简单轮询 | 智能动态编排，基于进度和反馈实时调整 |
| **并发** | 伪并发或顺序执行 | 真正的 DAG 层内并行 + 智能资源调度 |
| **学习** | 无在线学习 | L0-L3 四层持续学习，系统越用越聪明 |
| **反馈闭环** | 无自动化审核-修正闭环 | 审核→发现问题→自动修正→重新确认→直到通过 |
| **中断与重排** | 不支持任务中途调整 | 支持动态停止旧任务、按最新方案重新执行 |
| **模型差异化** | 统一模型 | 中央大脑用超长上下文模型，各专精大脑按领域选最优模型 |

### 1.3 系统隐喻：交响乐团

- **中央大脑** = 指挥家。不看乐谱细节，但掌握整首曲子的结构、节奏、情感走向。哪个声部该进入、哪个需要加强、哪里出了问题要停下来重排，都由指挥家决定。
- **专精大脑** = 各声部首席。小提琴首席、大提琴首席、管乐首席……各自领域的最高技艺，按照指挥家的意图执行。
- **工具层** = 乐器本身。直接发出声音，但需要有技巧的音乐家来演奏。
- **学习系统** = 乐团的排练记录。每次演出后复盘，哪个段落总出问题、哪个声部配合不默契，下次排练自动调整。

---

## 2. 架构总览

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                          L3: 中央大脑 Central Cortex                          │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐  │
│  │ 全局记忆    │  │ 战略编排器  │  │ 冲突仲裁器  │  │ 元认知反思引擎      │  │
│  │ Memory      │  │ Strategist  │  │ Arbiter     │  │ Meta-Cognitive      │  │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘  └──────────┬──────────┘  │
│         └─────────────────┴─────────────────┘                    │             │
│                           │                                      │             │
│                    ┌──────▼──────┐                               │             │
│                    │  对话总线   │◄───────────────────────────────┘             │
│                    │  Dialogue   │                                              │
│                    └──────┬──────┘                                              │
└───────────────────────────┼────────────────────────────────────────────────────┘
                            │
┌───────────────────────────▼────────────────────────────────────────────────────┐
│                        编排协议层 (Orchestration Protocol)                      │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐    │
│  │ 任务规划    │  │ 进度追踪    │  │ 动态预算    │  │ 中断/重排指令       │    │
│  │ TaskPlan    │  │ Progress    │  │ BudgetCtrl  │  │ Interrupt & Reroute │    │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────┘    │
└───────────────────────────┬────────────────────────────────────────────────────┘
                            │
        ┌───────────────────┼───────────────────┐
        │                   │                   │
┌───────▼───────┐  ┌────────▼────────┐  ┌──────▼───────┐
│  L2: Code     │  │  L2: Verifier   │  │  L2: Browser │
│  代码执行脑   │  │  审核验证脑     │  │  浏览器操作脑 │
│  Model: Code  │  │  Model: Reason  │  │  Model: Vis  │
└───────────────┘  └─────────────────┘  └──────────────┘
┌───────▼───────┐  ┌────────▼────────┐  ┌──────▼───────┐
│  L2: Data     │  │  L2: Quant      │  │  L2: Fault   │
│  数据处理脑   │  │  量化交易脑     │  │  故障修复脑   │
│  Model: Fast  │  │  Model: Math    │  │  Model: Fix  │
└───────────────┘  └─────────────────┘  └──────────────┘
                            │
┌───────────────────────────▼────────────────────────────────────────────────────┐
│                        L1: 反射层 Reflex Layer                                 │
│  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐     │
│  │File I/O │ │Shell    │ │Browser  │ │DB Query │ │API Call │ │Test Run │     │
│  └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────┘     │
└────────────────────────────────────────────────────────────────────────────────┘
                            │
┌───────────────────────────▼────────────────────────────────────────────────────┐
│                        L0: 元认知层 Meta-Cognitive Layer                        │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐    │
│  │ 能力画像    │  │ 任务评分    │  │ 自适应调度  │  │ 经验累积/策略进化   │    │
│  │ Capability  │  │ TaskScore   │  │ Scheduler   │  │ Evolution           │    │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────┘    │
└────────────────────────────────────────────────────────────────────────────────┘
```

---

## 3. L3 中央大脑（Central Cortex）详设

### 3.1 长上下文全局记忆

**核心要求**：中央大脑必须使用世界最长上下文的模型（如 200K+ tokens），承载整个项目的完整对话历史。

**记忆分层**：

| 层级 | 内容 | 持久化 | 生命周期 |
|------|------|--------|----------|
| 工作记忆 | 当前 turn 的上下文 | 内存 | 单次 LLM 调用 |
| 短期记忆 | 最近 N 轮对话 | SQLite | 单次 Run |
| 项目记忆 | 整个项目的任务状态、决策、成果 | SQLite + 文件 | 项目生命周期 |
| 长期记忆 | 跨项目的经验、模式、偏好 | PG/SQLite | 永久 |

**关键技术**：
- **Prompt Cache 三层模型**: L1 System（缓存）、L2 Task（半缓存）、L3 History（动态）
- **Context Engine 压缩**: 超长上下文自动摘要，保留关键决策点
- **记忆检索**: 基于语义相似度的向量检索，快速定位相关历史

### 3.2 智能战略编排器（Strategist）

**不是简单委派，而是战略决策**：

```
输入: 当前项目状态 + 新任务 + 历史经验
  │
  ▼
┌─────────────────┐
│ 1. 任务分解     │ → 将大任务拆分为可并行子任务
│ 2. 能力匹配     │ → 根据能力画像选择最佳大脑
│ 3. 依赖分析     │ → 构建 DAG，识别可并行层
│ 4. 风险评估     │ → 预判可能的失败点
│ 5. 预算分配     │ → 按复杂度动态分配 turn/token 预算
│ 6. 执行计划输出 │ → 生成结构化 TaskPlan
└─────────────────┘
```

**TaskPlan 结构**：

```go
type TaskPlan struct {
    PlanID        string
    Version       int           // 计划版本，支持迭代更新
    Goal          string        // 总体目标
    SubTasks      []SubTask     // 子任务列表
    Dependencies  [][]string    // DAG 依赖关系
    ParallelLayers [][]string   // 拓扑分层后的并行层
    Budget        TaskBudget    // 动态预算
    Checkpoints   []Checkpoint  // 关键检查点
    AbortSignal   chan struct{} // 中断信号通道
}

type SubTask struct {
    TaskID       string
    Kind         agent.Kind     // 目标大脑
    Instruction  string
    EstimatedTurns int          // 预估 turn 数（基于学习数据）
    VerificationCriteria []string // 验收标准
    RetryPolicy  RetryPolicy    // 重试策略
}
```

### 3.3 进度感知与动态调整

**核心能力**：中央大脑持续收集所有并发任务的进度，基于反馈思考后做出调整。

**进度追踪模型**：

```go
type ProjectProgress struct {
    ProjectID     string
    OverallPercent float64      // 总体进度 0-100
    Phase         ProjectPhase // 当前阶段
    ActiveRuns    []RunState   // 活跃运行状态
    CompletedTasks []TaskResult // 已完成任务
    BlockedTasks  []BlockedTask // 被阻塞任务
    QualityGates  []QualityGate // 质量门状态
}

type RunState struct {
    RunID       string
    TaskID      string
    BrainKind   agent.Kind
    Status      string     // running/completed/failed/cancelled
    CurrentTurn int
    MaxTurns    int
    TurnUsage   float64    // 已用 turn 比例
    LastOutput  string     // 最后输出摘要
    Confidence  float64    // 完成度置信度（LLM 评估）
}
```

**动态调整策略**：

1. **任务成功但质量不达标** → 触发 verifier 审核 → 发现问题 → 自动生成修复任务 → 分配给 code brain
2. **任务进度停滞**（多轮无实质进展）→ 分析原因 → 可能是预算不足/指令不清/能力不足 → 相应调整
3. **任务失败** → 分析错误类型 → 重试/换 brain/拆分为更小子任务
4. **并发任务间冲突**（如两个任务修改同一文件）→ 冲突仲裁 → 串行化或合并
5. **计划本身有问题**（架构师发现方案缺陷）→ 全局停止信号 → 重新制定计划 → 按新版本执行

### 3.4 中断与重排机制

**最关键的生产级能力**：

```go
// 全局中断信号
type InterruptSignal struct {
    SignalID    string
    Type        InterruptType // plan_changed / emergency_stop / priority_override
    AffectedTasks []string    // 空表示全部
    NewPlan     *TaskPlan     // 新计划（如适用）
    Reason      string
}

type InterruptType string
const (
    InterruptPlanChanged     InterruptType = "plan_changed"     // 方案变更
    InterruptEmergencyStop   InterruptType = "emergency_stop"   // 紧急停止
    InterruptPriorityOverride InterruptType = "priority_override" // 优先级覆盖
    InterruptDependencyChange InterruptType = "dependency_change" // 依赖变化
)
```

**执行流程**：

1. 中央大脑检测到需要调整（审核员反馈/用户指令/自诊断）
2. 向指定任务（或全部并发任务）发送 `InterruptSignal`
3. 各 Runner 在下一 turn 开始前检查中断信号
4. 收到中断的任务：
   - 保存当前 checkpoint
   -  gracefully 停止（不强制 kill，给机会保存状态）
   - 返回中断响应给中央大脑
5. 中央大脑收集所有响应后，基于 `NewPlan` 重新调度

### 3.5 元认知反思引擎

**让系统能"思考自己的思考"**：

每完成一个项目阶段，中央大脑自动进行反思：

```
反思模板：
1. 原计划 vs 实际执行：偏差在哪里？为什么？
2. 哪些任务分配给了错误的 brain？应该分配给谁的？
3. 哪些任务预算不足/过剩？下次怎么估？
4. 并发层之间有没有不必要的等待？
5. 质量门有没有漏掉问题？怎么加强？
6. 用户最满意的输出是什么？最不满意的？
```

反思结果写入 `长期记忆`，作为下次项目的经验。

---

## 4. L2 专精大脑集群

### 4.1 模型差异化配置

**每个专精大脑独立配置模型**：

| 大脑 | 推荐模型类型 | 理由 |
|------|-------------|------|
| **Central** | 超长上下文（200K+）通用模型 | 需要承载整个项目历史 |
| **Code** | 代码专用模型（如 Claude Code / o3） | 代码理解、生成、调试 |
| **Verifier** | 推理专用模型（如 o1 / DeepSeek-R1） | 审核需要深度推理，不怕慢 |
| **Browser** | 多模态模型（支持视觉） | 需要理解网页截图 |
| **Data** | 轻量快速模型 | 数据查询不需要复杂推理，要快 |
| **Quant** | 数学/金融专用模型 | 量化计算需要精确 |
| **Fault** | 诊断专用模型 | 故障分析需要系统性思维 |

**配置方式**：

```yaml
# ~/.brain/providers.yaml
providers:
  central:
    base_url: "https://api.anthropic.com"
    model: "claude-sonnet-4-20250514"
    max_tokens: 8192
    timeout: 300s  # 中央大脑可以慢，但必须准
  
  code:
    base_url: "https://api.anthropic.com"
    model: "claude-code-v1"
    max_tokens: 4096
    timeout: 180s
  
  verifier:
    base_url: "https://api.deepseek.com"
    model: "deepseek-reasoner"
    max_tokens: 4096
    timeout: 300s  # 推理模型慢但准
  
  browser:
    base_url: "https://api.anthropic.com"
    model: "claude-sonnet-4-20250514"  # 支持 vision
    max_tokens: 4096
    timeout: 180s
```

### 4.2 专精大脑的职责边界

**Code Brain**：
- 代码读写、编辑、重构
- 测试编写与执行
- 依赖管理
- **不做的**: 架构决策、跨模块协调

**Verifier Brain**：
- 代码审查（风格、安全、性能）
- 测试结果分析
- 验收标准检查
- **输出**: 审核报告（通过/不通过 + 问题清单）

**Browser Brain**：
- 网页浏览、操作、测试
- UI 自动化
- 截图分析
- **输出**: 操作结果 + 页面状态报告

**Fault Brain**：
- 故障诊断
- 自动修复尝试
- 混沌工程测试
- **输出**: 诊断报告 + 修复方案

### 4.3 专精大脑间的协作协议

**标准协作模式**：

```
模式 A: 委托-执行-报告（Delegate-Execute-Report）
  Central → delegate to Code → Code 执行 → 返回结果 → Central 决策

模式 B: 委托-执行-审核（Delegate-Execute-Verify）
  Central → delegate to Code → Code 执行 → Central 触发 Verifier → Verifier 审核 → 返回报告

模式 C: 并行委托-聚合（Parallel-Delegate-Merge）
  Central → 同时 delegate 给 Code + Browser → 各自执行 → Central 聚合结果

模式 D: 诊断-修复-验证（Diagnose-Fix-Validate）
  Fault 发现问题 → 通知 Central → Central 指派 Code 修复 → Verifier 验证
```

---

## 5. L0 元认知学习系统

### 5.1 四层学习架构

```
L3: 中央大脑经验记忆（跨项目）
  └── 项目级模式："这种项目应该先做数据库设计"
  └── 用户偏好："用户喜欢简洁的代码风格"
  └── 失败教训："不要在这种场景用 browser brain"

L2: 序列优化学习（工作流级）
  └── DAG 执行顺序优化
  └── 并行度自适应
  └── 预算分配策略

L1: 能力画像学习（大脑级）
  └── 每个 brain 的成功率、延迟、成本画像
  └── 任务类型 → 最佳 brain 映射
  └── 动态权重调整

L0: 领域自适应（工具级）
  └── 每个 brain 内部参数自调整
  └── 工具成功率追踪
  └── 环境适应性
```

### 5.2 智能化学习（不只是统计）

**当前系统的问题**：学习是简单的 EWMA 统计，没有真正的"理解"。

**升级方向**：

1. **因果学习**: 不只是"A 之后发生了 B"，而是"A 导致了 B，因为……"
2. **迁移学习**: 从项目 A 学到的经验应用到项目 B
3. **主动学习**: 系统主动请求反馈（"我不确定这个决策对不对，请确认"）
4. **对抗学习**: 模拟最坏情况，训练系统的鲁棒性

### 5.3 学习数据持久化

所有学习数据必须持久化：

```
SQLite/PostgreSQL 表结构：
- learning_profiles: 大脑能力画像
- task_outcomes: 任务结果记录
- project_patterns: 项目级模式
- user_preferences: 用户偏好
- failure_analysis: 失败分析
- evolution_log: 学习进化日志
```

---

## 6. EasyMVP 闭环工作流

### 6.1 完整流程

```
Phase 1: 需求理解
  用户输入需求 → Central 解析 → 生成需求文档 → 用户确认

Phase 2: 方案设计
  Central 设计方案 → 生成架构图/任务分解 → 用户确认

Phase 3: 方案审核（自动化闭环）
  ┌─────────────────────────────────────────────┐
  │  Verifier 审核方案                           │
  │     │                                       │
  │     ▼                                       │
  │  [通过?] ──Yes──► Phase 4                   │
  │     │ No                                    │
  │     ▼                                       │
  │  发现问题 → 自动生成修复任务                 │
  │     │                                       │
  │     ▼                                       │
  │  Central 重新设计/修改                       │
  │     │                                       │
  │     └──────────────► 再次审核（循环）       │
  └─────────────────────────────────────────────┘

Phase 4: 任务执行
  Central 按 DAG 调度任务 → 各专精大脑并行执行 → 进度实时同步
  
  （执行中可能触发 Phase 3 的审核-修正闭环）

Phase 5: 验收测试
  Verifier 全面验收 → Browser 端到端测试 → Fault 压力测试
  
  [全部通过?] ──Yes──► 项目完成
      │ No
      ▼
  生成修复任务 → 回到 Phase 4

Phase 6: 项目交付与复盘
  生成项目总结 → 学习系统记录经验 → 更新能力画像
```

### 6.2 状态机

```
[pending] ──► [analyzing] ──► [designing] ──► [reviewing] ──► [executing]
                                                           │
                        [reworking] ◄── [blocked] ◄─────────┘
                           │
                           └──► [completed] ──► [accepting] ──► [delivered]
                                                           │
                                                           └──► [rejected] ──► [reworking]
```

### 6.3 审核-修正闭环的自动化

**关键创新**：审核不是一次性的，而是自动化的循环。

```go
// 审核闭环控制器
type ReviewLoopController struct {
    MaxIterations int           // 最大迭代次数（防无限循环）
    ConvergenceThreshold float64 // 收敛阈值（问题数减少比例）
}

func (c *ReviewLoopController) Run(ctx context.Context, plan *TaskPlan) (*TaskPlan, error) {
    for i := 0; i < c.MaxIterations; i++ {
        // 1. 提交审核
        review := c.submitToVerifier(plan)
        
        // 2. 检查是否通过
        if review.Passed {
            return plan, nil
        }
        
        // 3. 分析问题，生成修复任务
        fixes := c.generateFixTasks(review.Issues)
        
        // 4. 执行修复
        fixedPlan := c.applyFixes(plan, fixes)
        
        // 5. 检查收敛
        if c.isConverged(review.Issues, fixedPlan) {
            return fixedPlan, fmt.Errorf("审核无法完全收敛，剩余 %d 个问题", len(review.Issues))
        }
        
        plan = fixedPlan
    }
    return plan, fmt.Errorf("达到最大审核迭代次数 %d", c.MaxIterations)
}
```

---

## 7. 实施路线图

### Phase 0: 文档与基线（现在）
- [x] 更新架构总纲（本文档）
- [ ] 更新所有落后设计文档
- [ ] 建立文档自动同步机制（代码变更 → 文档更新）

### Phase 1: 核心编排升级（1-2 周）
- [ ] 实现 TaskPlan 结构化任务规划
- [ ] 实现 InterruptSignal 中断重排机制
- [ ] 实现 ProjectProgress 进度追踪
- [ ] 升级 runner 支持动态预算调整
- [ ] 实现并发任务的冲突检测与仲裁

### Phase 2: 中央大脑智能化（2-3 周）
- [ ] 实现全局记忆持久化（项目级 SQLite）
- [ ] 实现上下文压缩与智能检索
- [ ] 实现元认知反思引擎
- [ ] 实现基于学习数据的任务复杂度预估
- [ ] 实现多模型路由（不同 brain 用不同 provider/model）

### Phase 3: EasyMVP 闭环（2-3 周）
- [ ] 实现方案审核自动化闭环
- [ ] 实现任务执行 DAG 调度
- [ ] 实现验收测试自动化
- [ ] 实现项目状态机全链路
- [ ] 实现审核-修正-确认循环

### Phase 4: 学习系统进化（3-4 周）
- [ ] 升级 L1 学习：从 EWMA 到因果学习
- [ ] 实现 L2 序列优化：DAG 执行顺序自适应
- [ ] 实现 L3 经验记忆：跨项目模式提取
- [ ] 实现主动学习：系统主动请求反馈

### Phase 5: 生产级硬化（持续）
- [ ] 全面测试覆盖（单元/集成/E2E）
- [ ] 性能基准与优化
- [ ] 可观测性（指标/日志/追踪）
- [ ] 混沌工程测试
- [ ] 安全审计

---

## 8. 关键设计原则

1. **长上下文优先**: 中央大脑必须用最长上下文的模型，这是架构基石
2. **持久化一切**: 所有状态、进度、反馈、学习数据必须持久化，不能存内存
3. **并发原生**: 系统设计之初就考虑并发，不是事后补丁
4. **反馈驱动**: 每个决策都基于反馈，没有反馈的系统是开环的、不可控的
5. **优雅中断**: 任务可以随时随地被中断和重排，且不会丢失状态
6. **模型差异化**: 不同 brain 用不同模型，不追求统一，追求最适合
7. **持续进化**: 系统每完成一个项目都比之前更聪明

---

*本文档是 MACCS v2.0 的架构总纲。所有子系统的设计文档必须以本文档为准。*
