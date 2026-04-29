# Brain 项目核心 SDK 与中央大脑代码分析

> 分析日期：2026-04-25
> 分析范围：SDK 全包（agent/、cli/、kernel/、loop/、protocol/、persistence/、tool/、sidecar/、llm/、security/ 等）、central/ 中央大脑、cmd/brain/ CLI 层
> **相关文档**：`memory/Brain项目系统分析与总结.md`（系统级审计视角，含 6 大脑逐一盘点、brain.json 一致性检查、CLI 命令全景、能力缺口清单）

---

## 一、SDK 各子包功能职责与接口定义

### 1.1 `agent/` — 大脑角色抽象

| 职责 | 说明 |
|------|------|
| **Kind 定义** | 定义 8 种内置大脑角色：`central`、`code`、`browser`、`verifier`、`fault`、`data`、`quant`、`desktop`，以及第三方扩展机制 |
| **Descriptor** | 描述大脑的 initialize 握手信息：Kind、Version、LLMAccessMode（proxied/direct/hybrid）、SupportedTools、Capabilities |
| **Agent 接口** | `Kind()` / `Descriptor()` / `Ready(ctx)` / `Shutdown(ctx)` |
| **RPCAgent 扩展** | 增加 `RPC()` 方法，暴露底层 BidirRPC 会话供 Orchestrator 注册反向 RPC handler |
| **BuiltinKinds()** | 返回 7 个专精大脑（不含 central），用于 Orchestrator 的向后兼容探测 |

**关键设计点**：Kind 是 `string` 类型而非 enum，允许第三方大脑零代码修改注册。LLMAccessMode 三种策略对应安全 Zone 模型（Zone-3 才能 direct）。

### 1.2 `protocol/` — 通信协议层

| 职责 | 说明 |
|------|------|
| **Frame 层** | `FrameReader` / `FrameWriter` 实现 Content-Length 定界帧（`Content-Length: N\r\n\r\n{body}`），支持 stdio/websocket 等多传输 |
| **BidirRPC** | 全双工 JSON-RPC 2.0 抽象，核心实现为 `bidirRPC` 结构体 |
| **ID 命名空间** | `k:<seq>`（Host/Kernel 端）vs `s:<seq>`（Sidecar 端），通过前缀路由区分自发出请求 vs 对端请求 |
| **方法常量** | `brain/execute`、`tools/call`、`llm.complete`、`llm.stream`、`subtask.delegate`、`specialist.call_tool`、`brain/metrics`、`brain/learn`、`brain/progress`、`human.request_takeover`、`$/shutdown`、`$/cancelRequest` 等 |
| **生命周期** | `Lifecycle` 枚举：`Pending → Running → WaitingTool → Completed / Failed / Canceled` |

**BidirRPC 关键机制**：
- **in-flight 窗口**：Kernel 端 32、Sidecar 端 64，防止内存泄漏
- **stale response 计数**：等待者已离开时收到的响应被计数，供健康监控
- **cancelRequest**：支持 `$` 前缀的内置取消通知，取消指定 ID 的 pending Call
- **并发调度**：每个请求在新 goroutine 中执行 handler，reader loop 永不阻塞

### 1.3 `loop/` — Agent Loop 执行引擎

| 职责 | 说明 |
|------|------|
| **Runner** | 驱动单条 Run 的生命周期：`pending → running → (tool loops) → completed/failed/canceled` |
| **Budget** | 五维预算控制：`MaxTurns` / `MaxCostUSD` / `MaxToolCalls` / `MaxLLMCalls` / `MaxDuration`，每轮强制检查 |
| **Turn 状态机** | `NewTurn` → build ChatRequest → LLM Call → dispatch tools → loop detection → state transition |
| **PreTurnHook** | 每轮开始前允许集成方动态重写本轮暴露的工具 schema 和 dispatch registry（用于 BrowserStage 自动切换等场景） |
| **Streaming** | `consumeStream` 将增量 token 合成为完整 `ChatResponse`，同时转发给 `StreamConsumer` |

**Runner.Execute 核心流程**（伪代码）：
```
run.Start()
for {
  run.Budget.CheckTurn()
  turn = NewTurn(run, turnIndex)
  // PreTurnHook 动态切换工具视图
  req = buildChatRequest(run, messages, opts)
  resp = Provider.Complete(req)  // 或 Provider.Stream
  run.Budget.CheckCost()
  messages = append(messages, assistantMessage(resp))
  toolUseBlocks = extractToolUse(resp)
  if no tool calls or stop_reason != "tool_use": break (completed)
  results = dispatchTools(ctx, toolUseBlocks)  // serial or batched
  messages = append(messages, toolResultMessage(results))
  if LoopDetector detects stuck: fail
}
```

### 1.4 `kernel/` — 大脑内核与编排

| 组件 | 职责 |
|------|------|
| **Kernel 结构体** | 顶级组装点，通过 functional options 装配所有 store、registry、provider、exporter。零耦合到具体实现（SQLite/MySQL、OTel/stdout） |
| **Orchestrator** | 管理专精大脑生命周期（启动/复用/关停）和四种调度机制：Delegate（子任务委派）、CallTool（胖大脑工具桥接）、BatchPlanner（同 turn 内 tool_call 并发分组）、ExecuteWorkflow（跨 brain DAG 编排） |
| **LLMProxy** | 处理 sidecar 反向 RPC 的 `llm.complete` / `llm.stream`，按 brain kind 路由到正确的 provider 和 model |
| **BrainPool (ProcessBrainPool)** | 全局共享进程池，多 Run 复用同一 sidecar，含 nil-placeholder 并发防重启动、健康检查、崩溃自动重启 |
| **CapabilityMatcher** | 三阶段能力匹配：硬标签过滤 → 软标签评分 → LearningEngine 加权排名 |
| **LearningEngine** | L1-L3 自适应学习：EWMA 能力画像、序列记录、异常模板、brain/metrics 摄入 |
| **TaskScheduler** | 任务级调度引擎，基于 L1 学习排名自动选择 brain |
| **ContextEngine** | 上下文装配与压缩：将 instruction + context 装配为消息列表，按 token 预算进行 summarization |
| **LeaseManager** | 资源租约管理，协调跨 brain 的资源互斥与共享访问 |

### 1.5 `persistence/` — 持久化层

| Store 类型 | 职责 | 实现 |
|------------|------|------|
| **PlanStore** | 持久化 BrainPlan + deltas | SQLite / FileStore |
| **ArtifactStore** | CAS-backed 内容寻址存储（文件内容按 SHA 去重） | `FSArtifactStore`（文件系统） |
| **ArtifactMetaStore** | Artifact 元数据索引（路径→SHA 映射、引用计数） | SQLite |
| **RunCheckpointStore** | Run 检查点，支持 resume | SQLite |
| **UsageLedger** | Token/cost 用量记录（per brain、per run） | SQLite |
| **RunStore** | Run 元数据和生命周期事件 | JSON（向后兼容） |
| **AuditLogger** | 审计日志，Zone 跨越事件 | `HashChainAuditLogger` |
| **LearningStore** | L1-L3 学习数据持久化 | SQLite |
| **SharedMessageStore** | 跨 brain 上下文传递（central ↔ specialist 的 shared bucket） | SQLite |
| **ResumeCoordinator** | 跨层级 resume 语义协调 | — |

**Driver 抽象**：`persistence.Open(driver, dsn)` 返回 `ClosableStores`，支持 `sqlite`（生产）、`file`（向后兼容）。

### 1.6 `tool/` — 工具注册与调用

| 职责 | 说明 |
|------|------|
| **Tool 接口** | `Name()` / `Schema()` / `Risk()` / `Execute(ctx, args)` |
| **Registry** | `MemRegistry` 内存注册表，支持 `Register` / `Lookup` / `List` |
| **Schema** | LLM-facing JSON Schema + `ToolConcurrencySpec`（并发控制说明） |
| **Result** | `Output`（json.RawMessage）+ `IsError`（语义错误标志） |
| **Risk 分级** | `Safe` / `Read` / `Write` / `Destructive`，供 Guardrail 和 fault_policy 使用 |

### 1.7 `llm/` — LLM Provider 抽象

| 职责 | 说明 |
|------|------|
| **Provider 接口** | `Complete(ctx, *ChatRequest) (*ChatResponse, error)` + `Stream(ctx, *ChatRequest) (StreamReader, error)` |
| **ChatRequest** | System + Messages + Tools + ToolChoice + Model + MaxTokens + CacheControl + RemainingBudget |
| **ContentBlock** | 统一表示 text / image / tool_use / tool_result / thinking / redacted_thinking |
| **Usage** | InputTokens / OutputTokens / CacheReadTokens / CacheCreationTokens / CostUSD |

### 1.8 `sidecar/` — Sidecar 运行时框架

| 职责 | 说明 |
|------|------|
| **BrainHandler 接口** | `Kind()` / `Version()` / `Tools()` / `HandleMethod(ctx, method, params)` |
| **RichBrainHandler** | 扩展 `SetKernelCaller`，获得向 Host 发起反向 RPC 的能力 |
| **Run 函数** | 每个 brain 的 `main()` 入口：信号处理 → stdio 接线 → RPC 初始化 → register handlers → 阻塞等待 |
| **内置 Handler** | `initialize`、`notifications/initialized`、`$/shutdown`、`tools/list`、`tools/call`、`brain/execute`、`brain/plan`、`brain/verify`、`brain/metrics`、`brain/learn` |

### 1.9 `cli/` — CLI 命令契约

| 职责 | 说明 |
|------|------|
| **13 个子命令** | `run`、`status`、`resume`、`cancel`、`list`、`logs`、`replay`、`tool`、`config`、`serve`、`doctor`、`version` |
| **Exit Code** | 定义 `ExitOK`、`ExitUsage`、`ExitSoftware`、`ExitNoPerm`、`ExitSignalTerm` |
| **标准库 flag** | 禁止使用 Cobra/urfave-cli/kingpin，仅用标准库 `flag` 包 |

---

## 二、核心数据流：CLI 输入 → Central Brain → Kernel → Agent → Tool

### 2.1 完整链路（以 `brain serve` HTTP API 为例）

```
┌─────────────────────────────────────────────────────────────────────────────┐
│   HTTP Client                                                               │
│   POST /v1/executions {prompt, brain, max_turns, mode, lifecycle, ...}     │
└────────────────────┬────────────────────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│   cmd/brain/cmd_serve.go                                                    │
│   ─ runManager.handleCreateExecution()                                      │
│   ─ 解析请求 → reserveSlot(maxConcurrent) → runtime.RunStore.Create()      │
│   ─ 创建 TaskExecution 状态机（Mode/Lifecycle/Restart）                       │
│   ─ mgr.launchReserved(entry, executeRun)                                   │
└────────────────────┬────────────────────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│   cmd/brain/cmd_serve.go :: executeRun()                                    │
│   ─ newExecutionEnvironment(workdir, mode, cfg)                             │
│   ─ buildManagedRegistry() — 注册 delegate tool + specialist bridge tools     │
│   ─ 创建 Orchestrator（共享全局 BrainPool）                                  │
│   ─ installHumanTakeoverBridge(orch)                                        │
│   ─ buildSystemPrompt(mode, sandbox) + buildOrchestratorPrompt(orch, reg)   │
│   ─ executeManagedRun() — 调用 loop.Runner.Execute()                        │
└────────────────────┬────────────────────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│   sdk/loop/runner.go :: Runner.Execute()                                    │
│   ─ run.Start() → pending → running                                         │
│   ─ 每 Turn: Budget.CheckTurn() → buildChatRequest → Provider.Complete()   │
│   ─ 如果 LLM 返回 tool_use blocks → dispatchTools()                        │
│   ─ dispatchTools 调用 tool.Registry.Lookup() → Tool.Execute()             │
│   ─ 结果 sanitize → append 到 messages → 下一轮                           │
│   ─ 最终: run.Complete() / run.Fail() / run.Cancel()                       │
└────────────────────┬────────────────────────────────────────────────────────┘
                     │
                     ▼ (当 tool_use 是 subtask.delegate 时)
┌─────────────────────────────────────────────────────────────────────────────┐
│   sdk/kernel/orchestrator.go :: Delegate()                                  │
│   ─ ContextEngine.Assemble() 压缩上下文（可选）                              │
│   ─ BrainPool.GetBrain(kind) → 启动/复用 sidecar                            │
│   ─ registerReverseHandlers(rpc, kind) — 注册 llm.complete + subtask.delegate│
│   ─ rpc.Call("brain/execute", payload, &result)                            │
│   ─ 结果返回后: recordDelegateOutcome() + sendBrainLearn()                  │
│   ─ ContextEngine.ClearShared() 切断跨 brain 消息边界                        │
└────────────────────┬────────────────────────────────────────────────────────┘
                     │
                     ▼ (sidecar 进程内部)
┌─────────────────────────────────────────────────────────────────────────────┐
│   sdk/sidecar/sidecar.go :: Run()                                           │
│   ─ stdio 接线: FrameReader(os.Stdin) + FrameWriter(os.Stdout)             │
│   ─ BidirRPC(RoleSidecar, reader, writer)                                   │
│   ─ 注册 initialize / tools/list / tools/call / brain/execute 等 handler    │
│   ─ 如果 RichBrainHandler → SetKernelCaller(&rpcKernelCaller{rpc})         │
│   ─ rpc.Start(ctx) → 阻塞直到 context 取消                                  │
└────────────────────┬────────────────────────────────────────────────────────┘
                     │
                     ▼ (brain/execute 在 sidecar 内部)
┌─────────────────────────────────────────────────────────────────────────────┐
│   brains/<kind>/main.go → BrainHandler.HandleMethod("brain/execute", ...)   │
│   ─ 解析 instruction → 构建本地 Agent Loop → 调用 LLM                       │
│   ─ LLM 调用通过 KernelCaller.CallKernel("llm.complete", ...) 反向 RPC    │
│   ─ Host 的 LLMProxy 转发到实际 Provider → 返回响应                          │
│   ─ 如果需要子任务再委派 → CallKernel("subtask.delegate", ...)              │
│   ─ 最终返回 brain/execute 结果                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 反向 RPC 数据流

Sidecar 需要 LLM 时并不直接调用 API，而是通过反向 RPC 到 Host：

```
sidecar (brain-browser)
  → KernelCaller.CallKernel("llm.complete", {system, messages, tools, model})
    → Host BidirRPC.Call("llm.complete", ...)
      → LLMProxy.handleComplete(kind="browser", params)
        → ProviderFactory(kind) → provider.Complete(chatReq)
          → Anthropic / OpenAI API
        → 返回 llmCompleteResponse（含 Usage.CostUSD）
      → Host 将响应写回 RPC frame
    → sidecar 收到响应，继续 Agent Loop
```

这种设计的核心价值：**sidecar 永不持有 LLM 凭证**，Host 统一管控 guardrails、cost、audit。

---

## 三、Orchestrator 四种调度机制分析

### 3.1 `Delegate` — 子任务委派（高层智能调度）

**场景**：Central Brain 的 LLM 决定"这个任务应该交给 code brain 处理"。
**调用链**：`DelegateTool.Execute()` → `Orchestrator.Delegate()` → `BrainPool.GetBrain(kind)` → sidecar RPC `brain.execute`

---

### 3.2 `CallTool` — 胖大脑工具桥接（低延迟直接调用）

**场景**：Central Brain 调用 data/quant 等已运行 sidecar 的内存工具（如 `quant.global_portfolio`）。
**调用链**：`BridgeTool.Execute()` → `Orchestrator.CallTool()` → sidecar RPC `tool.execute`

---

### 3.3 `BatchPlanner` — 同 turn 内 tool_call 并发分组

**场景**：LLM 一次返回多个 tool_use（如同时读多个文件），BatchPlanner 通过冲突图分析将无冲突的 tool_call 分到同一 batch 并行执行。
**实现**：`sdk/kernel/dispatch.go` + `sdk/loop/batch_planner.go`

---

### 3.4 `ExecuteWorkflow` — 跨 brain DAG 编排（WorkflowEngine）

**场景**：复杂任务需要拆分为多个子任务，按 DAG 依赖关系跨 brain 并行执行。
**调用链**：`WorkflowSubmitTool.Execute()` 或 `orch.ExecuteWorkflow()` → `NewWorkflowEngine()` → 拓扑排序 + 分层并行 → 每层节点调用 `NodeExecutor` → `o.Delegate()`
**三种接入模式**：
- **run CLI**：`brain run --workflow dag.json`
- **serve API**：`POST /v1/workflows` + SSE 事件推送
- **chat REPL**：LLM 自主调用 `central.submit_workflow` 工具，或用户手动 `/workflow` slash 命令

---

## 四、WorkflowEngine 核心设计

### 4.1 架构定位

WorkflowEngine 是 Orchestrator 之上的**宏观编排层**，与 BatchPlanner（微观并发层）互补：
- BatchPlanner：单 turn 内 goroutine 并行，同进程边界
- WorkflowEngine：跨 brain 多进程并行，DAG 拓扑驱动

### 4.2 核心类型

```go
type Workflow struct {
    ID    string         `json:"workflow_id"`
    Nodes []WorkflowNode `json:"nodes"`
    Edges []WorkflowEdge `json:"edges,omitempty"`
}

type WorkflowNode struct {
    ID            string   `json:"id"`
    BrainID       string   `json:"brain_id"`        // 目标 brain kind
    Prompt        string   `json:"prompt"`          // 节点任务描述
    DependsOn     []string `json:"depends_on,omitempty"`
    RequiredCaps  []string `json:"required_caps,omitempty"`
    PreferredCaps []string `json:"preferred_caps,omitempty"`
    TaskType      string   `json:"task_type,omitempty"`
}
```

### 4.3 执行流程

1. **拓扑排序**：计算 DAG 层级（`topoSort`）
2. **分层并行**：同层节点无依赖，通过 `sync.WaitGroup` + goroutine 并行执行
3. **NodeExecutor**：每个节点调用 `o.Delegate()`，自动通过 `CapabilityMatcher` 选择 brain（当 `BrainID` 为空时）
4. **数据传递**：节点间通过 Flow Edge（materialized / streaming）传递输出
5. **状态报告**：`reporter` 回调实时推送 `node.started` / `node.completed` / `node.failed` 事件

### 4.4 与 Flow Edge 的集成

- **materialized edge**：节点完成后输出写入 `flow.MemStore`，下游节点通过 ref 读取
- **streaming edge**：节点运行时通过 `flow.PipeRegistry` 实时传递数据

---

## 五、ShellExec / RunTests 实时流式输出

### 5.1 设计动机

之前的 `shell_exec` 和 `run_tests` 工具执行命令时，用户只能等待命令完成后看到截断的输出（100KB 上限）。对于长时间运行的命令（如编译、测试 suite），用户完全盲等。

### 5.2 实现方案

- `sdk/tool/command_exec.go` 新增 `ExecuteCommandRequestWithStreams(ctx, req, sb, cmdSandbox, stdoutW, stderrW)`
- 使用 `io.MultiWriter` 同时写入：
  - `bytes.Buffer`（保留完整输出给 LLM，仍受 100KB 截断限制）
  - 外部 `io.Writer`（实时输出给用户）
- `ShellExecTool` / `RunTestsTool` 新增 `StreamTo io.Writer` 字段
- CLI runtime 统一设置 `StreamTo = os.Stderr`

### 5.3 三种模式受益

| 模式 | 实时输出目标 | 用户体验 |
|------|-------------|----------|
| `brain run` | `os.Stderr` | CLI 实时打印命令输出 |
| `brain chat` | `os.Stderr` | REPL 中实时看到命令执行日志 |
| `brain serve` | HTTP SSE / 日志 | 服务端日志实时可见 |

---

## 原第三章内容

**场景**：Central Brain 的 LLM 决定"这个任务应该交给 code brain 处理"。

```go
func (o *Orchestrator) Delegate(ctx, req *SubtaskRequest) (*SubtaskResult, error)
```

**完整流程**：
1. **TargetKind 解析**：`target_kind` 现已可选；如果 `req.TargetKind == ""` 且 `capMatcher != nil`，通过三阶段匹配自动选择最佳 brain（硬标签过滤 → 软标签评分 → LearningEngine 加权：`capScore*0.7 + learnScore*0.3`）
2. **可用性检查**：`CanDelegate(kind)` — 检查磁盘上是否存在对应 sidecar 二进制
3. **ContextEngine.Assemble()**（可选）：将 instruction + context 装配为消息列表，按 `MaxTurns*4000` token 预算压缩
4. **BrainPool.GetBrain(kind)**：获取/启动 sidecar（含 nil-placeholder 并发控制、崩溃检测、自动重启）
5. **registerReverseHandlers(rpc, kind)**：在 sidecar RPC 会话上注册 `llm.complete`、`subtask.delegate`、`specialist.call_tool`、`brain/metrics`、`brain/progress`、`human.request_takeover`
6. **rpc.Call("brain/execute", payload)**：发送任务并等待结果
7. **边界清理**：`ContextEngine.ClearShared(central, targetKind)` — 切断跨 brain 消息桶，防止污染
8. **学习闭环**：`recordDelegateOutcome()` → `LearningEngine.RecordDelegateResult()` + `sendBrainLearn()` → 异步通知 sidecar `brain/learn`
9. **自动重试**：如果 sidecar 崩溃，remove + restart + retry once

**SubtaskRequest 关键字段**：
- `TaskID`、`TargetKind`、`Instruction`、`Context`、`Subtask`（不可变 caller intent）
- `Budget`（MaxTurns / MaxCostUSD / Timeout）
- `Execution`（workdir / file policy 边界继承）
- `RequiredCaps` / `PreferredCaps`（硬/软匹配标签）
- `TaskType`（用于 LearningEngine 的 L1 能力画像查询和更新）

### 3.2 `CallTool` — 确定性工具调用（能力复用）

**场景**：Caller 已经知道要调用哪个 brain 的哪个工具，无需运行该 brain 的完整 Agent Loop。

```go
func (o *Orchestrator) CallTool(ctx, req *SpecialistToolCallRequest) (*ToolCallResult, error)
```

**流程**：
1. 校验 `TargetKind`、`ToolName` 必填
2. `BrainPool.GetBrain(kind)` 获取/启动 sidecar
3. `rpc.Call("tools/call", {name, arguments, execution})`
4. 返回 `ToolCallResult`

**与 Delegate 的区别**：
| | Delegate | CallTool |
|---|---|---|
| **调用入口** | `brain/execute`（完整 Agent Loop） | `tools/call`（单工具直接执行） |
| **LLM 参与** | 是（sidecar 内部运行 Agent Loop） | 否（直接执行工具逻辑） |
| **适用场景** | 复杂子任务，需要多轮推理 | 已知工具调用，确定性操作 |
| **上下文** | 传递 instruction + context | 传递 arguments + execution spec |
| **budget** | 子任务级 Budget | 无独立 Budget |

### 3.3 `BatchPlanner` — 工具调用并行调度

**场景**：单轮 LLM 响应包含多个 `tool_use` blocks，需要判断哪些可以并行执行、哪些必须串行（资源冲突）。

**核心抽象**（`sdk/loop/batch_planner.go` + `sdk/kernel/` 调度层）：

```go
// loop 包
type ToolBatchPlanner interface {
    Plan(calls []ToolCallNode) (*BatchPlan, error)
    ResourceLocker() ResourceLocker
}

type BatchPlan struct {
    Batches []ToolBatch  // 每个 batch 内并行，batch 间串行
}

type ToolBatch struct {
    Calls  []ToolCallNode
    Leases []BatchLeaseRequest  // 资源租约请求
}
```

**Runner.dispatchToolsBatched 流程**：
1. 为每个 `tool_use` block 构建 `ToolCallNode`，附带 `ToolConcurrencySpec`（从 Registry 中工具 Schema 读取）
2. 调用 `BatchPlanner.Plan(nodes)` → 返回分组后的 `BatchPlan`
3. 获取 `ResourceLocker`（可能为 nil）
4. 对每个 batch：
   - **AcquireSet**：获取该 batch 所需的所有资源租约（通过 `LeaseManager.AcquireSet()`）
   - **并行执行**：batch 内多个 tool 用 `sync.WaitGroup` 并行调用
   - **ReleaseAll**：batch 完成后释放租约
5. 如果 Plan 失败或租约获取失败，回退到串行执行

**CLI 层适配**（`cmd/brain/bridge/batch_adapter.go`）：
- `BatchPlannerAdapter` 将 `kernel.BatchPlanner` 适配为 `loop.ToolBatchPlanner`
- `leaseLockerAdapter` 将 `kernel.LeaseManager` 适配为 `loop.ResourceLocker`

**设计意图**：
- 最大化 LLM 响应的并行度（如同时读取多个文件）
- 保证资源冲突的操作串行化（如同时写入同一文件）
- 租约机制支持跨 brain 的资源互斥（如 browser session、数据库连接）

---

## 四、Persistence 层设计

### 4.1 PlanStore — 计划持久化

```go
type PlanStore interface {
    Create(runID string, plan *Plan) error
    Get(runID string) (*Plan, error)
    Update(runID string, delta *PlanDelta) error
    List(filter PlanFilter) ([]*Plan, error)
}
```

- 支持 BrainPlan + delta 增量更新
- SQLite 实现通过 WAL 保证并发安全

### 4.2 ArtifactStore — 内容寻址存储

```go
type ArtifactStore interface {
    Put(ctx context.Context, content []byte) (hash string, err error)
    Get(ctx context.Context, hash string) ([]byte, error)
    Exists(ctx context.Context, hash string) bool
}
```

- **CAS (Content-Addressed Storage)**：文件内容按 SHA-256 去重
- `FSArtifactStore`：文件系统后端，按 hash 前两位分目录存储
- 元数据层（ArtifactMetaStore）管理路径→hash 映射和引用计数，支持 GC

### 4.3 RunStore — 运行记录

```go
type RunStore interface {
    Create(brainID, prompt, mode, workdir string) (*RunRecord, error)
    Finish(runID, status string, result json.RawMessage, summary string) (*RunRecord, error)
    Get(runID string) (*RunRecord, bool)
    List(limit int, statusFilter string) []*RunRecord
    AppendEvent(runID, eventType, message string, data json.RawMessage) error
}
```

- **双重实现**：SQLite（新）+ JSON file（向后兼容 `runs.json`）
- 记录 run 的完整生命周期事件（accepted、cancel、complete、fail 等）
- v3 扩展：TaskExecution 状态机（Mode/Lifecycle/Restart）与 RunStore 解耦，RunStore 仅记录元数据

### 4.4 其他持久化组件

| 组件 | 职责 |
|------|------|
| **RunCheckpointStore** | 保存 Run 中间状态，支持崩溃后 resume |
| **UsageLedger** | 按 brain / run 维度记录 token、cost、duration |
| **LearningStore** | EWMA 能力画像、序列记录、异常模板、交互序列、sitemap snapshot、human demo |
| **SharedMessageStore** | 跨 brain 上下文传递的持久化桶（central ↔ specialist） |
| **AuditLogger** | 审计日志链（HashChain 保证不可篡改） |

### 4.5 Driver 模式

```go
stores, err := persistence.Open("sqlite", dsn)
k := kernel.NewKernel(kernel.WithPersistence(stores.Stores))
```

- `ClosableStores` 统一管理所有 store 的生命周期
- `WithPersistence` 一次性注入所有非 nil store
- 向后兼容：`FileBackend` 使用 JSON file store

---

## 五、Agent 协议（stdio 通信）格式与生命周期

### 5.1 帧格式（Content-Length 定界）

```
Content-Length: <N>\r\n
Content-Type: application/vnd.jsonrpc\r\n
\r\n
<JSON body of exactly N bytes>
```

- `FrameReader` 逐帧读取，处理粘包/拆包
- `FrameWriter` 带超时写入（`DefaultWriteTimeout`）
- 与 LSP（Language Server Protocol）的 stdio 传输格式一致

### 5.2 JSON-RPC 2.0 消息格式

```go
type RPCMessage struct {
    JSONRPC string          `json:"jsonrpc"`  // 必须是 "2.0"
    ID      string          `json:"id,omitempty"`  // "k:<seq>" 或 "s:<seq>"
    Method  string          `json:"method,omitempty"`
    Params  json.RawMessage `json:"params,omitempty"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *RPCError       `json:"error,omitempty"`
}
```

### 5.3 ID 命名空间规则（§4.2）

| 角色 | 前缀 | 说明 |
|------|------|------|
| Host/Kernel | `k:` | Host 发出的请求 |
| Sidecar | `s:` | Sidecar 发出的请求 |

**路由规则**：
- 收到 `id` 以自身前缀开头 → 这是对自己请求的响应 → 查 waiter table
- 收到 `id` 以对端前缀开头 → 这是对端的请求 → dispatch 到 handler
- 收到 `id` 为空 → Notification → dispatch 到 handler，不发送响应

### 5.4 Sidecar 生命周期

```
1. 进程启动
   ↓
2. Run() 初始化
   ├── signal.Notify(SIGINT, SIGTERM)
   ├── FrameReader(os.Stdin) + FrameWriter(os.Stdout)
   ├── BidirRPC(RoleSidecar, reader, writer)
   ├── register "initialize" handler
   ├── register "notifications/initialized" (no-op)
   ├── register "$/shutdown" → cancel ctx
   ├── register "tools/list" → 返回 BrainHandler.Tools()
   ├── register "tools/call" → delegate to BrainHandler.HandleMethod
   ├── register brain/* methods → delegate to BrainHandler.HandleMethod
   ├── if RichBrainHandler → SetKernelCaller(&rpcKernelCaller{rpc})
   └── rpc.Start(ctx) → 启动 readLoop goroutine
   ↓
3. 等待 Host 的 "initialize" 请求
   Host 发送: {"jsonrpc":"2.0","id":"k:1","method":"initialize","params":{}}
   Sidecar 返回: {"jsonrpc":"2.0","id":"k:1","result":{protocolVersion, capabilities, serverInfo, brainDescriptor}}
   ↓
4. Host 发送 "notifications/initialized" (no-op ack)
   ↓
5. Sidecar 进入工作状态
   ├── 收到 "brain/execute" → 运行 Agent Loop
   │   ├── 内部调用 LLM → KernelCaller.CallKernel("llm.complete")
   │   ├── 内部需要子委派 → KernelCaller.CallKernel("subtask.delegate")
   │   ├── 内部需要工具 → 本地 Tool.Execute()
   │   └── 返回执行结果
   ├── 收到 "brain/metrics" → 返回聚合指标
   ├── 收到 "brain/learn" → 记录学习结果
   └── 收到 "$/shutdown" → cancel ctx → Run() 退出
   ↓
6. 退出
   ├── ctx.Done() 触发
   ├── rpc.Close()
   ├── 进程退出
```

### 5.5 Host-side Agent 生命周期（ProcessRunner）

```go
func (r *ProcessRunner) Start(ctx, kind, desc) (Agent, error)
```

1. `binResolver(kind)` 查找 sidecar 二进制路径
2. `exec.Command(path)` 启动子进程（stdin/stdout pipe）
3. Linux 设置 `Pdeathsig=SIGTERM`（父进程死亡时子进程自动退出）
4. Windows/macOS 依赖 stdin EOF 检测（sidecar 的 `rpc.Done()` 监控）
5. 创建 `FrameReader(pipe.stdout)` + `FrameWriter(pipe.stdin)`
6. 创建 `BidirRPC(RoleKernel, reader, writer)`
7. 发送 `initialize` 请求，等待 `Ready()` 返回
8. 返回 `processAgent`（实现 `Agent` + `RPCAgent`）

---

## 六、Central Brain 与 Kernel 的分工边界

### 6.1 职责矩阵

| 职责 | Central Brain (cmd/brain + central/) | Kernel (sdk/kernel/) |
|------|--------------------------------------|----------------------|
| **用户交互** | HTTP API (`brain serve`)、CLI REPL (`brain` 无参数)、Chat 模式 | 不直接面向用户 |
| **配置管理** | `brain.json` 解析、provider 配置、mode 解析 | 仅接收装配好的配置 |
| **权限模式** | `plan`、`default`、`accept-edits`、`auto`、`restricted`、`bypass-permissions` | 接收 mode，由 tool 层执行 |
| **系统提示词** | 构建 system prompt（L1+L2）、orchestrator prompt | 不构建 prompt |
| **工具注册** | `buildManagedRegistry()` — 注册所有工具 + delegate tool + specialist bridge | 提供 Registry 抽象，不决定注册哪些工具 |
| **Run 生命周期** | `runManager` 管理并发 slot、TaskExecution 状态机（Mode/Lifecycle/Restart） | `Runner.Execute()` 执行单条 Run |
| **Sidecar 管理** | 创建全局 `BrainPool`、Orchestrator、配置 LLMProxy | Orchestrator 实现 sidecar 生命周期 |
| **学习闭环** | 加载 LearningEngine、CapabilityMatcher、ContextEngine、SummaryDaemon | 提供学习引擎实现 |
| **事件总线** | `events.MemEventBus` — SSE 推送、hook runner 订阅 | 消费事件（如 TaskExecution 发 task.state.*） |
| **持久化** | `cliruntime.Runtime` 组装 SQLite/FileStore | `Kernel` 持有 store 接口 |
| **审计** | 组织级策略检查（OrgPolicyEnforcer） | AuditLogger 接口 |
| **Dashboard** | WebSocket Hub、静态 SPA、LearningProvider | 仅提供数据接口 |

### 6.2 关键边界说明

**Central Brain = 编排器 + 用户界面 + 配置中心**
- 决定"启动哪个 brain"、"用什么 model"、"什么权限模式"
- 管理全局状态（runManager、eventBus、BrainPool、LearningEngine）
- 构建系统提示词，注册工具集合

**Kernel = 执行引擎 + 协议层 + 持久化抽象**
- 执行 Agent Loop（Runner）
- 管理 sidecar 进程（Orchestrator + BrainPool）
- 处理 stdio 协议（protocol.FrameReader/Writer + BidirRPC）
- 提供持久化接口（PlanStore、ArtifactStore、RunStore 等）

**两者通过 Kernel 结构体连接**：
```go
type Runtime struct {
    Kernel       *kernel.Kernel       // SDK 层
    FileStore    *persistence.FileStore
    RunStore     *Store               // CLI 层 JSON store
    Stores       *persistence.ClosableStores
    ArtifactRoot string
}
```

---

## 七、与 6 个专精大脑的集成点

### 7.1 专精大脑列表

| Kind | 角色 | 集成文件 |
|------|------|----------|
| `code` | 代码编写与编辑 | `brains/code/` |
| `browser` | 无头浏览器驱动 | `brains/browser/` |
| `data` | 市场数据收集与特征计算 | `brains/data/` |
| `quant` | 交易策略与风险管理 | `brains/quant/` |
| `verifier` | 测试与验证 | `brains/verifier/` |
| `fault` | 混沌测试与故障注入 | `brains/fault/` |

### 7.2 集成机制

**1. 二进制发现与启动**
```go
func defaultBinResolver() func(agent.Kind) (string, error) {
    return func(kind agent.Kind) (string, error) {
        return exec.LookPath(fmt.Sprintf("brain-%s", kind))
    }
}
```

- 约定命名：`brain-<kind>`（如 `brain-code`、`brain-browser`）
- Orchestrator 启动时探测所有 `BuiltinKinds()` 的二进制文件是否存在
- 配置驱动：`OrchestratorConfig.Brains` 可显式注册第三方 brain（Binary / Args / Env / Model）

**2. LLM 代理**
```go
llmProxy := &kernel.LLMProxy{
    ProviderFactory: func(kind agent.Kind) llm.Provider {
        session, _ := openConfiguredProvider(cfg, string(kind), req.ModelConfig, "", "", "", "")
        return session.Provider
    },
}
```

- 每个 brain kind 可配置独立的 model（通过 `BrainRegistration.Model`）
- `LLMProxy.ModelForKind` 存储 kind→model 映射
- Model 解析优先级：请求显式 model > `ModelForKind` > provider 默认 model

**3. 工具桥接**
```go
registerSpecialistBridgeTools(reg, orch)
registerDelegateToolForEnvironment(reg, orch, env)
```

- `delegate` 工具：让 Central Brain 的 LLM 可以调用 `subtask.delegate` 委派任务
- `specialist.call_tool` 工具：让 Central Brain 可以直接调用专精 brain 的工具
- 这些工具注册在 Central Brain 的 ToolRegistry 中，对 LLM 可见

**4. 学习闭环**
```go
orch = kernel.NewOrchestratorWithPool(...,
    kernel.WithCapabilityMatcher(mgr.capMatcher),
    kernel.WithLearningEngine(mgr.learner),
    kernel.WithContextEngine(mgr.ctxEngine),
)
```

- **L0（sidecar 本地）**：sidecar 记录自身 brain/metrics，通过反向 RPC 上报
- **L1（Kernel 层）**：LearningEngine 维护 EWMA 能力画像，`RecordDelegateResult` 更新准确率/速度/成本/稳定性
- **L2（序列层）**：记录多步委派序列，`RecordSequence` 聚合总评分
- **L3（Central 层）**：CapabilityMatcher 使用 L1 数据 ranking brain，`TaskScheduler` 基于排名调度

**5. 上下文传递**
```go
// ContextEngine 装配上下文
tokenBudget := req.Budget.MaxTurns * 4000
assembled, _ := ctxEngine.Assemble(ctx, AssembleRequest{...})

// 任务完成后清理共享桶
ctxEngine.ClearShared(agent.Kind("central"), req.TargetKind)
```

- `SharedMessageStore` 持久化 central ↔ specialist 的跨 brain 消息
- `DefaultContextEngine` 负责装配、压缩、清理
- 每次 delegate 完成后主动清理，防止上下文污染

**6. 进度透传**
```go
rpc.Handle(protocol.MethodBrainProgress, func(ctx, params) {
    h := o.brainProgressHandler  // 由 cmd/brain 注入
    if h != nil { h(ctx, callerKind, params) }
})
```

- sidecar 的 Agent Loop 中 tool_start / tool_end / turn / content 事件
- 通过 `brain/progress` 反向 RPC 上报到 Host
- Host 的 `brainProgressHandler` 转发到 chat REPL 的 `progressCh`
- 实现流式子任务进度打印

### 7.3 Browser Brain 特殊集成

Browser Brain 有最复杂的集成链路：

| 集成点 | 实现 |
|--------|------|
| **Feature Gate** | `tool.ConfigureBrowserFeatureGate()` — 通过 license 校验启用浏览器功能 |
| **Runtime Projection** | `kernel.WriteBrowserRuntimeProjectionFile()` — 将 browser 运行时配置投影到 sync file |
| **CDP Session** | `tool.CurrentSharedBrowserSession()` — 共享 Chrome DevTools Protocol 会话 |
| **Human Event Source** | `cdp.NewCDPEventSource(sess)` — DOM 事件录制用于 human takeover |
| **Sitemap Cache** | `tool.SetSitemapCache(learningStoreSitemapCache)` — 复用 LearningStore 的 snapshot |
| **Pattern Library** | `tool.SetInteractionSink(learner)` — browser 交互序列落入 LearningStore |
| **Human Demo** | `tool.SetHumanDemoSink(learningStore)` — 真人录制序列持久化 |
| **Pattern Split** | `startPatternSplitScanner()` — 失败样本自动分裂生成变体工具 |

---

## 八、明显的设计缺陷与架构债务

### 8.1 接口耦合与类型断言泛滥

**问题**：Kernel 结构体中大量使用 `interface{}` 而非强类型接口：

```go
type Kernel struct {
    Orchestrator interface{}  // 应该是 *Orchestrator 或 OrchestratorInterface
    LLMProxy     interface{}  // 应该是 *LLMProxy 或 LLMProxyInterface
    BrainPool    BrainPool    // 还好这个有接口
}
```

**影响**：
- `WithOrchestrator(o interface{})` 和 `WithLLMProxy(p interface{})` 完全无类型安全
- 运行时类型断言失败 panic 风险高
- 无法在编译期检查 Orchestrator/LLMProxy 是否正确注入

**建议**：定义 `OrchestratorInterface` 和 `LLMProxyInterface`，将 `interface{}` 替换为具体接口。

### 8.2 RunStore 双重实现（JSON + SQLite）

**问题**：`cliruntime.Runtime` 中同时存在 `RunStore`（JSON）和 `Stores`（SQLite），`RunStore` 未被统一到 SQLite 中：

```go
type Runtime struct {
    RunStore *Store          // JSON file store（runs.json）
    Stores   *ClosableStores // SQLite store（含 RunStore 接口但未实际替换）
}
```

**影响**：
- Run 元数据散落在两个存储后端
- `handleCreateRun` 写 `runtime.RunStore.Create()`，但其他组件可能读 `runtime.Stores.RunStore`
- 数据一致性问题

**建议**：完成 `RunStore` 的 SQLite 迁移，移除 `runs.json` 依赖。

### 8.3 Orchestrator 与 BrainPool 职责重叠

**问题**：`Orchestrator` 和 `ProcessBrainPool` 有大量重复代码：
- 两者都有 `available` map、`registrations` map、`probeRegistration`/`probeBinResolver`
- `Orchestrator.getOrStartSidecar` 完全委托给 `BrainPool.GetBrain`
- `Orchestrator` 仍然保留 `runner` 字段但仅用于 backward compat

**影响**：
- 维护两份相同的探测/可用性逻辑
- `syncLLMModels` 在 Orchestrator 中实现，但数据来自 pool 的 registrations
- v3 过渡期后应彻底将 sidecar 管理移入 BrainPool

**建议**：Orchestrator 专注于"调度决策"（Delegate/CallTool/BatchPlanner），BrainPool 专注于"进程管理"，两者通过接口交互。

### 8.4 HumanTakeover 的循环依赖风险

**问题**：
- `sdk/kernel/orchestrator.go` 定义 `HumanTakeoverHandler` 类型，由 `cmd/brain` 层注入
- `cmd/brain` 层通过 `tool.SetHumanTakeoverCoordinator()` 设置全局协调器
- `tool.HumanTakeoverCoordinator` 接口在 `tool` 包中定义

**影响**：
- 反向依赖：`sdk/kernel` 依赖 `sdk/tool` 的 HumanTakeoverCoordinator（通过函数指针注入避免了直接 import，但概念上存在）
- 全局变量 `tool.humanTakeoverCoordinator` 是隐式状态，测试和并发场景难以控制
- `newHostHumanTakeoverCoordinator` 在 `cmd/brain` 中创建，但逻辑分散在多个文件中

### 8.5 LLMProxy 的 Streaming 假实现

**问题**：`LLMProxy.RegisterHandlers` 中 `llm.stream` 直接 fallback 到非流式 `complete`：

```go
rpc.Handle(protocol.MethodLLMStream, func(ctx, params) {
    return p.handleComplete(ctx, kind, params)  // 直接返回完整响应
})
```

**影响**：
- Sidecar 的 Agent Loop 中如果启用 streaming，实际拿不到增量 token
- StreamConsumer 无法工作，用户体验差（等待整个响应完成后才显示）
- 标记为 "for now" 的临时实现，但长期未完善

### 8.6 ContextEngine 的 Token 预算估算粗糙

**问题**：
```go
tokenBudget := req.Budget.MaxTurns * 4000  // 粗略估算
```

**影响**：
- 4000 tokens/turn 是硬编码 magic number，与 actual model context window 无关
- 如果 MaxTurns=20，预算为 80k，但 Claude 3.5 Sonnet 支持 200k context
- 预算估算不准确导致过早截断或过度压缩

### 8.7 BatchPlanner 回退路径缺乏统计

**问题**：`BatchPlanner.Plan()` 失败时回退到串行执行，但没有记录失败原因和频率。

**影响**：
- 无法判断 BatchPlanner 是否频繁失效（可能是 LeaseManager 资源竞争、Plan 算法 bug、或 ToolConcurrencySpec 缺失）
- 缺少 metrics 和告警，潜在性能瓶颈被掩盖

### 8.8 BrainPool 的 nil-placeholder 竞态条件

**问题**：`waitForSidecar` 使用轮询（50 × 100ms = 最多 5s）等待另一个 goroutine 完成启动。

**影响**：
- 不是事件驱动，浪费 CPU 和延迟
- 如果启动者 goroutine 在 5s 内未完成，等待者返回 timeout error
- 高并发场景下（如 burst 请求）大量 goroutine 轮询，性能劣化

**建议**：使用 `sync.Cond` 或 channel-based 通知机制替代轮询。

### 8.9 配置加载的多次重复

**问题**：`cmd/brain/cmd_serve.go` 中多次调用 `loadConfig()`：

```go
func runServe(args) {
    cfg, cfgErr := loadConfig()          // 第1次
    mode, _ := resolvePermissionMode(..., cfg)
    ...
    if cfgFile == nil && ... {           // handleCreateRun 中又 loadConfig()
        cfgFile, cfgErr := loadConfig()  // 第2次（甚至更多）
    }
}
```

**影响**：
- 配置文件可能被修改两次读取之间读到不同内容
- 不必要的 I/O 和解析开销
- 配置对象在多个层级传递不一致

### 8.10 LearningEngine 的全局状态注入风格

**问题**：大量使用全局 setter 进行跨层注入：

```go
tool.SetInteractionSink(learner)
tool.SetSitemapCache(learningStoreSitemapCache{store: runtime.Stores.LearningStore})
tool.SetHumanDemoSink(runtime.Stores.LearningStore)
tool.SetPatternFailureStore(runtime.Stores.LearningStore)
tool.SetHumanEventSourceFactory(newSharedBrowserHumanEventSourceFactory())
tool.SetOutcomeSink(globalAdaptivePolicy)
tool.SetSharedAnomalyTemplateLibrary(lib)
```

**影响**：
- 全局可变状态，测试隔离困难
- 无法为不同 Run 配置不同的 LearningStore
- 并发场景下 race condition 风险
- 违背依赖注入原则

**建议**：将这些 sink/cache/factory 作为 `Runner` 或 `Kernel` 的字段注入，而非全局变量。

### 8.11 v3 Execution 状态机的耦合

**问题**：`TaskExecution` 状态机同时存在于 `sdk/kernel/execution.go` 和 `cmd/brain/cmd_serve.go` 的 `runEntry.taskExec` 中。

**影响**：
- `executeRun` 函数长达 200+ 行，混合了状态机管理、模式判断（daemon/watch/oneshot）、restart 策略、Run 执行
- `runEntry` 既作为 HTTP API 的序列化对象，又持有 `*kernel.TaskExecution`（非序列化字段）
- 状态转换散布在多个地方，难以追踪

### 8.12 远程 Brain 连接池的孤立性

**问题**：`RemoteBrainPool` 虽然实现了 `BrainPool` 接口，但 `Orchestrator` 中直接调用 `o.pool.GetBrain()`，没有统一的远程/本地 fallback 逻辑。

**影响**：
- 如果本地 sidecar 不可用，不会自动尝试远程 brain
- 远程 brain 的故障恢复、重连、健康检查逻辑与本地进程池不共享
- 混合部署场景下用户体验不一致

---

## 九、总结

### 架构亮点

1. **清晰的层级分离**：CLI → Central Brain → Kernel → Agent → Tool 的链路层次分明
2. **协议层设计优秀**：BidirRPC 的全双工 ID 命名空间、in-flight 窗口、cancelRequest 机制健壮
3. **进程池共享**：BrainPool 使多 Run 复用 sidecar，避免重复启动开销
4. **学习闭环完整**：L0-L3 四级学习体系，从 sidecar 本地到 Central Brain 全局调度
5. **预算控制严格**：五维 Budget 在 Agent Loop 中每轮强制检查
6. **配置驱动扩展**：OrchestratorConfig.Brains 支持第三方 brain 热插拔
7. **安全模型到位**：LLMAccessMode、Zone 模型、AuditLogger、OrgPolicyEnforcer

### 主要债务

1. **类型安全**：Kernel 中 `interface{}` 泛滥，应替换为强类型接口
2. **全局状态**：tool 包的大量全局 setter 需改为依赖注入
3. **存储统一**：JSON RunStore 与 SQLite Stores 的并存需要清理
4. **streaming 完善**：LLMProxy 的 llm.stream 是假实现
5. **并发优化**：BrainPool 的 nil-placeholder 轮询应改为事件通知
6. **测试覆盖**：多个组件（BatchPlanner、ContextEngine、RemoteBrainPool）缺少充分测试

---

*分析完成。本文件基于对 brain 项目 SDK 全包、central/ 目录、cmd/brain/ CLI 层的完整代码阅读生成。*
