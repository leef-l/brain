# Brain v3 / MACCS 单一权威入口

> **版本**: v3.0(对应 brain v3 + MACCS v2 全 48 项 100% 完成)
> **最后更新**: 2026-05-02
> **读者**:新加入的开发者 / 想用 brain 的外部用户 / 项目维护者
> **作用**:30 分钟读完就能整体理解 brain v3,需要深入再跳到子文档

---

## 1. 它是什么

> Brain v3 是一个**分层认知多智能体协作系统**(MACCS — Multi-Agent Cognitive Collaboration System)。
> 由中央大脑 + 8 个专精大脑组成,通过 JSON-RPC 双向通信、Capability Lease 并发控制、四层学习引擎和七阶段闭环编排,
> 实现"越用越聪明"的多 brain 真并行协作。

### 与 Kimi 2.6 / OpenClaw / Hermes 的本质差异

| 维度 | 单 Agent 集群 (Kimi 2.6 / OpenClaw) | **Brain v3 / MACCS** |
|------|------------------------------------|---------------------|
| **上下文** | 单会话,无跨会话持久化 | 项目级长期记忆,SQLite 持久化 |
| **编排** | 预设工作流或简单轮询 | 智能动态编排,基于反馈实时调整 |
| **并发** | 伪并发或顺序执行 | 真 DAG 层内并行 + 智能资源调度 |
| **学习** | 无在线学习 | L0-L3 四层 + 5 项 Wave 5 增强 |
| **反馈闭环** | 无 | 审核→修正→重审→直到通过 |
| **中断重排** | 不支持任务中途调整 | 支持动态停止+按最新方案重排 |
| **模型差异化** | 统一模型 | 各 brain 按领域选最优 LLM |

---

## 2. 架构全景

### 2.1 四层认知架构

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  L3 战略层  Central Cortex (中央大脑)                                       │
│  - 全局记忆 ProjectMemory + MemoryRetriever (4 维加权检索)                   │
│  - 战略编排 PlanOrchestrator + ClosedLoopController (7 阶段闭环)            │
│  - 元认知反思 MetaCognitive + Reflect / Lessons / Recommendations            │
│  - 模型路由 ModelRouter (差异化 LLM)                                         │
│  - 自适应 Prompt AdaptivePromptManager (A/B 变体)                           │
└──────────────────────────────────────────────────────────────────────────────┘
                                      │ subtask.delegate
                                      ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  L2 战术层  Specialist Corps (8 专精大脑)                                    │
│  ┌──────────┬──────────┬──────────┬──────────┐                              │
│  │ code     │ browser  │ verifier │ fault    │                              │
│  ├──────────┼──────────┼──────────┼──────────┤                              │
│  │ data     │ quant    │ desktop  │ easymvp  │                              │
│  └──────────┴──────────┴──────────┴──────────┘                              │
└──────────────────────────────────────────────────────────────────────────────┘
                                      │ specialist.call_tool
                                      ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  L1 反射层  Reflex Layer (工具与环境)                                       │
│  read/write/edit/delete file - shell exec - search                           │
│  browser CDP - desktop - fault diagnose - verifier                           │
│  sandbox.go + command_sandbox_<linux/darwin/windows>.go                      │
└──────────────────────────────────────────────────────────────────────────────┘
                                      │ RecordSequence / Observe
                                      ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  L0 元认知层  Meta-Cognitive Layer (学习与进化)                              │
│  L1 BrainCapabilityProfile + RankBrains (EWMA + Wilson 折扣)                  │
│  L2 SequenceLearner + RecommendOrder                                          │
│  L3 ProjectMemory + UserPreference                                            │
│  Wave 5: 因果 / 迁移 / 主动 / 模式 / Prompt / 画像                            │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 仓库布局

```
brain-v3/
├── cmd/brain/                 CLI / HTTP 主入口 (~30 文件)
│   ├── chat/                  交互式 REPL
│   ├── command/               CLI 子命令
│   ├── config/                配置层 (MACCSConfig 等)
│   └── dispatcher.go          16 个顶级子命令分发
├── sdk/                       28 个子包
│   ├── kernel/  (~80)         编排核心
│   ├── loop/    (~22)         Run 主循环 + Turn
│   ├── llm/      (9)          Provider 抽象
│   ├── protocol/ (8)          JSON-RPC 帧 + Method 常量
│   ├── tool/    (~70)         工具实现 + Sandbox
│   ├── sidecar/ (16)          sidecar RPC + serve
│   ├── persistence/           SQLite WAL 持久化
│   ├── observability/         Span / Trace
│   ├── runtimeaudit/          审计
│   ├── security/              权限 / 沙箱
│   ├── flow/                  Workflow + Edge
│   ├── events/                EventBus
│   ├── license/               Ed25519 签名
│   ├── toolguard/ toolpolicy/ executionpolicy/  工具白名单 + 执行策略
│   ├── agent/ netutil/ shared/ test/ testing/   类型 / 工具
│   └── docs/                  子系统设计稿
├── brains/                    8 个真实大脑
│   ├── browser/  code/  data/  desktop/
│   ├── easymvp/  fault/  quant/  verifier/
│   └── hooks.example.json
├── central/docs/              中央大脑文档
├── shared/docs/               跨脑共享设计
├── docs/                      MACCS 顶级文档(权威入口)
├── scripts/                   构建/发布
├── npm/brain-cli/             npm 包装
└── go.mod / VERSION.json / CLAUDE.md / README.md
```

---

## 3. 关键执行链路

用户提交一个任务到产出全过程:

```
[用户输入] CLI 'brain run' 或 HTTP POST /v1/projects { goal: "做一个贪吃蛇游戏" }
    ↓
[1] ClosedLoopController.Execute(projectName, goal)         sdk/kernel/closed_loop_controller.go:98
    Phase 1 RequirementParser              goal → RequirementSpec
    Phase 2 DesignGenerator                spec → DesignProposal
    Phase 3 DesignReviewLoop               proposal 审核循环
    Phase 4 ExecutionScheduler.RunPlan     执行(详见下方)
    Phase 5 AcceptanceTester               真跑测试 (exec.CommandContext)
    Phase 6 DeliveryGenerator              生成 README + CHANGELOG
    Phase 7 RetrospectiveEngine            写 L2/L3 学习记录
    ↓
[2] ExecutionScheduler.RunPlan(execPlan)                    sdk/kernel/execution_scheduler.go:475
    BuildExecutionPlan:
       plan.ComputeParallelLayers()        拓扑分层
       SmartScheduler.Reschedule           冲突感知重排
    每层 RunPlan loop:
       NextBatch                            取本层 queued 任务
       ConflictDetector.Detect             资源冲突检测
       if blocker:
         DeadlockDetector.AddWaitEdge      Wait-For graph
         Detect 检环 → Arbiter.ResolveDeadlock 选 victim
       DelegateBatch:
         for each task: Orchestrator.delegateOnce
    ↓
[3] Orchestrator.delegateOnce(req)                          sdk/kernel/orchestrator.go:905
    resolveTargetKind:                     路由公式
       combined = capScore*0.4 + learnScore*0.25 + causalScore*0.35
       (5% 概率走 active learning 探索)
    ContextEngineWithMemory.Assemble      注入项目记忆 (15% TokenBudget)
    AdaptivePromptManager.SelectVariant   A/B Prompt 变体
    BrainPool.AcquireBrain                获取 sidecar 实例
    BidirRPC subtask.delegate             发起 stdio JSON-RPC
    ↓
[4] sidecar Runner.Execute (loop)                           sdk/loop/runner.go:164
    LLM 调用 → tool_use → specialist.call_tool 反向到 host
    Notify progress/report  (MACCS 1.6)    实时进度
    Notify trace.emit / audit.emit         可观测
    task_complete tool 触发 run 终止
    返回 result 给 host
    ↓
[5] Orchestrator.recordDelegateOutcome(req, result)
    SequenceLearner.RecordSequence (L2)
    CausalLearner.Observe + LearnRelations (5.1)
    assessActiveLearning:                  不确定时
       EventBus.Publish "brain.feedback.requested"
       consumeFeedbackRequests goroutine 收到 → 写 ProjectMemory lesson
    ↓
[6] ExecuteProject 后台:
    runPatternExtraction goroutine 异步抽 → ProjectMemory.Store (5.4)
    ↓
[7] 用户看到结果
    Phase 6 交付物 (README/CHANGELOG)
    Phase 7 复盘报告
```

---

## 4. MACCS 48 项能力对照表

> **接入率**:**48/48 = 100% 全量完成**(0139b5e + Wave 7)

| Wave | 任务 | 组件 | 代码位置 | 状态 |
|------|-----|------|---------|------|
| 0.1 | task_complete 终止 | runner.go | sdk/loop/runner.go:397 | ✅ |
| 0.2 | LLM 超时 90→180s | anthropic_provider | sdk/llm/anthropic_provider.go | ✅ |
| 0.3 | serve turn 20→50 | cmd_serve | cmd/brain/cmd_serve.go | ✅ |
| 1.1 | TaskPlan | task_plan | sdk/kernel/task_plan.go | ✅ |
| 1.2 | ProjectProgress | project_progress | sdk/kernel/project_progress.go | ✅ |
| 1.3 | InterruptSignal | interrupt | sdk/kernel/interrupt.go | ✅ |
| 1.4 | Runner 中断 | loop/interrupt | sdk/loop/interrupt.go | ✅ |
| 1.5 | Checkpoint 增强 | loop/checkpoint | sdk/loop/checkpoint.go | ✅ |
| 1.6 | 进度 RPC 双方法 | progress_rpc | sdk/kernel/progress_rpc.go | ✅ |
| 1.7 | 进度持久化 | progress_store | sdk/kernel/progress_store.go | ✅ |
| 1.8 | Orchestrator 集成 | orchestrator_plan | sdk/kernel/orchestrator_plan.go:38 | ✅ |
| 1.9 | 动态预算池 | dynamic_budget | sdk/kernel/dynamic_budget.go | ✅ |
| 1.10 | ReviewLoop 任务级 | review_loop | sdk/kernel/review_loop.go + WithReviewLoop | ✅ |
| 2.1-2.7 | 中央大脑智能化 | plan_orchestrator + 6 组件 | sdk/kernel/plan_orchestrator.go | ✅ |
| 3.1-3.10 | 7 阶段闭环 | closed_loop_controller | sdk/kernel/closed_loop_controller.go | ✅ |
| 4.1 | 资源访问追踪 | (并入 ExecutionScheduler) | sdk/kernel/execution_scheduler.go | ✅ |
| 4.2 | 冲突检测 | conflict_detector | sdk/kernel/conflict_detector.go | ✅ |
| 4.3 | 死锁检测(Wave 7) | deadlock_detector | sdk/kernel/deadlock_detector.go | ✅ |
| 4.4 | 仲裁策略(Wave 7) | arbiter | sdk/kernel/arbiter.go | ✅ |
| 4.5 | 智能重排 | smart_scheduler | sdk/kernel/smart_scheduler.go | ✅ |
| 5.1 | 因果学习(权重 0.35) | causal_learning | sdk/kernel/causal_learning.go | ✅ |
| 5.2 | 迁移学习 | transfer_learning | sdk/kernel/transfer_learning.go | ✅ |
| 5.3 | 主动学习+订阅 | active_learning | sdk/kernel/active_learning.go + plan_orchestrator.go:159 | ✅ |
| 5.4 | 模式提取异步 | pattern_extraction | sdk/kernel/pattern_extraction.go + plan_orchestrator.go:361 | ✅ |
| 5.5 | 自适应 Prompt | adaptive_prompt | sdk/kernel/adaptive_prompt.go | ✅ |
| 5.6 | 能力画像 | capability_profile | sdk/kernel/capability_profile.go | ✅ |
| 6.1 | HealthManager | health_check | sdk/kernel/health_check.go | ✅ |
| 6.2 | ChaosEngine | chaos_engine | sdk/kernel/chaos_engine.go | ✅ |
| 6.3 | PerfBenchmark | perf_benchmark | sdk/kernel/perf_benchmark.go | ✅ |
| 6.4 | Observability | observability | sdk/kernel/observability.go | ✅ |
| 6.5 | SecurityAuditor | security_audit | sdk/kernel/security_audit.go | ✅ |
| 6.6 | MultiProjectManager | multi_project | sdk/kernel/multi_project.go | ✅ |
| 6.7 | ProductionReadiness | production_readiness | sdk/kernel/production_readiness.go | ✅ |

完整逐项追踪见 `MACCS-实施进度追踪.md`。

---

## 5. 配置与运行

### 5.1 安装

```bash
# 方式 A: 从 GitHub Releases 下载预编译二进制(推荐)
# 见仓根 README.md §1

# 方式 B: 源码编译
git clone https://github.com/leef-l/brain.git
cd brain

# Linux/macOS
scripts/release/build-assets.sh 0.7.0

# Windows
scripts\release\build-assets.bat 0.7.0
```

产出 12 个二进制 → `dist/` 和 `$GOPATH/bin/`:
- `brain` (主 CLI)
- `brain-central`
- `brain-{code,browser,verifier,fault,desktop}-sidecar`
- `brain-{data,quant}` (独立模式) + `brain-{data,quant,easymvp}-sidecar`

### 5.2 配置文件 `~/.brain/config.json`

```jsonc
{
  // LLM Provider (单 provider 简写)
  "providers": {
    "anthropic": {
      "base_url": "https://api.anthropic.com",
      "api_key": "${ANTHROPIC_API_KEY}",
      "model": "claude-sonnet-4-6"
    }
  },
  "active_provider": "anthropic",

  // 安全模式
  "permission_mode": "open",                       // open / restricted
  "file_policy": {
    "allow_read":   ["**"],
    "allow_create": ["**"],
    "allow_edit":   ["**"],
    "deny": [".git/**", "**/.env", "**/secrets/**"]
  },

  // brain 注册(配置驱动 — 优先于自动发现)
  "brains": [
    { "kind": "code", "binary": "/usr/local/bin/brain-code-sidecar",
      "auto_start": true, "max_instances": 5,
      "model": "claude-sonnet-4-6" }
  ],

  // MACCS 配置 — 9 块全部默认启用
  "maccs": {
    "health":            { "enabled": true },
    "perf":              { "enabled": true },
    "observability":     { "enabled": true },
    "security":          { "enabled": true, "reject_severity": "high" },
    "multi_project":     { "enabled": true, "max_concurrent": 3, "queue_size": 16 },
    "adaptive_prompt":   { "enabled": true },
    "conflict":          { "enabled": true, "dry_run": true },
    "pattern_extractor": { "enabled": true },
    "deadlock":          { "enabled": true, "dry_run": true }
  }
}
```

### 5.3 启动

```bash
# 单次执行
brain run "做一个贪吃蛇游戏"

# 交互式 REPL
brain chat

# HTTP 服务
brain serve --addr :8080

# 完整 CLI 命令
brain chat / run / serve / status / resume / cancel / list / logs / replay
brain tool / config / brain / pattern / demo / doctor / version
```

### 5.4 HTTP API 调用

**三个执行入口**(同源到 ExecuteTaskPlan):

```bash
# 单次 Run(轻量)
curl -X POST http://localhost:8080/v1/runs \
  -H 'Content-Type: application/json' \
  -d '{ "goal": "Hello World", "brain": "code" }'

# 智能编排 Plan(含反思 + pattern + memory)
curl -X POST http://localhost:8080/v1/plans \
  -d '{ "goal": "...", "project_id": "..." }'

# 七阶段闭环 Project(完整 MACCS)
curl -X POST http://localhost:8080/v1/projects \
  -d '{ "project_name": "snake-game", "goal": "做一个贪吃蛇" }'
```

**观测**:

```bash
GET /v1/health             # 持续监控 (HealthManager)
GET /v1/readiness          # 启动期就绪 (7 项)
GET /v1/metrics/perf       # P50/P95/P99
GET /v1/observability      # Span 查询
GET /v1/brains             # 已注册大脑
GET /v1/tools              # 工具注册表
```

### 5.5 多项目管理(MACCS Wave 7+ 项目级持久化)

每个工作目录(workdir)可以对应**多个项目**(project),每个项目有独立 ID + 对话历史 + 项目记忆。

**chat 模式**:启动时强制选择已有项目 / 新建 / 跳过持久化:
```cmd
$ cd /home/u/my-product
$ brain chat
  当前工作目录下有 3 个项目:
    [1] todo-app          (上次活动: 2 小时前)
    [2] auth-redesign     (上次活动: 昨天)
    [3] perf-optimization (上次活动: 30 分钟前)
    [n] 新建项目
    [s] 不使用项目(单次对话,不持久化)
```

CLI flag 跳过交互:
```bash
brain chat --project todo-app          # 直接进 todo-app(不存在则新建)
brain chat --new-project xxx           # 强制新建
brain chat --no-project                # 直接进无项目模式
```

chat 内 `/project` 9 个子命令:
```
/project list / current / info
/project new <name> / switch <name|id> / rename <new>
/project delete <name|id> / save <name> / help
```

**run 模式**:默认**不持久化**(向后兼容);需 `--project NAME` 或 `--no-project` 显式声明。

**HTTP API**(`/v1/runs`):
```json
POST /v1/runs
{
  "prompt": "...",
  "brain": "central",
  "project_id": "abc123def456"            // 已有项目 ID
  // 或
  "project_name": "my-app",
  "workdir": "/home/u/my-product"          // 新建/找(workdir 必传)
}
```

详见 [`sdk/docs/35-项目级记忆与多项目管理.md`](../sdk/docs/35-项目级记忆与多项目管理.md)。

---

### 5.6 CLI 完整命令(详见 `sdk/docs/27-CLI命令契约.md`)

| 命令 | 用途 |
|------|------|
| `chat` | 交互式 REPL(类 Claude Code) |
| `run` | 单次 Run |
| `serve` | 启动 HTTP/WebSocket 服务 |
| `status` `resume` `cancel` `list` `logs` `replay` | Run 生命周期 |
| `tool` `config` `brain` `pattern` `demo` | 资源管理 |
| `doctor` | 环境诊断 |
| `version` | 版本信息 |

---

## 6. 进一步阅读

按主题深入:

### 6.1 MACCS 顶级文档(本目录)

| 文档 | 内容 |
|------|------|
| **本文** `docs/README.md` | 单一权威入口 |
| `MACCS-架构总纲-v2.md` | L0-L3 分层 + 与 Kimi 对比 + 核心理念 |
| `MACCS-中央大脑智能化编排规范.md` | TaskPlan / ProjectProgress / 动态预算 / 质量闭环 |
| `MACCS-实施进度追踪.md` | 48 项任务逐项追踪(48/48 = 100%) |
| `MACCS-实施路线图.md` | 7 个 Wave 实施记录 + 里程碑 |
| `工程控制论-简体/` | 钱学森原书 22 章理论基础 |

### 6.2 SDK 设计文档(`sdk/docs/`)

**核心规范(稳定)**:
- `02-BrainKernel设计.md` Frame / BidirRPC / 生命周期
- `20-协议规格.md` JSON-RPC 帧 + 方法命名空间
- `21-错误模型.md` 错误码体系
- `23-安全模型.md` Zone / 审批 / 工具控制
- `24-可观测性.md` 指标 / 日志 / 健康
- `25-测试策略.md`
- `26-持久化与恢复.md` SQLite WAL
- `27-CLI命令契约.md` 16 个子命令规范
- `28-SDK交付规范.md`

**架构与子系统**:
- `32-v3-Brain架构.md` ⭐ v3 总体架构
- `29-第三方专精大脑开发.md` ⭐ 第三方接入指南
- `33-Brain-Manifest规格.md` brain.json 字段
- `34-Brain-Package与Marketplace规范.md` 发布与商业化
- `35-BrainPool实现设计.md` 进程管理
- `35-LeaseManager实现设计.md` 资源锁
- `35-Dispatch-Policy-冲突图与Batch分组算法.md` 冲突 + 死锁 + 仲裁
- `35-Context-Engine详细设计.md` 上下文装配 + 项目记忆
- `35-自适应学习L1-L3算法设计.md` ⭐ Wave 5 全 6 项
- `35-跨脑通信协议设计.md` ⭐ 23 method + EventBus
- `35-BrainCapability标签与匹配算法.md` 路由公式
- `35-Manifest解析与版本化设计.md`
- `35-Flow-Edge存储与注册发现设计.md`
- `35-TaskExecution生命周期状态机.md`
- `35-语义审批分级设计.md`
- `35-统一Dashboard设计规格.md`
- `35-MCP-backed-Runtime设计.md`
- `37-远程专精大脑调用说明.md` 远程接入

**Browser Brain**:
- `39-Browser-Brain感知与嗅探增强设计.md`
- `40-Browser-Brain语义理解架构.md`
- `42-Browser-Brain异常感知层设计.md`

**理论基础**:
- `钱学森工程控制论-设计原则.md`

### 6.3 Brain 子模块文档

- `brains/data/docs/` 数据大脑
- `brains/quant/docs/` 量化大脑
- `central/docs/38-中央大脑核心职责.md` ⭐ 中央大脑职责定义
- `shared/docs/` 跨脑共享(三脑架构 / 数据库 / 目录)

### 6.4 用户向

- 仓根 `README.md` — 安装、CLI、HTTP API、配置完整说明
- 仓根 `CHANGELOG.md` — 版本更新历史
- 仓根 `SECURITY.md` — 安全策略

---

## 7. 关键事实速查

- **接入率**:**48/48 = 100%** ✅
- **架构**:L3 中央 → L2 8 大脑 → L1 工具 → L0 学习
- **3 入口同源**:`/v1/runs` / `/v1/plans` / `/v1/projects` → ExecuteTaskPlan
- **协议**:JSON-RPC 2.0 over stdio / HTTP / WebSocket
- **持久化**:SQLite WAL @ `~/.brain/brain.db`
- **路由公式**(Wave 7):`combined = cap*0.4 + learn*0.25 + causal*0.35`
- **active 探索率**:5%
- **lesson 阈值**:0.3
- **Wave 5 五项升级**:因果 / 迁移 / 主动 / 模式 / Prompt
- **Wave 6 七项硬化**:Health / Chaos / Perf / Observability / Security / MultiProj / Readiness
- **Wave 7 死锁路径**:从 ConflictDetector 语义层接入,LeaseManager 不动

---

## 8. 关键代码位置速查(给新手快速找路)

```
入口        cmd/brain/dispatcher.go              16 个 CLI 子命令
HTTP API    cmd/brain/cmd_serve.go               全部路由
配置        cmd/brain/config/config.go           Config + MACCSConfig

编排核心    sdk/kernel/orchestrator.go           Delegate + delegateOnce + resolveTargetKind:1591
            sdk/kernel/plan_orchestrator.go      ExecuteProject + consumeFeedbackRequests
            sdk/kernel/closed_loop_controller.go 7 阶段闭环
            sdk/kernel/execution_scheduler.go    RunPlan + AttachConflictControl/DeadlockControl

学习层      sdk/kernel/learning.go               L1/L2/L3 主体
            sdk/kernel/causal_learning.go        5.1
            sdk/kernel/transfer_learning.go      5.2
            sdk/kernel/active_learning.go        5.3
            sdk/kernel/pattern_extraction.go     5.4
            sdk/kernel/adaptive_prompt.go        5.5
            sdk/kernel/capability_profile.go     5.6

并发控制    sdk/kernel/lease.go                  Capability Lease
            sdk/kernel/conflict_detector.go      4.2
            sdk/kernel/deadlock_detector.go      4.3
            sdk/kernel/arbiter.go                4.4
            sdk/kernel/smart_scheduler.go        4.5

主循环      sdk/loop/runner.go                   Runner.Execute
            sdk/loop/turn_executor.go            Turn 执行
            sdk/loop/compressor.go               Context 压缩

LLM         sdk/llm/anthropic_provider.go        Anthropic
            sdk/llm/openai_provider.go           OpenAI
            sdk/llm/provider.go                  接口

协议        sdk/protocol/methods.go              23 个 method 常量
            sdk/protocol/frame.go                帧格式
            sdk/protocol/rpc.go                  BidirRPC

工具        sdk/tool/builtin_*.go                内置工具
            sdk/tool/registry.go                 注册表
            sdk/tool/sandbox.go                  沙箱

sidecar     sdk/sidecar/sidecar.go               BrainHandler + Run
            sdk/sidecar/streaming.go             跨端流式

8 大脑       brains/{browser,code,data,desktop,easymvp,fault,quant,verifier}/
```

---

## 9. 贡献与反馈

- 提 Issue:GitHub Issues
- PR 流程:见仓根 `CONTRIBUTING.md`(若存在)
- 安全漏洞:见仓根 `SECURITY.md`

**MACCS v2 全 48 项 100% 完成 🎉,准备走向 v3 / Wave 8...**
