# 02 · BrainKernel 设计

> **定位**：BrainKernel 是所有大脑（CentralBrain + N 个 SpecialistBrain）共享的**基础设施**，不是"又一个大脑"。它只做六件事：运行 Agent Loop、抽象 LLM Provider、持久化 BrainPlan、管理 Artifact、执行 Guardrail、记账与审计。它**不做**任何业务决策，不拆任务，不判断验收是否通过——这些是大脑自己的活。
>
> 本文档是整个执行器家族的"内核宪法"。只要这份文档定了，剩下 8 份文档都是在这份的接口约束下填内容。
>
> **下位规格文档索引**（read-by-reference，不重复正文）：
>
> | 编号 | 文档 | 作用 | 本文引用位置 |
> |------|------|------|--------------|
> | 20 | [stdio 协议规格 v1](./20-协议规格.md) | 比特级传输/会话层 RFC：帧格式、双向 RPC id 命名空间、生命周期状态机、心跳、背压、错误帧、合规测试 C-01 ~ C-20 | §12.5.4 |
> | 21 | [错误模型 v1](./21-错误模型.md) | 跨语言错误契约：BrainError 结构、Class 四维分类、error_code 保留清单、Fingerprint、Decide 决策矩阵、毒消息防御、tool_result 脱敏、合规测试 C-E-01 ~ C-E-20 | §13 |
> | 22 | [Agent Loop 规格 v1](./22-Agent-Loop规格.md) | Run/Turn/ToolCall 术语、三层 Prompt Cache、Streaming 协议、上下文压缩、ChatRequest schema、工具批次并发、per-tool 超时、中心化 Rate Limit、tool_result sanitizer、死循环检测、反思预算、合规测试 C-L-01 ~ C-L-20 | §4 / §5 / §6 |
> | 23 | [安全模型 v1](./23-安全模型.md) | 沙箱边界、凭证管理、LLMAccess 三模式凭证策略、审计事件、合规拒绝路径、CI 静态检查 | §6.2 / §12.5.7 |
> | 24 | [可观测性 v1](./24-可观测性.md) | OpenTelemetry 集成、Metrics / Traces / Logs 三信号清单、SLO / 告警矩阵 | §11 |
> | 25 | [测试策略 v1](./25-测试策略.md) | 合规测试执行环境、契约测试、混沌测试、跨语言 SDK 一致性验证 | — |
> | 26 | [持久化与恢复 v1](./26-持久化与恢复.md) | SQLite WAL BrainPlan、CAS artifact、Run resume、幂等写 | §7 / §8 |
> | 27 | [CLI 命令契约 v1](./27-CLI命令契约.md) | `brain` 命令树、退出码规范、输出格式（human/json/NDJSON）、stdin 协议、环境变量、工作目录布局、信号处理、向后兼容策略、合规测试 C-CLI-01 ~ C-CLI-20 | §12（面向用户的稳定接口） |
> | 28 | [SDK 交付规范 v1](./28-SDK交付规范.md) | 三级兼容性声明（Protocol/Kernel/CLI）、三段式版本号、参考实现 tiebreaker、SDK 包结构、必须实现的接口清单、150 条合规测试总览、发布流程、安全披露 SLA、商标保护、合规测试 C-SDK-01 ~ C-SDK-20 | 全文（第三方 SDK 实现者必读） |
>
> 本宪法的比特级 / 决策级规格 **以 20 ~ 28 为准**，本文如与之冲突按下位文档校正。
>
> **合规测试总数**：150 条（C-\* 20 + C-E-\* 20 + C-L-\* 20 + C-S-\* 20 + C-O-\* 15 + C-P-\* 15 + C-CLI-\* 20 + C-SDK-\* 20）。详见 [28 §8](./28-SDK交付规范.md) 及 [25-测试策略.md](./25-测试策略.md)。

---

## 目录

- [0. 决策清单 → 章节落实位置](#0-决策清单--章节落实位置)
- [1. 为什么需要一个 Kernel](#1-为什么需要一个-kernel)
- [2. Kernel 的六大职责与反职责](#2-kernel-的六大职责与反职责)
- [3. 核心接口：BrainAgent / Request / Result](#3-核心接口brainagent--request--result)
  - [3.5 SpecialistReport 汇报契约](#35-specialistreport-汇报契约)
  - [3.6 子任务验收状态机](#36-子任务验收状态机)
- [4. Agent Loop Runner](#4-agent-loop-runner)
- [5. LLM Provider 抽象层（接口层面）](#5-llm-provider-抽象层接口层面)
- [6. Tool Registry 与 Guardrail](#6-tool-registry-与-guardrail)
- [7. ArtifactStore 接口与双轨实现](#7-artifactstore-接口与双轨实现)
- [8. PlanStore 接口与双轨实现](#8-planstore-接口与双轨实现)
  - [8.3 证据聚合与审核员快照](#83-证据聚合与审核员快照)
- [9. BrainRegistry 接口与双轨实现](#9-brainregistry-接口与双轨实现)
  - [9.1 内置 BrainKind 常量](#91-内置-brainkind-常量)
- [10. Cost Meter 成本记账](#10-cost-meter-成本记账)
- [11. Trace / Audit Logger](#11-trace--audit-logger)
- [12. 并发模型与生命周期](#12-并发模型与生命周期)
  - [12.5 BrainRunner 抽象与全 sidecar 架构](#125-brainrunner-抽象与全-sidecar-架构)
    - [12.5.0 协议对标：BrainKernel 与 Anthropic MCP 的关系（决策 8）](#1250-协议对标声明brainkernel-与-anthropic-mcp-的关系对应决策-8)
- [13. 错误分类与恢复策略](#13-错误分类与恢复策略)
- [14. Kernel 的不做的事](#14-kernel-的不做的事)
- [15. Go 包结构与文件清单](#15-go-包结构与文件清单)

---

## 0. 决策清单 → 章节落实位置

本文档在编写过程中与用户确认了 **8 条关键设计决策**，每条决策在正文里都有明确的章节承载。未来任何人回头审阅时，按此表即可一次性对齐"决策 ↔ 文档位置"，避免"谈过但没落地"。

| # | 决策主题 | 用户确认口径 | 落实章节 | 正文锚点 |
|---|---|---|---|---|
| 1 | **子任务分级验收** | 按 RiskLevel × Confidence 分 low/medium/high 三档，自动决定是否起 verifier_brain | §3.6.3 | `对应决策 1` |
| 2 | **verifier_brain 独立** | verifier_brain 是独立的内置大脑，只读、无写工具、不参与实现 | §3.6.1 / §9.1 | `对应决策 2` |
| 3 | **三种验证路径共存** | 证据直通(低) + 询问专精大脑(中) + 故障大脑接管(高) 三条路径同时存在，由状态机串起来 | §3.6.2 | `对应决策 3` |
| 4 | **fault_policy 中央可配 + 两阶段权限** | fault_brain 触发阈值走中央大脑 JSON 配置；第一次只出修改方案，处理不好再全面接管 | §6.5 | `对应决策 4` |
| 5 | **连内置大脑也强制 sidecar** | 所有大脑(包括 central/verifier/fault/code/browser/mobile/game)都是独立进程，零例外，消灭内外代码分叉 | §12.5.1 / §12.5.2 | `对应决策 5` |
| 6 | **Runner 支持 cancel / health / shutdown** | BrainRunner 除了 Run 还必须提供 Cancel(取消正在跑的 Run) / Health(心跳) / Shutdown(优雅停机)，扩展性极强 | §12.5.3 / §12.5.8 | `对应决策 6` |
| 7 | **LLMAccess 三模式，默认代理** | 默认 proxied(sidecar 不直连 LLM，走主进程记账)；可切换 direct / hybrid，兼容第三方大脑自备 key | §12.5.7 | `对应决策 7` |
| 8 | **协议自研 + MCP Adapter Runner** | BrainKernel 的 stdio JSON-RPC 协议保持自研(深度双向 RPC 是 MCP 没有的)，同时提供 MCPAdapterRunner 让任意 MCP server 都能作为 tool 被 BrainAgent 消费 | §12.5.0 | `对应决策 8` |

> **追溯规则**：如果未来再出现关键设计决策（用户发话级别的"就这么定"），必须在本表追加一行，并在对应章节加一行 `对应决策 N` 的锚点。审阅者发现正文与本表不一致时，以本表为准回溯历史。

> **决策 8 的重要性**：这条决策是在文档主体写完后追加的。Anthropic MCP（Model Context Protocol）是 2024-2025 年 agent 生态的事实标准协议，Claude Code / Cursor / Windsurf / Continue 都已经采纳，生态里有 500+ 个 MCP server。本决策明确了 BrainKernel 与 MCP 的关系：**我们不 fork MCP，也不假装 MCP 不存在**。详见 §12.5.0。

---

## 1. 为什么需要一个 Kernel

多 Agent 系统最容易犯的错是**把相同的事在每个 Agent 里写一遍**：每个 Agent 自己调 LLM、自己解析 tool_call、自己管重试、自己算 token、自己存证据。这样六个大脑就有六份平行代码，任何一次 API 变化都要改六处，任何一个并发 bug 都会在六处以不同面貌出现。

BrainKernel 把这些"所有大脑都要做一遍的事"**一次写对**，向上暴露一个极简接口：

```
给我一个 BrainAgent（system prompt + 工具集 + LLM 配置） + Request
      ↓
      Kernel 跑 Agent Loop
      ↓
      返回 Result
```

大脑的差异只在三处：
1. **system prompt 不同**（中央大脑是规划者，CodeBrain 是程序员，BrowserBrain 是 QA）
2. **注册的工具不同**（中央大脑的工具是"调用其他大脑"，CodeBrain 的工具是 read_file/write_file/run_tests）
3. **LLM 配置不同**（中央大脑可能用 Claude Opus，CodeBrain 用 Claude Sonnet）

**Kernel 保证**：只要这三件事定了，任何大脑都能跑起来。你不需要为新的大脑重写 Agent Loop、不需要重新实现 tool_call 解析、不需要重新接 Claude/OpenAI。写一个新大脑 = 写一个 system prompt 文件 + 注册一组工具 + 写一个 YAML 配置，**不写 Kernel 代码**。

这是整个执行器家族最重要的一个结构决策。

---

## 2. Kernel 的六大职责与反职责

### 2.1 六大职责（Kernel 必须做）

| # | 职责 | 具体内容 |
|---|---|---|
| 1 | **运行 Agent Loop** | 给定一个 BrainAgent 和初始 Request，反复调 LLM → 解析 tool_call → 执行工具 → 回注 tool_result → 再调 LLM，直到 LLM 返回 stop 或达到 MaxSteps |
| 2 | **抽象 LLM Provider** | 把 Anthropic 的 `messages.create` 和 OpenAI 的 `chat.completions.create` 抹平成统一的 `llm.Provider.Chat(ctx, req) (*Response, error)`，Agent Loop 只调这一个接口 |
| 3 | **持久化 BrainPlan** | 通过 `PlanStore` 接口把中央大脑的计划和每个子任务的进度存进 DB（Atlas 模式）或本地 JSON（CLI 模式） |
| 4 | **管理 Artifact** | 通过 `ArtifactStore` 接口把截图/视频/diff/DOM 快照等二进制产物上传到 OSS/MinIO 或本地目录，库里只存元数据 |
| 5 | **执行 Guardrail** | 每个工具调用都要过两层白名单（插件默认白名单 + 任务级收紧），越界的调用直接返回 `tool_result { is_error: true, error: "guardrail_denied", ... }` 给 LLM，让 LLM 下一轮看到并调整 |
| 6 | **记账与审计** | Cost Meter（按 provider/model/brain 三维记账）+ Trace Logger（每次 LLM 调用和每次工具调用一条 trace）+ Audit Log（所有状态转移写 `workflow_transition_log`） |

### 2.2 反职责（Kernel 绝对不做）

| # | 反职责 | 说明 |
|---|---|---|
| 1 | **不做业务决策** | Kernel 不判断"任务是否完成"、"子任务该派给谁"、"重试还是放弃"。这些是大脑的 system prompt 决定的，Kernel 只忠实执行大脑的决定 |
| 2 | **不拆任务** | 任务拆分是 CentralBrain 的 system prompt 的活，Kernel 不替它拆 |
| 3 | **不维护对话历史** | Kernel 的 Agent Loop 是无状态的——每次 `Run` 调用都是独立的生命周期。跨 Run 的对话历史由 PlanStore 持久化，下次 Run 时由 Kernel 从 PlanStore 读回来注入 Request |
| 4 | **不 import 业务表** | `workflow/executor/kernel/` 目录下不允许 `import "easymvp/admin-go/app/mvp/internal/model/entity"` 这类语句。CI 静态检查会挡掉 |
| 5 | **不 import 编排层** | Kernel 不调 `workflow/orchestrator`、`workflow/stage`、`workflow/autonomy`——这些是上层消费者，Kernel 是它们的被调用者 |
| 6 | **不处理 HTTP/SSE** | Kernel 只提供 Go 函数接口。HTTP 层在 Atlas 的 controller 层或 CLI 的 main.go 里，Kernel 对这些一无所知 |
| 7 | **不决定工具的具体实现** | Kernel 提供 ToolRegistry 的注册机制，但具体工具的 Go 函数体由各个大脑（plugin）自己写，Kernel 只负责调度 |

**一句话**：Kernel 是**机械的调度器 + 严格的边界守卫 + 可靠的记账员**。任何"聪明"的判断都不属于 Kernel。

---

## 3. 核心接口：BrainAgent / Request / Result

### 3.1 BrainAgent 接口

所有大脑（包括 CentralBrain 和 SpecialistBrain）都实现这个接口：

```go
// workflow/executor/kernel/brain.go

package kernel

// BrainAgent 是一个带 LLM 的 Agent 定义。
// Kernel 通过这个接口拿到跑 Agent Loop 所需的全部信息。
type BrainAgent interface {
    // Kind 返回大脑类型
    // 中央大脑返回 "central"
    // 专精大脑返回 "code" / "browser" / "mobile" / "game" / "data" / "api"
    Kind() BrainKind

    // LLMConfig 返回这个大脑用的 LLM 配置
    // Kernel 会按此配置从 provider.Registry 拿到对应的 llm.Provider
    LLMConfig() LLMConfig

    // SystemPrompt 返回这个大脑的系统提示
    // 大脑可以在此根据 Request 做动态拼接（比如把 BrainPlan 注入进去）
    SystemPrompt(ctx context.Context, req *Request) (string, error)

    // RegisterTools 把这个大脑提供的工具注册到 Kernel 的 ToolRegistry
    // 返回的 []ToolSchema 是给 LLM 看的工具描述
    // 每个 ToolSchema 对应的实现函数已经在 registry 里
    RegisterTools(registry *ToolRegistry) ([]ToolSchema, error)

    // InitialMessages 构造 Agent Loop 的初始 user 消息
    // 中央大脑的初始消息通常是"用户原始任务 + 验收标准 + 当前 BrainPlan"
    // 专精大脑的初始消息通常是"中央大脑委派的子任务描述 + 相关上下文"
    InitialMessages(ctx context.Context, req *Request) ([]Message, error)

    // OnStop 在 Agent Loop 因为 LLM 返回 stop 而结束时被调用
    // 大脑可以在此做最终总结、触发持久化、验证计划完成度等
    // 返回的 Result 会被 Kernel 原样返回给调用方
    OnStop(ctx context.Context, req *Request, finalMessages []Message) (*Result, error)

    // OnMaxSteps 在达到 MaxSteps 未 stop 时被调用
    // 大脑可以返回一个"超时未完成"的 Result，或者返回 error 让调用方重试
    OnMaxSteps(ctx context.Context, req *Request, finalMessages []Message) (*Result, error)
}

// BrainKind 大脑类型
type BrainKind string

const (
    BrainCentral BrainKind = "central"
    BrainCode    BrainKind = "code"
    BrainBrowser BrainKind = "browser"
    BrainMobile  BrainKind = "mobile"
    BrainGame    BrainKind = "game"
    BrainData    BrainKind = "data"
    BrainAPI     BrainKind = "api"
)
```

**设计要点**：

1. **BrainAgent 是无状态的**——SystemPrompt / RegisterTools / InitialMessages 都接收 Request，根据 Request 动态生成。同一个 BrainAgent 实例可以被 Kernel 在不同请求之间复用（甚至并发复用），它不应该在字段里存跨请求的状态
2. **LLMConfig 是大脑自己决定的**——CentralBrain 返回 `claude-opus-4-6`，CodeBrain 返回 `claude-sonnet-4-6`。Kernel 不 override 这个决定
3. **OnStop / OnMaxSteps 是大脑的"出口钩子"**——Kernel 只决定循环什么时候结束，结束时怎么构造 Result 交给大脑自己决定。这是保证"Kernel 不做业务判断"的关键

### 3.2 LLMConfig 结构

```go
// LLMConfig 一个大脑的 LLM 配置
type LLMConfig struct {
    // Provider 标识，对应 llm.Registry 里注册的 adapter
    // 可选值：anthropic / openai / deepseek / gemini / ollama / ...
    Provider string

    // Model 模型 ID，按 provider 不同含义不同
    // anthropic: "claude-opus-4-6" / "claude-sonnet-4-6" / "claude-haiku-4-5-20251001"
    // openai: "gpt-5" / "gpt-4.1" / "o3"
    Model string

    // MaxSteps Agent Loop 最大步数
    // 中央大脑建议 20~30（不应该太长，否则失控）
    // 专精大脑建议 30~80（CodeBrain 可能 30，BrowserBrain 可能 80）
    MaxSteps int

    // Temperature 生成温度，0.0~1.0
    // 规划类建议 0.2~0.4，执行类建议 0.0~0.2
    Temperature float32

    // MaxTokens 单次响应 token 上限
    MaxTokens int

    // Vision 是否开启视觉输入
    // 中央大脑通常 false
    // BrowserBrain/MobileBrain/GameBrain 必须 true
    Vision bool

    // ExtendedThinking 是否启用 Claude 的 extended thinking
    // 仅 anthropic provider 下有效，其他 provider 忽略
    ExtendedThinking bool

    // Budget 预算控制
    // Kernel 的 CostMeter 累计超过此预算时中断循环
    Budget Budget
}

type Budget struct {
    // MaxTokensTotal 本次 Run 总 token 上限（prompt + completion）
    MaxTokensTotal int
    // MaxCostUSD 本次 Run 成本上限（美元）
    MaxCostUSD float64
    // MaxDuration 本次 Run 最长时间
    MaxDuration time.Duration
}
```

### 3.3 Request 结构

```go
// Request 给 Kernel 的一次执行请求
type Request struct {
    // RunID 本次 Run 的唯一 ID（雪花 ID）
    // Kernel 在 Trace/Audit/Artifact 里都会用这个 ID 做 key
    RunID int64

    // ParentRunID 如果本次 Run 是被另一个 Run 委派的（专精大脑被中央大脑调用），
    // 这里填中央大脑的 RunID；否则为 0
    ParentRunID int64

    // TaskID 本次 Run 关联的 DomainTask ID（Atlas 模式）
    // CLI 模式下可以为 0
    TaskID int64

    // WorkflowRunID 本次 Run 关联的 WorkflowRun ID（Atlas 模式）
    // CLI 模式下可以为 0
    WorkflowRunID int64

    // Goal 本次 Run 的目标
    // 中央大脑：用户原始任务（"加一个 /ping 接口并截图验证"）
    // 专精大脑：中央大脑委派的子任务描述（"在 api/v1 下新增 /ping handler，返回 200 OK"）
    Goal string

    // AcceptanceCriteria 验收标准列表
    AcceptanceCriteria []string

    // Context 上下文片段（来自上游，原样透传给大脑）
    // 可以是 architect 角色拆任务时给的背景、相关历史 diff、依赖的 subtask 的产出摘要等
    Context map[string]any

    // Workspace 工作空间（worktree / browser context / device handle）
    // Kernel 不解析它，只透传给大脑的工具
    Workspace *Workspace

    // Guardrail 任务级的 Guardrail 约束
    // 在插件默认白名单基础上再收紧
    Guardrail *GuardrailConfig

    // AllowedBrains 本次 Run 允许调用的大脑列表（中央大脑才用）
    // 空表示允许所有已注册的大脑
    // 可以用来做安全隔离：某个项目只允许 code + browser，不允许 mobile
    AllowedBrains []BrainKind

    // RequesterRole 谁在发起这次 Run
    // "architect" / "implementer" / "auditor" / "experience_reviewer" / "user"
    RequesterRole string

    // CreatedBy / DeptID 数据权限字段（Atlas 模式必填，CLI 模式可以为 0）
    CreatedBy int64
    DeptID    int64
}
```

**设计要点**：

- `Request` 是**不可变的**——Kernel 和大脑在 Run 过程中都不能修改它。所有可变状态走 `PlanStore` 和 `ArtifactStore`
- `Context` 是 `map[string]any`，故意不做强类型——让上游（Atlas 或 CLI）可以塞任意背景信息，Kernel 原样透传。大脑 SystemPrompt 里引用的字段由大脑自己定义
- `AllowedBrains` 是**安全层**的东西，不是业务层——某些敏感项目可能要禁用 MobileBrain，这个字段就是控制点

### 3.4 Result 结构

```go
// Result 一次 Run 的最终结果
type Result struct {
    // Status 最终状态
    Status RunStatus

    // Summary 大脑给出的终稿摘要（自然语言，≤500 字，给 UI 展示用）
    Summary string

    // Report 结构化汇报（专精大脑必填，中央大脑可选）
    // 详见 §3.5 SpecialistReport 汇报契约
    // 专精大脑必须通过 Report 工具产出此字段，否则 Kernel 拒绝结束 Run
    Report *SpecialistReport

    // Rejected 如果中央大脑拒绝了任务，这里带拒绝信息
    // 仅 CentralBrain 可以填此字段；SpecialistBrain 不允许
    Rejected *RejectedInfo

    // FinalPlan 如果是 CentralBrain，返回最终的 BrainPlan 快照
    FinalPlan *BrainPlan

    // Evidence 多模态证据引用列表
    Evidence []ArtifactRef

    // ToolCalls 本次 Run 的所有工具调用记录（按时间排序）
    // 用于审计、调试、重放
    ToolCalls []ToolCallRecord

    // StepsTaken 实际执行了多少步 Agent Loop
    StepsTaken int

    // TokensUsed 本次 Run 累计 token 使用量
    TokensUsed TokenUsage

    // CostUSD 本次 Run 的美元成本
    CostUSD float64

    // Duration 本次 Run 总时长
    Duration time.Duration

    // StartedAt / FinishedAt 时间戳
    StartedAt  time.Time
    FinishedAt time.Time
}

type RunStatus string

const (
    RunStatusSuccess    RunStatus = "success"     // 大脑通过 OnStop 成功结束
    RunStatusPartial    RunStatus = "partial"     // 部分完成（子任务完成了一部分）
    RunStatusFailed     RunStatus = "failed"      // 失败（LLM 报错、工具连续失败、预算超限）
    RunStatusMaxSteps   RunStatus = "max_steps"   // 达到 MaxSteps 未完成
    RunStatusGuarded    RunStatus = "guarded"     // 被 Guardrail 拒绝超过阈值次数
    RunStatusRejected   RunStatus = "rejected"    // 中央大脑主动拒绝任务
    RunStatusCancelled  RunStatus = "cancelled"   // 人工或上游取消
    RunStatusBudget     RunStatus = "budget"      // 预算超限
)

type RejectedInfo struct {
    ReasonCodes []string  // 详见 07-大脑委派工具协议.md 的 reject_task
    Description string
    Suggestions []string  // 建议的重写方向
}

type TokenUsage struct {
    PromptTokens     int
    CompletionTokens int
    CachedTokens     int  // Anthropic prompt caching 命中
    ReasoningTokens  int  // OpenAI o-series 的 reasoning token
}
```

---

### 3.5 SpecialistReport 汇报契约

**这是整个 BrainKernel 的一块基石**。它决定了：
- 专精大脑如何结构化告诉中央大脑"我干了什么"
- 中央大脑凭什么证据做内部验收
- EasyMVP 审核员（auditor 角色）如何看到整个项目的执行证据

对标 EasyMVP `admin-go/app/mvp/internal/engine/context_compressor.go` 的 token 压缩思想，但这里的产物是**结构化 JSON**，不是自然语言摘要——因为消费者不仅是 LLM，还有验收状态机、审核 UI、审计日志。

#### 3.5.1 设计原则

1. **专精大脑必须产出 Report**：Kernel 在 Run 结束时校验 `Result.Report != nil`，否则转 RunStatusFailed（reason=`missing_report`）
2. **Report 是不可变事实**：一旦写入 PlanStore 就不能修改，后续的 clarification、verification、escalation 都追加新记录，不覆盖原 Report
3. **证据指针 > 证据拷贝**：Report 里只存 ArtifactRef（sha256+path），真实证据体在 ArtifactStore；这样 Report 本体 ≤ 8KB，可整条塞回 LLM 上下文
4. **自评 + 他评双轨**：专精大脑自评 `risk_level` / `confidence`，中央大脑和 verifier_brain 再做他评；两者都记录，不相互覆盖

#### 3.5.2 SpecialistReport 结构体

```go
// SpecialistReport 专精大脑的结构化汇报
// 所有专精大脑必须通过 Report 工具产出此结构
type SpecialistReport struct {
    // ===== 身份 =====
    SubtaskID  string     // 对应的 execution_subtask.id
    BrainKind  BrainKind  // code_brain / browser_brain / mobile_brain / ...
    BrainRunID string     // 对应的 BrainRun.id，用于追溯

    // ===== 干了什么（事实层） =====
    // Actions 按时间顺序记录的动作清单，每个动作对应一次关键工具调用
    // 不是全量 ToolCall（那在 Trace 里），而是"对外有副作用或验收相关"的动作
    Actions []ReportAction

    // TouchedResources 本次 Run 触达的资源指纹（供资源锁冲突检测 + 审计）
    TouchedResources []ResourceFingerprint

    // Artifacts 产出的证据（截图、diff、日志片段、网络 trace 等）
    // 只存 ArtifactRef，真实内容在 ArtifactStore
    Artifacts []ArtifactRef

    // ===== 结果层 =====
    // Outcome 专精大脑自评的结果
    Outcome ReportOutcome

    // CompletedGoals 原任务目标中已完成的部分（对应 Request.Goals）
    CompletedGoals []string

    // UncompletedGoals 未完成的部分 + 原因
    UncompletedGoals []UncompletedItem

    // ===== 自评层（给验收状态机用） =====
    // RiskLevel 专精大脑自评的风险级别
    // 中央大脑可提升不可降低（例如专精大脑报 low，中央大脑读完后改 medium）
    RiskLevel RiskLevel

    // Confidence 0.0 ~ 1.0，专精大脑对自己产出的置信度
    // < 0.7 自动触发 verifier_brain 复核，无论 RiskLevel 是什么
    Confidence float32

    // SelfChecks 专精大脑在结束前自己跑的检查清单（可选）
    // 例如 code_brain 跑 go build、browser_brain 跑断言截图
    SelfChecks []SelfCheckResult

    // ===== 沟通层 =====
    // Questions 专精大脑的疑问（非阻塞，中央大脑决定是否回答）
    Questions []string

    // Suggestions 对后续步骤的建议（下一个子任务应该做什么）
    Suggestions []string

    // NeedsClarification 如果为 true，中央大脑必须在验收前通过
    // request_clarification 回问至少一次；详见 §3.6 clarifying 状态
    NeedsClarification bool

    // ===== 时间戳 =====
    StartedAt  time.Time
    FinishedAt time.Time
}

type ReportAction struct {
    Seq         int       // 动作序号（Run 内递增）
    Kind        string    // write_file / run_command / click_element / adb_shell / ...
    Target      string    // 目标资源（文件路径、URL、包名等）
    Description string    // 一句话人话描述
    ArtifactRef *ArtifactRef  // 如果动作产生了证据，这里是指针
    Success     bool
    DurationMs  int64
}

type ResourceFingerprint struct {
    Type string  // file / url / device / db / api_endpoint
    Key  string  // /path/to/file 或 https://... 或 emulator-5554
    Hash string  // 文件 sha256 / DOM 快照 sha256 / 空串
}

type ReportOutcome string

const (
    OutcomeSuccess          ReportOutcome = "success"           // 自认为全部目标完成
    OutcomePartialSuccess   ReportOutcome = "partial"           // 完成了一部分，其他部分有明确原因
    OutcomeBlocked          ReportOutcome = "blocked"           // 卡在外部依赖（等人工、等上游、等工具）
    OutcomeFailed           ReportOutcome = "failed"            // 尝试过但失败，建议重派或升级
    OutcomeRefused          ReportOutcome = "refused"           // 拒绝执行（越界 / 违反 Guardrail / 道德）
)

type UncompletedItem struct {
    Goal       string
    Reason     string
    Blocker    string  // 可选：阻塞项（"需要 ANTHROPIC_API_KEY" / "Android 模拟器未启动"）
    Suggestion string  // 可选：建议怎么解决
}

type RiskLevel string

const (
    RiskLow    RiskLevel = "low"    // 只读、只截图、只查询
    RiskMedium RiskLevel = "medium" // 写文件、改配置、装依赖
    RiskHigh   RiskLevel = "high"   // 改主干代码、git commit/push、部署、发消息、调用付费 API
)

type SelfCheckResult struct {
    Name    string  // "go build" / "playwright screenshot diff" / "adb logcat scan"
    Passed  bool
    Details string  // 失败时的输出片段
}
```

#### 3.5.3 Report 工具：专精大脑产出 Report 的唯一入口

每个专精大脑的工具集里**都会自动注入**一个 `report` 工具（由 Kernel 在 RegisterTools 之后追加），签名固定：

```json
{
  "name": "report",
  "description": "在结束 Run 前必须调用一次。产出结构化 SpecialistReport。调用后 Agent Loop 停止。",
  "input_schema": {
    "type": "object",
    "required": ["outcome", "risk_level", "confidence", "actions", "completed_goals"],
    "properties": {
      "outcome":           { "type": "string", "enum": ["success","partial","blocked","failed","refused"] },
      "risk_level":        { "type": "string", "enum": ["low","medium","high"] },
      "confidence":        { "type": "number", "minimum": 0, "maximum": 1 },
      "actions":           { "type": "array", "items": { "$ref": "#/defs/ReportAction" } },
      "touched_resources": { "type": "array", "items": { "$ref": "#/defs/ResourceFingerprint" } },
      "artifacts":         { "type": "array", "items": { "$ref": "#/defs/ArtifactRef" } },
      "completed_goals":   { "type": "array", "items": { "type": "string" } },
      "uncompleted_goals": { "type": "array", "items": { "$ref": "#/defs/UncompletedItem" } },
      "self_checks":       { "type": "array", "items": { "$ref": "#/defs/SelfCheckResult" } },
      "questions":         { "type": "array", "items": { "type": "string" } },
      "suggestions":       { "type": "array", "items": { "type": "string" } },
      "needs_clarification":{ "type": "boolean" }
    }
  }
}
```

**Kernel 的强约束**（写进 Agent Loop 的 §4 伪代码）：
- 专精大脑**必须**以 `report` 工具调用作为 Run 的最后一步
- 调用 `report` 之后的任何 tool_call 都会被 Kernel 忽略（已落幕）
- 如果 Agent Loop 达到 MaxSteps 仍未调用 `report`，Kernel 在 OnMaxSteps 里自动合成一个 `outcome=failed, reason=max_steps_without_report` 的兜底 Report
- 中央大脑**不用** `report` 工具，而是用 `finish_run` 工具（见 07-大脑委派工具协议）

#### 3.5.4 SpecialistReport 的存储位置

```
execution_subtask 表新增字段：
  report_json       JSON     -- 完整的 SpecialistReport（≤ 8KB）
  outcome           VARCHAR  -- 冗余 report.outcome，便于索引
  risk_level        VARCHAR  -- 冗余 report.risk_level
  confidence        DECIMAL  -- 冗余 report.confidence
  verification_status VARCHAR -- 见 §3.6 验收状态机
```

CLI 模式下：`~/.easymvp-exec/runs/<run_id>/subtasks/<subtask_id>/report.json`

#### 3.5.5 为什么不复用 `Result.Summary`

- `Summary` 是自然语言，给 UI 展示用，不可结构化检索
- `Summary` 的字段集合会随每个大脑不同而漂移，无法做统一验收
- `Summary` 进 LLM 上下文会被改写；`Report` 是事实，必须不可变

两者并存：`Summary` 给人看，`Report` 给机器 + LLM + 审核员看。

---

### 3.6 子任务验收状态机

每个 `execution_subtask` 从创建到终结都会走下面这个状态机。**Kernel 只定义状态和转换规则**，具体谁在什么时候推进状态由中央大脑决定（见 03-CentralBrain 设计）。

#### 3.6.1 状态枚举

```go
type VerificationStatus string

const (
    VerifPending     VerificationStatus = "pending"      // 已派发，专精大脑尚未结束
    VerifReported    VerificationStatus = "reported"     // 专精大脑已产出 Report，等中央大脑决策
    VerifVerifying   VerificationStatus = "verifying"    // 中央大脑已起 verifier_brain，等复核
    VerifClarifying  VerificationStatus = "clarifying"   // 中央大脑在回问原专精大脑（带约束重启）
    VerifEscalated   VerificationStatus = "escalated"    // 已交给 fault_brain 接管
    VerifAccepted    VerificationStatus = "accepted"     // 内部验收通过，事实固化
    VerifRejected    VerificationStatus = "rejected"     // 内部验收失败，回滚资源，通知中央大脑重规划
)
```

**对应决策 2（verifier_brain 独立）**：状态 `verifying` 和 `escalated` 分别对应两个**独立的内置大脑**——`verifier_brain` 只读复核，`fault_brain` 接管故障。它们不是 central_brain 的内部分支，也不是 specialist 的子模式，而是和 code_brain / browser_brain 平级的顶级 BrainKind。详见 §9.1 内置 BrainKind 常量。

**为什么必须独立**：
- **零利益冲突**：复核大脑如果是"出活大脑自己的一个方法"，等于让它给自己打分，天然失信
- **可独立演进**：verifier_brain 和 fault_brain 可以单独换模型、单独升级 prompt、单独做 A/B 测试，不污染实现类大脑
- **可独立授权**：企业场景下复核大脑可以走更贵但更准的模型（如 Opus），fault_brain 可以限定只允许企业管理员触发

#### 3.6.2 状态转换图

```
                         ┌────────────────────────────────────┐
                         ↓                                    │
    pending ──→ reported ──→ verifying ──→ accepted           │
                   │             │                            │
                   │             ├──→ clarifying ──→ reported ┘
                   │             │       (回问原专精大脑，带约束重启)
                   │             │
                   │             └──→ escalated ──→ accepted
                   │                                │
                   │                                └──→ rejected
                   │
                   └──→ rejected（专精大脑 outcome=refused 时直接拒）
```

**对应决策 3（三种验证路径共存）**：上面这张状态图刻意让**三条验证路径同时存在**，由中央大脑按 Report 的 RiskLevel/Confidence 动态选择，而不是非此即彼：

| 路径 | 从哪到哪 | 触发条件 | 本质 |
|---|---|---|---|
| **A · 证据直通** | `reported → accepted` | RiskLevel=low 且 Confidence≥0.7 | 中央大脑自读 Report 里的 artifacts/metrics，直接认账 |
| **B · 询问专精大脑** | `reported → verifying → accepted/rejected` | RiskLevel=medium 或 low+低置信 | 起 verifier_brain 用只读工具集独立复核（可能再 `clarifying` 回问原实现者） |
| **C · 故障大脑接管** | `reported → escalated → accepted/rejected` | RiskLevel=high 或 B 路径连续失败 | 起 fault_brain 按 fault_policy 接管，先 advisory 后 takeover（见 §6.5） |

**三条路径不是互斥选项，而是一条流水线的三档入口**：低风险走 A 省钱、中风险走 B 独立复核、高风险/B 失败再升 C 抢救。任一路径的失败都会回落到 `rejected`，由中央大脑触发重规划。这是用户明确拍板的："三种都要，能共存"。

#### 3.6.3 分级验收规则（对应决策 1）

根据 `Report.RiskLevel` + `Report.Confidence` 自动决定下一步：

| RiskLevel | Confidence | 动作 |
|-----------|-----------|------|
| low | ≥ 0.7 | 中央大脑自读 Report → 直接 `accepted` |
| low | < 0.7 | 中央大脑自读 Report，遇疑起 verifier_brain（快速模式） |
| medium | 任意 | **必须**起 verifier_brain 复核（快速模式，MaxSteps=10） |
| high | 任意 | **必须**起 verifier_brain 独立复核（深度模式，MaxSteps=25），且允许 request_clarification 回问 |
| 任意 | 任意 + `needs_clarification=true` | 必须先 `clarifying` 至少一轮 |

**中央大脑可提升不可降低**：中央大脑读完 Report 后可以把 low 升级为 medium/high，但不能把 high 降级为 low。升级后按升级后的规则走。

#### 3.6.4 Kernel 对状态机的责任

Kernel **不**自己推进状态（那是中央大脑的业务逻辑，由 03-CentralBrain 定义）。Kernel 只提供：

1. **存储与并发安全**：`PlanStore.UpdateSubtaskVerification(subtaskID, oldStatus, newStatus)` 用乐观锁/CAS 保证转换原子
2. **非法转换校验**：Kernel 内置一张转换表，拒绝 `accepted → verifying` 这种反向转换
3. **历史记录**：每次转换写入 `execution_subtask_verification_log` 附表，供审计
4. **事件广播**：状态变化通过 SSE 推给 UI，不等中央大脑下一步决策

---

## 4. Agent Loop Runner

> **📌 正式规格另见**：本节是**概念视图**。Agent Loop 的**行为契约**（术语与状态机、Prompt Cache 三层策略、Streaming 事件流、上下文压缩、ChatRequest 字段、工具批次并发、per-tool 超时、中心化 rate limit、tool_result 污染防御、死循环检测、反思预算）由独立 RFC 文档 [22-Agent-Loop规格.md](./22-Agent-Loop规格.md) 承载。任何对 Agent Loop 运行时行为的疑问 **MUST** 以 22 号文档为准，本节伪代码只是教学用途，**不是实现规格**。
>
> 22 号文档的 20 条合规测试（C-L-01 ~ C-L-20）是 v1 Runner 的**验收必过项**。

这是 Kernel 最核心的一段代码。所有大脑（中央 + 专精）共用这一份循环。

### 4.1 算法伪代码

```
func Run(ctx, agent, req) (*Result, error):
    // 1. 初始化
    llm = providerRegistry.Get(agent.LLMConfig().Provider)
    systemPrompt = agent.SystemPrompt(ctx, req)
    toolSchemas = agent.RegisterTools(toolRegistry)
    messages = agent.InitialMessages(ctx, req)

    // 2. 预算初始化
    budget = NewBudgetTracker(agent.LLMConfig().Budget)
    costMeter.BeginRun(req.RunID, agent.Kind(), agent.LLMConfig())

    // 3. 循环
    for step in 0..agent.LLMConfig().MaxSteps:
        // 3a. 检查预算
        if budget.Exceeded(): return failed(BudgetExceeded)
        if ctx.Err() != nil: return failed(ctx.Err())

        // 3b. 调 LLM
        traceLLMCallStart(req.RunID, step)
        resp, err = llm.Chat(ctx, ChatRequest{
            Model:            agent.LLMConfig().Model,
            System:           systemPrompt,
            Messages:         messages,
            Tools:            toolSchemas,
            Temperature:      agent.LLMConfig().Temperature,
            MaxTokens:        agent.LLMConfig().MaxTokens,
            Vision:           agent.LLMConfig().Vision,
            ExtendedThinking: agent.LLMConfig().ExtendedThinking,
        })
        traceLLMCallEnd(req.RunID, step, resp, err)

        if err != nil:
            if isTransient(err):
                sleep(backoff(step))
                continue  // 重试同一步
            return failed(err)

        // 3c. 更新预算
        budget.AddLLMUsage(resp.Usage)
        costMeter.AddLLMUsage(req.RunID, resp.Usage, step)

        // 3d. 记录 assistant 消息到 messages 历史
        messages = append(messages, Message{
            Role:      "assistant",
            Content:   resp.Content,
            ToolCalls: resp.ToolCalls,
        })

        // 3e. 如果 LLM 返回 stop（没有 tool_call），调大脑的 OnStop 退出循环
        if len(resp.ToolCalls) == 0:
            return agent.OnStop(ctx, req, messages)

        // 3f. 并发执行所有 tool_call
        //     （Claude 和 OpenAI 都支持一个 assistant message 里返回多个 tool_use）
        toolResults = executeToolsParallel(ctx, req, resp.ToolCalls)

        // 3g. 把 tool_result 作为 user 消息回注
        messages = append(messages, Message{
            Role:    "user",
            ToolResults: toolResults,
        })

    // 4. 达到 MaxSteps
    return agent.OnMaxSteps(ctx, req, messages)
```

### 4.2 关键实现细节

#### 4.2.1 并发执行 tool_call

Claude 和 OpenAI 都支持**一个 assistant message 里返回多个 tool_use**——这是中央大脑能发并发委派的关键。Kernel 的实现：

```go
func (k *Kernel) executeToolsParallel(
    ctx context.Context,
    req *Request,
    calls []ToolCall,
) []ToolResult {
    results := make([]ToolResult, len(calls))
    var wg sync.WaitGroup

    // 并发上限：Kernel 配置，默认 8
    sem := make(chan struct{}, k.config.MaxConcurrentTools)

    for i, call := range calls {
        wg.Add(1)
        sem <- struct{}{}
        go func(idx int, c ToolCall) {
            defer wg.Done()
            defer func() { <-sem }()
            results[idx] = k.executeOneTool(ctx, req, c)
        }(i, call)
    }

    wg.Wait()

    // 保持原顺序——LLM 需要 tool_use_id 对齐
    return results
}
```

**关键约束**：
- 并发执行，但按 `tool_use_id` **严格对齐**顺序。LLM 通过 id 匹配 tool_result 到 tool_use
- 并发上限由 Kernel 配置（`max_concurrent_tools`，默认 8），防止中央大脑一次派 50 个子任务把 LLM API 打爆
- 某个工具失败不影响其他并发工具——每个都独立收敛到 `ToolResult{IsError: true}` 返回

#### 4.2.2 工具调用执行的内部流程

每个 tool_call 在 `executeOneTool` 里经过这些步骤：

```go
func (k *Kernel) executeOneTool(
    ctx context.Context,
    req *Request,
    call ToolCall,
) ToolResult {
    start := time.Now()
    traceToolCallStart(req.RunID, call)

    // 1. 查 ToolRegistry 找到工具实现
    tool, ok := k.toolRegistry.Lookup(call.Name)
    if !ok {
        return errorResult(call, "tool_not_found",
            fmt.Sprintf("tool %s not registered", call.Name))
    }

    // 2. 参数反序列化 + JSON Schema 校验
    input, err := tool.ParseInput(call.Arguments)
    if err != nil {
        return errorResult(call, "invalid_arguments", err.Error())
    }

    // 3. Guardrail 校验（两层：插件默认 + 任务级收紧）
    if err := k.guardrail.Check(ctx, req, tool, input); err != nil {
        recordGuardrailDenial(req.RunID, call, err)
        return errorResult(call, "guardrail_denied", err.Error())
    }

    // 4. 执行工具
    output, err := tool.Invoke(ctx, req, input)
    duration := time.Since(start)
    traceToolCallEnd(req.RunID, call, output, err, duration)

    if err != nil {
        return errorResult(call, "tool_execution_failed", err.Error())
    }

    // 5. 如果工具产生了 Artifact，通过 ArtifactStore 持久化
    if output.Artifacts != nil {
        for _, art := range output.Artifacts {
            ref, uploadErr := k.artifactStore.Put(ctx, req.RunID, art)
            if uploadErr != nil {
                // 产物上传失败 = 整个工具调用失败
                return errorResult(call, "artifact_upload_failed", uploadErr.Error())
            }
            output.ArtifactRefs = append(output.ArtifactRefs, ref)
        }
    }

    // 6. 构造成功的 ToolResult
    return ToolResult{
        ToolUseID:    call.ToolUseID,
        Content:      output.Content,
        ArtifactRefs: output.ArtifactRefs,
        IsError:      false,
    }
}
```

**关键点**：

- **失败也返回 tool_result，不 panic、不退循环**——让 LLM 下一轮看到 error 自己决定怎么办。这是多 Agent 架构错误恢复的基础
- **Guardrail 拒绝和执行失败分两种错误码**——LLM 看到 `guardrail_denied` 会知道"我方向错了"，看到 `tool_execution_failed` 会知道"可能重试"
- **Artifact 上传失败 = 工具失败**——不允许"工具跑成功了但截图丢了"，这会让后续的判官看不到证据

#### 4.2.3 重试策略

Kernel 的重试分两层：

| 层级 | 触发条件 | 重试方式 |
|---|---|---|
| **LLM 调用级** | `isTransient(err)`：429 rate limit / 500 / 502 / 503 / context deadline | 指数退避，`min(2^step * 500ms, 10s)`，最多 3 次。超限后抛给上层 |
| **工具调用级** | **Kernel 不重试工具**——工具失败后返回 tool_result，由 LLM 决定下一步怎么做 | 永不，工具失败 = LLM 看到 error |

**故意不做的事**：
- **不做"自动 rewrite prompt 重试"**——这会混淆调试，LLM 自己看到 error 比 Kernel 偷偷改 prompt 更透明
- **不做"工具失败后换一个工具重试"**——这是大脑的决策，Kernel 不越权

### 4.3 Agent Loop 的错误出口

Agent Loop 可能以下面任一方式结束：

| 结束原因 | Result.Status | 触发 |
|---|---|---|
| LLM 返回 stop | success / partial / rejected | 大脑的 OnStop 决定 |
| 达到 MaxSteps | max_steps | 大脑的 OnMaxSteps 决定 |
| 预算超限 | budget | Kernel 内部 |
| Context 被 cancel | cancelled | Kernel 内部 |
| LLM 调用连续失败 | failed | Kernel 内部 |
| Guardrail 连续拒绝超阈值 | guarded | Kernel 内部（默认 10 次连续被拒就退） |

**连续 Guardrail 拒绝计数**：如果 LLM 连续 N 次（默认 10）工具调用全部被 Guardrail 拒绝，Kernel 判定"LLM 认知错乱"，强制退出。避免 LLM 陷入"我必须改 .gitignore → 被拒 → 我必须改 .gitignore → 被拒"的死循环把 token 烧光。

---

## 5. LLM Provider 抽象层（接口层面）

> **📌 相关规格**：Provider Adapter **MUST** 与 [22-Agent-Loop规格.md §3](./22-Agent-Loop规格.md#3-prompt-cache-分层策略) 定义的三层 Prompt Cache 和 [§4 Streaming 协议](./22-Agent-Loop规格.md#4-streaming-协议) 兼容；**MUST** 实现 `SupportsPromptCache()` / `ApplyCacheControl()` 两个钩子；429 / Rate Limit 处理 **MUST** 走 [22 §9 中心化退避](./22-Agent-Loop规格.md#9-rate-limit-与中心化退避) 而非各自实现。


本节只定义 Kernel 侧**看得到的接口**。具体 adapter 实现见 `05-LLM-Provider抽象层.md`。

### 5.1 llm.Provider 接口

```go
// workflow/executor/kernel/llm/provider.go

package llm

type Provider interface {
    // Name 返回 provider 名称，用于日志和记账
    Name() string

    // Chat 发一次请求，同步返回完整响应
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

    // ChatStream 发一次请求，流式返回
    // 本 Kernel 目前不使用 stream（Agent Loop 等完整响应才能解析 tool_call）
    // 但保留接口以便未来给 CLI 的 --stream 模式用
    ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)

    // CountTokens 估算一段 prompt 的 token 数
    // 用于预算预估
    CountTokens(ctx context.Context, req CountTokensRequest) (int, error)

    // Capabilities 返回这个 provider 支持的特性
    // Kernel 根据此决定是否能把 Vision/ExtendedThinking/ParallelToolCalls 传下去
    Capabilities() Capabilities
}

type ChatRequest struct {
    Model            string
    System           string
    Messages         []Message
    Tools            []ToolSchema
    Temperature      float32
    MaxTokens        int
    Vision           bool
    ExtendedThinking bool
    StopSequences    []string
    // 更多字段见 05 文档
}

type ChatResponse struct {
    Content      []ContentBlock  // text / thinking / tool_use
    ToolCalls    []ToolCall      // 从 Content 里 extract 出来的便捷列表
    StopReason   StopReason      // stop / tool_use / max_tokens / safety
    Usage        TokenUsage
}

type Capabilities struct {
    SupportsVision              bool
    SupportsExtendedThinking    bool
    SupportsParallelToolCalls   bool
    SupportsPromptCaching       bool
    MaxContextTokens            int
    MaxOutputTokens             int
}
```

### 5.2 Provider 注册表

```go
// workflow/executor/kernel/llm/registry.go

type Registry struct {
    providers map[string]Provider
}

func (r *Registry) Register(name string, p Provider) error { ... }
func (r *Registry) Get(name string) (Provider, error) { ... }
func (r *Registry) List() []string { ... }
```

Atlas 启动时在 `workflow/executor/kernel/llm/registry.go` 的 `init` 或 `Bootstrap` 里注册两个默认 provider：`anthropic` 和 `openai`。CLI 模式下由 `cmd/easymvp-exec/main.go` 注册。

### 5.3 扩展新 provider 的成本承诺

扩展一个新 provider（比如 DeepSeek / Gemini / Ollama）的工作量硬指标：

- **代码**：≤ 300 行 Go（一个 `deepseek_provider.go` 文件）
- **工作量**：≤ 1 个工作日
- **改动范围**：只加文件，不改 Kernel 任何代码

这个指标在 `05-LLM-Provider抽象层.md` 里有详细拆解和验收 checklist。Kernel 的职责是把 `Provider` 接口写得足够薄，让这个承诺成立。

---

## 6. Tool Registry 与 Guardrail

> **📌 相关规格**：工具调用的**运行时行为**（Tool Call 生命周期、批次并发、JSON Schema 校验、死循环检测、per-tool 超时、tool_result sanitizer 流水线、不可信内容 `<tool_output>` 标记）由 [22-Agent-Loop规格.md §7 / §8 / §10 / §11](./22-Agent-Loop规格.md#7-工具调用规格) 承载。本节只定义 **ToolRegistry 接口** 和 **Guardrail 两层白名单的配置**，不定义运行时调度行为。


### 6.1 ToolRegistry 接口

```go
// workflow/executor/kernel/tool.go

type ToolRegistry struct {
    tools map[string]Tool
}

type Tool interface {
    // Name 工具名，全局唯一（包括所有大脑注册的工具）
    // 命名约定：<brain_kind>.<verb>，例如 code.read_file / browser.navigate / central.code_brain
    Name() string

    // Schema 给 LLM 看的工具描述（JSON Schema）
    Schema() ToolSchema

    // ParseInput 反序列化 + 校验参数
    ParseInput(raw json.RawMessage) (any, error)

    // Invoke 执行
    // ctx 可能被 cancel，实现必须响应
    // req 是当前 Run 的上下文，实现可以从中读取 Workspace / Guardrail 等
    // input 是 ParseInput 的返回值
    Invoke(ctx context.Context, req *Request, input any) (*ToolOutput, error)
}

type ToolOutput struct {
    // Content 给 LLM 看的结果文本（必填）
    Content string

    // Artifacts 需要由 ArtifactStore 持久化的产物
    // 可以为 nil（没有二进制产物）
    Artifacts []Artifact

    // ArtifactRefs 已经持久化完成的引用（Kernel 填，工具不填）
    ArtifactRefs []ArtifactRef
}
```

**命名规范铁律**：
- 中央大脑的工具名以 `central.` 开头（`central.code_brain` / `central.plan_store_update` / `central.reject_task`）
- 专精大脑的工具名以大脑名开头（`code.read_file` / `browser.navigate` / `mobile.tap`）
- **所有工具名全局唯一**——避免不同大脑的同名工具混淆（比如 code.screenshot 和 browser.screenshot 如果都叫 screenshot 会炸）

### 6.2 Guardrail 两层白名单

每次工具调用都过两层检查：

```
      tool_call
          │
          ▼
┌────────────────────────┐
│ 第一层：插件默认白名单   │  ← 由大脑在 RegisterTools 时注册
│                        │
│ 例：CodeBrain 的       │
│ write_file 只允许写    │
│ 非黑名单路径           │
│ (.git/** .gitignore   │
│  go.mod 等永远拒绝)    │
└──────────┬─────────────┘
           │ passed
           ▼
┌────────────────────────┐
│ 第二层：任务级收紧       │  ← 由 Request.Guardrail 注入
│                        │
│ 例：本次任务的          │
│ AllowPaths = ["api/**"]│
│ 只允许写 api/ 下面      │
│ 即使插件允许写 src/ 也  │
│ 被拒绝                  │
└──────────┬─────────────┘
           │ passed
           ▼
     tool.Invoke(ctx, req, input)
```

**两层的区别**：

| 维度 | 第一层：插件默认 | 第二层：任务级收紧 |
|---|---|---|
| **什么时候定** | 大脑开发时在代码里写死 | 每个 Request 运行时决定 |
| **谁来收紧** | 大脑插件代码 | 上游（architect 角色或 CLI 参数）|
| **能放开吗** | 第二层**不能放开第一层**。只能更严 |
| **示例** | CodeBrain 永远不能写 .git/** | 本任务只能写 api/v1/handler/ 下面 |

### 6.3 Guardrail 配置结构

```go
type GuardrailConfig struct {
    // AllowPaths 白名单路径（glob 语法）
    // 只有 CodeBrain / MobileBrain install_apk 等涉及文件系统的工具关心
    AllowPaths []string

    // BlockPaths 黑名单路径（glob 语法）
    // 在 AllowPaths 内但匹配 BlockPaths 的路径仍然被拒
    BlockPaths []string

    // AllowDomains 白名单域名
    // 只有 BrowserBrain / GameBrain 关心
    AllowDomains []string

    // BlockDomains 黑名单域名
    BlockDomains []string

    // AllowPackages 白名单 Android package
    // 只有 MobileBrain 关心
    AllowPackages []string

    // AllowDevices 白名单设备 serial
    // 只有 MobileBrain 关心
    AllowDevices []string

    // MaxToolInvocationsPerStep 单步最大工具并发
    // 默认 8，Kernel 层面约束
    MaxToolInvocationsPerStep int

    // MaxConsecutiveGuardrailDenials 连续 Guardrail 拒绝的阈值
    // 超过就强制退出循环
    MaxConsecutiveGuardrailDenials int  // 默认 10
}
```

### 6.4 越界事件的记录

每次 Guardrail 拒绝都写一条 `guardrail_denial` 事件到审计日志：

```go
type GuardrailDenialEvent struct {
    RunID     int64
    Step      int
    ToolName  string
    Input     json.RawMessage
    DenyLayer string  // "plugin_default" / "task_level"
    Reason    string
    Timestamp time.Time
}
```

Atlas 模式下写到 `workflow_transition_log` 表的专用子类型；CLI 模式下写到 `~/.easymvp-exec/runs/<run_id>/guardrail.log`。这些日志是**事后对 LLM 行为做统计**的基础——通过看"某个大脑的哪些工具最常被拒"可以知道哪个大脑的 system prompt 需要优化。

### 6.5 fault_policy.json：验收升级与故障大脑权限（对应决策 4）

**对应决策 4（fault_policy 中央可配 + 两阶段权限）**：用户的原话是："触发阙值又中央大脑可配置文件，json之类的。权限边界，第一次只出修改方案，如果没处理好，后续全面接管所有所有权限。" 本节就是把这两条需求落成具体的配置协议。

验收状态机（§3.6）的升级阈值、clarification 轮次上限、fault_brain 的触发条件与权限边界**全部由中央大脑从配置文件加载**，不在 Kernel 里硬编码。这样不同项目、不同组织可以按自己的容忍度调节。

**加载顺序（三级回退）**：
1. 运行时注入：`Request.FaultPolicyOverride`（临时覆盖，优先级最高）
2. 中央大脑配置：中央大脑的 `LLMConfig.FaultPolicyPath` 指向的 JSON（项目级）
3. 平台默认：`config/fault_policy.default.json`（所有项目共享的兜底）

**配置格式**：

```json
{
  "escalation": {
    "verifier_reject_threshold": 2,
    "_doc_verifier_reject": "同一子任务被 verifier_brain 驳回多少次后升级到 fault_brain",

    "specialist_retry_threshold": 3,
    "_doc_specialist_retry": "同一子任务重派原专精大脑的最大次数",

    "clarification_round_limit": 2,
    "_doc_clarification_round": "request_clarification 最多对同一子任务回问几轮",

    "confidence_floor_for_auto_accept": 0.7,
    "_doc_confidence_floor": "低于此置信度即使 low 风险也必须走 verifier_brain"
  },

  "verifier_brain": {
    "quick_mode_max_steps": 10,
    "deep_mode_max_steps": 25,
    "model_preference": ["claude-opus-4-6", "gpt-5"],
    "_doc": "deep 模式给 high 风险用，quick 模式给 medium 用"
  },

  "fault_brain": {
    "first_round": {
      "mode": "advisory",
      "allowed_tools": ["read_file", "git_log", "grep", "trace_query", "artifact_get"],
      "output_contract": "propose_fix",
      "max_steps": 20,
      "_doc": "第一次接管只出修复方案，不能动手"
    },
    "second_round": {
      "mode": "full_takeover",
      "inherit_tools_from": "<specialist_kind>",
      "require_human_ack": false,
      "max_steps": 40,
      "_doc": "第一轮方案未解决后升级为全面接管，继承原专精大脑全部工具权限"
    },
    "third_round": {
      "mode": "reject_and_report",
      "_doc": "两轮 fault_brain 都失败后，放弃该子任务，写入 rejected 状态"
    }
  },

  "audit_visibility": {
    "include_trace_tail_lines": 50,
    "include_full_artifacts": true,
    "redact_secrets": true,
    "_doc": "控制 GetProjectEvidenceSnapshot 对审核员暴露的证据粒度"
  }
}
```

**Kernel 层只负责**：
- 解析和校验 JSON（schema 检查 + 默认值填充）
- 把解析后的结构作为 `FaultPolicy` 对象传给中央大脑
- 在 §3.6 状态机转换时检查相关阈值（比如 `verifier_reject_threshold` 是否超限）

**Kernel 不做**：
- 不决定"什么时候升级"——这是中央大脑的决策
- 不硬编码任何阈值
- 不根据阈值变化重启 Run

**配置热加载**：CLI 模式下 `easymvp-exec run --fault-policy ./my.json` 可覆盖；Atlas 模式下写入 `mvp_config` 表的 `fault_policy_json` 键，中央大脑每次启动新 Run 时重读。

---

## 7. ArtifactStore 接口与双轨实现

> **下级规格**：ArtifactStore 的字节级实现（CAS 键算法 `sha256/<hex>`、双轨存储布局、去重规则、垃圾回收）见 [26-持久化与恢复.md §6 CAS 内容寻址存储](./26-持久化与恢复.md)。本节只定义 Go 接口契约，实现者必须以 26 的规格为准。

### 7.1 接口

```go
// workflow/executor/kernel/artifact.go

type ArtifactStore interface {
    // Put 上传一个 artifact，返回引用
    // runID 用于分桶；artifact 包含 kind/content/caption
    Put(ctx context.Context, runID int64, artifact Artifact) (ArtifactRef, error)

    // Get 按引用读取（用于审计重放、CLI 查看）
    Get(ctx context.Context, ref ArtifactRef) (io.ReadCloser, error)

    // List 列出某个 run 的所有 artifact
    List(ctx context.Context, runID int64) ([]ArtifactRef, error)

    // Delete 删除（用于清理、GDPR）
    Delete(ctx context.Context, ref ArtifactRef) error
}

type Artifact struct {
    Kind     ArtifactKind  // screenshot / video / dom_snapshot / frame_seq / trace / tool_call_log / diff_ref / log
    MimeType string
    Content  io.Reader     // 二进制内容
    Caption  string        // 人类可读的说明
    Step     int           // 在 Agent Loop 的哪一步产生
    Metadata map[string]any
}

type ArtifactRef struct {
    ID        int64         // Atlas 模式：execution_artifact.id；CLI 模式：递增序号
    RunID     int64
    Kind      ArtifactKind
    MimeType  string
    URL       string        // Atlas 模式：OSS URL；CLI 模式：file:///.../ 路径
    Caption   string
    Step      int
    CreatedAt time.Time
}
```

### 7.2 双轨实现

| 模式 | 实现 | 存储 |
|---|---|---|
| **Atlas** | `DBArtifactStore` | MinIO/OSS 存二进制 + `execution_artifact` 表存元数据 |
| **CLI** | `LocalArtifactStore` | `~/.easymvp-exec/runs/<run_id>/artifacts/` 目录 + `metadata.json` |

两个实现都位于 `workflow/executor/kernel/artifact/` 子包，通过 Kernel 启动时的 `Bootstrap` 注入。

**DBArtifactStore 的实现要点**：
- 上传走 MinIO 的 `PutObject`，URL 格式 `s3://<bucket>/<run_id>/<artifact_id>_<kind>.bin`
- 元数据同时写 `execution_artifact` 表，字段见 `docs/下一代AI协作开发平台方案.md` §10.2
- 上传和写表**必须事务化**：上传成功但写表失败 → 删对象；写表成功但上传失败 → 不可能发生（上传在前）

**LocalArtifactStore 的实现要点**：
- 目录结构 `~/.easymvp-exec/runs/<run_id>/artifacts/<kind>_<seq>.bin`
- 元数据写 `~/.easymvp-exec/runs/<run_id>/artifacts/metadata.jsonl`（每行一条 JSON）
- 单机并发安全靠文件锁（`flock`）

---

## 8. PlanStore 接口与双轨实现

> **下级规格**：PlanStore 的双轨持久化布局（SQLite WAL solo / MySQL cluster）、BrainPlan 增量写入协议、Run Resume 在 Turn 边界的检查点机制、幂等写入规则见 [26-持久化与恢复.md §4 / §5 / §7 / §8](./26-持久化与恢复.md)。本节只定义 Go 接口契约与并发语义，实现者必须以 26 的规格为准。

### 8.1 接口

```go
// workflow/executor/kernel/plan.go

type PlanStore interface {
    // Create 创建新计划
    // 由 central_brain 的 plan_store.create 工具间接调用
    Create(ctx context.Context, plan *BrainPlan) (int64, error)

    // Update 更新计划（包括子任务状态）
    // 由 central_brain 的 plan_store.update 工具间接调用
    // Kernel 在此处拿行锁，保证并发安全
    Update(ctx context.Context, planID int64, mutator func(*BrainPlan) error) error

    // Get 读取整份计划
    Get(ctx context.Context, planID int64) (*BrainPlan, error)

    // GetByTaskID 按 TaskID 查（一个 DomainTask 最多一份 active 计划）
    GetByTaskID(ctx context.Context, taskID int64) (*BrainPlan, error)

    // List 列出某个 workflow_run 下的所有计划
    List(ctx context.Context, workflowRunID int64) ([]*BrainPlan, error)
}

type BrainPlan struct {
    ID                  int64
    TaskID              int64
    WorkflowRunID       int64
    Goal                string
    AcceptanceCriteria  []string
    Status              PlanStatus
    CurrentRound        int
    CentralBrainModel   string
    Subtasks            []Subtask
    RejectedInfo        *RejectedInfo
    StartedAt           time.Time
    FinishedAt          *time.Time
    CreatedBy           int64
    DeptID              int64
}

type Subtask struct {
    ID           int64
    PlanID       int64
    SubtaskCode  string  // 中央大脑起的本地 ID，如 "s1" / "s2"
    OrderIndex   int
    BrainKind    BrainKind
    Spec         string  // 给专精大脑的任务描述
    Context      map[string]any
    Guardrail    *GuardrailConfig
    Status       SubtaskStatus
    DependsOn    []string  // 依赖的其他 subtask_code
    Attempts     int
    MaxAttempts  int
    EvidenceRefs []ArtifactRef

    // Report 专精大脑的结构化汇报（见 §3.5 SpecialistReport）
    // 从 nil（pending）→ 首次填充（reported）→ 不可变
    // 后续的 clarification 追加到 Clarifications 列表而不覆盖 Report
    Report *SpecialistReport

    // VerificationStatus 验收状态机当前状态（见 §3.6）
    VerificationStatus VerificationStatus

    // VerificationHistory 验收状态转换历史（供审计与重放）
    VerificationHistory []VerificationTransition

    // Clarifications request_clarification 回问的问答记录
    Clarifications []ClarificationRound

    // VerifierReports verifier_brain 的复核报告（可能多轮）
    VerifierReports []VerifierReport

    // FaultHandoff 如果升级到 fault_brain，这里记录接管详情
    FaultHandoff *FaultHandoffRecord

    ReportText   string   // 兼容字段：Report.Summary 或 Summary 的降级展示
    ErrorCode    string
    ErrorMessage string
    StartedAt    *time.Time
    FinishedAt   *time.Time
}

type VerificationTransition struct {
    FromStatus VerificationStatus
    ToStatus   VerificationStatus
    Actor      string  // "central_brain" / "verifier_brain" / "fault_brain" / "human"
    Reason     string
    At         time.Time
}

type ClarificationRound struct {
    Round    int
    AskedAt  time.Time
    Question string
    Answer   string
    AnsweredAt time.Time
    // 被约束重启的专精大脑 Run ID
    SpecialistRerunID int64
}

type VerifierReport struct {
    Round         int
    VerifierRunID int64
    Mode          string  // "quick" / "deep"
    Verdict       string  // "pass" / "fail" / "need_clarify"
    Reasons       []string
    Evidence      []ArtifactRef
    At            time.Time
}

type FaultHandoffRecord struct {
    TriggerReason   string
    Rounds          []FaultRoundRecord
    FinalVerdict    string  // "resolved" / "rejected"
}

type FaultRoundRecord struct {
    Round        int     // 1 = advisory, 2 = full_takeover, 3 = reject_and_report
    Mode         string
    FaultRunID   int64
    ProposedFix  string  // 第一轮的修复方案
    AppliedFix   bool    // 第二轮是否真的动手
    Outcome      string
    At           time.Time
}

type PlanStatus string
const (
    PlanStatusPlanning  PlanStatus = "planning"
    PlanStatusRunning   PlanStatus = "running"
    PlanStatusCompleted PlanStatus = "completed"
    PlanStatusFailed    PlanStatus = "failed"
    PlanStatusRejected  PlanStatus = "rejected"
)

type SubtaskStatus string
const (
    SubtaskStatusPending     SubtaskStatus = "pending"
    SubtaskStatusInProgress  SubtaskStatus = "in_progress"
    SubtaskStatusDone        SubtaskStatus = "done"
    SubtaskStatusFailed      SubtaskStatus = "failed"
    SubtaskStatusSkipped     SubtaskStatus = "skipped"
)
```

**详细的存储格式、并发安全、一致性约束**见 `06-BrainPlan持久化协议.md`。这里只列 Kernel 侧的接口。

### 8.2 并发安全关键点

- `Update` 方法内部**必须**拿行锁（MySQL 下用 `SELECT ... FOR UPDATE`，CLI 下用 `flock`）
- Update 是 read-modify-write 的原子操作，不允许外部直接 UPDATE execution_plan 表
- 中央大脑**从不直接写 PlanStore**——它只能通过 `plan_store.update` 工具间接触发，该工具在实现里调 `planStore.Update`，这样 Kernel 可以在调用前后插入审计和校验

### 8.3 证据聚合与审核员快照

对标 EasyMVP `admin-go/app/mvp/internal/engine/context_compressor.go` 的 token 压缩思想。PlanStore 需要提供一个"把整个 run 的事实浓缩成审核员能一屏看完"的接口：

```go
type PlanStore interface {
    // ... 前面的方法 ...

    // GetProjectEvidenceSnapshot 返回某个 workflow_run 下的全量证据快照
    // 消费者：
    //   1. EasyMVP 审核员 UI（直接渲染）
    //   2. auditor 角色的 ChatStream 上下文（作为 system 段注入）
    //   3. CLI 的 `easymvp-exec evidence show <run_id>` 命令
    //   4. 监管/合规导出
    GetProjectEvidenceSnapshot(
        ctx context.Context,
        workflowRunID int64,
        opts SnapshotOptions,
    ) (*ProjectEvidenceSnapshot, error)
}

type SnapshotOptions struct {
    IncludeTraceTail    bool  // 每个子任务附带最后 N 条 ToolCall
    TraceTailLines      int   // 由 fault_policy.audit_visibility.include_trace_tail_lines 决定
    IncludeFullArtifacts bool // 是否内联 artifact 内容（大文件用指针）
    RedactSecrets       bool  // 对 env/token/key 做脱敏
    MaxTokens           int   // 压缩目标 token 上限（0=不压缩）
    ForRole             string // "auditor" / "human_reviewer" / "compliance" / "central_brain"
}

type ProjectEvidenceSnapshot struct {
    WorkflowRunID int64
    ProjectID     int64
    GeneratedAt   time.Time

    // TaskSummary 顶层任务一句话摘要
    TaskSummary string

    // Plans 所有 BrainPlan 的扁平列表（一个 run 可能有多轮重规划）
    Plans []PlanEvidence

    // AggregateCost 全局 token/美元消耗
    AggregateCost TokenUsage
    AggregateUSD  float64

    // RiskHotspots 整个项目中被标为 high 风险或 verifier 驳回过的子任务
    // 审核员的注意力应集中于此
    RiskHotspots []SubtaskEvidence

    // FaultEscalations 升级到 fault_brain 的子任务清单
    FaultEscalations []SubtaskEvidence

    // ClarificationRounds 发生过 request_clarification 的子任务清单
    ClarificationRounds []SubtaskEvidence
}

type PlanEvidence struct {
    PlanID       int64
    Round        int      // 第几轮重规划
    Goal         string
    Status       PlanStatus
    RejectedInfo *RejectedInfo
    Subtasks     []SubtaskEvidence
}

type SubtaskEvidence struct {
    SubtaskID   int64
    SubtaskCode string
    BrainKind   BrainKind
    Spec        string
    Report      *SpecialistReport       // 完整 Report
    Verdicts    []VerifierReport        // 所有 verifier_brain 的裁定
    Clarifications []ClarificationRound
    Fault       *FaultHandoffRecord
    TraceTail   []ToolCallRecord        // 最后 N 条（可选）
    TokensUsed  TokenUsage
    CostUSD     float64
    Status      VerificationStatus
}
```

**压缩策略**（当 `MaxTokens > 0` 时）：

1. **优先保留**：所有 VerifRejected / VerifEscalated 的子任务完整 Report + Verdicts
2. **其次保留**：所有 RiskLevel=high 子任务的 Report（但 Actions 可裁剪到前 10 条）
3. **摘要化**：low 风险且 accepted 的子任务只保留 Report.Outcome + CompletedGoals + Artifacts 指针
4. **指针化**：artifact 内容 > 4KB 时仅存 ArtifactRef，UI 懒加载
5. **全局去重**：同一 resource 被多个子任务触达只显示一次 + 触达次数

**为什么不直接用 context_compressor.go**：
- `context_compressor.go` 输入输出都是自然语言，没有结构
- 这里的消费者包括结构化查询（审核员 UI 想按 risk_level 过滤）和 LLM，必须结构化
- Atlas 落地后可以把 EasyMVP 的 `context_compressor` 改成调用这个 Snapshot，完成两套压缩逻辑的合并

**双轨实现**：
- Atlas 模式：`DBPlanStore.GetProjectEvidenceSnapshot` 一次性 JOIN `execution_plan / execution_subtask / execution_artifact` + 内存聚合
- CLI 模式：`LocalPlanStore` 扫描 `~/.easymvp-exec/runs/<run_id>/` 目录下所有 plan.json + report.json 聚合

**权限与脱敏**：
- `RedactSecrets=true` 时，Kernel 扫描所有 string 字段，匹配到 `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `^sk-` / `^pk_` 等模式直接替换为 `[REDACTED]`
- Atlas 模式下 Snapshot 的查询必须走 EasyMVP 铁律 §13 的 DataScope（五级数据权限），CLI 模式无此要求

---

## 9. BrainRegistry 接口与双轨实现

Kernel 需要知道"当前有哪些大脑可以调用"。这是 `BrainRegistry` 的职责。

```go
// workflow/executor/kernel/registry_brain.go

type BrainRegistry interface {
    // Register 注册一个大脑（Atlas 启动时或 CLI init 时）
    Register(ctx context.Context, desc BrainDescriptor) error

    // Get 按 kind + code 获取大脑
    Get(ctx context.Context, kind BrainKind, code string) (BrainAgent, error)

    // GetDefault 获取某个 kind 的默认大脑
    GetDefault(ctx context.Context, kind BrainKind) (BrainAgent, error)

    // List 列出所有已注册的大脑
    List(ctx context.Context) ([]BrainDescriptor, error)

    // HealthCheck 检查某个大脑是否可用
    HealthCheck(ctx context.Context, kind BrainKind, code string) error
}

type BrainDescriptor struct {
    Kind              BrainKind
    Code              string       // 同 kind 下唯一，例如 "code.default" / "code.legacy_aider"
    Version           string
    LLMConfig         LLMConfig    // 默认配置，可被任务级 override
    DefaultGuardrail  GuardrailConfig
    Capabilities      []string     // ["write_file", "run_tests", "git_diff"]
    SystemPromptRef   string       // 指向三维预设库的引用
    IsDefault         bool
    CreatedAt         time.Time

    // Transport 决定该大脑用哪种 BrainRunner 运行（见 §12.5）
    // v2：所有大脑（包括内置的 central/verifier/fault）统一走 subprocess
    // v3（Atlas 企业版）：新增 rpc
    Transport         BrainTransport

    // SubprocessSpec 当 Transport = subprocess 时的 sidecar 启动配置
    // v2 所有大脑必填
    SubprocessSpec    *SubprocessSpec

    // RPCSpec 仅当 Transport = rpc 时生效（v3 Atlas 企业版扩展点）
    RPCSpec           *RPCSpec

    // LLMAccess sidecar 的 LLM 访问模式（见 §12.5.7）
    // 默认 proxied：sidecar 通过反向 RPC 让主进程调 LLM
    LLMAccess         LLMAccessMode
}

type BrainTransport string

const (
    TransportSubprocess BrainTransport = "subprocess" // v2 唯一支持的方式
    TransportRPC        BrainTransport = "rpc"        // v3 Atlas 企业版扩展点
)

type SubprocessSpec struct {
    BinaryPath       string        // sidecar 二进制的绝对路径
    Args             []string      // 启动参数
    Env              []string      // 额外环境变量（API key 通过 initialize 参数下发，不放这里）
    WorkingDir       string        // 子进程工作目录
    ProtocolVersion  string        // stdio 协议版本，默认 "v1"
    StartTimeout     time.Duration // 握手超时（默认 10s）
    ShutdownGrace    time.Duration // 优雅停机等待（默认 5s）
    IdleTimeout      time.Duration // 空闲多久后自动停子进程（默认 300s，0=常驻）
    MaxConcurrentRun int           // 单个 sidecar 实例最大并发 Run 数（默认 4）
    PoolSize         int           // 该 kind 的 sidecar 进程池大小（默认 2，按并发需求调）
    RestartPolicy    RestartPolicy
}

type RestartPolicy struct {
    MaxRestarts        int           // 熔断前的最大重启次数（默认 3）
    RestartWindow      time.Duration // 重启计数的窗口（默认 5min）
    BackoffInitial     time.Duration // 首次重启延迟（默认 1s）
    BackoffMax         time.Duration // 最大重启延迟（默认 30s）
    CircuitBreakHours  int           // 熔断后禁用小时数（默认 1）
}

type RPCSpec struct {
    Endpoint  string        // gRPC 地址
    AuthToken string        // 认证令牌
    TLS       *TLSConfig
}

type LLMAccessMode string

const (
    // LLMAccessProxied 默认：sidecar 通过 llm.complete 反向 RPC 让主进程调 LLM
    // 优势：Guardrail/Cost/Trace 集中在主进程；sidecar 零 LLM 客户端代码；
    //       主进程换 provider 不影响任何 sidecar
    LLMAccessProxied LLMAccessMode = "proxied"

    // LLMAccessDirect 允许 sidecar 自己持有 API key 直连 LLM provider
    // 优势：流式延迟最低；sidecar 可使用主进程未集成的 provider
    // 代价：sidecar 必须仍然通过 trace.emit 上报 Cost/Trace 事件，否则主进程熔断
    // 启用条件：BrainDescriptor 显式配置 + 主进程通过 initialize 下发 API key
    LLMAccessDirect LLMAccessMode = "direct"

    // LLMAccessHybrid 默认代理 + 按需申请直连
    // sidecar 调 llm.requestDirectAccess 申请一段时间的直连窗口
    // 典型场景：某个大脑大多数时候用内置 provider，偶尔需要调用自定义本地模型
    LLMAccessHybrid LLMAccessMode = "hybrid"
)
```

**Atlas 模式**：`DBBrainRegistry` 从 `brain_registry` 表读（取代老的 `ai_engine_config` 表）
**CLI 模式**：`YAMLBrainRegistry` 从 `~/.easymvp-exec/brains.yaml` 读

### 9.1 内置 BrainKind 常量（对应决策 2）

**对应决策 2**：`BrainVerifier` 和 `BrainFault` 作为**顶级 BrainKind 常量**列出来，和 `BrainCode`、`BrainBrowser` 等实现类大脑并列，这是验证/兜底大脑"独立"这一决策在类型系统层面的固化——它们不是 central_brain 的子方法，也不是 specialist 的子模式，而是独立注册、独立启动、独立 sidecar、独立 system prompt、独立 LLM 配置的**一等公民大脑**。

```go
type BrainKind string

const (
    // 编排层
    BrainCentral  BrainKind = "central_brain"  // 唯一的中央大脑

    // 业务专精（可按需扩展）
    BrainCode     BrainKind = "code_brain"
    BrainBrowser  BrainKind = "browser_brain"
    BrainMobile   BrainKind = "mobile_brain"
    BrainGame     BrainKind = "game_brain"
    BrainData     BrainKind = "data_brain"     // 数据分析/SQL
    BrainAPI      BrainKind = "api_brain"      // 第三方 API 编排

    // 验收与兜底（Kernel 必须预置的内置大脑）
    BrainVerifier BrainKind = "verifier_brain" // 只读验收，不生产
    BrainFault    BrainKind = "fault_brain"    // 故障接管，分两轮权限
)
```

**verifier_brain / fault_brain 的特殊性**：

| 维度 | verifier_brain | fault_brain |
|------|----------------|-------------|
| 触发者 | 中央大脑（按 §3.6 分级规则自动触发） | 中央大脑（按 §6.5 fault_policy 阈值自动触发） |
| 工具集 | 只读工具（read_file / git_diff / browser_assert / adb_inspect / artifact_get / trace_query） | **两段式**：第一轮仅 advisory 只读，第二轮继承原专精大脑全部工具 |
| system prompt | "你是验收员，只判定不生产"；输出 verdict + reasons + evidence | "你是故障分析师，先出方案后接管"；第一轮输出 propose_fix，第二轮执行修复 |
| 能否修改 BrainPlan | 不能 | 不能（只能向中央大脑汇报） |
| 能否触发 fault_brain | 不能（避免递归） | 不能（不自调） |
| 输入上下文 | 原子任务 Spec + 原专精大脑 Report + 全部 Artifact | 全部历史 Report + verifier 驳回理由 + 全部 Trace |
| 必须产出 | VerifierReport（通过 Report 工具变体） | FaultRoundRecord（通过 fault_report 工具） |

**Kernel 必须在启动时预注册这两种内置大脑**——即使它们没有对应的数据库配置行，也要用内置 system prompt 兜底，保证验收链路永远可用。verifier_brain 和 fault_brain 的具体 system prompt 和工具清单见 04-SpecialistBrain设计文档的"内置大脑"章节。

---

## 10. Cost Meter 成本记账

### 10.1 接口

```go
// workflow/executor/kernel/cost.go

type CostMeter interface {
    BeginRun(runID int64, brain BrainKind, cfg LLMConfig)
    AddLLMUsage(runID int64, usage TokenUsage, step int)
    AddArtifactUpload(runID int64, bytes int64)
    EndRun(runID int64) RunCostSummary

    // Query 按条件汇总
    Query(ctx context.Context, filter CostFilter) (*CostReport, error)
}

type RunCostSummary struct {
    RunID          int64
    Brain          BrainKind
    Provider       string
    Model          string
    PromptTokens   int
    CompletionTokens int
    CachedTokens   int
    TotalTokens    int
    USD            float64
    Duration       time.Duration
}

type CostFilter struct {
    WorkflowRunID int64
    TaskID        int64
    Brain         *BrainKind
    Provider      string
    Model         string
    Since         *time.Time
    Until         *time.Time
}
```

### 10.2 计价表

Kernel 持有一份 `pricing.yaml`（可热更新），按 `provider + model` 查单价：

```yaml
# config/pricing.yaml
anthropic:
  claude-opus-4-6:
    input_usd_per_mtok: 15.00
    output_usd_per_mtok: 75.00
    cache_read_usd_per_mtok: 1.50
  claude-sonnet-4-6:
    input_usd_per_mtok: 3.00
    output_usd_per_mtok: 15.00
    cache_read_usd_per_mtok: 0.30
  claude-haiku-4-5-20251001:
    input_usd_per_mtok: 1.00
    output_usd_per_mtok: 5.00

openai:
  gpt-5:
    input_usd_per_mtok: 10.00
    output_usd_per_mtok: 30.00
  gpt-4.1:
    input_usd_per_mtok: 2.00
    output_usd_per_mtok: 8.00
  o3:
    input_usd_per_mtok: 60.00
    output_usd_per_mtok: 240.00
    reasoning_usd_per_mtok: 240.00
```

**注意**：价格数字仅为示意，上线时以供应商官方定价为准。`workflow/executor/kernel/cost/pricing_loader.go` 在启动时加载，可通过 `POST /internal/cost/reload-pricing` 热更。

### 10.3 三维记账

每条记账带三个维度：
- **Provider**：anthropic / openai / ...
- **Model**：具体模型 ID
- **Brain**：central / code / browser / ...

这样可以查到类似这样的报表：

```
2026-04-11 成本报表:
  CentralBrain (claude-opus-4-6):     $12.34  (84 runs, 412k tokens)
  CodeBrain    (claude-sonnet-4-6):    $8.76  (512 runs, 2.8M tokens)
  BrowserBrain (claude-sonnet-4-6):   $15.20  (156 runs, 5.1M tokens incl vision)
  Total:                               $36.30
```

---

## 11. Trace / Audit Logger

> **下级规格**：
> - **可观测性导出**（OTel Resource 属性、metric 命名规范、基数预算、告警矩阵）见 [24-可观测性.md](./24-可观测性.md)。本节定义的 Trace 事件是 24 中 OTel span 的来源，两者必须一致。
> - **审计事件与安全**（hash chain 完整性、敏感字段脱敏、凭证访问审计）见 [23-安全模型.md §8 审计事件](./23-安全模型.md)。AuditEvent 的结构在 23 中冻结，本节只讨论写入路径。

### 11.1 Trace 数据结构

Kernel 在运行时产生两类 trace 事件：

```go
type LLMCallTrace struct {
    RunID       int64
    Brain       BrainKind
    Provider    string
    Model       string
    Step        int
    Request     ChatRequest   // 脱敏后
    Response    *ChatResponse // 脱敏后
    Usage       TokenUsage
    Latency     time.Duration
    Err         error
    Timestamp   time.Time
}

type ToolCallTrace struct {
    RunID      int64
    Brain      BrainKind
    Step       int
    ToolName   string
    ToolUseID  string
    Input      json.RawMessage
    Output     *ToolOutput
    Err        error
    Guardrail  *GuardrailDenialEvent  // 如果被拒
    Latency    time.Duration
    Timestamp  time.Time
}
```

### 11.2 输出目标（双轨）

| 模式 | 输出目标 |
|---|---|
| **Atlas** | `workflow_transition_log` 子类型 `llm_call` / `tool_call`，同时通过 OpenTelemetry exporter 送 Tempo |
| **CLI** | `~/.easymvp-exec/runs/<run_id>/traces.jsonl`（每行一条 JSON） |

### 11.3 脱敏规则

Kernel 在写 trace 前对 Request/Response 做脱敏：
- API Key 类字段（`Authorization` header / `x-api-key`）**永远不落地**
- Messages 内容长度 > 10KB 的截断到 10KB + 标记 `truncated=true`
- 图片 Vision 内容**只写 URL，不写 base64**（base64 在 ArtifactStore 里有完整版）
- 工具输入的 `content` / `text` / `script` 字段长度 > 4KB 截断

---

## 12. 并发模型与生命周期

### 12.1 一次 Run 的完整生命周期

```
Atlas mode:
  Orchestrator 决定跑一个任务
     ↓
  构造 Request（含 RunID、Workspace、Guardrail、AllowedBrains）
     ↓
  获取 CentralBrain （从 BrainRegistry）
     ↓
  kernel.Run(ctx, centralBrain, req)
     │
     ├─ Agent Loop 开始
     │   │
     │   ├─ 中央大脑第一轮 LLM 调用 → 返回 tool_use: plan_store.create
     │   ├─ 执行工具 → 写 execution_plan + execution_subtask 表
     │   ├─ 第二轮 LLM → 返回 tool_use: code_brain(s1), browser_brain(s2) [并发]
     │   │   │
     │   │   ├─ 子 Run 1: kernel.Run(ctx, codeBrain, subReq1)
     │   │   │     └─ 自己的 Agent Loop → 完成 → 返回 Result
     │   │   │
     │   │   └─ 子 Run 2: kernel.Run(ctx, browserBrain, subReq2)
     │   │         └─ 自己的 Agent Loop → 完成 → 返回 Result
     │   │
     │   │  两个 subRun 完成后合并为 ToolResult 回注
     │   │
     │   ├─ 第三轮 LLM → plan_store.update (标记 s1/s2 done)
     │   ├─ 第四轮 LLM → plan_store.update + 返回 stop
     │   └─ centralBrain.OnStop → Result{status: success}
     │
     └─ 返回 Result
     ↓
  Orchestrator 读取 Result，落库到 DomainTask，触发下一阶段
```

### 12.2 并发模型

- **单个 Run 内**：Agent Loop 是**时序的**（一步接一步调 LLM），但**一步内的多个 tool_call 是并发的**（goroutine pool，Sem 限流）
- **多个 Run 之间**：完全并行。Atlas 可以同时跑 N 个任务，每个任务都跑自己的 kernel.Run
- **父子 Run 嵌套**：中央大脑的 tool_call = 调用一个子 Run（跑专精大脑）。子 Run 是**同步调用**，中央大脑这一步的 Agent Loop 会等所有子 Run 完成再继续下一步

### 12.3 父子 Run 的资源隔离

子 Run 继承父 Run 的：
- Workspace（同一个 worktree/browser context/device）
- Guardrail（不能放开，只能进一步收紧）
- CreatedBy / DeptID（权限链路）
- ParentRunID（用于 trace 追踪）

子 Run 不继承父 Run 的：
- LLMConfig（专精大脑有自己的 LLM，和中央大脑可能不同）
- Messages 历史（子 Run 从 InitialMessages 重新开始）
- Budget（独立记账，但累计到父 Run 的 totalCost）

### 12.4 context.Context 的传递

- 父 Run 的 ctx 被子 Run 继承——父 Run 取消时子 Run 同时取消
- 每个子 Run 可以有**自己的超时**（`context.WithTimeout`）——Kernel 在启动子 Run 前检查父 ctx 的 deadline，如果父 deadline 比 LLMConfig.Budget.MaxDuration 短，用父 deadline
- LLM 调用的 timeout 由 Kernel 的 HTTP client 控制，默认 90s 一次 API 调用

> **下级规格**：
> - **CLI 稳定接口**（`brain run` / `brain status` / `brain resume` / `brain serve` 等 13 个子命令、退出码、输出格式、信号处理、向后兼容策略）见 [27-CLI命令契约.md](./27-CLI命令契约.md)。Kernel 在 solo 模式下由 CLI 嵌入式启动，在 cluster 模式下由 `brain serve` 启动——本节定义的 BrainRunner 抽象是两种模式的共同内核。
> - **SDK 交付合规**（三级兼容性声明、三段式版本号、150 条合规测试、发布流程、安全披露）见 [28-SDK交付规范.md](./28-SDK交付规范.md)。第三方语言实现 Kernel 时必须满足 28 的交付清单才能声称兼容 v1。

### 12.5 BrainRunner 抽象与全 sidecar 架构（对应决策 5 / 6 / 7 / 8）

这一节回答两个根本性的架构问题：

1. **"大脑是以什么形式存在的？"** → §12.5.1 ~ §12.5.10（决策 5 / 6 / 7）
2. **"大脑的对外通信协议和 MCP 生态是什么关系？"** → §12.5.0（决策 8）

**答案 1（v2 铁律）**：**所有大脑都是独立的 sidecar 二进制，通过 stdio JSON-RPC 与主进程通信。** 包括 central_brain / verifier_brain / fault_brain 在内的全部内置大脑，以及 code_brain / browser_brain / mobile_brain / game_brain 等专精大脑，**没有例外**。主二进制 `easymvp-exec` 只承担 Kernel（Agent Loop 之外的部分）、Runner、工具执行、LLM 代理、持久化——**不含任何 BrainAgent 实现**。

**答案 2（战略决策）**：**BrainKernel 的 stdio JSON-RPC 协议保持自研，但同时提供 `MCPAdapterRunner` 让任意 MCP server 作为 tool 被 BrainAgent 消费。** 我们不 fork MCP，也不假装 MCP 不存在。详见 §12.5.0。

本节集中承载四条决策：
- **决策 5** · 连内置大脑也强制 sidecar（§12.5.1 / §12.5.2）
- **决策 6** · Runner 支持 cancel / health / shutdown（§12.5.3 / §12.5.8）
- **决策 7** · LLMAccess 三模式，默认代理（§12.5.7）
- **决策 8** · 自研协议 + MCP Adapter Runner（§12.5.0）

---

#### 12.5.0 协议对标声明：BrainKernel 与 Anthropic MCP 的关系（对应决策 8）

**背景**：Anthropic 在 2024 年末发布 Model Context Protocol（MCP），2025 年 Claude Code / Cursor / Windsurf / Continue / Zed 等主流 AI 编辑器陆续采纳，MCP 生态里已有 500+ 开源 server（filesystem / git / github / slack / postgres / puppeteer / brave-search / sequential-thinking / memory / everything / time / fetch ...）。MCP 定义了三个核心原语：
- **Tools**：可被 LLM 调用的函数（最接近 BrainKernel 的 `tool.invoke`）
- **Resources**：可被 LLM 读取的静态/动态内容（最接近 BrainKernel 的 `artifact.get`）
- **Prompts**：可被用户/LLM 触发的模板（BrainKernel 不直接对应）

MCP 的传输层是 stdio JSON-RPC 2.0（`Content-Length` 头部帧 + UTF-8 JSON body），和我们 §12.5.4 的协议在传输层 **骨架几乎同构**，但在**语义层**有三处本质差异：

##### 12.5.0.1 为什么不直接用 MCP（自研的必要性）

| 维度 | MCP 现状 | BrainKernel 需要什么 | 差距判断 |
|---|---|---|---|
| **双向 RPC 深度** | 主要是 client → server 单向调用；server → client 只有有限的 `sampling/createMessage`（让 client 代调 LLM）和 `roots/list` | sidecar 要反向调主进程的 **4 类** 服务：`llm.*` / `tool.*` / `plan.*` / `trace.*`，其中 `plan.update` / `subtask.delegate` / `artifact.put` 是 MCP 完全没有的业务概念 | **MCP 协议无法表达**，必须扩展或自研 |
| **业务语义原语** | Tool / Resource / Prompt 三原语是**通用工具调用**抽象 | BrainKernel 需要 BrainPlan / Subtask / SpecialistReport / VerificationStatus / FaultPolicy 这类**执行器家族专属**的一等原语 | MCP 是 general-purpose 协议，装不下领域语义 |
| **进度与流式** | `$/progress` notification 基础支持 | 需要支持 text delta / thinking delta / tool_use input delta 三类流式事件 + 跨进程 SSE 转发 | MCP 的 progress 太薄，需要我们自己定义 ChatEvent 契约 |
| **生命周期方法** | `initialize` / `ping` / `shutdown` 三件套 | 我们额外需要 `brain.cancel` / `health.ping`（带 active_runs/memory_rss） / `brain.reload` / `llm.rotateCredentials` | MCP 没有 run-level cancel 语义（MCP 面向 "tool call" 粒度） |
| **错误模型** | JSON-RPC 2.0 标准 `{code, message, data}` | 需要 `Retryable / Class / TraceID / Cause 链路 / Fingerprint` 等结构化字段 | MCP 的 error data 是自由字段，我们要定死 schema |
| **资源模型** | Resource 是 URI + MIME + 可读取 | Artifact 需要 upload session / sha256 校验 / presigned URL / GC / 跨进程共享 workspace 路径 | MCP 的 resource 只读，没有上传模型 |

**结论**：如果强行用 MCP 做超集，我们需要扩展 MCP 的程度相当于"在 MCP 之上再发明一个子协议"，最终大脑开发者既要学 MCP 又要学 BrainKernel 扩展，**认知成本反而更高**。而且 MCP 是 Anthropic 主导的开放协议，未来版本演进不由我们控制，在核心执行器协议上长期依赖外部路线图是战略风险。

##### 12.5.0.2 为什么不假装 MCP 不存在（生态兼容的必要性）

放弃 MCP 兼容意味着放弃 500+ 现成 MCP server 带来的生态杠杆。对照具体场景：

- **code_brain 想用 GitHub tools**：MCP 有官方 `@modelcontextprotocol/server-github`，实现了 search_code / create_issue / list_pull_requests 等 30+ 工具。如果不兼容 MCP，我们自己要从头写一个"code_brain 内置 github 工具集"。
- **browser_brain 想用 Puppeteer**：MCP 有 `@modelcontextprotocol/server-puppeteer`。
- **data_brain 想查 Postgres**：MCP 有 `@modelcontextprotocol/server-postgres`。
- **企业场景想接 Slack / Jira / Confluence**：MCP 生态都有。

**结论**：丢掉 MCP 生态意味着每个专精大脑都要自己重造工具库，工作量至少是 10 倍级别的浪费。

##### 12.5.0.3 方案 C 的落地：自研协议 + MCPAdapterRunner

我们采取"**协议自研，生态兼容**"的双层架构：

```
┌────────────────────────────────────────────────────────────────┐
│                  BrainKernel（主进程 easymvp-exec）             │
│                                                                │
│   ┌──────────────────────────────────────────────────────┐    │
│   │  BrainRunner 接口（决策 6）                           │    │
│   │    Transport() / Run() / Cancel() / Health()         │    │
│   │    Shutdown()                                         │    │
│   └────────────────┬─────────────────────────────────────┘    │
│                    │                                           │
│      ┌─────────────┼────────────────────┐                      │
│      │             │                     │                      │
│      ▼             ▼                     ▼                      │
│  ┌──────────┐ ┌──────────┐    ┌─────────────────────┐         │
│  │Subprocess│ │   RPC    │    │  MCPAdapterRunner   │         │
│  │ Runner   │ │  Runner  │    │  (NEW, 决策 8 落地)  │         │
│  │(v2 主力) │ │(v3 企业) │    │                     │         │
│  └────┬─────┘ └────┬─────┘    └──────────┬──────────┘         │
│       │            │                      │                    │
└───────┼────────────┼──────────────────────┼────────────────────┘
        │            │                      │
        │            │                      │ 按 MCP spec 说 stdio JSON-RPC
        │            │                      │（Content-Length 头部帧 + 标准方法名）
        │            │                      ▼
        │            │       ┌──────────────────────────────┐
        │            │       │  外部 MCP Server             │
        │            │       │  - server-github             │
        │            │       │  - server-puppeteer          │
        │            │       │  - server-postgres           │
        │            │       │  - server-slack              │
        │            │       │  - 任意第三方 MCP server      │
        │            │       └──────────────────────────────┘
        │            │
        │            │ 按 BrainKernel 自研协议 stdio JSON-RPC
        │            │（自研帧 + 14 方法 + 反向 RPC）
        ▼            ▼
┌──────────────────────────────────────┐
│  BrainKernel Sidecar（我们写的大脑）  │
│  - easymvp-central-brain             │
│  - easymvp-verifier-brain            │
│  - easymvp-fault-brain               │
│  - easymvp-code-brain                │
│  - easymvp-browser-brain             │
│  - easymvp-mobile-brain              │
│  - easymvp-game-brain                │
│  - 第三方 brain-sdk-go 写的大脑       │
└──────────────────────────────────────┘
```

**关键点**：
- 主进程同时支持**两个 Runner 家族**：`SubprocessRunner`（跑我们自研协议的大脑）和 `MCPAdapterRunner`（跑标准 MCP server）
- 它们共享 `BrainRunner` 接口，对 Kernel 内部完全透明
- MCP server **不是** 被 Kernel 当成 BrainAgent 来 Run，而是被注册为某个大脑可调用的 **tool 集合**（见下文）

##### 12.5.0.4 MCPAdapterRunner 的语义边界

MCP server 不是大脑。这是一条严格的边界：

| 能力 | BrainKernel Sidecar | MCP Server |
|---|---|---|
| 跑 Agent Loop（调 LLM、解析 tool_call、循环推进） | ✅ 是它的本职 | ❌ 完全不会 |
| 实现 BrainAgent 接口（SystemPrompt / RegisterTools / InitialMessages） | ✅ 是 | ❌ 不是 |
| 接收 `brain.run` 请求跑完整子任务 | ✅ 是 | ❌ 不是 |
| 暴露一组 Tool 供 LLM 调用 | ✅ 是 | ✅ **是，这是它唯一的能力** |
| 反向调主进程 plan/trace/llm | ✅ 是 | ❌ 不是 |

**所以 MCPAdapterRunner 实际上不是 Runner 而是 ToolProvider**。把它塞进 Runner 接口只是实现上的复用——我们让 ToolRegistry 在启动时调 `MCPAdapterRunner.DiscoverTools(mcpServerSpec)`，把 MCP server 的 `tools/list` 响应转换成 `ToolSchema[]` 注册到 ToolRegistry。每次某个大脑的 LLM 发出针对这些工具的 tool_use 时，Kernel 把调用通过 MCP 协议转发到对应 MCP server，拿到 result 再回注给大脑。

为了避免命名混淆，§15 的包结构里会把它命名为：

```go
// workflow/executor/kernel/mcpadapter/
//   adapter.go          // MCPServerAdapter 类型
//   discover.go         // 启动时拉取 tools/list, resources/list
//   invoke.go           // tool_use → mcp tools/call 的桥接
//   frame.go            // MCP Content-Length 帧编解码（和我们自研协议帧不同）
//   registry.go         // MCP server 注册表
```

##### 12.5.0.5 配置示例

`~/.easymvp-exec/brains.yaml` 里大脑和 MCP server 混合声明：

```yaml
brains:
  # 我们自研的 sidecar 大脑
  - kind: code_brain
    code: default
    transport: subprocess
    subprocess:
      binary: /opt/easymvp/bin/easymvp-code-brain
      protocol_version: v1
      pool_size: 2

  - kind: browser_brain
    code: default
    transport: subprocess
    subprocess:
      binary: /opt/easymvp/bin/easymvp-browser-brain
      protocol_version: v1

mcp_servers:
  # 外部 MCP server 作为 tool provider
  - name: github
    command: [npx, "-y", "@modelcontextprotocol/server-github"]
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: ${GITHUB_TOKEN}
    expose_to_brains: [code_brain, central_brain]  # 哪些大脑可以使用这些工具
    tool_prefix: "github."  # 注册到 ToolRegistry 时加前缀，避免冲突

  - name: postgres
    command: [docker, run, "--rm", "-i", mcp/postgres, "${DATABASE_URL}"]
    expose_to_brains: [data_brain]
    tool_prefix: "pg."

  - name: puppeteer
    command: [npx, "-y", "@modelcontextprotocol/server-puppeteer"]
    expose_to_brains: [browser_brain]
    tool_prefix: "pptr."
```

**关键字段**：
- `transport` 只用于我们自研大脑（subprocess / rpc），MCP server 不走这个字段
- `mcp_servers` 是独立顶级数组，明确与 `brains` 分家
- `expose_to_brains` 控制哪些大脑能看到这些工具（零信任默认：不显式暴露就不可见）
- `tool_prefix` 是必需的，防止不同 MCP server 的同名工具打架（例如 github 和 gitlab 都有 `list_repos`）

##### 12.5.0.6 Guardrail 对 MCP Tool 的处理

MCP server 是外部代码，对 MCP 工具的 Guardrail 必须**比自研工具更严格**：

1. **参数白名单**：MCP tool 的参数默认走 Guardrail 路径/域名/包名三层校验，和自研工具同等标准
2. **额外的命令校验**：对 `execute_command` / `run_script` 类 MCP 工具（例如某些 shell MCP server），Guardrail 自动注入命令黑名单（`rm -rf` / `curl | bash` / `dd` / `sudo`）
3. **超时强制**：MCP tool 的 invoke 默认 30s 超时，不可关闭
4. **结果过滤**：MCP tool 的 result 文本回注给 LLM 前必须过 `secret redactor`（对应技术审阅 G.1，与自研 tool_result 同样标准）
5. **速率限制**：默认每个 MCP server 每分钟 60 次 tool_call 上限，超限触发 `rate_limited` 并写审计

##### 12.5.0.7 哪些 MCP 原语我们暂不采纳

为了保持内核纯度，v2 里我们 **只采纳 MCP 的 Tool 原语**，不采纳：

- **MCP Resources**：我们有自己的 ArtifactStore + content_ref 模型，MCP Resource 的 URI 模型和我们的 artifact 元数据不兼容。v2.1 可考虑在 ArtifactStore 里加 `MCPResourceAdapter`，把 MCP resource 映射成只读 artifact。
- **MCP Prompts**：BrainKernel 的 system prompt 是大脑自己拼的（SystemPrompt 方法），不需要外部模板。
- **MCP Sampling**（反向 LLM 调用）：这条和我们的 `llm.complete` 功能重合但方向相反——MCP sampling 是 server 让 client 代调 LLM，而我们的 `llm.complete` 是 sidecar 让主进程代调。我们不用 MCP sampling，因为我们的大脑是 sidecar 而非 MCP server，MCP 的 sampling 协议不适配。

##### 12.5.0.8 反向兼容：让 BrainKernel sidecar 被 MCP client 消费？

问：能不能让我们的 sidecar 同时也是 MCP server，被 Claude Code / Cursor 直接消费？

**答：v2 不支持，v2.2 作为可选能力**。原因：

- 我们 sidecar 的核心能力是跑 Agent Loop（接收 `brain.run` → 独立跑到完成 → 产出 SpecialistReport）。MCP server 没有这个模型，MCP client 只会对它说 `tools/call` 要一次性结果。
- 强行让 sidecar 双重身份会让协议处理代码分叉，违反决策 5 的"零分叉"原则。
- 如果用户想把 code_brain 的能力暴露给 Claude Code，v2.2 可以提供一个独立二进制 `easymvp-mcp-bridge`，它作为 MCP server 接收 `tools/call`，内部去 spawn 一个 code_brain sidecar 跑一次性任务，把 SpecialistReport 转成 MCP tool result 返回。**这是 bridge 模式，不是双重身份**。

##### 12.5.0.9 决策 8 的落地清单

在 §15 Go 包结构里会新增以下条目（v2 第一批）：

```
workflow/executor/kernel/mcpadapter/
├── adapter.go           # MCPServerAdapter 类型（~250 行）
├── discover.go          # 启动时拉取 tools/list、resources/list（~180 行）
├── invoke.go            # tool_use → mcp tools/call 桥接（~200 行）
├── frame.go             # MCP Content-Length 帧编解码器（~150 行）
├── registry.go          # MCP server 实例注册表（~120 行）
├── guardrail.go         # MCP 专属 Guardrail 强化（~180 行）
├── redactor.go          # MCP result 脱敏（~100 行）
└── adapter_test.go      # 用 @modelcontextprotocol/server-everything 做集成测试
```

预计新增代码量约 **1200 行**（v2 首发），对应新增 PR：**PR-20 MCP Adapter 首发**（依赖 PR-13 到 PR-19 完成后启动）。

> **重要约定**：`mcpadapter` 包是**整个 kernel 里唯一允许出现 MCP 协议字段的地方**。主进程的 ToolRegistry 看到的永远是统一 `ToolSchema`，看不到 MCP 的 `tools/list` 结构；大脑的 LLM 看到的工具描述也完全一致。MCP 的所有协议细节都被 `mcpadapter` 吃掉，外部零感知。

---

#### 12.5.1 为什么全 sidecar（对应决策 5）

传统做法会让内置大脑走"进程内捷径"、第三方大脑走"stdio 长路"，结果是：
- 内置和第三方代码路径不一致，bug 在一边有一边没
- stdio 协议在内部用不到，演进停滞
- 第三方开发者永远是二等公民，生态起不来

**强制全 sidecar 的价值**：

1. **架构纯度极致**：Kernel 代码里**不存在**"如果是内置大脑"的分支。唯一的 Runner 是 `SubprocessRunner`，所有大脑共享同一条代码路径
2. **stdio 协议天天在用**：内置大脑就是 stdio 协议最好的 eating-own-dog-food，协议缺陷立刻暴露
3. **内置大脑就是最好的开发范例**：第三方开发者 clone `easymvp-code-brain` 的源码就能照猫画虎，Kernel 文档 = 开发者文档
4. **崩溃隔离天然具备**：任何大脑 panic 都不会波及主进程和其他大脑
5. **升级解耦**：Kernel 换 LLM provider 不需要重编大脑；大脑改 system prompt 不需要重编 Kernel
6. **跨语言可行**：Python/Rust/TypeScript 实现的大脑 day 1 就能接入

**承担的代价与缓解**：

| 代价 | 缓解方案 |
|------|---------|
| 子进程启动开销（10~200ms） | 进程池 + 常驻（IdleTimeout=300s，热门大脑常驻） |
| 跨进程调试复杂 | 跨进程 Trace ID + 结构化日志 + `easymvp-exec debug <run_id>` 命令 |
| stdio 吞吐上限（单进程单管道） | 单个 sidecar 支持 MaxConcurrentRun 并发（默认 4），不够用起 PoolSize 个副本 |
| LLM 调用延迟（反向 RPC 往返） | 默认 proxied；对延迟敏感的大脑可开 LLMAccessDirect 直连 |
| 每个大脑都要编译单独二进制 | `easymvp-brain-sdk` Go 模块 + 模板 + `easymvp-exec brain scaffold` 生成命令 |

#### 12.5.2 进程拓扑（对应决策 5）

```
        easymvp-exec (主进程 / Kernel)
        ┌─────────────────────────────────────┐
        │  • BrainRunner (SubprocessRunner)    │
        │  • ToolRegistry  +  Guardrail        │
        │  • LLM Provider Pool (Anthropic,     │
        │      OpenAI, DeepSeek, ...)          │
        │  • ArtifactStore / PlanStore         │
        │  • CostMeter  +  Tracer              │
        │  • 进程池管理 + 熔断器                 │
        └───────┬──────────────────────────────┘
                │ stdio (JSON-RPC 2.0)
                │ 每个 sidecar 独立 pipe
   ┌────────────┼────────────┬────────────┬────────────┐
   ▼            ▼            ▼            ▼            ▼
[central]  [verifier]   [fault]      [code]     [browser]
 _brain     _brain      _brain       _brain      _brain
 (内置)     (内置)       (内置)       (内置)      (内置)
                                       ▲
                                       │
                                 [python_brain]  (第三方，格式完全一致)
```

**关键观察**：图中所有方框的形态完全相同。Kernel 对待它们的代码路径一字不差。

#### 12.5.3 BrainRunner 接口（对应决策 6）

**对应决策 6**：BrainRunner 的方法集是决策 6 的直接落实——除了 `Run`，**必须**包含 `Cancel / Health / Shutdown` 三个方法。这是整个家族扩展性的支点：任何新增大脑（含第三方）只需在 sidecar 端实现协议响应即可被取消、被探活、被优雅停机，无需改 Kernel 一行代码。

```go
// workflow/executor/kernel/runner.go

// BrainRunner 定义"如何运行一个 BrainAgent"
// v2 唯一实现是 SubprocessRunner；v3 Atlas 企业版将加 RPCRunner
type BrainRunner interface {
    // Transport 返回该 Runner 支持的传输类型
    Transport() BrainTransport

    // Run 执行一次 Agent Loop
    // 对调用者而言语义一致：输入 Request，输出 Result
    // 具体怎么跑（子进程/远程）对调用者透明
    Run(ctx context.Context, desc BrainDescriptor, req *Request) (*Result, error)

    // Cancel 取消正在跑的 Run（对应 stdio brain.cancel）
    // sidecar 必须在 ShutdownGrace 内响应
    Cancel(ctx context.Context, runID int64) error

    // Health 返回当前 Runner 管理的所有 sidecar 实例的健康状态
    Health(ctx context.Context) ([]SidecarHealth, error)

    // Shutdown 优雅停机
    // 发送 shutdown 给所有 sidecar，等待它们结束，超时则 SIGKILL
    Shutdown(ctx context.Context) error
}

type SidecarHealth struct {
    BrainKind    BrainKind
    InstanceID   string
    PID          int
    Uptime       time.Duration
    ActiveRuns   int
    TotalRuns    int64
    RestartCount int
    MemoryRSS    int64
    LastError    string
    Status       string  // "running" / "idle" / "crashed" / "circuit_broken"
}
```

**Kernel 路由**（v2 只有一条路径）：

```go
func (k *Kernel) runAgent(ctx context.Context, kind BrainKind, req *Request) (*Result, error) {
    desc, err := k.brainRegistry.GetDefault(ctx, kind)
    if err != nil { return nil, err }

    // v2 唯一 Runner
    return k.subprocessRunner.Run(ctx, desc, req)
}
```

中央大脑通过 `code_brain()` 工具委派任务时，最终都会调到这一行。中央大脑自己也是 sidecar，它的委派请求通过反向 RPC（`subtask.delegate`）传回主进程，由主进程再起另一个 sidecar 跑对应的专精大脑。

#### 12.5.4 stdio 协议 v1（完整方法集）

> **📌 正式规格另见**：本节是方法集一览。**传输层（帧格式）、双向 RPC id 命名空间、生命周期状态机、心跳协议、背压与流控、错误帧编码**这些比特级细节放在独立 RFC 文档 [20-协议规格.md](./20-协议规格.md) 中，任何第三方大脑 SDK 实现者应以 20 号文档为准。本节只保留"有哪些方法、参数是什么、用来做什么"的业务级视图。

协议基于 **JSON-RPC 2.0**，承载在 sidecar 的 stdin/stdout 之上。帧格式为 **LSP 风格 Content-Length 头部帧**（非 NDJSON），每帧是 `Content-Length: <N>\r\n\r\n<N 字节 UTF-8 JSON body>`，单帧上限 **16 MiB**。主进程和 sidecar 都可主动发起请求（双向 RPC），通过 id 的 `k:` / `s:` 前缀区分命名空间避免撞车。stderr 是 sidecar 自己的日志（主进程收集作为调试信息，不参与协议）。

**完整规格、合规测试（C-01 ~ C-20）、BNF 文法、与 LSP/MCP/DAP 的对照表**：见 [20-协议规格.md](./20-协议规格.md)。以下各小节仅给出业务语义。

##### 12.5.4.1 协议设计原则

1. **双向 JSON-RPC**：主进程和 sidecar 都既是客户端又是服务端。请求/响应通过 `id` 字段匹配，`id` 为 null 的是通知（不需要响应）
2. **强类型方法空间**：方法名按 `namespace.verb` 分层，避免冲突
3. **请求超时靠主进程**：每个主→子请求主进程自己设超时（发送时带 `_timeout_ms` 参数），超时则认为大脑卡死，触发 Cancel
4. **取消传递**：主进程取消父 Run 时，会对所有相关 sidecar 发 `brain.cancel`；sidecar 必须取消自己正在进行的 LLM 调用并清理状态
5. **协议版本强绑**：`initialize` 时交换协议版本，不兼容直接拒绝加载 sidecar，不做隐式降级

##### 12.5.4.2 主进程 → sidecar 方法清单

| # | 方法 | 参数 | 返回 | 说明 |
|---|------|------|------|------|
| 1 | `initialize` | `{protocol_version, kernel_version, capabilities, llm_config, llm_credentials?, workspace_path, run_context}` | `{protocol_version, brain_version, brain_capabilities, supported_tools[]}` | 握手。`llm_credentials` 仅当 `LLMAccess=direct/hybrid` 时下发。sidecar 返回自己声明的工具清单（Kernel 用来校验 tool.invoke 是否合法） |
| 2 | `brain.describe` | `{}` | `{kind, version, system_prompt_hash, tools[], default_llm_config, features[]}` | 获取大脑元信息。Kernel 启动时扫描所有 sidecar 调用此方法构建 BrainRegistry。可离线调用（不需要 run_context） |
| 3 | `brain.run` | `{run_id, request: <Request JSON>}` | `{result: <Result JSON>}` | 启动一次 Agent Loop。run_id 由主进程分配。sidecar 返回 result 即 Run 结束 |
| 4 | `brain.cancel` | `{run_id, reason, grace_ms}` | `{cancelled: true, partial_result?}` | 取消正在跑的 Run。sidecar 必须在 grace_ms 内响应。响应 `partial_result` 允许 sidecar 返回已有的部分 SpecialistReport |
| 5 | `health.ping` | `{}` | `{uptime_seconds, active_runs, total_runs, memory_rss, last_error}` | 心跳。主进程每 30s 发一次。连续 3 次失败触发重启 |
| 6 | `shutdown` | `{grace_ms}` | `{ok: true}` | 优雅停机。sidecar 停止接收新 Run，等待所有进行中的 Run 结束，然后退出 0 |

##### 12.5.4.3 sidecar → 主进程方法清单（反向 RPC）

| # | 方法 | 参数 | 返回 | 谁可调 |
|---|------|------|------|--------|
| 7 | `tool.invoke` | `{run_id, tool_name, input, step}` | `{content, artifacts[], error?}` | 所有大脑。Kernel 在执行前跑 Guardrail 两层白名单检查（见 §6），不通过直接返回 error |
| 8 | `llm.complete` | `{run_id, messages, tools?, tool_choice?, model?, max_tokens?, temperature?, stream?}` | `{message, tool_calls[], usage, stop_reason}` | 所有大脑。主进程根据 sidecar 的 `LLMConfig.Provider` 路由到对应 provider adapter |
| 9 | `llm.stream` | 同 `llm.complete` | 流式（多条 notification + 一条 final） | 所有大脑。v2.0 可选实现，v2.1 强制 |
| 10 | `artifact.put` | `{run_id, kind, mime_type, content_base64 or content_ref, caption, step, metadata}` | `{artifact_ref}` | 所有大脑。大文件用 `content_ref`（sidecar 先写到共享 workspace 临时目录） |
| 11 | `plan.update` | `{plan_id, mutator_op}` | `{updated_plan, conflict?}` | **仅 central_brain**。Kernel 根据调用者的 BrainKind 校验权限，其他大脑调用直接拒绝 |
| 12 | `subtask.delegate` | `{parent_run_id, target_kind, target_code?, request}` | `{delegated_run_id, result}` | **仅 central_brain**。Kernel 接到此调用后起一个新的 sidecar Run（对应 target_kind），完成后把 Result 返回给 central_brain |
| 13 | `trace.emit` | `{run_id, event_type, data, timestamp}` | `{ok}` | 所有大脑。写入 Trace/Audit Logger（见 §11）。当 `LLMAccess=direct` 时 sidecar **必须**通过此方法上报 token/cost 事件，否则熔断 |
| 14 | `log.emit` | `{level, message, fields}` | `{ok}` | 所有大脑。结构化日志上报，等价于 stderr 但带 run_id 关联 |

##### 12.5.4.4 可选扩展方法（v2.1+）

| # | 方法 | 用途 |
|---|------|------|
| 15 | `llm.requestDirectAccess` | `LLMAccessHybrid` 模式下申请临时直连窗口 |
| 16 | `stream.start/chunk/end` | LLM 流式输出的透传（v2.1 强制） |
| 17 | `workspace.read` / `workspace.write` | 显式的工作区读写操作（替代 tool.invoke 的路径操作，提供更强语义） |
| 18 | `brain.introspect` | 运行时检查大脑当前状态（message 栈长度、已用步数等），供调试 |

##### 12.5.4.5 协议示例（一次完整 Run 的时序）

```
# 1) 主进程启动 code_brain sidecar，发握手
→ {"jsonrpc":"2.0","id":1,"method":"initialize","params":{
    "protocol_version":"v1",
    "kernel_version":"2.0.0",
    "capabilities":{"streaming":false,"direct_llm":false},
    "llm_config":{"provider":"anthropic","model":"claude-opus-4-6","max_steps":40},
    "workspace_path":"/workspace/run_123/worktree",
    "run_context":{"run_id":123,"workflow_run_id":456,"task_id":789}
  }}
← {"jsonrpc":"2.0","id":1,"result":{
    "protocol_version":"v1","brain_version":"1.0.0",
    "brain_capabilities":{"git_ops":true,"test_runner":true},
    "supported_tools":["code.read_file","code.write_file","code.run_cmd","code.git_diff","code.run_tests"]
  }}

# 2) 主进程发 brain.run
→ {"jsonrpc":"2.0","id":2,"method":"brain.run","params":{
    "run_id":123,
    "request":{"goal":"implement user login","acceptance_criteria":[...]}
  }}

# 3) sidecar 开始 Agent Loop，反向调 LLM
← {"jsonrpc":"2.0","id":101,"method":"llm.complete","params":{
    "run_id":123,
    "messages":[{"role":"system","content":"..."},{"role":"user","content":"..."}],
    "tools":[...]
  }}
→ {"jsonrpc":"2.0","id":101,"result":{
    "message":{"role":"assistant","content":"..."},
    "tool_calls":[{"id":"tc1","name":"code.read_file","input":{"path":"auth.go"}}],
    "usage":{"prompt_tokens":1500,"completion_tokens":200}
  }}

# 4) sidecar 反向调工具（Kernel 执行读文件）
← {"jsonrpc":"2.0","id":102,"method":"tool.invoke","params":{
    "run_id":123,"tool_name":"code.read_file","input":{"path":"auth.go"},"step":1
  }}
→ {"jsonrpc":"2.0","id":102,"result":{
    "content":"package auth\n\nfunc Login(...)...",
    "artifacts":[]
  }}

# 5) sidecar 继续 Agent Loop ... (省略多轮 llm.complete + tool.invoke)

# 6) sidecar 产出 artifact
← {"jsonrpc":"2.0","id":150,"method":"artifact.put","params":{
    "run_id":123,"kind":"diff","mime_type":"text/x-diff",
    "content_base64":"...","caption":"auth.go login fix","step":8
  }}
→ {"jsonrpc":"2.0","id":150,"result":{"artifact_ref":{"id":9001,"url":"s3://..."}}}

# 7) sidecar 写 trace
← {"jsonrpc":"2.0","id":null,"method":"trace.emit","params":{
    "run_id":123,"event_type":"step_completed","data":{"step":8,"tools_called":3}
  }}

# 8) sidecar 最终产出 SpecialistReport 并返回 brain.run
← {"jsonrpc":"2.0","id":2,"result":{
    "result":{
      "status":"success",
      "summary":"implemented login endpoint with session token",
      "report":{
        "subtask_id":"s1","brain_kind":"code_brain","outcome":"success",
        "risk_level":"medium","confidence":0.85,
        "actions":[...],"completed_goals":[...],"artifacts":[{"id":9001}]
      },
      "steps_taken":8,"tokens_used":{...},"cost_usd":0.42
    }
  }}

# 9) 主进程关闭 sidecar（idle timeout 或 shutdown）
→ {"jsonrpc":"2.0","id":999,"method":"shutdown","params":{"grace_ms":5000}}
← {"jsonrpc":"2.0","id":999,"result":{"ok":true}}
```

##### 12.5.4.6 协议版本与演进策略

- v1 协议冻结的方法：#1 ~ #14（必须全部实现）
- v1 可选方法：#15 ~ #18（主进程和 sidecar 协商支持）
- **向后兼容铁律**：v1 协议发布后任何方法的参数只能加字段不能改/删；返回值同理
- **v2 协议定义的情形**：如果需要打破 v1 契约，新起 v2 协议，sidecar 在 initialize 时声明支持哪个版本，主进程同时支持 v1 和 v2 一段时间后逐步淘汰 v1
- 协议的 JSON Schema 和 Go 代码由单一事实源生成（`workflow/executor/kernel/protocol/v1/schema.json`）

#### 12.5.5 SubprocessRunner 实现要点

```go
// workflow/executor/kernel/runner/subprocess.go

type SubprocessRunner struct {
    pool          *SidecarPool          // 按 BrainKind 分组的进程池
    protocol      *StdioProtocol        // JSON-RPC 编解码器
    toolExecutor  ToolExecutor          // 处理反向 tool.invoke
    llmProxy      LLMProxy              // 处理反向 llm.complete
    planStore     PlanStore             // 处理反向 plan.update / subtask.delegate
    artStore      ArtifactStore         // 处理反向 artifact.put
    tracer        Tracer                // 处理反向 trace.emit / log.emit
    circuitBreaker *CircuitBreaker      // 熔断器
    mu            sync.RWMutex
}

type SidecarPool struct {
    instances map[BrainKind][]*SidecarInstance  // 每个 kind 多个实例
    config    map[BrainKind]*SubprocessSpec
}

type SidecarInstance struct {
    Kind       BrainKind
    InstanceID string
    Process    *os.Process
    Stdin      io.WriteCloser  // JSON-RPC 请求通道
    Stdout     *bufio.Reader   // JSON-RPC 响应通道
    Stderr     io.Reader       // 日志通道
    ActiveRuns map[int64]*runState
    LastUsed   time.Time
    RestartCount int
    mu         sync.Mutex
}
```

**关键实现细节**：

1. **请求多路复用**：单个 sidecar 实例支持 MaxConcurrentRun 并发 Run（默认 4）。主进程维护 `ActiveRuns` map，通过 JSON-RPC `id` 字段匹配响应
2. **反向 RPC 处理**：主进程开一个 goroutine 循环读 sidecar 的 stdout。收到消息按 `id` 是否为 null 分流：
   - `id != null` 且 `method == null` → 是对主进程之前发出请求的响应，喂给对应的 waiting channel
   - `id != null` 且 `method != null` → 是 sidecar 发来的反向 RPC 请求，交给 handler 处理后回 id 相同的 response
   - `id == null` → 是 notification（trace.emit/log.emit），异步处理不回响应
3. **崩溃检测**：任何一次 write/read 失败、协议解析失败、sidecar exit → 标记实例 crashed → 所有 ActiveRun 收到 error → 调度器重试或升级
4. **熔断器**：按 RestartPolicy 计数。连续崩溃超阈值 → 熔断 CircuitBreakHours 小时，期间 `runAgent` 对该 kind 直接返回 error
5. **进程池伸缩**：按 `PoolSize` 预启动实例；高峰期主进程可以临时起额外实例；空闲超 IdleTimeout 的额外实例自动下线
6. **workspace 隔离**：主进程为每个 Run 分配一个 workspace 目录，通过 `initialize.workspace_path` 下发；sidecar 的所有 `tool.invoke` 路径操作都被 Guardrail 限定在这个目录内

#### 12.5.6 第三方大脑 SDK

发布 `github.com/leef-l/brain` Go 模块，第三方开发者的最小示例：

```go
package main

import "github.com/leef-l/brain/sdk"

func main() {
    brain := sdk.New(sdk.BrainDef{
        Kind:    "python_brain",
        Version: "1.0.0",
        SystemPrompt: `你是 Python 专家。使用提供的工具完成任务。`,
        DefaultLLMConfig: sdk.LLMConfig{
            Provider: "anthropic",
            Model:    "claude-opus-4-6",
            MaxSteps: 40,
        },
        Tools: []sdk.ToolDef{
            {Name: "python.run_script", Schema: ...},
            {Name: "python.pip_install", Schema: ...},
        },
        // 当大脑需要做业务逻辑判断时的回调
        OnLLMResponse: func(ctx sdk.RunContext, resp sdk.LLMResponse) error { ... },
    })

    sdk.Serve(brain)  // 阻塞的 stdio 循环，实现全部协议方法
}
```

编译：`go build -o easymvp-python-brain .`

安装：把二进制放到 `~/.easymvp-exec/brains/` 目录或通过 `easymvp-exec brain install ./easymvp-python-brain`

注册：`easymvp-exec brain register python_brain --binary easymvp-python-brain`，生成 `~/.easymvp-exec/brains.yaml` 条目：

```yaml
brains:
  - kind: python_brain
    code: default
    transport: subprocess
    llm_access: proxied
    subprocess:
      binary: /Users/x/.easymvp-exec/brains/easymvp-python-brain
      protocol_version: v1
      pool_size: 2
```

**SDK 的内部工作**：
- 实现完整的 stdio JSON-RPC 服务端
- 把反向 RPC（llm.complete / tool.invoke 等）封装成 `sdk.LLMClient` / `sdk.ToolInvoker` 等易用 API
- 管理 message 栈、Agent Loop（和主进程的 `agentloop.Run` 共用同一份实现，通过 `go mod` 引用 `workflow/executor/kernel/agentloop`）
- 自动处理 `brain.cancel` / `shutdown` / `health.ping`
- 第三方开发者只需要写 system prompt、工具定义、大脑特有的行为回调，~100 行 Go 就能出一个新大脑

#### 12.5.7 LLMAccess 三种模式的细节（对应决策 7）

> **下级规格**：LLMAccess 三模式的完整威胁模型、凭证最小化下发规则、direct 模式下 `trace.emit` 强制上报的熔断策略、hybrid 模式的临时窗口 TTL 策略见 [23-安全模型.md §5 LLMAccess 三模式凭证策略](./23-安全模型.md)。本节只概述行为契约，实现者必须以 23 的规格为准。

**对应决策 7（默认代理，兼容直连）**：用户原话是"默认不能，但是可以开启，兼容一下，扩展性要极强"。这里"不能"指的是 sidecar 默认不能直接拿 API key 调 LLM（否则 Guardrail 和成本审计就穿透了），"可以开启"指的是第三方大脑可以显式切到 direct 模式自备 key。三种模式对应 `BrainDescriptor.LLMAccess`：

| 模式 | sidecar 如何调 LLM | Kernel 的监督力度 | 适用场景 |
|------|--------------------|-------------------|---------|
| `proxied`（默认） | 反向 `llm.complete` 让主进程代调 | Guardrail/Cost/Trace 全在主进程，100% 透明 | 大多数大脑，尤其是涉及成本控制和审计的场景 |
| `direct` | sidecar 自己持 API key 直连 | sidecar **必须**调 `trace.emit` 上报 prompt/completion token 和成本事件 | 对延迟极度敏感、或使用主进程未集成的 provider（如本地 Ollama） |
| `hybrid` | 默认 proxied，按需 `llm.requestDirectAccess(ttl)` 申请临时直连 | 直连窗口内同样要求 trace 上报 | 少数场景需要临时 direct，避免全程 direct 的风险 |

**direct 模式的安全约束**：

- initialize 时主进程向 sidecar 下发的 `llm_credentials` 只包含**必要的最小 provider 列表**，不是所有 provider 的 key
- API key 通过 initialize 参数下发，不走环境变量（防止 ps aux 泄露）
- sidecar 必须实现 "trace.emit 才算数" 规则——每次 LLM 调用结束后立即上报，否则主进程认为你在作弊
- 主进程定期对 direct 模式大脑做对账：`trace.emit 上报的 token 数` vs `主进程从 provider 的 API 拿到的实际用量`（需要 provider 支持查用量），不一致 → 熔断
- Guardrail 针对 LLM 的部分（如"禁止调用昂贵模型"）降级为事后审计——direct 模式大脑无法被事前拦截，但事后会被触发报警

**何时选 direct**：
- 本地模型（Ollama/vLLM）：延迟敏感，且没必要走两次网络
- 流式输出要求极高：双向流通过 stdio 传输比反向 RPC 少一层封装
- 实验性 provider：主进程还没集成的 provider，sidecar 自己写 adapter 先跑起来

**何时选 proxied**（99% 的场景）：
- 成本审计是硬要求
- 多个大脑共享同一个 provider 的 rate limit，需要集中调度
- Guardrail 需要事前拦截昂贵调用

#### 12.5.8 取消、心跳、优雅停机（对应决策 6）

**对应决策 6（Runner 加 cancel/health/shutdown）**：用户原话是"需要扩展性极强，是否需要加：cancel（取消正在跑的 Run）/ health（心跳检测）/ shutdown（优雅停）都需要"。下面三小节分别定义这三个方法的协议、时序和失败路径。

**取消（brain.cancel）**：

1. 用户按 Ctrl+C 或 Kernel 收到父 ctx 取消
2. Kernel 调 `runner.Cancel(ctx, runID)` → SubprocessRunner 找到对应的 sidecar 实例和 runID
3. 发 `brain.cancel {run_id, reason, grace_ms: 5000}` 给 sidecar
4. sidecar 必须：
   - 停止向 LLM 发新请求（等待当前 LLM 调用返回或超时）
   - 停止发 tool.invoke
   - 组装一份带 `outcome: cancelled` 的部分 SpecialistReport
   - 在 grace_ms 内回 brain.cancel 的响应，带 partial_result
5. 如果 sidecar 在 grace_ms 内没响应，主进程发 SIGTERM；再 5s 没退出发 SIGKILL
6. Kernel 将对应的 execution_subtask 状态改为 cancelled，记录 Trace

**心跳（health.ping）**：

1. 主进程每 30s 对每个常驻 sidecar 发 `health.ping`
2. sidecar 必须在 3s 内响应
3. 连续 3 次失败 → 认为 sidecar 卡死 → 强杀 + 从进程池移除 + 按 RestartPolicy 重启
4. 响应中的 `active_runs` / `memory_rss` 写入 Prometheus 指标，供监控面板

**优雅停机（shutdown）**：

1. Kernel 接到 SIGTERM 或 `easymvp-exec shutdown` 命令
2. SubprocessRunner 对所有 sidecar 并发发 `shutdown {grace_ms: 30000}`
3. sidecar 停止接收新 Run；等待 ActiveRuns 中所有 Run 自然结束或主进程发 brain.cancel
4. 所有 ActiveRuns 结束 → sidecar 回 `shutdown` 响应 → 主进程 close stdin
5. sidecar 收到 EOF 退出 0
6. 超 grace_ms 未退出 → SIGTERM → SIGKILL

#### 12.5.9 对现有代码的影响与落地清单

**v2 必做**（不再分 v1/v2 阶段，一次性落地）：

1. **协议层**（`workflow/executor/kernel/protocol/v1/`）
   - `schema.json`（JSON Schema，14 个方法的完整定义）
   - `types.go`（Go 结构体，代码生成或手写）
   - `codec.go`（编解码 + id 管理 + 双向路由）

2. **Runner 层**（`workflow/executor/kernel/runner/`）
   - `runner.go`（BrainRunner 接口）
   - `subprocess.go`（SubprocessRunner 主体 ~2000 行）
   - `pool.go`（SidecarPool 进程池）
   - `circuit_breaker.go`（熔断器）
   - `rpc.go`（空文件 + TODO 注释，v3 Atlas 企业版填）

3. **反向 RPC handler**（`workflow/executor/kernel/reverse/`）
   - `tool_handler.go`（处理 tool.invoke）
   - `llm_handler.go`（处理 llm.complete + 代理到 LLM Provider）
   - `artifact_handler.go`
   - `plan_handler.go`（含 central_brain 权限校验）
   - `delegate_handler.go`（subtask.delegate 递归启动子 Run）
   - `trace_handler.go`

4. **SDK**（独立 Go 模块 `github.com/leef-l/brain`）
   - `sdk.go`（用户 API）
   - `serve.go`（stdio 循环主体）
   - `agentloop/`（和主进程共用的 Agent Loop 核心）
   - `examples/`（code_brain / browser_brain 的完整源码作为范例）

5. **sidecar 本体**（每个大脑一个独立二进制）
   - `cmd/easymvp-central-brain/`
   - `cmd/easymvp-verifier-brain/`
   - `cmd/easymvp-fault-brain/`
   - `cmd/easymvp-code-brain/`
   - `cmd/easymvp-browser-brain/`
   - `cmd/easymvp-mobile-brain/`
   - `cmd/easymvp-game-brain/`
   - 每个大脑 ~200~500 行 Go（system prompt + 工具集定义 + SDK 调用）

6. **CLI 安装/管理命令**
   - `easymvp-exec brain list`
   - `easymvp-exec brain install <path>`
   - `easymvp-exec brain register <kind> --binary <path>`
   - `easymvp-exec brain scaffold <kind>`（生成新大脑模板）
   - `easymvp-exec debug <run_id>`（跨进程调试）

**v2 行数预估**：
| 模块 | 行数 |
|------|------|
| 协议层 | ~1200 |
| SubprocessRunner | ~2500 |
| 反向 RPC handler | ~1500 |
| brain-sdk-go | ~1800 |
| 7 个内置 sidecar 本体 | ~2500 |
| CLI brain 子命令 | ~800 |
| 跨进程调试工具 | ~400 |
| **合计** | **~10700 行** |

比原先"v1 ~6700 + v2 增量 ~2300"要多 ~1700 行，主要来自"7 个内置大脑都要独立 main 函数 + protocol + runner 的完整性 + debug 工具"。但**没有任何一行是返工代码**，也没有任何一次协议变更。

#### 12.5.10 为什么不是 Go plugin (.so)

- 只支持 Linux/macOS，Windows 不行
- plugin 和主程序的 Go 版本必须完全一致，依赖版本必须一致 → 工程上几乎不可能维护
- plugin 加载后不能卸载，热更新不可行
- 官方明确说 plugin 是"实验性"
- 不支持跨语言

`SubprocessRunner` + stdio 完美替代：进程隔离、跨平台、跨语言、不绑 Go 版本、天然支持崩溃重启。

---

## 13. 错误分类与恢复策略

> **📌 正式规格另见**：本节是业务层概览。**BrainError 结构体、Class 四维分类、error_code 保留清单、Fingerprint 算法、Decide 决策矩阵、毒消息防御、SafetyRefused 升级通道、tool_result 脱敏视图、错误预算与 SLO、合规测试 C-E-01 ~ C-E-20**等跨语言正式规格放在独立文档 [21-错误模型.md](./21-错误模型.md)。业务代码实现错误处理时 **必须** 以 21 号文档为准，本节只是给内核架构阅读者一个粗粒度的心智模型。

### 13.1 Class 四维分类（v1 冻结）

错误的**唯一决策字段**是 `Class`，不是 error_code。调度器 / 重试器 / 熔断器 / 告警器只看 Class。v1 定义 **6 个** ErrorClass 枚举：

| Class | 含义 | 默认 retryable | 默认动作 |
|-------|------|----------------|----------|
| `Transient` | 瞬时故障（网络闪断 / LLM 5xx / 短期限速） | **true** | 指数退避 ≤ 3 次 |
| `Permanent` | 永久失败（编译错 / 测试失败 / 参数非法） | **false** | 立即 Fail |
| `UserFault` | 用户输入错（prompt 缺字段 / 文件格式错） | **false** | 返回 UI 让用户改 |
| `QuotaExceeded` | 配额耗尽（日 token / 速率） | **false** | 进 cooldown，暂停大脑 |
| `SafetyRefused` | 安全拒绝（LLM refusal / 策略闸门 / 沙箱拒绝） | **false** | **AskHuman**（进入人工节点） |
| `InternalBug` | 内部 bug（panic / assertion）| **false** | DegradeBrain，连续 3 次 Quarantine |

`Class × retryable × fault_policy × Health × attempt` 的完整决策矩阵见 [21 附录 B](./21-错误模型.md#附录-b--class--retryable-决策矩阵)，本节不展开。

### 13.2 error_code 快速参考（常见子集）

error_code 是 Class 之下的细粒度字符串常量，完整 v1 保留清单（8 个命名空间 ~40 条）见 [21 附录 A](./21-错误模型.md#附录-a--error_code-保留清单-v1)。内核侧最常用的几条：

| error_code | Class | LLM 看到什么 |
|------------|-------|--------------|
| `llm_rate_limited_shortterm` | Transient | 不看到（Kernel 透明重试） |
| `llm_upstream_5xx` | Transient | 不看到 |
| `llm_context_overflow` | Permanent | 不看到（终止 Run） |
| `llm_safety_refused` | SafetyRefused | 不看到（升级人工节点） |
| `tool_not_found` | Permanent | 看到脱敏后的 `tool_result{ok:false, error:{...}}` |
| `tool_input_invalid` | UserFault | 看到 error，下一步可改参数 |
| `tool_sandbox_denied` | SafetyRefused | **不看到**（升级人工，不给 LLM 反思机会） |
| `tool_execution_failed` | Permanent | 看到 error（受反思循环限制：同 fingerprint 超 2 次强制 Fail） |
| `brain_overloaded` | Transient | 不看到 |
| `frame_too_large` | Permanent | 不看到（协议层错误，Run 直接失败） |
| `deadline_exceeded` | Transient | 不看到 |
| `panicked` | InternalBug | 不看到（立即 DegradeBrain + P1 告警） |

### 13.3 LLM 看到的错误格式（脱敏视图）

Kernel 回注给 LLM 的 `tool_result{is_error: true}` **不是**原始 BrainError，而是经过 [21 §11.1 tool_result 脱敏规则](./21-错误模型.md#111-tool_result-脱敏视图)过滤的白名单视图——`fingerprint` / `trace_id` / `internal_only` / `raw_stderr` / `cause` 都**不会**流到 LLM。脱敏后的统一格式：

```json
{
  "tool_use_id": "toolu_01abc",
  "content": [
    {
      "type": "text",
      "text": "ERROR: tool_sandbox_denied\nThe path \".gitignore\" is in the blocklist and cannot be written.\nHint: Allowed paths: [\"src/**\", \"tests/**\"]"
    }
  ],
  "is_error": true
}
```

LLM 看到这样的结构化错误后**通常能自己调整**：看到 sandbox 拒绝 + allowed list，下一轮挑 allowed 路径。但有两条硬约束（详见 [21 §11](./21-错误模型.md#11-把错误交还给-llmtool_result-污染防御)）：

1. **message 必须是 Kernel 自己生成的一句话摘要**，禁止把 sidecar stderr 原样夹带（防 prompt injection）
2. **同 fingerprint 错误反思循环上限 2 次**——第 3 次重复错误 Decide 直接 Fail，不再给 LLM "再改一下"的机会（防死循环）

### 13.4 终止 Run 的所有路径

Kernel 会在以下情况立刻终止 Run（而不是继续循环）：

1. `ctx.Done()` 被触发（外部取消或父 deadline 到）
2. 累计成本超过 `LLMConfig.Budget.MaxCostUSD`
3. 累计 token 超过 `LLMConfig.Budget.MaxTokensTotal`
4. 运行时长超过 `LLMConfig.Budget.MaxDuration`
5. 达到 `LLMConfig.MaxSteps`（但走 `OnMaxSteps` 而不是硬终止，让大脑有机会返回 partial Result）
6. LLM 连续返回 `llm_context_too_long`（3 次）
7. 连续 `guardrail_denied` 超过 `MaxConsecutiveGuardrailDenials`
8. LLM 返回 `safety_refusal`（Claude 或 OpenAI 的内容策略拒绝）

---

## 14. Kernel 的不做的事

已经在 §2.2 的反职责表里列过，这里重申并扩展。这些事**不要往 Kernel 里塞**，塞了就是破坏独立产品形态的前提。

### 14.1 Kernel 绝对不做（写进 CI 静态检查）

1. **不 import `easymvp/admin-go/app/mvp/internal/model/entity`** —— 任何 GoFrame 生成的业务实体
2. **不 import `easymvp/admin-go/app/mvp/internal/workflow/orchestrator`** —— 编排层
3. **不 import `easymvp/admin-go/app/mvp/internal/workflow/stage`** —— 阶段层
4. **不 import `easymvp/admin-go/app/mvp/internal/workflow/autonomy`** —— 自治层
5. **不 import `github.com/gogf/gf/v2/frame/g`** —— GoFrame 全局对象
6. **不 import `github.com/gogf/gf/v2/database/gdb`** —— GoFrame DB 层

Kernel 与业务代码之间的**唯一接触面**是：`ArtifactStore` / `PlanStore` / `BrainRegistry` / `llm.Provider` 这四个接口。Atlas 提供这四个接口的 DB 版实现，CLI 提供本地版实现，Kernel 只拿到接口不 care 实现细节。

### 14.2 Kernel 不做业务判断（运行时约束）

1. **不判断任务是否完成**——大脑的 OnStop 决定
2. **不判断子任务该派给谁**——中央大脑的 LLM 决定
3. **不判断失败该重试还是放弃**——LLM 看到 error 自己决定
4. **不修改 system prompt**——BrainAgent 提供什么就用什么
5. **不过滤 LLM 的输出**——Kernel 只做 tool_call 的 schema 校验，不检查"LLM 说的话是否合适"
6. **不做 prompt engineering**——Kernel 不在 system prompt 里偷偷加 "You must always..."

### 14.3 Kernel 明确不提供的功能

1. **不提供对话式 UI**——Kernel 没有 HTTP/WebSocket/SSE，也没有 stdin 交互
2. **不提供持久化对话历史**——每次 Run 都是独立生命周期
3. **不提供内置的 RAG/vector store**——如果大脑需要知识库，走工具调用方式（大脑注册一个 `recall_context` 工具，自己连 vector DB）
4. **不提供工作流编排**——"任务 A 完成后跑任务 B" 是 Atlas 的 workflow 编排层的职责，Kernel 只跑单个 Run
5. **不提供多租户隔离**——Kernel 只看 RunID，多租户由 Atlas 层通过 `CreatedBy/DeptID` 实现
6. **不提供鉴权**——鉴权在 Atlas 的 controller 层或 CLI 的命令行参数层处理完

---

## 15. Go 包结构与文件清单

```
workflow/executor/kernel/
├── go.mod                          # 独立子 module: github.com/easymvp/executor
├── doc.go                          # 包级注释
│
├── brain.go                        # BrainAgent / BrainKind / BrainDescriptor / BrainTransport / LLMAccessMode
├── request.go                      # Request / GuardrailConfig / Workspace
├── result.go                       # Result / RunStatus / RejectedInfo / TokenUsage / SpecialistReport
├── report.go                       # SpecialistReport / ReportAction / VerificationStatus（§3.5 / §3.6）
├── config.go                       # LLMConfig / Budget / KernelConfig / FaultPolicy
│
├── agentloop/                      # Agent Loop 核心（主进程和 sidecar-sdk 共用）
│   ├── loop.go                     # 主循环算法 (§4)
│   ├── step.go                     # 单步执行逻辑
│   ├── retry.go                    # LLM 调用重试
│   └── terminate.go                # 所有终止分支
│
├── protocol/
│   └── v1/
│       ├── schema.json             # 14 个方法的 JSON Schema 单一事实源
│       ├── types.go                # 协议数据类型（由 schema 生成）
│       ├── codec.go                # JSON-RPC 2.0 编解码 + id 分配
│       ├── router.go               # 双向路由（主/反向 请求区分）
│       └── version.go              # 协议版本号常量与兼容检查
│
├── runner/
│   ├── runner.go                   # BrainRunner 接口 + 路由表
│   ├── subprocess.go               # SubprocessRunner 主体（v2 唯一实现）
│   ├── pool.go                     # SidecarPool 进程池
│   ├── instance.go                 # SidecarInstance 单实例管理
│   ├── circuit_breaker.go          # 熔断器
│   ├── health_monitor.go           # health.ping 周期检测
│   ├── cancel.go                   # brain.cancel 实现
│   ├── shutdown.go                 # 优雅停机
│   └── rpc.go                      # 空文件 + TODO 注释（v3 Atlas 企业版填）
│
├── reverse/                        # 反向 RPC handler（sidecar → 主进程）
│   ├── dispatcher.go               # 分发器
│   ├── tool_handler.go             # tool.invoke
│   ├── llm_handler.go              # llm.complete / llm.stream
│   ├── artifact_handler.go         # artifact.put
│   ├── plan_handler.go             # plan.update（含 central_brain 权限校验）
│   ├── delegate_handler.go         # subtask.delegate（递归启动子 Run）
│   ├── trace_handler.go            # trace.emit / log.emit
│   └── llm_direct_handler.go       # llm.requestDirectAccess（LLMAccessHybrid）
│
├── tool.go                         # Tool / ToolSchema / ToolOutput / ToolCallRecord
├── tool_registry.go                # ToolRegistry 实现
│
├── guardrail.go                    # Guardrail 结构 + Check 方法
├── guardrail_paths.go              # 路径白名单算法（glob 匹配）
├── guardrail_domains.go            # 域名白名单算法
├── fault_policy.go                 # fault_policy.json 加载与解析（§6.5）
│
├── artifact.go                     # Artifact / ArtifactRef / ArtifactStore 接口
├── artifact/
│   ├── db_store.go                 # DBArtifactStore（Atlas 用）
│   └── local_store.go              # LocalArtifactStore（CLI 用）
│
├── plan.go                         # BrainPlan / Subtask / PlanStore 接口
├── plan/
│   ├── db_store.go                 # DBPlanStore（Atlas 用）
│   ├── local_store.go              # LocalPlanStore（CLI 用）
│   ├── snapshot.go                 # GetProjectEvidenceSnapshot 实现（§8.3）
│   └── compressor.go               # 证据聚合与压缩算法
│
├── registry_brain.go               # BrainRegistry 接口
├── registry/
│   ├── db_registry.go              # DBBrainRegistry
│   ├── yaml_registry.go            # YAMLBrainRegistry
│   └── scanner.go                  # 扫描 brains.yaml + 调用 brain.describe 自动注册
│
├── llm/
│   ├── provider.go                 # Provider 接口
│   ├── registry.go                 # provider 注册表
│   ├── message.go                  # 统一 Message / Content 结构
│   ├── tool_schema.go              # 统一 ToolSchema
│   ├── anthropic/
│   │   ├── provider.go             # Anthropic adapter
│   │   ├── message_convert.go
│   │   └── tool_convert.go
│   └── openai/
│       ├── provider.go             # OpenAI adapter
│       ├── message_convert.go
│       └── tool_convert.go
│
├── cost/
│   ├── meter.go                    # CostMeter 实现
│   ├── pricing.go                  # 计价表加载与查询
│   ├── reconcile.go                # direct 模式 cost 对账
│   └── pricing.yaml                # 默认计价表
│
├── trace/
│   ├── trace.go                    # LLMCallTrace / ToolCallTrace
│   ├── logger.go                   # 双轨 Logger 接口
│   ├── db_logger.go                # Atlas 用
│   ├── local_logger.go             # CLI 用
│   └── cross_process.go            # 跨进程 Trace ID 串联
│
├── internal/
│   ├── backoff.go
│   ├── jsonschema.go
│   └── goroutinepool.go
│
├── testutil/
│   ├── fake_sidecar.go             # 测试用假 sidecar（内存 stdio 模拟）
│   ├── fake_provider.go
│   ├── fake_store.go
│   └── scenarios.go
│
├── cmd/easymvp-exec/               # 主二进制
│   ├── main.go
│   ├── init.go                     # easymvp-exec init
│   ├── run.go                      # easymvp-exec run
│   ├── resume.go                   # easymvp-exec resume <run_id>
│   ├── brain_list.go               # easymvp-exec brain list
│   ├── brain_install.go            # easymvp-exec brain install <path>
│   ├── brain_register.go           # easymvp-exec brain register
│   ├── brain_scaffold.go           # easymvp-exec brain scaffold <kind>
│   ├── debug.go                    # easymvp-exec debug <run_id>（跨进程调试）
│   ├── shutdown.go                 # easymvp-exec shutdown
│   └── version.go
│
├── cmd/easymvp-central-brain/      # 中央大脑 sidecar
│   ├── main.go                     # 调用 brain-sdk-go sdk.Serve
│   ├── prompt.go                   # system prompt
│   └── tools.go                    # 工具集（plan/subtask/delegate 等）
│
├── cmd/easymvp-verifier-brain/     # 验收大脑 sidecar
│   ├── main.go
│   ├── prompt.go
│   └── tools.go                    # 只读工具集
│
├── cmd/easymvp-fault-brain/        # 故障大脑 sidecar
│   ├── main.go
│   ├── prompt.go
│   └── tools.go                    # 两段式权限（advisory / full_takeover）
│
├── cmd/easymvp-code-brain/         # 编码专精大脑 sidecar
│   ├── main.go
│   ├── prompt.go
│   └── tools.go
│
├── cmd/easymvp-browser-brain/      # 浏览器专精大脑 sidecar
│   ├── main.go
│   ├── prompt.go
│   └── tools.go
│
├── cmd/easymvp-mobile-brain/       # 移动专精大脑 sidecar
│   ├── main.go
│   ├── prompt.go
│   └── tools.go
│
└── cmd/easymvp-game-brain/         # 游戏专精大脑 sidecar
    ├── main.go
    ├── prompt.go
    └── tools.go
```

**额外独立模块**（单独 repo 或 Go 子 module）：

```
brain-sdk-go/                       # github.com/leef-l/brain
├── go.mod
├── sdk.go                          # 用户 API: New / Serve / BrainDef / ToolDef
├── serve.go                        # stdio JSON-RPC 主循环
├── client.go                       # 反向 RPC 封装（LLMClient / ToolInvoker / ArtifactWriter）
├── agentloop/                      # 与主进程 kernel/agentloop 同步的副本（go.mod 引用）
├── protocol/                       # 协议数据类型（与 kernel/protocol/v1 同源）
├── handlers.go                     # brain.run / brain.cancel / health.ping / shutdown 自动处理
└── examples/
    ├── python_brain/               # 完整范例
    └── minimal_brain/              # 最小范例
```

### 15.1 行数预估（v2 全 sidecar 架构）

| 模块 | 预估行数 | 备注 |
|---|---|---|
| 核心类型 + 接口 (brain/request/result/report/config) | ~700 | 含 SpecialistReport / VerificationStatus |
| Agent Loop 核心 | ~700 | |
| Protocol v1（schema + types + codec + router） | ~1200 | |
| Runner 层（subprocess + pool + breaker + health + cancel + shutdown） | ~2500 | 最重的一块 |
| Reverse RPC handlers（7 个 + 权限校验） | ~1500 | |
| Tool Registry + Guardrail + FaultPolicy | ~500 | |
| ArtifactStore (接口 + 两实现) | ~500 | |
| PlanStore (接口 + 两实现 + Snapshot 压缩) | ~900 | Snapshot 压缩算法单独 ~300 |
| BrainRegistry (接口 + 两实现 + scanner) | ~400 | |
| LLM Provider 接口 + Anthropic + OpenAI adapter | ~1200 | |
| Cost Meter + 对账 | ~500 | direct 模式对账 ~200 |
| Trace Logger + 跨进程串联 | ~500 | |
| internal 工具 | ~200 | |
| cmd/easymvp-exec 主二进制入口 + 子命令 | ~1500 | init/run/brain/debug/shutdown |
| 7 个内置 sidecar 本体 (cmd/easymvp-*-brain) | ~2500 | 每个 ~300~400 行（prompt + tools + main） |
| brain-sdk-go 独立模块 | ~1800 | |
| 测试（Kernel 自身 + 协议 + sidecar 集成） | ~2500 | 覆盖率 ≥ 80% |
| **合计** | **~19100** | |

**这个数字比"~6700（原 v1）"大了接近 3 倍**，根本原因：
1. 全 sidecar 架构强制主进程和大脑进程分离（~5000 行协议/runner/reverse 层）
2. 7 个内置大脑都要独立 main + 工具定义（~2500 行）
3. brain-sdk-go 作为开发者生态入口（~1800 行）
4. 测试量翻倍（协议层、sidecar 进程管理、跨进程串联都要测）

**这是"直接上 v2"的成本**。回报是：
- 一次性交付完整形态，无后续接口返工
- 第一个 CLI release 就支持第三方大脑生态
- Kernel 和大脑完全解耦，升级互不影响
- 内置大脑就是最好的开发范例文档

### 15.2 分 PR 落地的建议切分

见 `09-PR实施路线图.md` 的 PR-13 ~ PR-19（v2 直接交付）：

- **PR-13**：核心类型 + Agent Loop + Guardrail + FaultPolicy（~2400 行）
  - `brain.go / request.go / result.go / report.go / config.go`
  - `agentloop/` 核心
  - `guardrail.go / fault_policy.go`
  - 依赖：无
  - 可 merge 的验收标准：单元测试通过，不能端到端跑

- **PR-14**：Protocol v1（~1200 行）
  - `protocol/v1/` 全部
  - JSON Schema 冻结
  - 依赖：PR-13
  - 验收标准：协议编解码往返测试 100% 通过，fake sidecar 可跑通握手

- **PR-15**：Runner + Reverse RPC handlers（~4000 行）
  - `runner/` 全部
  - `reverse/` 全部
  - `tool.go / tool_registry.go`
  - 依赖：PR-13, PR-14
  - 验收标准：能跑通一个最小的 echo-brain sidecar，支持 initialize/brain.run/brain.cancel

- **PR-16**：ArtifactStore + PlanStore + BrainRegistry + CostMeter + Tracer（~2800 行）
  - `artifact/ / plan/ / registry/ / cost/ / trace/` 全部
  - 双轨实现（Atlas DB + CLI Local）
  - 依赖：PR-13
  - 验收标准：DBPlanStore 能写 execution_plan 表，Snapshot 接口有单元测试

- **PR-17**：LLM Provider 抽象层（~1200 行）
  - `llm/` 全部（接口 + Anthropic + OpenAI）
  - 依赖：PR-13
  - 详见 05-LLM-Provider抽象层.md

- **PR-18**：brain-sdk-go 独立模块 + 2 个内置 sidecar（~2400 行）
  - `brain-sdk-go/` 完整 SDK
  - `cmd/easymvp-central-brain/` + `cmd/easymvp-code-brain/`（先跑通最小闭环）
  - 依赖：PR-13 ~ PR-17
  - 验收标准：中央大脑能拆任务并委派给 code_brain，code_brain 能产出 SpecialistReport

- **PR-19**：剩余 5 个内置 sidecar + CLI brain 子命令（~2600 行）
  - `cmd/easymvp-verifier-brain/ / easymvp-fault-brain/`
  - `cmd/easymvp-browser-brain/ / easymvp-mobile-brain/ / easymvp-game-brain/`
  - `cmd/easymvp-exec/brain_*.go` 子命令
  - `cmd/easymvp-exec/debug.go`
  - 依赖：PR-18
  - 验收标准：完整的 6 阶段工作流能跑通端到端，verifier_brain 验收 + fault_brain 兜底链路可用

- **PR-20**（后续）：Atlas 集成 + 生产可用性（~1500 行）
  - 绑到 workflow orchestrator
  - 监控指标
  - 文档与样例

7 个 PR 串行合入，每个 PR 合入后执行器家族都能编译并跑对应层级的测试。

---

## 附：与主方案文档的交叉引用

| 概念 | 主方案文档锚点 | 本文档锚点 |
|---|---|---|
| DomainTask 新字段 | `下一代AI协作开发平台方案.md` §4.4 | §3.3 Request |
| execution_artifact / execution_plan / execution_subtask 表 | 同 §10.2 | §7 / §8 |
| PR-13 BrainKernel | 同 §13.5.3 PR-13 | §15.2 |
| 独立产品形态 | 同 §9.10 → 将迁移到 `08-独立产品形态.md` | §2.2 / §14.1 |
| CentralBrain 的工具集和 system prompt | — | 详见 `03-CentralBrain设计.md` |
| SpecialistBrain 各个大脑细节 | — | 详见 `04-SpecialistBrain设计.md` |
| LLM Provider adapter 的完整实现 | — | 详见 `05-LLM-Provider抽象层.md` |
| BrainPlan 表 DDL 和状态机 | 主方案 §10.2 + §10.3 | 详见 `06-BrainPlan持久化协议.md` |
| 大脑委派工具的 JSON Schema 清单 | — | 详见 `07-大脑委派工具协议.md` |

