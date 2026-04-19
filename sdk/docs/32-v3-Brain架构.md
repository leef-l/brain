# 32. v3 Brain 架构

> **状态**：v8 · 2026-04-18
> **上位规格**：[02-BrainKernel设计.md](./02-BrainKernel设计.md)
> **下位规格**：[33-Brain-Manifest规格.md](./33-Brain-Manifest规格.md) / [34-Brain-Package与Marketplace规范.md](./34-Brain-Package与Marketplace规范.md)
> **变更记录**：v1（2026-04-13）初版，v2（2026-04-16）行业对标后重排优先级，将多脑协作运行时提升为主轴，v3（2026-04-16）新增四层自适应学习体系，v4（2026-04-16）多任务架构重设计：三层分离，v5（2026-04-16）收敛为 TaskExecution + Capability Lease + Dispatch Policy + Flow Edge 四正交概念，引入 AcquireSet 死锁防护、ToolConcurrencySpec、LeaseScope，v6（2026-04-17）全面细化 8 大核心子系统设计（§13），新增独立规格文档 35 系列，v7（2026-04-17）补充 P0 差距：Brain Capability 标签体系、跨脑通信协议、LeaseManager 独立文档、端到端时序与模块依赖图，v8（2026-04-18）新增 §15 上层产品接入边界（runtime vs domain 职责划分、四条接入铁律）

---

## 0. 版本背景

v1 的架构方向没有错：**Brain-first, manifest-driven, runtime-pluggable**。

但 v1 的排序有问题——过度押注 Manifest/Package/Marketplace 这条"生态先行"路线，而代码实际已经在往"多脑运行时控制面"走。行业对标（OpenClaw 2026.4.15-beta.1 / Hermes Agent v0.9.0）进一步确认：**当前市场先赢的是"可运行、可观测、可协作、可控"，不是"manifest 很完整"。**

v2 的核心调整：

> **把多脑协作本身当作 runtime 的核心能力来建设，生态是它的自然延伸——不是先做完 runtime 再做生态，而是从第一天就在多脑管理中沉淀生态基础。**

这是我们和 OpenClaw（单体 skills）、Hermes（单 agent 自我改进）的根本区别：**中央大脑 + N 个专精大脑的多脑并行协作**，这是护城河，不能丢。

---

## 1. 设计结论

v3 的顶层架构：

- **Brain-first**：顶层产品对象永远是 `Brain`
- **Multi-brain native**：多脑协作不是扩展功能，是内核能力
- **Runtime-observable**：运行时状态可见、可控、可审计
- **Manifest-driven**：稳定契约由 `Brain Manifest` 描述
- **Package-delivered**：分发、安装、授权通过 `Brain Package`
- **Policy-governed**：权限、审批、门禁统一收敛在 policy 层

一句话：

> **中央大脑协调 N 个专精大脑，每个大脑有 manifest 身份、有 runtime 实现、有 policy 门禁、有 dashboard 可见性——用户安装的是 package，kernel 调度的是 brain。**

---

## 2. 为什么多脑协作是主轴

### 2.1 行业现状

| 项目 | 架构模式 | 协作能力 |
|------|----------|----------|
| OpenClaw | 单体 + Skills 插件 | 无多脑协作，skills 是工具集合 |
| Hermes Agent | 单 Agent + Learning Loop | 自我改进，无并行脑 |
| Claude Code | 单 Agent + MCP 工具 | 工具扩展，无脑级别委托 |
| **Brain** | **中央大脑 + N 专精大脑** | **进程隔离、工具隔离、跨脑委托、跨脑授权** |

别人有的（Dashboard、Task 管理、Context Engine、MCP 生态），我们可以做。
别人没有的（多脑并行协作、专精大脑生态），是我们的差异化。

### 2.2 代码已经在走这条路

当前 v0.7.0 已经实现的多脑基础：

| 能力 | 实现位置 | 状态 |
|------|----------|------|
| 多脑进程管理 | `sdk/kernel/orchestrator.go` | ✅ 已实现 |
| 配置驱动注册 | `OrchestratorConfig.Brains` | ✅ 已实现 |
| 跨脑委托 | `SubtaskRequest/SubtaskResult` | ✅ 已实现 |
| 跨脑授权策略 | `SpecialistToolCallAuthorizer` | ✅ 已实现 |
| Bridge Tool 直调 | `specialist_bridge.go` | ✅ 已实现 |
| 动态 prompt 生成 | `buildOrchestratorPrompt` | ✅ 已实现 |
| LLM Proxy 路由 | `sdk/kernel/llm_proxy.go` | ✅ 已实现 |
| 6 个专精大脑 | `brains/data,quant,code,browser,verifier,fault` | ✅ 已实现 |
| Quant WebUI | `brains/quant/webui/` | ✅ 已实现 |
| serve HTTP API | `cmd_serve.go` | ✅ 已实现 |
| doctor 诊断 | `cmd_doctor.go`（8 项检查） | ✅ 已实现 |
| 多任务支持 | `runManager` + `SubtaskRequest` | ⚠️ 部分实现 |

文档应该跟上代码，而不是代码追文档。

---

## 3. v3 的七个核心概念

### 3.1 Brain

`Brain` 是唯一顶层产品对象。

它表示一个可被 Kernel/Central 识别、调度、健康检查、授权、计量、审计的专精执行单元。

约束：

- central **只 delegate 给 brain**
- orchestrator **只管理 brain**
- 用户安装、启用、禁用、升级的都是某个 brain

### 3.2 Central Brain（中央大脑）

中央大脑是多脑协作的协调者，不是"又一个专精大脑"。

职责：

- 任务理解与规划
- 脑选择与委托
- 子任务验收与审查
- 跨脑协调与冲突仲裁
- 用户交互入口

Central 不直接做业务，它做决策。

### 3.3 Brain Manifest

`Brain Manifest` 是 brain 的稳定契约文件。

它声明：brain 是谁、会什么、怎么跑、需要什么门禁。

v3 精简为 **7 个核心字段**（从 v1 的 12 字段收敛）：

| 字段 | 必选 | 说明 |
|------|------|------|
| `schema_version` | 是 | 固定 `1` |
| `kind` | 是 | Brain 唯一标识，如 `browser`、`quant` |
| `name` | 是 | 展示名 |
| `brain_version` | 是 | semver |
| `capabilities` | 是 | 能力标签数组 |
| `runtime` | 是 | 运行时定义 |
| `policy` | 否 | 工具范围、审批模式、沙箱声明 |

可选扩展字段（不阻塞 v3.0）：`description`、`task_patterns`、`compatibility`、`license`、`health`、`metadata`

**设计原则**：先跑起来，再补完整。Manifest 的价值在于让 orchestrator 能读取它做脑发现，而不是做一份完美的 JSON Schema。

### 3.4 Brain Runtime

`Brain Runtime` 定义 brain 怎么跑。

v3 支持四类 runtime：

| 类型 | 说明 | 优先级 |
|------|------|--------|
| `native` | 本地 sidecar 二进制，自己实现 `brain/execute` | **P0，已实现** |
| `mcp-backed` | brain host 内部绑定 MCP server | **P0，需落地** |
| `hybrid` | 本地工具 + MCP 能力混合 | P1 |
| `remote` | brain 运行在远端 | P2，冻结 schema 即可 |

MCP 的位置：**capability binding protocol + runtime backend**，不是顶层产品对象。

### 3.5 Brain Package

`Brain Package` 是分发与安装单位。

它负责交付，不负责思考。

v3 对 Package 的定位调整：

- **不只是"分发容器"**，而是：
  - 安装单元
  - 权限边界单元
  - 依赖归属单元
  - 升级/回滚单元
- **不优先做 Marketplace**，先做本地生命周期：`brain install / activate / deactivate / list / upgrade / rollback`
- Marketplace 是 Package 跑通后的自然延伸，不是前置条件

### 3.6 Brain Capability（脑级能力标签）

`Capability` 是 brain 的能力标签与路由信号。

用于 delegate 候选筛选、搜索、policy 映射。

不取代 brain，只辅助 brain 被发现和选择。

> **与 Tool Capability Binding 的区分**：
> - **Brain Capability**（本节）：脑级别，回答"这个脑会什么"，用于发现和路由。粒度是 brain。
> - **Tool Capability Binding**（§7.7.4 ToolConcurrencySpec）：工具级别，回答"这个工具触碰什么资源"，用于并发控制。粒度是 tool × resource。
>
> 两者名字相近但职责正交，不要混淆。

### 3.7 Control Plane（控制面）

这是 v2 新增的核心概念。

`Control Plane` 是多脑系统的可见性和管理层：

- **Dashboard**：所有活跃 brain 的状态、工具、权限、健康度
- **Task Model**：TaskExecution 统一执行模型（Mode × Lifecycle × Restart 策略组合）
- **Event Stream**：brain 间通信、状态变更、异常告警的统一事件流
- **Auth Status**：Provider 认证状态、凭证健康、模型可用性

控制面不是"附属页面"，是产品的核心交付件。

---

## 4. 运行时总模型

```text
                    ┌──────────────────────────────────┐
                    │        Control Plane              │
                    │  dashboard · events · auth · task │
                    └──────────────┬───────────────────┘
                                   │ observe / manage
                    ┌──────────────▼───────────────────┐
                    │         Central Brain              │
                    │  plan / route / delegate / review  │
                    └──────────────┬───────────────────┘
                                   │
                      delegate only to Brain
                                   │
          ┌────────────────────────┼────────────────────────┐
          │                        │                        │
          ▼                        ▼                        ▼
  ┌───────────────┐        ┌───────────────┐        ┌───────────────┐
  │ Native Brain  │        │ MCP-backed    │        │ Hybrid Brain  │
  │               │        │ Brain         │        │               │
  │ local tools   │        │ host + MCP    │        │ local + MCP   │
  │ sidecar proc  │        │ sidecar proc  │        │ sidecar proc  │
  └───────┬───────┘        └───────┬───────┘        └───────┬───────┘
          │                        │                        │
          ▼                        ▼                        ▼
  local registry           MCP bindings            local + MCP
```

要点：

1. `central` 永远只 delegate 给 `brain`
2. `MCP server` 是 runtime 背后的 capability backend，不是 delegate 目标
3. Control Plane 观测和管理所有层级
4. 所有 brain 都是独立进程（sidecar），零例外

---

## 5. 多脑协作模型

### 5.1 协作拓扑

```text
                    Central Brain
                   ╱      │       ╲
                  ╱       │        ╲
           delegate   delegate   delegate
              ╱          │           ╲
     ┌────────┐    ┌─────────┐    ┌──────────┐
     │  Data  │───▶│  Quant  │    │  Code    │
     │ Brain  │    │  Brain  │    │  Brain   │
     └────────┘    └─────────┘    └──────────┘
      mmap ring      ↕ auth        独立
      buffer       review_trade    workspace
```

### 5.2 跨脑通信双通道

| 通道 | 延迟 | 适用场景 | 实现 |
|------|------|----------|------|
| 快路径 | < 0.1ms | 实时数据分发（Data → Quant） | `/dev/shm` mmap ring buffer |
| 慢路径 | 1-5ms | RPC 调用、委托、审查 | stdio JSON-RPC 2.0 |

### 5.3 跨脑授权矩阵

当前 `SpecialistToolCallAuthorizer` 已实现的白名单：

| 调用方 | 目标 | 允许的工具 |
|--------|------|-----------|
| quant | data | `data.get_candles`, `data.get_snapshot`, `data.get_feature_vector` 等 |
| quant | central | `central.review_trade` |
| data | central | `central.data_alert` |
| verifier | browser | `browser.*` |

v3 目标：从静态白名单升级为**语义审批分级**（见 §7.3）。

---

## 6. v3 分阶段路线

### Phase A：资源层——Brain Pool + Lease Model（v3.0 核心）

**目标**：先把资源调度做对

**核心问题**：当前 serve 模式每个 run 各建一个 Orchestrator，sidecar 复用失效。这不是"优化"，是架构缺陷——6 个 run 同时跑会 fork 6 套 sidecar 进程。

| 编号 | 工作项 | 基础 | 交付物 |
|------|--------|------|--------|
| A-1 | Brain Pool | Orchestrator.active pool 已有原型 | 全局 sidecar 生命周期管理，三种复用策略（详见 §7.7） |
| A-2 | Lease Model | ProcessRunner 已有进程管理 | brain/tool 级别的租约获取、释放、超时回收（详见 §7.8） |
| A-3 | Task Runtime | runManager + Run/Turn 已有 | TaskExecution 统一模型 + Mode × Lifecycle × Restart 策略组合（详见 §7.7） |
| A-4 | Orchestrator 瘦身 | Orchestrator 已有 delegate/CallTool | 降级为协调接口，sidecar 管理下沉到 Brain Pool |
| A-5 | 统一 Dashboard | Quant WebUI 已实现 | 全脑 Dashboard：brain 状态、任务、事件流、provider 认证 |
| A-6 | 事件流 | OTLP + runtimeaudit 分散存在 | 统一 EventBus 聚合层 |

### Phase B：调度层——Parallel Execution + Scheduler（v3.1）

**目标**：再把并发执行做快

| 编号 | 工作项 | 基础 | 交付物 |
|------|--------|------|--------|
| B-1 | Parallel Tool Batch | Agent Loop turn_executor 已有批量执行 | tool_call 并发元数据：serial / parallel / exclusive（详见 §7.9） |
| B-2 | Scheduler | 无 | 任务派发引擎：依赖检查、并发控制、brain 选择 |
| B-3 | 语义审批分级 | SpecialistToolCallAuthorizer 已有 | 5 级审批类，与 Lease 权限集成 |
| B-4 | Context Engine | 三层 Prompt Cache 框架已有 | 可插拔上下文装配层：压缩、摘要、跨 turn 记忆、brain 间上下文共享 |
| B-5 | 自适应工具策略 | tool_profiles/active_tools 已有 | 基于任务上下文动态调整工具集 |
| B-6 | 四层自适应学习 | Quant WeightAdapter/SymbolScorer 已有 L0 | 多脑协作学习体系：L0 Brain 级 → L1 协作级 → L2 策略级 → L3 用户级（详见 §7.6） |
| B-7 | MCP-backed Runtime | mcpadapter 已实现 | 第一个 MCP-backed brain 参考实现 + host 约定 |

### Phase C：编排层——Workflow + Brain Identity（v3.2）

**目标**：最后把多任务编排做完整

| 编号 | 工作项 | 基础 | 交付物 |
|------|--------|------|--------|
| C-1 | Workflow Engine | TaskExecution + Dispatch Policy 已就绪 | 任务图（DAG）：节点是 TaskExecution，边是 Flow Edge（materialized / streaming） |
| C-2 | Background Job | serve HTTP API 已有 | 长驻任务：Data Brain 持续采集、Quant Brain 自动交易、watch/retry/resume |
| C-3 | Manifest v1 解析器 | 33 号文档 schema 已定义 | orchestrator 读取 manifest.json 做脑发现，替代硬编码注册 |
| C-4 | 本地 Brain 管理 | — | `brain install / activate / deactivate / upgrade / rollback` |
| C-5 | 第三方 Brain 接入 | 29 号文档已有指南 | 标准 sidecar 模板 + 合规测试套件 |

### Phase D：分发与远程（v3.3+）

**目标**：让多脑系统"可分发"

| 编号 | 工作项 | 说明 |
|------|--------|------|
| D-1 | Package 规范落地 | 标准目录布局、签名校验、安装器 |
| D-2 | Marketplace 索引 | brain 发现、搜索、兼容筛选 |
| D-3 | Remote Runtime | 远程 brain、多租户、企业控制面 |
| D-4 | 组织级授权 | 企业 license、edition 管理、集中 policy |

**阶段逻辑**：

> **先把资源调度做对（Phase A），再把并发执行做快（Phase B），最后把多任务编排做完整（Phase C）。** 生态从 Phase A 就在建设——Brain Pool 的复用策略就是第三方 brain 接入的基础，Lease Model 就是多租户的前身。

---

## 7. 重点设计细节

### 7.1 TaskExecution：唯一的执行对象（Phase A-3）

当前代码把所有执行都塞进 `Run` 一个概念里。v4 不再定义 Run / Task / Job 三种核心对象——核心对象只有一个：**TaskExecution**。差异放在 policy 上（详见 §7.7.1）。

API 按 policy 语义分入口，但后端统一为 TaskExecution：

```text
POST   /v1/executions              → 创建 TaskExecution（通用入口）
POST   /v1/runs                    → 创建 TaskExecution(Interactive+OneShot)（用户友好别名）
POST   /v1/jobs                    → 创建 TaskExecution(Daemon+Always)（用户友好别名）
GET    /v1/executions              → 列出所有执行单元
GET    /v1/executions/{id}         → 查询状态
GET    /v1/executions/{id}/events  → SSE 事件流
POST   /v1/executions/{id}/stop   → 优雅停止
GET    /v1/dashboard/overview      → 全局状态总览
```

### 7.2 统一 Dashboard（Phase A-1）

从 Quant WebUI 扩展为全脑控制面：

```text
┌─────────────────────────────────────────────────────┐
│  Brain Dashboard                                     │
├──────────┬──────────┬──────────┬───────────────────── │
│  Brains  │  Tasks   │  Events  │  Provider Status    │
├──────────┼──────────┼──────────┼─────────────────────│
│ ● data   │ Run #12  │ 14:32 ⚠ │ Claude: ✓ healthy  │
│   9 tools│  running │ data lag │ DeepSeek: ✓ healthy│
│ ● quant  │ Job #3   │ 14:31 ✓ │ HunYuan: ✗ expired │
│   14 tool│  active  │ trade ok │                     │
│ ○ code   │ Run #11  │ 14:30 ✓ │ Vault: 3 keys      │
│   idle   │  done    │ delegate │ 2 expiring soon     │
│ ● browser│          │          │                     │
│   running│          │          │                     │
└──────────┴──────────┴──────────┴─────────────────────┘
```

Dashboard 技术方案：

- **后端**：扩展 `brain serve`，新增 `/v1/dashboard/*` API 端点
- **前端**：内嵌 SPA（复用 Quant WebUI 的 embed 模式）
- **实时推送**：WebSocket（复用 Quant WebUI 的 `/ws` 模式）
- **数据源**：orchestrator 状态 + runManager + EventBus + Vault

### 7.3 语义审批分级（Phase B-1）

从"工具白名单"升级为"能力等级审批"：

| 等级 | 名称 | 含义 | 审批方式 |
|------|------|------|----------|
| L0 | `readonly` | 只读操作 | 自动通过 |
| L1 | `workspace-write` | 工作区文件修改 | 当前模式决定 |
| L2 | `exec-capable` | 执行外部命令 | 需确认 |
| L3 | `control-plane` | 系统控制操作 | 需确认 + 审计 |
| L4 | `external-network` | 外部网络请求 | 需确认 + 审计 + 可选人工审批 |

每个工具在注册时声明自己的审批等级，审批策略基于等级而不是工具名。

### 7.4 Context Engine（Phase B-2）

在现有三层 Prompt Cache 基础上，增加统一的上下文装配层：

```text
┌─────────────────────────────────────┐
│          Context Engine              │
├──────────┬──────────┬───────────────┤
│ Compressor│ Memory  │ Brain-Shared  │
│ 上下文压缩│ 跨 turn │ 跨脑上下文共享│
│ 摘要/裁剪 │ 记忆持久│ 委托时传递    │
└──────────┴──────────┴───────────────┘
         ↓           ↓           ↓
┌─────────────────────────────────────┐
│      三层 Prompt Cache (已有)        │
│  L1 System │ L2 Task │ L3 History  │
└─────────────────────────────────────┘
```

接口：

```go
type ContextEngine interface {
    Assemble(ctx context.Context, req AssembleRequest) ([]llm.Message, error)
    Compress(ctx context.Context, messages []llm.Message) ([]llm.Message, error)
    Share(ctx context.Context, from BrainKind, to BrainKind, context []llm.Message) error
}
```

### 7.5 Manifest 精简方案（Phase C-1）

v3 的 Manifest 先做"能用"，不做"完美"：

```json
{
  "schema_version": 1,
  "kind": "browser",
  "name": "Browser Brain",
  "brain_version": "1.0.0",
  "capabilities": ["web.browse", "web.extract"],
  "runtime": {
    "type": "native",
    "entrypoint": "bin/brain-browser"
  },
  "policy": {
    "approval_class": "workspace-write"
  }
}
```

解析器只需要：

1. 读取 `manifest.json`
2. 校验 `schema_version` + `kind` + `runtime.type`
3. 注册到 orchestrator
4. 完成

后续字段按需扩展，不阻塞启动。

### 7.6 四层自适应学习体系（Phase B-5）

这是多脑系统和单 agent 系统的**根本差异化**。

Hermes Agent 的 Learning Loop 只能做"单 agent 自我改进"——一个脑从自己的历史里学。我们的多脑架构天然支持四个维度的学习，是单 agent 系统做不到的：

```text
┌─────────────────────────────────────────────────────────────┐
│                     L3 用户级学习                             │
│  学习用户偏好和工作模式                                       │
│  例："这个用户习惯先看数据再做决策，自动预取"                    │
│  ┌─────────────────────────────────────────────────────────┐│
│  │                  L2 策略级学习                           ││
│  │  学习 delegation 策略和工具组合                           ││
│  │  例："这类任务先 data 再 quant 的成功率比反过来高"         ││
│  │  ┌─────────────────────────────────────────────────────┐││
│  │  │               L1 协作级学习                          │││
│  │  │  学习哪个 brain 更擅长什么任务                        │││
│  │  │  例："代码审查 delegate 给 verifier 比 code 效果好"   │││
│  │  │  ┌─────────────────────────────────────────────────┐│││
│  │  │  │            L0 Brain 级学习                       ││││
│  │  │  │  单个 brain 从自己的执行历史中学习                 ││││
│  │  │  │  例：Quant 的 WeightAdapter / SymbolScorer       ││││
│  │  │  └─────────────────────────────────────────────────┘│││
│  │  └─────────────────────────────────────────────────────┘││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
```

#### 四层学习详解

| 层级 | 学习主体 | 学习内容 | 数据来源 | 存储位置 | 已有基础 |
|------|----------|----------|----------|----------|----------|
| **L0 Brain 级** | 每个专精 brain | 自身执行效果、领域参数优化 | 执行结果反馈 | brain 本地存储 | ✅ Quant 部分实现 |
| **L1 协作级** | Central | 哪个 brain 更擅长什么任务 | delegate 结果评分 | Central 记忆 | ⚠️ 有 delegate 但无评分 |
| **L2 策略级** | Central | delegation 顺序、工具组合效果 | 多步任务执行链路 | 策略库 | ⚠️ 有多步执行但无策略学习 |
| **L3 用户级** | Central | 用户偏好、工作模式、上下文习惯 | 用户交互历史 | 用户 profile | ❌ 待实现 |

#### L0 专精大脑学习能力设计

L0 是整个学习体系的地基。每个专精大脑都应该有**领域特化的自适应学习能力**，而不是只有 Quant 会学习。

通用 L0 学习框架：

```go
// 每个专精大脑实现此接口
type BrainLearner interface {
    // 记录一次执行的结果反馈
    RecordOutcome(ctx context.Context, taskID string, outcome Outcome) error
    // 基于历史反馈调整自身参数/策略
    Adapt(ctx context.Context) error
    // 导出学习指标供 Central 读取（用于 L1 协作级学习）
    ExportMetrics(ctx context.Context) (BrainMetrics, error)
}
```

各专精大脑的 L0 学习内容：

| Brain | 学习什么 | 怎么学 | 存储什么 | 已有基础 |
|-------|----------|--------|----------|----------|
| **Quant** | 策略权重、品种评分、信号阈值 | EWMA 衰减 + 盈亏反馈 | WeightAdapter 权重表、SymbolScorer 评分表 | ✅ 已实现 |
| **Data** | 数据源可靠性、采集频率、异常模式 | 延迟/错误率统计 + 异常频率分析 | 数据源评分表、异常 pattern 库 | ⚠️ 有监控但无学习 |
| **Code** | 代码风格偏好、review 通过率 | 用户接受/拒绝反馈 + review 结果 | 风格模型、review 统计表 | ❌ 待实现 |
| **Browser** | 网站结构模式、提取规则有效性、反爬策略 | 提取成功率 + 页面结构变更检测 | 站点模型库、选择器有效性表 | ❌ 待实现 |
| **Verifier** | 验证规则有效性、误报率、漏报率 | 验证结果与实际结果对比 | 规则权重表、误报/漏报统计 | ❌ 待实现 |
| **Fault** | 故障模式识别、恢复策略有效性、告警准确率 | 故障处理结果反馈 + 告警验证 | 故障模式库、恢复策略评分表 | ❌ 待实现 |

逐脑详解：

**Quant Brain（已有，作为参考实现）**

```text
交易执行 → 盈亏反馈
    ↓
WeightAdapter: 策略权重 EWMA 衰减调整
SymbolScorer:  品种得分基于历史胜率/盈亏比
    ↓
下次选策略/选品种时使用更新后的权重
```

**Data Brain**

```text
数据采集 → 延迟/错误/数据质量反馈
    ↓
SourceReliabilityScorer:
  - 按数据源统计：平均延迟、错误率、数据完整率
  - 低可靠性数据源自动降级（降频 / 切备用源）
AnomalyPatternLearner:
  - 记录历史异常模式（时间、类型、影响范围）
  - 相似模式再现时提前预警
    ↓
下次采集时优先可靠数据源，遇到已知异常模式时快速响应
```

**Code Brain**

```text
代码生成/修改 → 用户接受/拒绝/修改反馈
    ↓
StyleAdapter:
  - 跟踪用户对命名、缩进、注释风格的偏好
  - 记录用户修改后的最终版本 vs 生成版本的 diff
ReviewPassRate:
  - 按修改类型统计 review 通过率
  - 通过率低的类型自动加强自检
    ↓
下次生成代码时贴合用户风格，高打回率的修改类型自动加强自检
```

**Browser Brain**

```text
网页提取 → 提取成功率 + 内容质量反馈
    ↓
SiteModelLearner:
  - 记录每个域名的页面结构模式（SPA/SSR/API）
  - 学习最优提取策略（CSS 选择器 / XPath / API 直调）
SelectorValidator:
  - 定期验证已保存选择器的有效性
  - 页面结构变更时自动尝试修复选择器
AntiCrawlAdapter:
  - 记录触发反爬的请求模式
  - 自适应调整请求频率、UA 轮换策略
    ↓
下次访问同一站点时使用已学到的最优策略
```

**Verifier Brain**

```text
验证执行 → 验证结果 vs 实际结果对比
    ↓
RuleWeightAdapter:
  - 高准确率规则提权，高误报规则降权
  - 从未触发的规则标记为候选清理
FalsePositiveTracker:
  - 统计各规则的误报率（验证失败但实际正确）
  - 误报率 > 阈值的规则自动降级为 warning
FalseNegativeTracker:
  - 统计漏报率（验证通过但实际有问题）
  - 漏报率高的领域自动增加验证深度
    ↓
下次验证时使用调整后的规则权重，减少噪音、降低漏报
```

**Fault Brain**

```text
故障处理 → 恢复结果反馈
    ↓
FaultPatternDB:
  - 记录故障签名（错误类型 + 上下文特征）
  - 相似故障再现时匹配历史模式，推荐已验证的恢复策略
RecoveryScorer:
  - 按恢复策略统计成功率和恢复耗时
  - 同类故障优先使用成功率最高的策略
AlertAccuracyTracker:
  - 统计告警的准确率（真故障 vs 噪音）
  - 低准确率的告警源/规则自动调整阈值
    ↓
下次遇到故障时：更快识别、更准定位、更高效恢复
```

**L0 → L1 上报机制**

每个专精大脑定期将学习指标上报给 Central，供 L1 协作级学习使用：

```go
type BrainMetrics struct {
    BrainKind       string             // 哪个大脑
    Period          time.Duration      // 统计周期
    TaskCount       int                // 执行任务数
    SuccessRate     float64            // 成功率
    AvgLatency      time.Duration      // 平均耗时
    ConfidenceTrend float64            // 置信度趋势（上升/下降）
    DomainMetrics   map[string]float64 // 领域特化指标
}
```

Central 消费这些指标来更新 L1 的 brain 能力画像——**L0 的学习结果自然流入 L1，L1 的评估反过来影响 Central 对该 brain 的委托频率，形成正反馈循环。**

#### 学习循环机制

```text
执行 → 采集 → 评估 → 更新 → 执行
  │       │       │       │
  │       │       │       └─ 更新策略/权重/偏好
  │       │       └─ 对比预期与实际效果
  │       └─ 记录执行链路、耗时、结果质量
  └─ 使用当前策略执行任务
```

核心接口：

```go
// === L0: 专精大脑侧接口（每个 brain sidecar 实现） ===

type BrainLearner interface {
    RecordOutcome(ctx context.Context, taskID string, outcome Outcome) error
    Adapt(ctx context.Context) error
    ExportMetrics(ctx context.Context) (BrainMetrics, error)
}

// === L1-L3: Central 侧接口（中央大脑实现） ===

type LearningEngine interface {
    // L1: 协作级学习——消费各 brain 上报的 BrainMetrics，维护能力画像
    IngestBrainMetrics(ctx context.Context, metrics BrainMetrics) error
    RankBrainsForTask(ctx context.Context, taskType string) ([]BrainRanking, error)

    // L1: delegate 结果评分
    RecordDelegateResult(ctx context.Context, from BrainKind, to BrainKind, taskType string, score float64) error

    // L2: 策略级学习——执行链路优化
    RecordExecutionChain(ctx context.Context, chain ExecutionChain, outcome Outcome) error
    SuggestChain(ctx context.Context, taskType string) (ExecutionChain, float64, error)

    // L3: 用户级学习——用户偏好建模
    RecordUserPreference(ctx context.Context, userID string, pref PreferenceSignal) error
    GetUserProfile(ctx context.Context, userID string) (UserProfile, error)
}
```

#### 与 Hermes Learning Loop 的对比

| 维度 | Hermes | Brain v3 |
|------|--------|----------|
| 学习范围 | 单 agent 自我改进 | 四层：Brain/协作/策略/用户 |
| 学习产物 | 新 skill 生成 | 6 脑各自领域模型 + 能力画像 + 策略库 + 用户 profile |
| 跨脑学习 | 不支持（单 agent） | L0 上报 → L1 消费，形成正反馈循环 |
| 策略优化 | 无 | delegation 顺序和工具组合自动优化 |
| 用户适应 | 无 | 基于交互历史的偏好建模 |

**L0 已有基础，L1-L3 是 Phase B 新增。这四层加在一起，就是"agent 团队越用越好"——不只是单个 agent 更聪明，而是整个团队的协作效率在持续提升。**

### 7.7 统一执行模型（Phase A 核心）

v4 不再围绕 Brain Pool / Scheduler / Job 分别设计，而是统一为**四个正交概念**：

> **TaskExecution + Capability Lease + Dispatch Policy + Flow Edge**

不单独抬成顶层概念的东西：
- 不要单独一个大而全的 Scheduler（变成 turn_executor 内部策略）
- 不要把复用策略硬绑定在 brain 上（绑在 capability lease 上）
- 不要把 Job 做成和 Run/Task 平级的新对象（变成 lifecycle policy）
- 不要把 Artifact Pipe 做成脱离 DAG 的另一套传输系统（变成 flow edge 的一种）

#### 7.7.1 TaskExecution：唯一的执行对象

不定义 Run / Task / Job 三种核心对象。核心对象只有一个：**TaskExecution**。差异放在 policy 上。

```go
type TaskExecution struct {
    ID          string
    ParentID    string              // 谁生成了我（顶层为空）
    Context     context.Context
    Messages    []llm.Message
    Budget      Budget
    Inputs      []EdgeRef           // 输入边（materialized ref 或 streaming pipe）
    Outputs     []EdgeRef           // 输出边
    Status      ExecutionStatus

    Mode        ExecutionMode       // 入口语义：前台 or 后台
    Lifecycle   LifecyclePolicy     // 生命周期：一次性 / 持续 / 监控
    Restart     RestartPolicy       // 重启策略
}

type EdgeRef struct {
    EdgeMode EdgeMode              // materialized / streaming
    Ref      string                // CAS ref 或 pipe ID
}
```

三个 policy 维度正交，不混用：

- **ExecutionMode**：入口语义，决定"用户能不能看到输出流"
- **LifecyclePolicy**：生命周期，决定"什么时候结束"
- **RestartPolicy**：崩溃行为，决定"挂了怎么办"

```go
type ExecutionMode string
const (
    ModeInteractive ExecutionMode = "interactive"  // 前台，输出流直连用户
    ModeBackground  ExecutionMode = "background"   // 后台，通过 EventBus 通知
)

type LifecyclePolicy string
const (
    OneShot LifecyclePolicy = "oneshot"   // 执行完即结束
    Daemon  LifecyclePolicy = "daemon"    // 持续运行直到被停止
    Watch   LifecyclePolicy = "watch"     // 监控型，有事件时通知
)

type RestartPolicy string
const (
    Never     RestartPolicy = "never"
    OnFailure RestartPolicy = "on-failure"
    Always    RestartPolicy = "always"
)
```

通过 policy 组合覆盖所有场景：

| 场景 | Mode | Lifecycle | Restart | 说明 |
|------|------|-----------|---------|------|
| 用户 `brain run` | `interactive` | `oneshot` | `never` | 前台执行一次 |
| Central delegate 子任务 | `background` | `oneshot` | `never` | 内部调度单元 |
| Data Brain 行情采集 | `background` | `daemon` | `always` | 后台长驻，崩溃自动重启 |
| Quant 自动交易 | `background` | `daemon` | `on-failure` | 后台长驻，只在异常时重启 |
| chat `/background` | `background` | `watch` | `on-failure` | 后台监控，有结果推送通知 |

所有 TaskExecution 共用同一套：lease、cancellation、observability、restart、artifact edge。不为 Job 单独造类型。

#### 7.7.2 Capability Lease：对能力实例加锁，不是对 brain 加锁

**核心洞察**：冲突不在 brain 级别，在资源级别。`quant.query_portfolio` 和 `quant.place_order` 的锁粒度完全不同。

Lease 是三元组：**Capability × ResourceKey × AccessMode**

```go
type LeaseRequest struct {
    Capability  string      // "execution.order" / "fs.write" / "session.browser"
    ResourceKey string      // "account:paper-main" / "workdir:/repo-a" / "browser:session-1"
    AccessMode  AccessMode
    Scope       LeaseScope
}

type AccessMode string
const (
    SharedRead        AccessMode = "shared-read"
    SharedWriteAppend AccessMode = "shared-write-append"
    ExclusiveWrite    AccessMode = "exclusive-write"
    ExclusiveSession  AccessMode = "exclusive-session"  // 黏住会话状态
)
```

示例：

| tool | Capability | ResourceKey | AccessMode |
|------|-----------|-------------|------------|
| `quant.get_positions` | `portfolio.read` | `account:*` | SharedRead |
| `quant.place_order` | `execution.order` | `account:paper-main` | ExclusiveWrite |
| `code.read_file` | `fs.read` | `workdir:/repo-a` | SharedRead |
| `code.write_file` | `fs.write` | `workdir:/repo-a` | ExclusiveWrite |
| `browser.click` | `session.browser` | `browser:session-1` | ExclusiveSession |
| `data.subscribe` | `market.subscribe` | `symbol:BTC-USDT` | SharedRead |

两个 task 同时查 Quant 的持仓——SharedRead，不冲突，并行跑。
两个 task 同时下单到同一个账户——ExclusiveWrite 同一个 ResourceKey，排队。
两个 task 操作不同账户——ExclusiveWrite 但 ResourceKey 不同，不冲突，并行跑。

#### 7.7.3 LeaseScope：什么时候释放

```go
type LeaseScope string
const (
    ScopeTurn   LeaseScope = "turn"    // 一轮 tool batch 结束即释放
    ScopeTask   LeaseScope = "task"    // task 完成才释放（browser session 独占）
    ScopeDaemon LeaseScope = "daemon"  // 持续持有直到停止（行情订阅）
)
```

| 场景 | Scope | 说明 |
|------|-------|------|
| 一轮并发 tool batch 的文件写锁 | `turn` | batch 执行完自动释放 |
| delegate 给 browser 的整个 task | `task` | 整个 task 期间独占 session |
| Data Brain 行情订阅 | `daemon` | 持续持有直到被停止 |

Scope 直接影响：
- 什么时候释放
- 能不能跨 turn 继承
- 崩溃恢复后是否自动续租（daemon scope → 自动续租）

#### 7.7.4 ToolConcurrencySpec：Intent 冻结在 tool schema 上

Lease 的参数不应该让 prompt 或调用方随便填。Intent 冻结在 **tool schema** 上，工具注册时声明。

拆成两个子概念，避免能力分类和并发控制混在一起：
- **CapabilityBinding**：工具触碰什么资源（能力分类），用于治理和路由——注意这是 **工具级别** 的绑定，与 §3.6 Brain Capability（脑级别路由信号）是不同层级
- **LeaseTemplate**：并发控制，用于锁管理

```go
type ToolConcurrencySpec struct {
    // CapabilityBinding
    Capability          string

    // LeaseTemplate
    ResourceKeyTemplate string        // "account:{{account_id}}" / "workdir:{{path}}"
    AccessMode          AccessMode
    Scope               LeaseScope

    // 申请参数
    AcquireTimeout      time.Duration
    RetryPolicy         RetryPolicy
}
```

注册示例：

```go
tool.Schema{
    Name: "quant.place_order",
    Concurrency: ToolConcurrencySpec{
        Capability:          "execution.order",
        ResourceKeyTemplate: "account:{{account_id}}",
        AccessMode:          ExclusiveWrite,
        Scope:               ScopeTurn,
        AcquireTimeout:      5 * time.Second,
        RetryPolicy:         RetryPolicy{MaxRetries: 2, Backoff: 100 * time.Millisecond},
    },
}
```

**ResourceKey 提取**：Phase A 用模板字符串（从 tool_call 参数填充），Phase B 预留 extractor 扩展位：

```go
type LeaseTemplate struct {
    ResourceKeyTemplate  string // Phase A：模板替换 "account:{{account_id}}"
    ResourceKeyExtractor string // Phase B：JSONPath / arg selector（可选，模板不够时手写）
}
```

### 7.8 Dispatch Policy：turn_executor 的调度内核（Phase B）

不做独立的 Scheduler 服务。并发调度是 **turn_executor 的内部策略**（Batch Planner）。

#### 核心流程

当 LLM 一次返回多个 tool_call 时：

```text
tool_calls
  → 从每个 tool 的 ToolConcurrencySpec 推导 LeaseRequest
  → 用 ResourceKey 构建冲突图（conflict graph）
  → 按冲突图分组为互不冲突的并行 batch
  → 对每个 batch 用 AcquireSet 原子申请全部 lease
  → 并行执行 batch 内的 tool_call
  → 下一个 batch...
  → 结果按原 tool_call 顺序回填
```

**关键设计**：并发性由资源冲突推导出来，不靠人工硬标 parallel/serial/exclusive。

示例：

```text
LLM 返回 4 个 tool_call：
  call_1: data.get_snapshot(BTC)        → SharedRead    symbol:BTC
  call_2: data.get_snapshot(ETH)        → SharedRead    symbol:ETH
  call_3: quant.place_order(paper-main) → ExclusiveWrite account:paper-main
  call_4: code.write_file(/repo-a/x.go) → ExclusiveWrite workdir:/repo-a

冲突分析：
  call_1 和 call_2：不同 ResourceKey，不冲突
  call_1 和 call_3：不同 Capability，不冲突
  call_3 和 call_4：不同 Capability + 不同 ResourceKey，不冲突
  → 全部可并行！

如果再加一个 call_5: quant.place_order(paper-main)：
  call_3 和 call_5：同 Capability + 同 ResourceKey + ExclusiveWrite
  → 冲突，分两个 batch

Batch 1 (并行): call_1 + call_2 + call_3 + call_4
Batch 2 (等 Batch 1 完成): call_5
```

### 7.9 死锁防护三层策略（设计约束）

Capability Lease 引入了细粒度锁，**必须**在设计层面防死锁。这不是附录，是 Phase A 的硬约束。

**P0（Phase A）：AcquireSet 批量原子申请**

```go
func (lm *LeaseManager) AcquireSet(ctx context.Context, reqs []LeaseRequest) ([]Lease, error) {
    // 1. 按 canonical order 排序（Capability + ResourceKey 字典序）
    sort.Sort(byCanonicalOrder(reqs))
    // 2. 依次尝试获取
    // 3. 任何一个失败 → 全部释放已获取的 → 退避重试
    // 4. 全部超时 → 返回 ErrAcquireTimeout
}
```

核心约束：
- turn_executor 先从一轮 tool_call 推导出所有 LeaseRequest
- 按 batch 原子申请——申请失败则整批不执行，退避重试
- 每个 batch 有 timeout + jitter backoff
- **禁止普通工具执行过程中再隐式追加跨资源 lease**

> **适用边界**：AcquireSet 只覆盖**单 turn 内已知的 tool batch**。对于 daemon / watch / streaming edge 运行期动态新增的资源需求，不走 AcquireSet，而是由 P2 层（Wait-for Graph + Victim Selection）兜底。

这样大部分死锁在结构上被消除——task 不能边跑边拿第二把锁。

**P1（Phase A 补充）：Canonical Lease Ordering**

即使做了 AcquireSet，内部也按稳定顺序（Capability + ResourceKey 字典序）排序后申请。避免实现层面因 map 遍历顺序不同导致的不一致。

**P2（Phase C）：Wait-for Graph + Victim Selection**

当出现以下场景时才需要：
- 流式 task 长期持有 lease（daemon scope）
- workflow 节点动态扩张资源集
- task 内部允许二次申请 lease（需显式 opt-in）

此时引入等待图检测环路，选择优先级低的 task 中断。Phase A/B 不需要。

### 7.10 Flow Edge：任务间数据传递（Phase C）

任务间传递数据不应该发明独立的 Pipe 子系统。数据传递是 **Workflow DAG 的边**，边自带传输语义。

#### 两种边类型

```go
type EdgeMode string
const (
    EdgeMaterialized EdgeMode = "materialized"  // 完成后写入 CAS，通过 ref 传递
    EdgeStreaming    EdgeMode = "streaming"      // 运行中通过 pipe/channel 传递
)
```

```text
Task A ──materialized──▶ Task B    （A 完成 → 写 CAS → B 读 ref）
Task A ──streaming────▶ Task B    （A 产出数据 → B 实时消费）
```

Streaming Edge Backend：

| Backend | 说明 | 适用 |
|---------|------|------|
| `pipe` | 进程内 channel | 同主机短生命周期 |
| `ringbuf` | /dev/shm mmap | Data→Quant 已有（不再是硬编码特例） |
| `queue` | 外部 MQ | 跨主机（后置） |

后面可以扩展成 topic/pubsub，不需要推翻模型。

### 7.11 Brain Pool 与 Orchestrator 重构

有了 Capability Lease，Brain Pool 和 Orchestrator 的职责边界变得清晰。

**Brain Pool**：只管进程，不管锁。

```go
type BrainPool interface {
    GetBrain(ctx context.Context, kind agent.Kind) (protocol.BidirRPC, error)
    Status() map[agent.Kind]BrainStatus
    Shutdown(ctx context.Context) error
}
```

sidecar 复用策略只影响进程管理，不影响并发控制。

> **明确边界**：shared-service / exclusive-session / ephemeral-worker 是**默认进程管理策略**，不是并发语义，不是锁语义。并发控制全部由 Capability Lease 负责，与 Brain Pool 层无关。

| 策略 | brain | 进程行为 |
|------|-------|---------|
| **shared-service** | Data, Quant | 长驻单例，多 task 共享进程 |
| **exclusive-session** | Browser | 单例，Lease 控制并发 |
| **ephemeral-worker** | Code, Verifier, Fault | 按需启动，空闲回收 |

**并发控制全部下沉到 Capability Lease——不在 Brain Pool 层做。**

**Orchestrator 瘦身**：不管锁，不管进程。

```go
type Orchestrator struct {
    pool          BrainPool
    leaseManager  *LeaseManager
    registrations map[agent.Kind]*BrainRegistration
    authorizer    ToolCallAuthorizer
}

func (o *Orchestrator) Delegate(ctx context.Context, req *SubtaskRequest) (*SubtaskResult, error) {
    rpc, err := o.pool.GetBrain(ctx, req.TargetKind)
    if err != nil {
        return nil, err
    }
    // lease 由 turn_executor 在外层 AcquireSet 已获取
    // Orchestrator 只做路由
    return callBrainExecute(ctx, rpc, req)
}
```

### 7.12 架构总览

```text
┌─────────────────────────────────────────────────────────┐
│  用户层                                                  │
│  chat (串行 REPL)  /  serve (并发 HTTP API)              │
└───────────────┬─────────────────────────────────────────┘
                │
┌───────────────────▼─────────────────────────────────────────┐
│  TaskExecution                                           │
│  唯一执行对象，通过 policy 组合覆盖所有场景               │
│  LifecyclePolicy × LeasePolicy × RestartPolicy           │
└───────────────┬─────────────────────────────────────────┘
                │
┌───────────────▼─────────────────────────────────────────┐
│  turn_executor + Dispatch Policy                         │
│  读取 ToolConcurrencySpec → 冲突分析 → batch 分组        │
│  AcquireSet 原子申请 → 并行执行 → 结果回填               │
└──────┬────────────────────────────┬─────────────────────┘
       │                            │
       ▼                            ▼
┌──────────────────┐    ┌───────────────────────────────┐
│  LeaseManager    │    │  Orchestrator（瘦身后）         │
│  Capability ×    │    │  只做 delegate / CallTool 路由  │
│  ResourceKey ×   │    │  不管锁，不管进程               │
│  AccessMode ×    │    └────────────────┬────────────────┘
│  Scope           │                   │
│                  │    ┌──────────────▼────────────────┐
│  AcquireSet      │    │  Brain Pool                    │
│  canonical order │    │  sidecar 进程管理（不管锁）     │
│  timeout+backoff │    │  shared / exclusive / ephemeral│
└──────────────────┘    └──────────────┬────────────────┘
                                       │
                        ┌──────────────▼────────────────┐
                        │  ProcessRunner                 │
                        │  fork/exec + stdio JSON-RPC    │
                        │  （已有，不需要改）              │
                        └───────────────────────────────┘
```

**关键分离**：
- **LeaseManager** 管锁——Capability × ResourceKey × AccessMode × Scope
- **Brain Pool** 管进程——启动、复用、回收 sidecar
- **Orchestrator** 管路由——delegate/CallTool 到正确的 brain
- **turn_executor** 管调度——冲突分析、batch 分组、并行执行
- 四者互不知道对方的内部实现

**chat 和 serve 在这个架构下的行为**：

| 模式 | 行为 | 说明 |
|------|------|------|
| **chat** | TaskExecution(Interactive+OneShot) → 串行 turn → 结果输出 | 天然串行，不硬塞并发 |
| **serve** | 并发创建多个 TaskExecution → 共享 Brain Pool → LeaseManager 控制并发 | HTTP 层并发，Lease 层隔离 |
| **chat + /background** | TaskExecution(Watch+PushNotify) → 后台独立运行 → EventBus 通知 | 前台交互 + 后台监控 |
| **Data 行情采集** | TaskExecution(Daemon+Always) → daemon scope lease → 持续运行 | 崩溃自动重启 |


---

## 8. 与 v1 架构的对比

### 8.1 不变的

| 设计决策 | 状态 |
|----------|------|
| Brain-first 顶层产品对象 | **保持** |
| central 只 delegate 给 brain | **保持** |
| MCP 是 runtime backend，不是顶层对象 | **保持** |
| 所有 brain 强制 sidecar | **保持** |
| stdio JSON-RPC 2.0 自研协议 | **保持** |
| Manifest 作为稳定契约 | **保持** |

### 8.2 变的

| v1 | v2 | 原因 |
|----|-----|------|
| Manifest 12 字段 | 核心 7 字段 + 可选扩展 | 先跑起来 |
| Package/Marketplace 是 v3.0 核心 | 降到 Phase D | 本地生态先行 |
| runtime 四分法平行推进 | native P0，mcp-backed P0，其余后置 | 聚焦 |
| 无控制面概念 | Control Plane 是 Phase A 核心 | 行业趋势 + 代码已有基础 |
| 无任务模型扩展 | TaskExecution 统一模型 + Capability Lease + Dispatch Policy | 多任务已有基础 |
| 静态工具白名单 | 语义审批分级 | 行业趋势 |
| 无 Context Engine | 可插拔上下文装配层 | 直接影响 agent 质量和成本 |
| 无系统级学习能力 | 四层自适应学习体系（L0-L3） | 多脑协作的核心差异化，单 agent 无法复制 |
| Orchestrator 承载一切 | TaskExecution + Capability Lease + Dispatch Policy + Flow Edge 四正交概念 | 每个概念职责单一 |
| per-run 各建 Orchestrator | 全局 Brain Pool（管进程）+ LeaseManager（管锁）分离 | 解决 sidecar 复用失效和资源冲突 |
| Run 是唯一任务单位 | TaskExecution + LifecyclePolicy 组合 | 不造新类型，用 policy 覆盖所有场景 |
| 串行 delegate | turn_executor 冲突分析 + AcquireSet 批量原子申请 | 并发性由资源冲突推导，不靠人工标记 |
| 无并发控制 | Capability × ResourceKey × AccessMode × Scope 四维 Lease | 细粒度资源锁，不是粗暴的 brain 级锁 |
| 无死锁防护 | P0 AcquireSet + P1 canonical ordering + P2 wait-for graph | 三层递进，Phase A 就消除大部分死锁面 |
| remote runtime 平行推进 | 明确降级后置 | 减少文档和代码差距 |

---

## 9. 我们的差异化

这是 v3 必须反复回答的问题：**我们和 OpenClaw/Hermes/Claude Code 有什么不同？**

| 维度 | 他们 | 我们 |
|------|------|------|
| **架构** | 单 Agent + 工具/插件扩展 | 中央大脑 + N 专精大脑并行协作 |
| **协作** | 无脑级别委托 | 跨脑委托 + 跨脑授权 + 双通道通信 |
| **隔离** | 工具级别 | 进程级别 + 工具级别双重隔离 |
| **专精** | 通用 agent + 通用工具 | 每个领域有专精大脑（量化、数据、代码、浏览器） |
| **可观测** | 单 agent 日志 | 多脑 Dashboard + 事件流 + 跨脑追踪 |
| **学习** | 单 agent 自我改进（Hermes Learning Loop） | 四层学习：Brain 级/协作级/策略级/用户级，越用越好 |
| **商业化** | 插件/技能市场 | 专精大脑生态（免费版/Pro 版/企业版） |

一句话：**别人做的是"一个更强的 agent"，我们做的是"一个 agent 团队"。**

---

## 10. Delegate 语义冻结规则

为让未来 3-5 年不反复推翻模型，冻结以下规则（冻结版本 v1.0，随 v3.0 协议同步冻结）：

1. `central` 只 delegate 给 `brain`
2. delegate 的执行入口仍是 `brain/execute`
3. `MCP server` 不能直接成为 delegate target
4. license / health / policy 在 delegate 前必须先过门禁
5. `brain_version` 不需要和 central 强制对齐
6. runtime 可以替换，但 manifest 契约尽量稳定

### 10.1 协议级强制执行位置

| 规则 | 主防线文件 | 检查函数 | 二次校验 |
|------|-----------|---------|---------|
| #1 身份鉴权 | `orchestrator.go` | `registerReverseHandlers()` 闭包内 `callerKind != KindCentral` | `Delegate()` 入口 |
| #2 执行入口 | `orchestrator.go` | `delegateOnce()` 硬编码 `"brain/execute"` | `protocol/methods.go` 常量 |
| #3 MCP 非 target | `orchestrator.go` | `CanDelegate()` 只对 `available[kind]` 的 brain 返回 true | MCPAdapter 不在 available map |
| #4 门禁三要素 | `orchestrator.go` | `Delegate()` 入口顺序：license→policy→health→lease | Lease 在门禁全通过后才申请 |
| #5 版本独立 | `orchestrator.go` | `getOrStartSidecar()` 只检查 `protocol_version`，不检查 `brain_version` | — |
| #6 契约稳定 | `Register()` | Manifest capability set 前向兼容检查（新增 OK，删除告警） | — |

### 10.2 门禁执行顺序（与 Capability Lease 的交互）

```text
subtask.delegate 请求到达
  → ① 规则 #1 身份鉴权（callerKind == KindCentral）
  → ② license 检查（目标 brain 是否持有有效 license）
  → ③ policy 检查（ExecutionPolicy / ToolPolicy）
  → ④ health 检查（BrainPool.HealthCheck）
  → ⑤ Capability Lease AcquireSet（并发资源竞争）
  → ⑥ delegateOnce → brain/execute
```

门禁①-④是快速布尔判断，不消耗资源。Lease⑤可能阻塞。**两者严格分层：门禁失败不进入 AcquireSet，避免无效占锁。**

### 10.3 MCP-backed Brain 的正确姿势

mcp-backed brain 是规则 #3 的"正确绕过"——它必须是一个 **wrapper brain**，对外暴露标准 `brain/execute` 接口，对内通过 MCPAdapter 转发给 MCP server：

```text
┌─────────────────────────────────────────────┐
│  mcp-backed brain（合法 delegate target）    │
│  ┌──────────────┐    ┌────────────────────┐ │
│  │  Agent Loop  │───▶│  MCPAdapter        │ │
│  │  brain/execute│   │  mcp.github.*      │ │
│  └──────────────┘    └────────────────────┘ │
└─────────────────────────────────────────────┘
```

BrainPool 注册的是 wrapper brain binary，而非 MCP server binary。

### 10.4 冻结规则变更流程

| 变更类型 | 最短 Deprecation 期 | 是否需要 RFC |
|---------|-------------------|------------|
| 破坏性修改（收紧/替换语义） | 1 个 major 版本 | 必须 |
| 放宽性修改 | 1 个 major 版本 | 必须 |
| 澄清性修改（不改语义） | 无 | 可选 |

### 10.5 违规检测

5 个审计事件类型：`audit.delegate_rejected`（规则 #1）、`audit.protocol_deviation`（规则 #2）、`audit.invalid_delegate_target`（规则 #3）、`audit.policy_violation`（规则 #4）、`audit.manifest_breaking`（规则 #6）。全部通过 EventBus 广播，Dashboard 实时展示违规率。

---

## 11. 与当前仓库的衔接

### 11.1 已有能力映射

| 现有 | v3 概念 |
|------|---------|
| 内置 sidecar | `native brain` |
| `kernel/mcpadapter` | `mcp binding backend` |
| 第三方 sidecar 开发指南 | `native brain` 接入文档 |
| 付费授权方案 | `brain package` 商业化扩展 |
| Quant WebUI | Control Plane 的领域实例 |
| `Orchestrator.active` pool | Brain Pool 的原型（需要抽取+扩展复用策略） |
| `ProcessRunner` | Brain Pool 的底层进程管理（不需要改） |
| `runManager` | Task Runtime 的 Run 层（需要扩展 Task/Job） |
| `loop/turn_executor` 批量 tool_call | Scheduler parallel batch 的基础 |
| `SpecialistToolCallAuthorizer` | 语义审批分级的前身 |
| `buildOrchestratorPrompt` | Manifest 驱动发现的过渡方案 |

### 11.2 Phase A 需要新增什么

| 交付物 | 涉及的现有代码 | 新增工作量 |
|--------|---------------|-----------|
| Brain Pool（进程管理） | 从 `Orchestrator.active` 抽取，新增 `sdk/kernel/pool.go` | **大**（核心改造） |
| LeaseManager（资源锁） | 新增 `sdk/kernel/lease.go`，ToolConcurrencySpec 定义 | **大**（核心新增） |
| TaskExecution + Policy | 重构 `runManager` + `loop/run.go`，统一为 TaskExecution | 中等 |
| Orchestrator 瘦身 | 重构 `sdk/kernel/orchestrator.go`，移除 sidecar 管理和锁 | 中等 |
| turn_executor Dispatch | 扩展 `loop/turn_executor.go`，增加冲突分析 + AcquireSet | 中等 |
| 全脑 Dashboard | 扩展 `cmd_serve.go` + 复用 `brains/quant/webui/` 模式 | 中等 |
| EventBus 聚合层 | 新增 `sdk/events/`，聚合 runtimeaudit + protocol 事件 | 中等 |

---

## 12. 最终结论

v3 的正式口径：

> **Multi-brain native, runtime-observable, manifest-driven, package-delivered, policy-governed.**

排序：

1. 先把资源调度做对——Brain Pool + Capability Lease + AcquireSet 死锁防护（Phase A）
2. 再把并发执行做快——turn_executor Dispatch Policy + 冲突推导（Phase B）
3. 最后把多任务编排做完整——Workflow DAG + Flow Edge（Phase C）
4. 生态分发和远程（Phase D）

实施顺序的压缩表述：
1. 修 serve，去掉 per-run orchestrator
2. 引入 capability/resource-based lease
3. 在 turn_executor 做冲突分析 + batch dispatch
4. 给 workflow 边增加 materialized/streaming
5. 把后台任务落成 TaskExecution + DaemonPolicy

**中央大脑 + 专精大脑的多脑协作，是我们的护城河。但护城河不是靠"有多脑"就够的——要靠资源调度做对、并发执行做快、任务编排做完整，才能真正把"agent 团队"的架构优势兑现为用户体验优势。**

---

## 13. 细化设计规格索引（v6 新增）

v5 确定了四正交概念的顶层架构后，v6 对 8 个核心子系统进行了详细设计。每个子系统有独立的规格文档（35 系列），本节是索引和关键决策摘要。

### 13.1 TaskExecution 生命周期状态机

> **详细规格**：[35-TaskExecution生命周期状态机.md](./35-TaskExecution生命周期状态机.md)

**关键决策**：

- 状态从 8 个扩展为 **11 个**：新增 `waiting_event`（watch 空闲态）、`draining`（优雅退出过渡态）、`restarting`（退避等待重启）
- `failed` / `crashed` 不再是严格终态——RestartPolicy 允许时先进入 `restarting`，再回到 `pending`
- Mode 切换（interactive → background）是**元数据操作**，不触发状态转移，实质是把 StreamConsumer 从直连用户改为写 EventBus
- 三种 Lifecycle 终止条件：oneshot 靠 LLM 最终答复、daemon 靠 stop 指令或重启耗尽、watch 靠 idle_timeout 或 stop
- RestartPolicy 退避策略：指数退避 + ±20% 随机抖动，默认 1s 起步、最大 5min
- 父子传播：三种 ChildFailurePolicy（`propagate_immediately` / `wait_all` / `ignore`）

**状态转移总图**：

```text
                      [调度]
(create) ──────▶ pending ──────────────────────────┐
                    │                               │ [cancel]
                    │ [调度]                         ▼
                    ▼                           canceled ◀── draining ◀──┐
                 running ◀── [resume] ── paused                         │
                    │  ▲                                                │
               [B]  │  │ [C]                                           │
                    ▼  │                                                │
               waiting_tool                                             │
                    │                                                   │
                    │ [完成]                                             │
                    ▼                                                    │
                 completed ◀── [idle_timeout] ── waiting_event          │
                                                    ▲  │                │
                                                    └──┘ [事件触发]      │
                                                                        │
running ──[失败]──▶ failed ──[重启]──▶ restarting ──[到期]──▶ pending   │
running ──[崩溃]──▶ crashed ──[重启]──▶ restarting                      │
                                           │ [耗尽]                     │
                                           ▼                            │
                                     failed/crashed (终态)              │
running ──[stop]──▶ draining ───────────────────────────────────────────┘
```

**迁移路径**：`loop.Run` 保持不动作为底层引擎；新建 `sdk/execution/` 包在其上叠加 TaskExecution。`/v1/runs` 保留为别名路由到 `TaskExecution(Interactive+OneShot)`。

### 13.2 LeaseManager 实现设计

> **详细规格**：[35-LeaseManager实现设计.md](./35-LeaseManager实现设计.md)

**关键决策**：

- **锁策略**：分段锁（256 shard），按 ResourceKey hash 分片，避免全局锁热点
- **Lease 对象**：ID + 三元组（Capability × ResourceKey × AccessMode）+ Holder + Scope + TTL + 续租计数
- **续租机制**：daemon scope 自动续租（heartbeat = TTL/3），连续错过 3 次心跳强制撤销
- **崩溃回收**：双通道——ProcessRunner exit hook（快路径）+ TTL 过期扫描（兜底），按 scope 倒序释放（daemon > task > turn）

**完整兼容性矩阵**：

| 已持有 ↓ \ 新申请 → | SharedRead | SharedWriteAppend | ExclusiveWrite | ExclusiveSession |
|---|:---:|:---:|:---:|:---:|
| **SharedRead** | ✅ | ✅ | ❌ | ❌ |
| **SharedWriteAppend** | ✅ | ✅ | ❌ | ❌ |
| **ExclusiveWrite** | ❌ | ❌ | ❌ | ❌ |
| **ExclusiveSession** | ❌ | ❌ | ❌ | ❌ |

**AcquireSet 算法**：canonical ordering（ResourceKey + Capability 字典序）→ 逐个获取 → 失败全部回滚 → 指数退避 + Full Jitter 重试 → 超时返回 ErrAcquireTimeout。冲突时监听 waiterCh 快速唤醒。

**P2 Wait-for Graph**：DFS 三色染色检测环路 → 复合评分选 victim（优先级×10 + 持有时长×0.01 + 成本×0.1）→ ForceRevokeHolder 解锁。

**核心接口**：

```go
type LeaseManager interface {
    AcquireSet(ctx context.Context, reqs []LeaseRequest) ([]Lease, error)
    Release(ctx context.Context, leaseID LeaseID) error
    ReleaseAll(ctx context.Context, holder HolderID, scope LeaseScope) error
    Query(ctx context.Context, filter LeaseFilter) ([]LeaseSnapshot, error)
    Renew(ctx context.Context, leaseID LeaseID, ttl time.Duration) error
    ForceRevoke(ctx context.Context, leaseID LeaseID, reason string) error
    ForceRevokeHolder(ctx context.Context, holder HolderID, reason string) (int, error)
    Subscribe(ctx context.Context, filter LeaseEventFilter) (<-chan LeaseEvent, error)
    Snapshot(ctx context.Context) (LeaseManagerSnapshot, error)
    Close() error
}
```

**Metrics**：`brain_lease_active_total`、`brain_lease_acquire_duration_seconds`、`brain_lease_conflict_by_resource_total`、`brain_lease_deadlock_detected_total` 等 12 项 Prometheus 指标 + `/debug/leases` 调试端点。

### 13.3 Dispatch Policy 冲突图与 Batch 分组算法

> **详细规格**：[35-Dispatch-Policy-冲突图与Batch分组算法.md](./35-Dispatch-Policy-冲突图与Batch分组算法.md)

**关键决策**：

- **冲突图**：邻接矩阵（N×N bool），节点是 `ToolCallNode`（含从 ToolConcurrencySpec + tool_call 参数推导出的 LeaseRequest）
- **冲突判定**：同一 ResourceKey + 至少一方 Exclusive → 冲突。不同 ResourceKey 永不冲突（即使同 Capability）
- **ResourceKey 模板解析**：`{{field_name}}` 从 tool_call JSON 参数提取，字段缺失用 `*` 通配符（保守策略）
- **分组算法**：贪心图着色（Welsh-Powell 变体，按度数降序），O(N²)，N≤32 下 < 100μs
- **错误策略三档**：`ContinueBatch`（默认，失败不影响其他）、`FailBatch`（第一个失败取消本 batch）、`FailAll`（任何失败终止整个 dispatch）

**新增包结构**：`sdk/loop/dispatch/`（`planner.go`、`conflict.go`、`result.go`），在 `Runner.dispatchTools()` 处集成。

### 13.4 Brain Pool 实现设计

> **详细规格**：[35-BrainPool实现设计.md](./35-BrainPool实现设计.md)

**关键决策**：

- **接口扩展至 8 个方法**：GetBrain、ReturnBrain、Status、HealthCheck、Drain、WarmUp、Register、Shutdown
- **Entry 状态机**：5 态（starting → idle ↔ in-use → draining → closed），异常走 dead 旁路
- **三种策略详细行为**：
  - shared-service：长驻单例，30s 健康检查，连续 3 次失败触发重启
  - exclusive-session：单例 + waiter channel 等待队列，30s 等待超时
  - ephemeral-worker：idle stack + 预热池（默认 2 个），空闲 5 分钟回收，最大 8 并发实例
- **与 ProcessRunner 关系**：BrainPool 包装 ProcessRunner（改名为 BrainRunner），不替代
- **锁顺序**：brainPool.mu > kindPool.mu > poolEntry.mu，持锁期间禁止 I/O

**迁移路径**：`getOrStartSidecar()` 的 5 个职责拆分进 Pool；Orchestrator 移除 `active map` + `runner` 字段，改为注入 `BrainPool` 接口；handler 注册保留在 Orchestrator（通过 `OnStart` 回调钩子）。

### 13.5 统一 Dashboard 设计规格

> **详细规格**：[35-统一Dashboard设计规格.md](./35-统一Dashboard设计规格.md)

**关键决策**：

- **11 个 REST API 端点**：overview / brains / brains/:kind / brains/:kind/restart / executions / executions/:id / events / providers / leases / leases/:id
- **WebSocket `/ws/dashboard`**：按频道订阅（brains/executions/events/providers），7 种推送消息类型，30s Ping/Pong
- **6 个数据模型 DTO**：OverviewDTO、BrainStatusDTO、ExecutionDTO、EventDTO、ProviderDTO、LeaseDTO
- **EventBus 聚合层**：内存环形缓冲（10000 条）+ fan-out 到 WebSocket 订阅者，三个来源汇聚（runtimeaudit + Orchestrator + 持久化存储）
- **向后兼容**：`/v1/runs` 完整保留，响应加 `X-Deprecated-Use` header
- **前端嵌入**：`cmd/brain/dashboard/static/` embed.FS，Vanilla JS（无 npm 构建），`:7701` 全脑控制面 / `:8380` Quant 业务面板独立运行

### 13.6 Flow Edge 存储与注册发现设计

> **详细规格**：[35-Flow-Edge存储与注册发现设计.md](./35-Flow-Edge存储与注册发现设计.md)

**关键决策**：

- **CAS 存储层**：Blake3 hash（比 SHA-256 快 6 倍），ref 格式 `blake3:<hex>`，三种后端（Local 默认 / SQLite / S3）
- **写入原子性**：先写 tmp/ → 流式计算 hash → 原子 rename，同内容并发写入幂等
- **GC 策略**：mark-and-sweep（扫描 TaskExecution.Inputs/Outputs 为 root）+ age-based + size-based LRU
- **PipeRegistry**：Create / Get / List / UpdateState / Delete / Watch 接口，pipe 状态机 creating → active → draining → closed
- **三种 backend 统一抽象**：`StreamWriter` / `StreamReader` 接口，工厂按 BackendConf 路由
- **背压策略**：StrategyBlock（阻塞）/ StrategyOverwrite（ringbuf 默认）/ StrategySpillToCAS（溢出到 CAS 不丢数据）
- **现有 ringbuf 迁移**：三步走——引入 Frame 泛型单元 → 注册进 PipeRegistry → 清理 quant 直接 import

### 13.7 Context Engine 详细设计

> **详细规格**：[35-Context-Engine详细设计.md](./35-Context-Engine详细设计.md)

**关键决策**：

- **Assemble 流水线 5 步**：委托头部注入 → 跨 Turn 记忆注入 → L3 History 加载 → BrainKind/TaskType 过滤 → token 超预算压缩
- **Compress 三层策略**：窗口裁剪（低延迟无 LLM）→ LLM 摘要（高质量有延迟）→ 贪心预算约束（兜底）
- **跨脑共享协议**：`SubtaskContext` 携带三类结构化信息——RelevantMessages（≤10 条）+ PriorResults（前置子任务摘要，各 ≤200 token）+ SharedMemory（记忆摘要）；结果回传只 merge 摘要（≤500 token）
- **隐私边界**：private 标记的消息、凭证关键词、policy 元数据、其他专精脑执行轨迹一律不跨脑传递
- **跨 Turn 记忆**：MemoryEntry 含语义槽位、重要性、访问计数；淘汰评分 = 年龄×(1-Importance)/(1+AccessCount)；`user_preference` 和 `delegation_score` 永不淘汰
- **Token 计数**：Anthropic 近似（字节数/3.5 + 10% 余量）、OpenAI/DeepSeek 用 tiktoken 精确

**各 brain 默认 token 预算**：Central 85% / Code 80% / Quant 75% / Browser 65% / Data 60%。

### 13.8 v3 重构路径与开发计划

> **详细规格**：[35-v3重构路径与开发计划.md](./35-v3重构路径与开发计划.md)

**核心矛盾**：`cmd_serve.go` 每个 run 各建一个 Orchestrator，6 并发 run = 6 套 sidecar 进程。Phase A 最早修。

**重构顺序（13 步依赖链）**：

```text
Step 1: BrainPool 接口 ─┐
Step 2: EventBus 接口 ──┤─→ Step 3: 全局 Pool 注入 serve ─→ Step 4: Orchestrator 瘦身
                        └─→ Step 5: EventBus SSE 端点
Step 6: TaskExecution 模型 ─→ Step 7: HTTP API 统一
Step 8: LeaseManager ─→ Step 10: Dispatch Policy (BatchPlanner)
Step 9: Dashboard API + SPA
Step 11-13: Context Engine / MCP Runtime / 语义审批（Phase B 入口）
```

**Phase A 拆为 7 个可独立合并的 PR**：

| PR | 范围 | 工时 | 前置 |
|----|------|------|------|
| A-1 | BrainPool 抽取 (`sdk/kernel/pool.go`) | 1.5 天 | 无 |
| A-2 | 全局 Pool 注入 serve（修架构缺陷） | 1 天 | A-1 |
| A-3 | Orchestrator 瘦身 | 0.5 天 | A-2 |
| A-4 | EventBus (`sdk/events/`) | 1 天 | 无（与 A-1 并行） |
| A-5 | TaskExecution + `/v1/executions` | 2 天 | A-3 |
| A-6 | LeaseManager 骨架 | 1.5 天 | 无（与 A-1 并行） |
| A-7 | Dashboard API + SPA | 3 天 | A-4 + A-5 |

**Phase A 合计**：10.5 人天，关键路径 6 天（2 人并行约 2 周）

**并行开发矩阵**：

```text
开发者 1: A-1 → A-2 → A-3 → A-5 → A-7（关键路径）
开发者 2: A-4 → A-6 → A-7 协助前端（辅助路径）
```

**不需要动的文件**：`sdk/loop/run.go`、`sdk/kernel/process_runner.go`、`sdk/kernel/llm_proxy.go`、`sdk/protocol/`——底层 Loop 引擎稳定。

### 13.9 语义审批分级设计

> **详细规格**：[35-语义审批分级设计.md](./35-语义审批分级设计.md)

**关键决策**：

- **审批决策树三层优先级**：tool.ApprovalClass 显式声明 → manifest.min_approval_level 向上取整 → 工具名前缀启发式兜底（16 条规则）
- **Manifest 约束**：`min_approval_level`（只升不降）+ `require_human_approval_above`（强制人工确认阈值）
- **与 Lease 的映射**：审批在 BatchPlanner.Plan() 推导 LeaseRequest 后、AcquireSet 前执行，作为门禁层
- **Mode 差异**：interactive L2+ 需确认；background L3+ 默认拒绝，可通过 manifest `background_allow_levels` 豁免
- **接口**：`SemanticApprover` + `ApproverMiddleware` 中间件链（StaticMatrix → ManifestPolicy → Audit → ModeAware）
- **迁移**：三阶段从 SpecialistToolCallAuthorizer 过渡（适配层 → 语义升级 → 静态矩阵下线）

### 13.10 Manifest 解析与版本化设计

> **详细规格**：[35-Manifest解析与版本化设计.md](./35-Manifest解析与版本化设计.md)

**关键决策**：

- **解析器四阶段**：发现（目录扫描 `~/.brain/brains/<kind>/brain.json`）→ 读取 → 校验（JSON Schema v1 + Go 四层验证）→ 注册
- **版本化策略**：schema_version 升级走 RFC → 发布 → 6 个月过渡期 → 废弃；breaking change 仅在 major 版本允许
- **可选扩展字段引入时机**：description/task_patterns（Phase A）、compatibility/license（Phase B）、health/metadata（Phase C）
- **热更新**：FileWatcher + 防抖 → 校验新 manifest → Drain 旧实例 → 替换注册 → WarmUp 新实例；回滚机制保底
- **与 BrainPool 映射**：`ToPoolRegistration()` 根据 policy.approval_class 推导进程策略（workspace-write→ephemeral, control-plane→exclusive 等）

### 13.11 MCP-backed Runtime 设计

> **详细规格**：[35-MCP-backed-Runtime设计.md](./35-MCP-backed-Runtime设计.md)

**关键决策**：

- **三层架构**：Brain Protocol（brain/execute）→ BrainHost（wrapper sidecar）→ MCPAdapter → N 个 MCP Server 子进程
- **工具发现**：MCP server 的 tools/list → 加 prefix → 合并为 brain 工具集；`concurrency_overrides` 覆盖默认 ToolConcurrencySpec
- **路由**：tool_call 按 prefix 路由到对应 MCPAdapter，参数/结果格式转换透明
- **健康检查**：双层——BrainPool 层 ping + adapter 层 tools/list ping；聚合为 Ready/Degraded/Unhealthy 三档
- **native vs mcp-backed 对比**：24 个维度详细比较 + 选型决策指南
- **实现路径**：8 个子任务，合计 6 人天

### 13.12 自适应学习 L1-L3 算法设计

> **详细规格**：[35-自适应学习L1-L3算法设计.md](./35-自适应学习L1-L3算法设计.md)

**关键决策**：

- **L1 协作级**：
  - 四维评分（准确率/速度/成本/稳定性），权重按场景动态调整
  - Wilson score 置信区间修正，小样本向 0.5 收缩
  - EWMA 更新，alpha 按任务频率动态（0.1/0.2/0.3）
  - 冷启动：20% 探索预算 + Manifest capabilities 预设评分
  - 持久化：`~/.brain/central/learning/l1/`，30 天滚动窗口淘汰

- **L2 策略级**：
  - ExecutionChain DAG 表示，链路指纹用于快速模式匹配
  - 因果推断四步：样本量检查 → CI 重叠 → Simpson 悖论分层 → 时间稳定性
  - 探索利用：ε-贪心（ε=0.15）+ UCB 修正 + 自动 ε 衰减

- **L3 用户级**：
  - UserProfile 四维度（工作风格/任务偏好/沟通风格/时间模式）
  - 9 种 PreferenceSignal + diff 解析细粒度偏好
  - EWMA + 各维度半衰期（20-90 天）+ 急剧转变快速学习（3x LR）
  - 隐私保护：数据类型可共享矩阵，8 种数据类型逐一标注

- **全局闭环**：L0→L1→L2→L3 数据流回路，各层触发时机明确

### 13.13 死锁防护 P2 补充与持久化迁移

**P2 Wait-for Graph 补充**（内联）：

- **完整算法**：AddEdge → detectCycleFrom（DFS 三色染色）→ SelectVictim → ForceRevokeHolder
- **权重设计依据**：优先级×10（外部契约，最强保护因子）、持有时长×0.01（仅打平局用，避免逆转优先级）、成本×0.1（软性指标，参与但不主导）
- **二阶影响**：被 revoke 的 task 通过 `leaseRevokeCh` 收到通知 → ctx cancel → 状态转 interrupted → 按 RestartPolicy 决定重试策略
- **性能边界**：v3 设计约束下 WFG 节点上界 ~256，DFS 检测 < 500μs；检测异步投递到 wfgChecker goroutine，不阻塞 AcquireSet 关键路径

**持久化迁移**（内联）：

- **策略**：读时 lazy 填充，无需一次性迁移脚本——新字段全部 `omitempty`，旧记录反序列化为零值后由 `fillDefaults()` 填充
- **回滚安全**：旧二进制读新格式 JSON 时 `encoding/json` 忽略未知字段，行为正常
- **`/v1/runs` 废弃时间表**：PR-A5 合并后软废弃（加 `X-Deprecated-Use` header）→ Phase B 完成后正式标记 deprecated → Phase D 后（≥2 个主版本、≥60 天公示）可删除

### 13.14 Brain Capability 标签与匹配算法

> 详见 → [35-BrainCapability标签与匹配算法](./35-BrainCapability标签与匹配算法.md)

- **标签体系**：四类标签（function / domain / resource / mode），每类均有标准枚举值；分 primary / secondary 两级
- **匹配算法三阶段**：(1) 硬匹配——required capabilities 全部满足方可入围；(2) 软匹配——preferred 按满足比率加权；(3) 负载感知——当前 lease 占用率越低得分越高
- **评分公式**：`Score = 100 + soft_match_ratio × 50 + (1 - load_ratio) × 30`，平局打破：primary 命中数 → 负载 → 注册时间 → 字典序
- **ResourceKey 冲突判定**：命名规范 `{capability_type}:{resource_id}`；4×4 AccessMode 兼容矩阵（Read+Read ✅ / Exclusive+任何 ❌）
- **动态更新**：Brain 启动时注册 → 停止时注销 → hot-reload 时 drain-then-re-register

### 13.15 跨脑通信协议设计

> 详见 → [35-跨脑通信协议设计](./35-跨脑通信协议设计.md)

- **快路径（Ring Buffer）**：`/dev/shm/brain_{session}_{src}_{dst}` 内存映射；64B Header + 环形 Data 区；14B Frame Header（type/flags/seq/payload_len/checksum）；MsgPack 序列化；p99 < 100μs
- **慢路径（JSON-RPC 2.0）**：UDS（同机）/ TCP+TLS（跨机）；5 个方法（delegate/result/cancel/heartbeat/context_share）；11 个 Brain 专用错误码（-32100 到 -32110）；p99 < 5ms
- **跨脑授权**：从静态白名单演进到动态 Capability-based 令牌（JWT-like：issuer/subject/capabilities/expiry/scope）；三级隐私边界（public/team/private）
- **Remote 扩展（Phase D）**：gRPC proto 定义 + DNS-SD/etcd 服务发现 + 网络分区降级
- **Hybrid 模型**：native 优先路由 → MCP fallback；熔断器 + 三层降级策略

### 13.16 端到端时序与模块依赖图

> 详见 → [35-端到端时序与模块依赖图](./35-端到端时序与模块依赖图.md)

- **模块依赖**：13 子系统四层分布（Control Plane / Execution / Resource / Storage），mermaid graph 标注所有调用边和接口方法
- **5 条核心时序**：(1) 用户请求全路径（8 阶段）；(2) Delegate 委托（含门禁序列）；(3) 并行 Batch 执行（含错误策略决策点）；(4) 故障恢复（双通道 Lease 回收 → 重调度）；(5) 热更新 Manifest
- **门禁全景图**：license → delegate_freeze → policy → health → approve → acquireSet → execute（越便宜越靠前）
- **接口契约总表**：8 张表覆盖全部跨模块调用（输入/输出/错误处理）
- **25 条关键不变量**：7 大类（Lease / TaskExecution / Dispatch / Delegate / BrainPool / Context / 系统级），每条标注执行机制和违反后果

---

## 14. 35 系列规格文档列表

| 编号 | 文档 | 对应章节 | Phase |
|------|------|---------|-------|
| 35-1 | [TaskExecution生命周期状态机](./35-TaskExecution生命周期状态机.md) | §7.7.1 / §13.1 | A |
| 35-2 | [LeaseManager实现设计](./35-LeaseManager实现设计.md) | §7.7.2 / §7.9 | A |
| 35-3 | [Dispatch-Policy-冲突图与Batch分组算法](./35-Dispatch-Policy-冲突图与Batch分组算法.md) | §7.8 / §13.3 | B |
| 35-4 | [BrainPool实现设计](./35-BrainPool实现设计.md) | §7.11 / §13.4 | A |
| 35-5 | [统一Dashboard设计规格](./35-统一Dashboard设计规格.md) | §7.2 / §13.5 | A |
| 35-6 | [Flow-Edge存储与注册发现设计](./35-Flow-Edge存储与注册发现设计.md) | §7.10 / §13.6 | C |
| 35-7 | [Context-Engine详细设计](./35-Context-Engine详细设计.md) | §7.4 / §13.7 | B |
| 35-8 | [v3重构路径与开发计划](./35-v3重构路径与开发计划.md) | §11 / §13.8 | A-D |
| 35-9 | [MCP-backed-Runtime设计](./35-MCP-backed-Runtime设计.md) | §3.4 / §6 / §13.11 | B |
| 35-10 | [语义审批分级设计](./35-语义审批分级设计.md) | §7.9 / §13.9 | A |
| 35-11 | [Manifest解析与版本化设计](./35-Manifest解析与版本化设计.md) | §3.5 / §13.10 | A |
| 35-12 | [自适应学习L1-L3算法设计](./35-自适应学习L1-L3算法设计.md) | §7.6 / §13.12 | B-C |
| 35-13 | [BrainCapability标签与匹配算法](./35-BrainCapability标签与匹配算法.md) | §3.6 / §13.14 | A |
| 35-14 | [跨脑通信协议设计](./35-跨脑通信协议设计.md) | §5.2-§5.3 / §13.15 | A-D |
| 35-15 | [端到端时序与模块依赖图](./35-端到端时序与模块依赖图.md) | §7.12 / §13.16 | — |

---

## 15. Phase E：统一持久化 + 缺口补全（v3.4，2026-04-18 新增）

### 15.1 背景

Phase A-D 完成后的核查发现 9 个缺口，其中**持久化碎片化**是根因：

| 当前方案 | 问题 |
|---------|------|
| JSON 全量重写（RunStore/PlanStore） | 每次写 O(N)，run 多了严重卡顿 |
| 纯内存（LearningEngine/Context/AuditLog） | 进程重启全丢，L1-L3 学习无效 |
| 无搜索能力 | 无法语义检索历史上下文 |

竞品对标（2026-04）：

| 产品 | 持久化核心 | 记忆层 | 学习机制 |
|------|----------|--------|---------|
| **Codex** | SQLite + .jsonl.zst | LLM 自动提取结构化记忆 | 结构化记忆提取 |
| **Claude Code** | Markdown 文件 | CLAUDE.md 三层 + compaction | 无 |
| **OpenClaw** | **SQLite + Markdown 混合** | FTS5 + 向量搜索 + LCM DAG 摘要 | 无 |
| **Hermes Agent** | **SQLite FTS5** | 10ms/10K+ 文档检索 | Skill 文档自动生成（40% 加速） |

**共识：SQLite WAL 做结构化存储，Markdown 做人类可读层。**

### 15.2 统一持久化方案

```
~/.brain/
├── config.json              # 保持 JSON（人工编辑，低频）
├── brain.db                 # SQLite WAL（统一持久化）
│   ├── runs                 # Run 记录
│   ├── run_events           # 事件流（替代全量重写）
│   ├── plans                # Plan 记录
│   ├── plan_deltas          # Plan 增量
│   ├── checkpoints          # Checkpoint
│   ├── usage_records        # 用量计费
│   ├── brain_scores         # L1 协作级学习（EWMA + Wilson）
│   ├── task_sequences       # L2 序列学习
│   ├── user_prefs           # L3 用户偏好
│   ├── shared_context       # 跨脑上下文传递
│   ├── audit_events         # 安全审计链（append-only）
│   └── memory_fts           # FTS5 全文搜索索引
├── memory/                  # Markdown 记忆层
│   ├── MEMORY.md            # 核心记忆（启动时加载）
│   └── skills/              # 自动生成的技能文档
├── artifacts/               # CAS 大二进制（保持文件系统）
├── logs/                    # sidecar 日志（保持 O_APPEND）
└── brains/                  # manifest + binary（保持目录结构）
```

### 15.3 Phase E 工作项

| 编号 | 工作项 | 优先级 | 依赖 |
|------|--------|--------|------|
| E-1 | SQLite 驱动实现（`sdk/persistence/driver_sqlite.go`） | P0 | 无 |
| E-2 | RunStore/PlanStore 迁移到 SQLite | P0 | E-1 |
| E-3 | LearningEngine 持久化（Save/Load brain_scores） | P0 | E-1 |
| E-4 | 5 脑 L0 BrainLearner 实现 | P0 | E-3 |
| E-5 | L1-L3 接入 delegate 执行路径 | P0 | E-3 |
| E-6 | Context Engine LLM 摘要路径 | P1 | E-1 |
| E-7 | Context SharedMessages 持久化 | P1 | E-1 |
| E-8 | Streaming Edge 打通到 WorkflowEngine | P1 | 无 |
| E-9 | AuditLog 持久化 | P1 | E-1 |
| E-10 | upgrade/rollback 命令实现 | P2 | 无 |
| E-11 | Package 签名校验 | P2 | 无 |
| E-12 | Dashboard WebSocket 推送 | P2 | 无 |

### 15.4 并行执行矩阵

```
Sprint E-1（并行，无依赖）：
  [E-1]  SQLite 驱动
  [E-8]  Streaming Edge 打通
  [E-10] upgrade/rollback
  [E-11] Package 签名

Sprint E-2（依赖 E-1）：
  [E-2]  RunStore/PlanStore 迁移
  [E-3]  LearningEngine 持久化
  [E-9]  AuditLog 持久化
  [E-12] Dashboard WebSocket

Sprint E-3（依赖 E-2/E-3）：
  [E-4]  5 脑 L0 BrainLearner
  [E-5]  L1-L3 接入执行路径
  [E-6]  Context Engine LLM 摘要
  [E-7]  Context SharedMessages 持久化
```

---

## 15. 上层产品接入边界

> **v8 新增** · 2026-04-18
> 
> 本节定义 Brain v3 作为运行时底座被上层产品（如 EasyMVP、IDE 插件、CI/CD 系统等）接入时的职责边界。

### 15.1 核心原则

Brain v3 是 **runtime source of truth**，上层产品是 **domain/product source of truth**。两者通过 `run_id` / `execution_id` 做关联，不共库、不共 UI、不混合生命周期。

```
┌────────────────────────────────────────────┐
│  上层产品（EasyMVP / IDE / CI）              │
│  ├─ 自有数据库（project/plan/acceptance）    │
│  ├─ 自有前端（工作台/审计/验收）              │
│  ├─ 领域投影层                               │
│  │   └─ 消费 runtime 事件 → 投影为领域对象    │
│  └─ 关联键：run_id / execution_id            │
│                                              │
│       引用，不复制 ↕                          │
│                                              │
│  Brain v3（运行时底座）                       │
│  ├─ brain.db（run/execution/event/artifact）  │
│  ├─ brain serve API                          │
│  ├─ EventBus + SSE + WebSocket               │
│  └─ replay / audit 原始产物                   │
└────────────────────────────────────────────┘
```

### 15.2 Brain v3 负责

| 领域 | 具体内容 |
|------|---------|
| **TaskExecution 生命周期** | 12 状态机、Mode×Lifecycle×Restart |
| **runtime 事件源** | EventBus、SSE、WebSocket 推送 |
| **execution/runs API** | `/v1/runs`、`/v1/executions` |
| **原始产物** | logs、replay、audit trail、artifact |
| **多脑调度** | Orchestrator、TaskScheduler、L0-L3 学习 |
| **运行时策略** | tool policy、sandbox、file policy、approval |
| **runtime observability** | Dashboard、brain/provider/lease 状态 |

### 15.3 上层产品负责

| 领域 | 具体内容 |
|------|---------|
| **领域对象** | project、plan、task、acceptance 等业务实体 |
| **工作流状态机** | 方案审核 → 编译 → 执行 → 验收的业务流程 |
| **领域投影** | 把 runtime 事件映射为项目进度、风险、待处理 |
| **UI 聚合** | 工作台、ActionInbox、WorkspaceSnapshot |
| **领域索引** | replay index、evidence linkage、project projection |

### 15.4 四条接入铁律

**1. 独立数据库，按引用关联**

上层产品不应把业务表建在 `brain.db` 中。Brain v3 的库是 runtime persistence（关注 run/execution/event），上层产品的库是 domain persistence（关注 project/plan/acceptance）。两者迁移频率、回滚策略、兼容约束完全不同。

```
brain.db          ←  brain-v3 独占
product.db        ←  上层产品独占
关联方式：run_id / execution_id 外键引用
```

**2. 不自建底层源，但必须做领域投影**

上层产品**不应**重新发明：
- runtime 状态机（用 Brain v3 的 TaskExecution）
- 通用事件总线（用 Brain v3 的 EventBus）
- 通用任务调度器（用 Brain v3 的 TaskScheduler/Orchestrator）
- 底层 run persistence 协议

上层产品**必须**自建：
- run binding（哪个 run 属于哪个项目/任务）
- domain event projection（runtime 事件 → 项目进度）
- workspace aggregate（聚合视图）
- evidence linkage（证据与 run 的关联）

**3. 后端消费 runtime 事件，前端只接产品聚合接口**

上层产品前端不应直接连 Brain v3 的 Dashboard WebSocket。正确做法：

```
Brain v3 EventBus → 上层产品后端（消费+投影）→ 上层产品前端
```

Brain v3 Dashboard 是 runtime control plane，上层产品 UI 是 product workflow cockpit，关注点不同。

**4. 原始产物复用，领域索引自建**

- raw replay/log/audit artifact → 由 Brain v3 产出和存储
- 项目级 replay 索引、领域关联、decision trail → 由上层产品维护

上层产品不复制底层全量 runtime 数据，只做必要的索引和投影。

### 15.5 接入 API 契约

上层产品通过以下 API 与 Brain v3 交互：

| API | 用途 | 数据方向 |
|-----|------|---------|
| `POST /v1/runs` | 创建 run | 产品 → runtime |
| `GET /v1/runs/{id}` | 查询 run 状态 | 产品 ← runtime |
| `POST /v1/runs/{id}/cancel` | 取消 run | 产品 → runtime |
| `GET /v1/dashboard/events` | SSE 事件流 | 产品 ← runtime（实时） |
| `GET /v1/dashboard/ws` | WebSocket 事件流 | 产品 ← runtime（实时） |
| `GET /v1/dashboard/overview` | 全局状态 | 产品 ← runtime |

上层产品后端订阅事件流，将 runtime 事件投影为自己的领域对象后，再通过自己的 API 提供给前端。

### 15.6 数据生命周期

```
runtime 事件产生
  ↓
Brain v3 持久化到 brain.db（audit_logs / run_events）
  ↓
上层产品后端通过 API/SSE/WS 消费事件
  ↓
投影为领域对象写入 product.db（binding / projection / aggregate）
  ↓
上层产品前端从 product.db 聚合展示
```

两个数据库独立迁移、独立备份、独立生命周期管理。Brain v3 升级不影响上层产品的领域数据，反之亦然。
