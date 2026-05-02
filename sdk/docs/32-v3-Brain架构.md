# 32. Brain v3 架构总览

> **版本**: v3.0(全量对齐代码 + MACCS v2 编排层完整描述)
> **更新日期**: 2026-05-02
> **范围**: Brain SDK 全栈架构,从 transport 到编排层
> **配套文档**: 各子系统设计稿见 `sdk/docs/35-*.md`,顶级权威入口 `docs/README.md`

---

## 0. 一句话定义

> Brain v3 是一个**分层认知多智能体协作系统**(MACCS),由中央大脑 + 8 个专精大脑组成,
> 通过 JSON-RPC 双向通信、Capability Lease 并发控制、四层学习引擎和七阶段闭环编排,
> 实现"越用越聪明"的多 brain 真并行协作。

---

## 1. 四层架构(MACCS)

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  L3 战略层  Central Cortex(中央大脑)                                       │
│  - 全局记忆 ProjectMemory + MemoryRetriever                                  │
│  - 战略编排 PlanOrchestrator + ClosedLoopController(7 阶段闭环)             │
│  - 元认知反思 MetaCognitive + Reflect/Lessons/Recommendations                 │
│  - 模型路由 ModelRouter(差异化 LLM)                                        │
│  - 自适应 Prompt AdaptivePromptManager(A/B 变体)                            │
└──────────────────────────────────────────────────────────────────────────────┘
                                      │
                            (subtask.delegate)
                                      ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  L2 战术层  Specialist Corps(8 专精大脑)                                    │
│  ┌─────────┬─────────┬─────────┬──────────┐                                 │
│  │ code    │ browser │ verifier│ fault    │                                 │
│  ├─────────┼─────────┼─────────┼──────────┤                                 │
│  │ data    │ quant   │ desktop │ easymvp  │                                 │
│  └─────────┴─────────┴─────────┴──────────┘                                 │
│  - 各 brain 的 Runner.Execute LLM 主循环                                     │
│  - 各 brain 自有 L0 学习(brains/<kind>/learner.go)                          │
└──────────────────────────────────────────────────────────────────────────────┘
                                      │
                              (specialist.call_tool)
                                      ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  L1 反射层  Reflex Layer(工具与环境)                                       │
│  - read_file / write_file / edit_file / shell_exec / search                  │
│  - browser.* / desktop.* / fault.* / verifier.* 等专精工具                   │
│  - sandbox.go + command_sandbox_<os>.go(Linux/Darwin/Windows)               │
└──────────────────────────────────────────────────────────────────────────────┘
                                      │
                              (RecordSequence / Observe)
                                      ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  L0 元认知层  Meta-Cognitive Layer(学习与进化)                              │
│  - L1 BrainCapabilityProfile + RankBrains                                    │
│  - L2 SequenceLearner + RecommendOrder                                       │
│  - L3 ProjectMemory + UserPreference                                         │
│  - 5 项 Wave 5 增强:5.1 因果 / 5.2 迁移 / 5.3 主动 / 5.4 模式 / 5.5 Prompt │
│  - 5.6 能力画像可视化                                                         │
└──────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. 仓库布局

```
brain-v3/
├── cmd/brain/                CLI / HTTP 主入口(~30 .go 文件)
│   ├── chat/                 交互式 REPL
│   ├── command/              CLI 子命令
│   ├── config/               配置层(MACCSConfig 等)
│   └── dispatcher.go         16 个顶级子命令分发
├── sdk/                      28 个子包
│   ├── kernel/    ~80 文件   编排核心
│   ├── loop/      ~22 文件   Runner + Turn 主循环
│   ├── llm/       9 文件     Provider 抽象
│   ├── protocol/  8 文件     JSON-RPC 帧 + Method 常量
│   ├── tool/      ~70 文件   工具实现 + Sandbox
│   ├── sidecar/   16 文件    sidecar 端 RPC + serve
│   ├── agent/                Kind 类型定义
│   ├── events/               EventBus
│   ├── flow/                 Workflow + Edge
│   ├── observability/        Span / Trace
│   ├── persistence/          SQLite WAL 持久化
│   ├── runtimeaudit/         审计日志
│   ├── security/             权限/沙箱
│   ├── executionpolicy/      执行策略
│   ├── license/              授权 + Ed25519 签名
│   ├── toolguard/ toolpolicy/ 工具白名单
│   ├── netutil/ shared/ test/ testing/ ...
│   └── docs/                 子系统设计稿
├── brains/                   8 个真实大脑
│   ├── browser/  code/  data/  desktop/
│   ├── easymvp/  fault/  quant/  verifier/
│   └── hooks.example.json
├── central/docs/             中央大脑文档
├── shared/docs/              跨脑共享设计
├── docs/                     MACCS 顶级文档(权威入口)
├── scripts/                  构建/发布
├── npm/brain-cli/            npm 包装
└── go.mod / VERSION.json / CLAUDE.md / README.md
```

---

## 3. 8 个内置大脑

| Kind | 路径 | sidecar 入口 | 主要能力 |
|------|------|--------------|---------|
| `browser` | `brains/browser/` | `cmd/main.go` | UI 模式学习、anomaly 检测、CDP 控制 |
| `code` | `brains/code/` | `cmd/main.go` | 代码生成/编辑、shell 执行、测试 |
| `data` | `brains/data/` | **`cmd/brain-data-sidecar/main.go`**(双模式)| 数据处理、回放、质量评分 |
| `desktop` | `brains/desktop/` | `cmd/main.go` | 桌面自动化 |
| `easymvp` | `brains/easymvp/` | **`cmd/brain-easymvp-sidecar/main.go`** | 端到端 MVP 闭环 |
| `fault` | `brains/fault/` | `cmd/main.go` | 故障诊断与修复 |
| `quant` | `brains/quant/` | **`cmd/brain-quant-sidecar/main.go`**(双模式)| 量化交易策略、回测 |
| `verifier` | `brains/verifier/` | `cmd/main.go` | 审核验证、形式化检查 |

> **双模式说明**:`data/quant` 同时支持
> - **Sidecar 模式**:被 Kernel 通过 stdio JSON-RPC 调用(`cmd/brain-<kind>-sidecar/`)
> - **独立运行模式**:作为完整服务直接启动(`cmd/main.go`)

---

## 4. CLI 子命令(`cmd/brain/dispatcher.go`)

| 命令 | 用途 |
|------|------|
| `chat` | 交互式 REPL(类 Claude Code) |
| `run` | 单次 Run(prompt → result) |
| `serve` | 启动 HTTP/WebSocket 服务 |
| `status` / `resume` / `cancel` / `list` / `logs` / `replay` | Run 生命周期管理 |
| `tool` | 工具注册表管理 |
| `config` | 配置管理 |
| `brain` | 已安装大脑管理 |
| `pattern` | UI 模式库导入导出 |
| `demo` | 人类示范序列管理 |
| `doctor` | 环境诊断 |
| `version` | 版本信息 |

---

## 5. HTTP API 路由(`cmd/brain/cmd_serve.go`)

### 5.1 系统类
- `GET /health` / `GET /v1/readiness` / `GET /v1/health`
- `GET /v1/version` / `GET /v1/tools` / `GET /v1/brains`

### 5.2 可观测
- `GET /v1/metrics/perf` — PerfCollector P50/P95/P99
- `GET /v1/observability` — ObservabilityHub Span 查询(可 `?trace_id` 过滤)

### 5.3 三入口同源
| 端点 | 编排器 | 用途 |
|------|--------|------|
| `/v1/runs` 或 `/v1/executions` | Orchestrator | 单 Run |
| `/v1/plans` | PlanOrchestrator | 含 reflection / pattern / memory 的智能编排 |
| `/v1/projects` | ClosedLoopController | 7 阶段闭环(需求→交付→复盘) |

> 三个入口最终都收敛到 `Orchestrator.ExecuteTaskPlan`,差异在于"上层是否做反思/审核/复盘"。

### 5.4 Job / Workflow / Chaos
- `POST/GET /v1/jobs` / `GET /v1/jobs/{id}` — 后台任务
- `POST/GET /v1/workflows` — DAG 工作流
- `POST/DELETE /v1/chaos/experiments` / `GET /v1/chaos/history` — 混沌注入

---

## 6. 协议层(`sdk/protocol/`)

23 个 JSON-RPC 方法常量分 7 类(详见 `35-跨脑通信协议设计.md`):

- 系统 3:`initialize` / `shutdown` / `heartbeat`
- LLM 4:`llm.complete` / `llm.stream` / `llm.requestDirectAccess` / `llm/stream/delta`
- 工具 2:`tool.invoke` / `specialist.call_tool`
- 编排 6:`plan.create` / `plan.update` / `subtask.delegate` / `artifact.put` / `artifact.get` / `human/request_takeover`
- 观测 5:`brain/metrics` / `brain/progress` / `brain/stream/write` / `trace.emit` / `audit.emit`
- MACCS 1.6 进度 2:`progress/report` / `progress/query`
- MACCS 1.3 中断 1:`interrupt/send`

**3 种 transport**:stdio(本地 sidecar)/ HTTP(远程同步)/ WebSocket(远程双向)

---

## 7. 编排核心(`sdk/kernel/`)

### 7.1 Orchestrator — 单层 delegate 路由(`orchestrator.go`)

公开 API:
- `Delegate(req)` / `DelegateBatch(batch)` — 派发
- `ExecuteTaskPlan(plan, progress, reporter)` — 执行 TaskPlan
- `CallTool(req)` — 专家反向调主进程工具
- 11 个 `With*` Option 注入(ContextEngine / CapabilityMatcher / LearningEngine / SemanticApprover / LeaseManager / MCPBrainPool / ChaosEngine / PerfCollector / Observability / ReviewLoop)

### 7.2 PlanOrchestrator — 智能编排(`plan_orchestrator.go`)

```go
func (po *PlanOrchestrator) ExecuteProject(ctx, plan) (*ProjectExecutionResult, error)
```

集成 ReviewLoop(MACCS 1.10)、MetaCognitive(MACCS 2.4)、DynamicBudget(MACCS 1.9)、MemoryRetriever(MACCS 2.2)、ContextEngine(MACCS 2.5)、ModelRouter(MACCS 2.6)、PatternExtractor(MACCS 5.4)、ExperienceStore、ProjectMemory、`consumeFeedbackRequests` goroutine(MACCS 5.3 反馈订阅)。

### 7.3 ClosedLoopController — 7 阶段闭环(`closed_loop_controller.go`)

```
Phase 1: Requirement(RequirementParser)
Phase 2: Design(DefaultDesignGenerator)
Phase 3: Review(DesignReviewLoop)
Phase 4: Schedule(ExecutionScheduler.RunPlan)
Phase 5: Test(AcceptanceTester,exec.CommandContext 真跑)
Phase 6: Delivery(DefaultDeliveryGenerator,生成 README/CHANGELOG)
Phase 7: Retrospect(RetrospectiveEngine,L2/L3 学习写回)
```

---

## 8. 并发控制(MACCS Wave 4 + Wave 7)

| 组件 | 文件 | 责任 |
|------|------|------|
| `LeaseManager` | `lease.go` | 资源锁(canonical-order 防死锁) |
| `ConflictDetector` | `conflict_detector.go` | 任务级资源冲突检测(MACCS 4.2) |
| `SmartScheduler` | `smart_scheduler.go` | 冲突感知重排(MACCS 4.5) |
| `DeadlockDetector` | `deadlock_detector.go` | wait-for graph DFS 检环(MACCS 4.3) |
| `Arbiter` | `arbiter.go` | 死锁仲裁选 victim(MACCS 4.4) |
| `ExecutionScheduler` | `execution_scheduler.go` | 把以上组装到 RunPlan + Wave 7 接入 |

详见 `35-Dispatch-Policy-冲突图与Batch分组算法.md`。

---

## 9. 学习层(MACCS Wave 5 全 6 项)

| 组件 | 文件 | MACCS |
|------|------|-------|
| `SequenceLearner` + `LearningEngine` | `learning.go` | L1 + L2 |
| `CausalLearner` | `causal_learning.go` | 5.1 因果 |
| `TransferLearner` | `transfer_learning.go` | 5.2 迁移 |
| `ActiveLearner` | `active_learning.go` | 5.3 主动 |
| `PatternExtractor` | `pattern_extraction.go` | 5.4 模式 |
| `AdaptivePromptManager` | `adaptive_prompt.go` | 5.5 自适应 Prompt |
| `CapabilityAssessor` | `capability_profile.go` | 5.6 画像 |

**核心路由公式**(0139b5e Wave 7):
```
combined = capScore*0.4 + learnScore*0.25 + causalScore*0.35
```

详见 `35-自适应学习L1-L3算法设计.md`。

---

## 10. 生产硬化(MACCS Wave 6)

| 组件 | 文件 | MACCS | API |
|------|------|-------|-----|
| `HealthManager` | `health_check.go` | 6.1 | `GET /v1/health` |
| `ChaosEngine` | `chaos_engine.go` | 6.2 | `POST/DELETE /v1/chaos/experiments` |
| `PerfCollector` | `perf_benchmark.go` | 6.3 | `GET /v1/metrics/perf` |
| `ObservabilityHub` | `observability.go` | 6.4 | `GET /v1/observability` |
| `SecurityAuditor` | `security_audit.go` | 6.5 | `POST /v1/projects` 入参审计 |
| `MultiProjectManager` | `multi_project.go` | 6.6 | 项目级配额 + 429 |
| `ReadinessChecker` | `production_readiness.go` | 6.7 | `GET /v1/readiness` |

---

## 11. 配置层(`cmd/brain/config/config.go`)

### 11.1 Config 顶级字段
`Mode` / `Endpoint` / `DefaultBrain` / `DefaultModel` / `Output` / `LogLevel` / `Diagnostics` / `Timeout` / `Budget` / `ChatMode` / `PermissionMode` / `ServeWorkdirPolicy` / `APIKey` / `BaseURL` / `Model` / `Providers` / `ActiveProvider` / `Sandbox` / `FilePolicy` / `ToolProfiles` / `ActiveTools` / **`MACCS`**

### 11.2 MACCSConfig 9 块
| 块 | 关联 Wave | 默认 |
|---|----------|------|
| `Health` | 6.1 | enabled=true |
| `Perf` | 6.3 | enabled=true |
| `Observability` | 6.4 | enabled=true |
| `Security` | 6.5 | enabled=true, reject_severity="high" |
| `MultiProject` | 6.6 | max_concurrent=3, queue_size=16 |
| `AdaptivePrompt` | 5.5 | enabled=true |
| `Conflict` | 4.2/4.5 | enabled=true, dry_run=true |
| `PatternExtractor` | 5.4 | enabled=true |
| **`Deadlock`**(Wave 7) | 4.3/4.4 | enabled=true, dry_run=true |

---

## 12. 持久化(`sdk/persistence/`)

SQLite WAL 统一,数据库文件 `~/.brain/brain.db`(Phase E)。

| 表 | 用途 |
|----|------|
| `runs` / `task_executions` | Run 生命周期 |
| `learning_*` | L1/L2/L3 学习状态(c4fe85b 自动 Save/Load) |
| `audit_log` | 审计事件 |
| `project_memory` | 项目级记忆 |
| `shared_messages` | 跨脑共享消息(异步写入) |
| `experience` / `pattern` | MACCS 5.4 模式提取 |

---

## 13. 远程双模式(`sdk/discovery.go` + `remote*.go`)

```
本地: stdio + ProcessRunner → 进程内 sidecar
远程: HTTP/WebSocket BidirRPC → DiscoverableBrainPool
        + ServiceDiscovery(DNS SRV / Static)
        + CircuitBreaker
```

详见 `37-远程专精大脑调用说明.md`。

---

## 14. 安全模型(详见 `23-安全模型.md`)

```
Zone:    public / restricted / private
审批:   trivial → notable → safety → critical(SemanticApprover 4 级)
权限:   open / restricted(file_policy + sandbox)
组织级: OrgPolicy + PermissionMatrix + RevocationList(企业版)
工具:   ToolGuard 白名单 + 风险评估 + 失败日志
```

---

## 15. 完整执行链路示例

```
用户输入 → CLI 'brain run' 或 HTTP POST /v1/projects
  ↓
ClosedLoopController.Execute(projectName, goal)
  Phase 1 RequirementParser:goal → RequirementSpec
  Phase 2 DesignGenerator:spec → DesignProposal
  Phase 3 DesignReviewLoop:proposal 审核循环 → 通过
  Phase 4 ExecutionScheduler.RunPlan(buildplan(proposal))
        BuildExecutionPlan:
          plan.ComputeParallelLayers()              # 拓扑分层
          SmartScheduler.Reschedule(layers, decls)  # 冲突感知重排
        RunPlan loop:
          NextBatch → ConflictDetector.Detect → 报 blocker
          if blocker:
            DeadlockDetector.AddWaitEdge → Detect 检环
            if 环:Arbiter.ResolveDeadlock 选 victim → MarkFailed
          DelegateBatch:
            for each task in batch:
              Orchestrator.delegateOnce:
                resolveTargetKind:cap*0.4 + learn*0.25 + causal*0.35
                ContextEngineWithMemory.Assemble(注入项目记忆)
                AdaptivePromptManager.SelectVariant(注入 system prompt)
                BrainPool.AcquireBrain → BidirRPC
                RPC subtask.delegate → sidecar Runner.Execute
                  sidecar Runner 内部:
                    LLM 调用 → tool_use → specialist.call_tool 反向到 host
                    Notify progress/report / trace.emit / audit.emit
                    task_complete tool 触发 run 终止
                返回 result
              recordDelegateOutcome:L2 RecordSequence + 5.1 Causal Observe
              assessActiveLearning:5.3 → EventBus brain.feedback.requested
                consumeFeedbackRequests:写 ProjectMemory lesson
  Phase 5 AcceptanceTester:exec.CommandContext 真跑测试
  Phase 6 DeliveryGenerator:生成 README + CHANGELOG
  Phase 7 RetrospectiveEngine:写 L2/L3 学习记录
ExecuteProject 后台:
  runPatternExtraction:goroutine 异步抽 → ProjectMemory.Store(MACCS 5.4)
```

---

## 16. 关键事实速查

- **接入率**:**MACCS v2 全 48 项 100% 完成**(0139b5e + Wave 7)
- **架构层级**:L3 中央 → L2 8 大脑 → L1 工具 → L0 学习
- **3 入口同源**:runs / plans / projects → ExecuteTaskPlan
- **协议**:JSON-RPC 2.0 over stdio / HTTP / WebSocket
- **持久化**:SQLite WAL @ `~/.brain/brain.db`
- **路由公式**:`cap*0.4 + learn*0.25 + causal*0.35`(0139b5e)
- **active 探索率**:5%
- **lesson 阈值**:0.3(0139b5e 从 0.5 降下)
- **8 大脑**:code, browser, verifier, fault, data, quant, desktop, easymvp

---

## 17. 子系统设计稿索引(详细见各份)

| 子系统 | 设计稿 |
|-------|-------|
| BrainKernel 核心 | `02-BrainKernel设计.md` |
| 协议规格 | `20-协议规格.md` + `35-跨脑通信协议设计.md` |
| 错误模型 | `21-错误模型.md` |
| 安全模型 | `23-安全模型.md` |
| 可观测性 | `24-可观测性.md` |
| 测试策略 | `25-测试策略.md` |
| 持久化与恢复 | `26-持久化与恢复.md` |
| CLI 命令契约 | `27-CLI命令契约.md` |
| SDK 交付规范 | `28-SDK交付规范.md` |
| 第三方专精大脑开发 | `29-第三方专精大脑开发.md` |
| Manifest 规格 | `33-Brain-Manifest规格.md` |
| BrainPool | `35-BrainPool实现设计.md` |
| LeaseManager | `35-LeaseManager实现设计.md` |
| Dispatch Policy | `35-Dispatch-Policy-冲突图与Batch分组算法.md` |
| Context Engine | `35-Context-Engine详细设计.md` |
| 自适应学习 L1-L3 | `35-自适应学习L1-L3算法设计.md` |
| 能力匹配算法 | `35-BrainCapability标签与匹配算法.md` |
| TaskExecution 状态机 | `35-TaskExecution生命周期状态机.md` |
| 语义审批分级 | `35-语义审批分级设计.md` |
| Manifest 解析 | `35-Manifest解析与版本化设计.md` |
| Flow/Edge 注册发现 | `35-Flow-Edge存储与注册发现设计.md` |
| 统一 Dashboard | `35-统一Dashboard设计规格.md` |
| MCP Runtime | `35-MCP-backed-Runtime设计.md` |
| 远程专精大脑调用 | `37-远程专精大脑调用说明.md` |
| 控制论原则 | `钱学森工程控制论-设计原则.md` |

## 18. 上层文档

- 单一权威入口:`../../docs/README.md`
- MACCS 架构总纲:`../../docs/MACCS-架构总纲-v2.md`
- 中央大脑职责:`../../central/docs/38-中央大脑核心职责.md`
- 实施进度:`../../docs/MACCS-实施进度追踪.md`(48/48 = 100%)
- 实施路线图:`../../docs/MACCS-实施路线图.md`
