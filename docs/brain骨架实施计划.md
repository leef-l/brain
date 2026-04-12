# brain/ Go 骨架实施计划

> **状态**：✅ v0.1.0 冻结 · 2026-04-12
> **所属任务**：Round 4 之后的 F 任务（起 Go 骨架）
> **恢复策略**：如果对话被打断、上下文被压缩、或者换了新的 Claude 会话，读本文档可以从任何中断点继续实施。所有决策、分工、验收标准都在这里，不依赖对话历史。

---

## 0. 为什么需要这份计划

F 任务是要把 10 篇规格（02 + 20~28）的接口体系落地为 Go 代码骨架，总共约 40~60 个文件，分布在 12 个子包里。直接串行写有三个问题：

1. **容易中断**：一次性写 40 个文件跨度太大，上下文稀释或对话中断就要从头梳理
2. **无法并行**：Go 子包之间有明确的拓扑依赖，但第一波里有 7 个无依赖的子包，串行写浪费时间
3. **无法恢复**：没有文档化的计划，换个 claude 会话根本不知道写到哪里、还剩什么

所以整个 F 任务切换到 **Agent Teams 并行模式**：

- **主线 agent**：负责规格决策、依赖管理、最终集成、编译验证、汇报
- **并行子 agent**：每个子包一个子 agent，在自己的独立上下文里写代码
- **计划文档**（本文档）：作为所有 agent 的共同事实源

---

## 1. 最终产物清单

**目标**：在 `brain/` 下建立一个可编译、可运行 `brain version` 与 `brain doctor` 的 Go 骨架。

**总体要求**：
- `cd admin-go && go build ./brain/...` 必须零错误通过
- `./brain version` / `./brain version --short` / `./brain version --json` 必须符合 27-CLI命令契约.md §17
- `./brain doctor` 必须符合 27 §16（网络相关检查返回 skipped）
- 其他 11 个子命令注册到 dispatcher，执行时返回 BrainError + 退出码 70（`unimplemented in v0.1.0 skeleton`）
- 未实现的接口方法体使用 `panic("unimplemented: <spec-ref>")`
- 所有新文件使用 `github.com/leef-l/brain/...` import 前缀（module 名是 `easymvp`，不是 `github.com/easymvp/...`）
- 零新依赖：不引入 cobra / urfave-cli / kingpin，使用标准库 `flag`

### 1.1 目录结构

```
admin-go/
├── brain/
│   ├── doc.go                          # [主线·已完成] package brain 总览
│   ├── version.go                      # [主线·已完成] ProtocolVersion/KernelVersion/SDKVersion 常量
│   ├── VERSION.json                    # [主线·已完成] 28 §4.5 规格 metadata
│   │
│   ├── agent/                          # [主线·已完成] 02 §3
│   │   └── agent.go                    #   Kind/LLMAccessMode/Descriptor/Agent interface
│   │
│   ├── errors/                         # [波1·agent-errors] 21-错误模型
│   │   ├── class.go                    #   ErrorClass 四维分类
│   │   ├── codes.go                    #   error_code 保留常量（~40 条）
│   │   ├── error.go                    #   BrainError struct
│   │   ├── decide.go                   #   Decide(err) 决策函数签名
│   │   └── fingerprint.go              #   Fingerprint(err) 签名
│   │
│   ├── llm/                            # [波1·agent-llm] 02 §5 + 22
│   │   ├── provider.go                 #   LLMProvider interface
│   │   ├── chat_request.go             #   ChatRequest struct (含 CacheControl/Budget)
│   │   ├── chat_response.go            #   ChatResponse struct
│   │   └── usage.go                    #   Usage（token 统计）
│   │
│   ├── tool/                           # [波1·agent-tool] 02 §6
│   │   ├── tool.go                     #   Tool interface
│   │   ├── registry.go                 #   ToolRegistry interface
│   │   ├── schema.go                   #   ToolSchema struct
│   │   └── risk.go                     #   RiskLevel (safe/low/med/high/critical)
│   │
│   ├── observability/                  # [波1·agent-obs] 24
│   │   ├── metrics.go                  #   MetricsRegistry interface
│   │   ├── trace.go                    #   TraceExporter interface
│   │   └── logs.go                     #   LogExporter interface
│   │
│   ├── security/                       # [波1·agent-sec] 23
│   │   ├── zones.go                    #   5 个信任区常量
│   │   ├── vault.go                    #   Vault interface
│   │   ├── sandbox.go                  #   Sandbox 4 维接口（FS/NET/PROC/SYS）
│   │   ├── llm_access.go               #   LLMAccessStrategy interface
│   │   └── audit.go                    #   AuditEvent struct
│   │
│   ├── cli/                            # [波1·agent-cli-const] 27
│   │   ├── exit_codes.go               #   15 个退出码常量
│   │   ├── output.go                   #   OutputFormat (human/json)
│   │   └── version_info.go             #   VersionInfo struct（版本输出 schema）
│   │                                   #   注意：波1 只写常量和 struct，不写 dispatcher
│   │
│   ├── protocol/                       # [波2·agent-protocol] 20
│   │   ├── methods.go                  #   方法名常量（llm.complete/tool.invoke/...）
│   │   ├── frame.go                    #   Content-Length 帧编解码接口
│   │   ├── rpc.go                      #   双向 JSON-RPC 2.0 接口
│   │   ├── lifecycle.go                #   initialize/shutdown/heartbeat
│   │   └── errors.go                   #   protocol 层错误（引 errors 包）
│   │
│   ├── loop/                           # [波2·agent-loop] 22
│   │   ├── run.go                      #   Run struct + RunState
│   │   ├── turn.go                     #   Turn struct + TurnExecutor interface
│   │   ├── budget.go                   #   4 层 Budget（Run/Turn/Tool/LLMCall）
│   │   ├── cache.go                    #   PromptCacheBuilder interface
│   │   ├── stream.go                   #   StreamConsumer interface + 5 类事件
│   │   ├── sanitizer.go                #   ToolResultSanitizer interface
│   │   └── loop_detector.go            #   LoopDetector interface
│   │
│   ├── persistence/                    # [波2·agent-persist] 26
│   │   ├── plan_store.go               #   PlanStore interface + BrainPlan/BrainPlanDelta
│   │   ├── artifact_store.go           #   ArtifactStore + Artifact + Ref
│   │   ├── run_checkpoint.go           #   RunCheckpointStore + Checkpoint
│   │   ├── usage_ledger.go             #   UsageLedger interface
│   │   ├── artifact_meta.go            #   ArtifactMetaStore interface
│   │   ├── resume.go                   #   ResumeCoordinator interface
│   │   └── cas.go                      #   CAS 键计算（sha256/<hex>）
│   │
│   ├── testing/                        # [波2·agent-test] 25
│   │   ├── compliance_runner.go        #   ComplianceRunner interface
│   │   ├── cassette_recorder.go        #   CassetteRecorder interface
│   │   ├── cassette_player.go          #   CassettePlayer interface
│   │   └── fake_sidecar.go             #   FakeSidecar 骨架
│   │
│   └── kernel/                         # [波3·主线] 02 §12
│       ├── kernel.go                   #   Kernel struct（顶层聚合器）
│       ├── runner.go                   #   BrainRunner interface
│       └── transport.go                #   BrainTransport interface
│
├── cmd/
│   └── brain/                          # [波3·主线] CLI 二进制入口
│       ├── main.go                     #   entrypoint + 全局选项解析
│       ├── dispatcher.go               #   13 个子命令路由
│       ├── cmd_version.go              #   ✓ 实际实现（27 §17）
│       ├── cmd_doctor.go               #   ✓ 实际实现（27 §16）
│       └── cmd_stub.go                 #   11 个占位命令统一 stub
```

**注**：`cmd/brain/` 放在 `cmd/`（与 `admin-go/app/` 同级），不放在 `brain/cmd/`。理由：Go 习惯是 `cmd/<binary>/main.go` 位于 module 根，且这样避免 `brain/` 包被二进制污染（`brain/` 下只有库代码）。

---

## 2. 规格依赖拓扑

下面是子包间的 import 依赖图。依赖是**单向**的，不允许出现循环。

```
                             ┌────────┐
                             │ agent  │ ← 02 §3  (已完成)
                             └────────┘
                                  ↑
                                  │
┌──────────┐  ┌─────┐  ┌──────┐  │  ┌──────────────┐  ┌──────────┐
│  errors  │  │ llm │  │ tool │  │  │ observability│  │ security │   波1
└──────────┘  └─────┘  └──────┘  │  └──────────────┘  └──────────┘
    ↑  ↑         ↑       ↑       │         ↑                ↑
    │  │         │       │       │         │                │
    │  │         │       │       │         │                │
    │  │         └──┬────┘       │         │                │
    │  │            │            │         │                │
    │  │          ┌─┴─────────┐  │         │                │
    │  └──────────┤   loop    │──┘         │                │  波2
    │             └───────────┘            │                │
    │  ┌───────────┐      ↑                │                │
    └──┤ protocol  │──────┘                │                │
       └───────────┘                       │                │
                    ┌──────────────┐       │                │
                    │ persistence  │───────┤                │
                    └──────────────┘       │                │
                            ↑              │                │
                    ┌───────┴───┐          │                │
                    │  testing  │──────────┤                │
                    └───────────┘          │                │
                                           │                │
                    ┌──────────────────────┴────────────────┘
                    │
                    │ ┌──────┐                                              波3
                    └─┤ kernel ├→ 聚合所有子包，由主线完成
                      └──┬───┘
                         │
                    ┌────┴────────┐
                    │ cmd/brain/  │  主线实现 version/doctor + 占位 stubs
                    └─────────────┘
```

**关键约束**：

- `agent` 零依赖
- 波 1 子包互相独立（`errors` / `llm` / `tool` / `observability` / `security` / `cli`）
- 波 1 允许 import `agent`
- 波 1 之间**严禁**互相 import（如果某个波 1 子包发现需要引另一个波 1 的类型，说明拓扑错了，应提升到波 2）
- 波 2 可以 import 波 1 + agent
- `kernel/`、`cmd/brain/` 属于波 3，可以 import 任何前面的包

---

## 3. 分工矩阵

| 波次 | Agent 名 | 子包 | 规格来源 | 文件数 | 文件列表 |
|-----|---------|------|---------|------:|---------|
| - | 主线 | `brain/*.go` + `agent/` | 02 顶层 | 4 | doc.go / version.go / VERSION.json / agent/agent.go |
| **1** | agent-errors | `errors/` | 21 | 5 | class.go / codes.go / error.go / decide.go / fingerprint.go |
| **1** | agent-llm | `llm/` | 02 §5 + 22 | 4 | provider.go / chat_request.go / chat_response.go / usage.go |
| **1** | agent-tool | `tool/` | 02 §6 | 4 | tool.go / registry.go / schema.go / risk.go |
| **1** | agent-obs | `observability/` | 24 | 3 | metrics.go / trace.go / logs.go |
| **1** | agent-sec | `security/` | 23 | 5 | zones.go / vault.go / sandbox.go / llm_access.go / audit.go |
| **1** | agent-cli-const | `cli/` 常量部分 | 27 | 3 | exit_codes.go / output.go / version_info.go |
| **2** | agent-protocol | `protocol/` | 20 | 5 | methods.go / frame.go / rpc.go / lifecycle.go / errors.go |
| **2** | agent-loop | `loop/` | 22 | 7 | run.go / turn.go / budget.go / cache.go / stream.go / sanitizer.go / loop_detector.go |
| **2** | agent-persist | `persistence/` | 26 | 7 | plan_store.go / artifact_store.go / run_checkpoint.go / usage_ledger.go / artifact_meta.go / resume.go / cas.go |
| **2** | agent-test | `testing/` | 25 | 4 | compliance_runner.go / cassette_recorder.go / cassette_player.go / fake_sidecar.go |
| **3** | 主线 | `kernel/` + `cmd/brain/` | 02 §12 + 27 | 8 | kernel/{kernel.go,runner.go,transport.go} + cmd/brain/{main.go,dispatcher.go,cmd_version.go,cmd_doctor.go,cmd_stub.go} |

**文件总数**：4（已完成）+ 24（波1）+ 23（波2）+ 8（波3）= **59 个 .go 文件 + 1 个 VERSION.json = 60 个文件**

---

## 4. 共同代码风格约束

**所有 agent 必须遵守**，否则最后集成时会冲突。

### 4.1 Import 前缀

Module 名是 `easymvp`（不是 `github.com/easymvp/...`），所以 import 形如：

```go
import (
    "github.com/leef-l/brain/agent"
    "github.com/leef-l/brain/errors"
    "github.com/leef-l/brain/llm"
)
```

### 4.2 未实现方法体

所有未实现的接口方法或函数使用这个 **确切模式**：

```go
// Decide returns the retry decision for an error. See 21-错误模型.md §6.
func Decide(err *BrainError) Decision {
    panic("unimplemented: 21-错误模型.md §6 Decide")
}
```

- `panic` 字符串格式必须是 `"unimplemented: <spec-file> §<section> <function-or-method-name>"`
- 这样后续 grep `"unimplemented:"` 能定位所有 TODO

### 4.3 包注释

每个子包的"首文件"（字母顺序第一个 .go 文件）必须有 package 级注释：

```go
// Package errors implements the cross-language error contract defined in
// 21-错误模型.md.
//
// The BrainError struct, ErrorClass four-dimensional taxonomy, Decide retry
// matrix, and Fingerprint algorithm are all frozen v1 contracts — see the
// spec for normative behavior.
package errors
```

### 4.4 类型注释

每个导出类型/函数/方法必须有 godoc 注释，**必须包含规格引用**：

```go
// ErrorClass is the four-dimensional taxonomy defined in 21-错误模型.md §3.
// Implementations MUST classify every error into exactly one Class.
type ErrorClass string
```

### 4.5 Panic / Error 不混用

- 未实现代码：`panic("unimplemented: ...")`（上文 §4.2）
- 运行时错误：返回 `*errors.BrainError`（而不是 `error`）
- 编程错误：`panic(...)` 带简短消息
- 不使用 `log.Fatal` / `os.Exit`（除了 `cmd/brain/main.go` 的最终退出）

### 4.6 不引入外部依赖

- 使用标准库 `encoding/json`、`flag`、`fmt`、`os`、`time`、`context`、`crypto/sha256` 等
- **不引入** cobra / urfave-cli / zap / logrus / viper / zerolog / otel-go（otel-go 将来引入，但波 1-3 阶段不引）
- 如果某个子 agent 觉得必须引外部库，**停下来在本文档 §8 "决策日志"里记录原因**，主线审批后再动

### 4.7 名字与规格对齐

- 导出类型名必须与规格中的名字一致（`ErrorClass`、`BrainError`、`ChatRequest`、`PlanStore`、`CAS`、...）
- 不要自作主张改名（例如不要写 `Error` 替代 `BrainError`）

### 4.8 `go build` 洁癖

每个子 agent 必须自己在自己的子包下跑：

```
cd admin-go && go build ./brain/<subpkg>/...
```

确保自己的包独立编译。**未通过者不得交付**。

---

## 5. 每个子包的接口规格速查

下面是每个子包要导出的类型、函数、方法的**简要列表**。子 agent 必须以此为准，不能少写，可以多写但要在注释里标注 "skeleton extension"。

完整规格请参考 `docs/next-gen-executor/` 对应文档，但下面的摘要足够写骨架。

### 5.1 errors/（21-错误模型）

**必选导出**：

```go
// class.go
type ErrorClass string
const (
    ClassTransient      ErrorClass = "transient"
    ClassPermanent      ErrorClass = "permanent"
    ClassUserFault      ErrorClass = "user_fault"
    ClassQuotaExceeded  ErrorClass = "quota_exceeded"
    ClassSafetyRefused  ErrorClass = "safety_refused"
    ClassInternalBug    ErrorClass = "internal_bug"
)

// codes.go  — error_code 保留清单（~40 条，按命名空间）
const (
    // sidecar.*
    CodeSidecarCrashed       = "sidecar.crashed"
    CodeSidecarTimeout       = "sidecar.timeout"
    CodeSidecarHandshakeFail = "sidecar.handshake_fail"
    // tool.*
    CodeToolDenied     = "tool.denied"
    CodeToolTimeout    = "tool.timeout"
    CodeToolArgsInvalid = "tool.args_invalid"
    CodeToolRuntime    = "tool.runtime"
    // llm.*
    CodeLLMRateLimit   = "llm.rate_limit"
    CodeLLMQuota       = "llm.quota"
    CodeLLMBadRequest  = "llm.bad_request"
    CodeLLMSafety      = "llm.safety"
    CodeLLMTimeout     = "llm.timeout"
    CodeLLMNetwork     = "llm.network"
    // frame.* (wire protocol)
    CodeFrameCorrupt   = "frame.corrupt"
    CodeFrameTooLarge  = "frame.too_large"
    // brain.*
    CodeBrainBudget    = "brain.budget_exhausted"
    CodeBrainLoop      = "brain.loop_detected"
    CodeBrainState     = "brain.invalid_state"
    // db.*
    CodeDBConflict     = "db.conflict"
    CodeDBUnavailable  = "db.unavailable"
    // internal.*
    CodeInternalBug    = "internal.bug"
    CodeInternalPanic  = "internal.panic"
    // (至少 20 条，可以扩展到 40 条)
)

// error.go
type BrainError struct {
    Class        ErrorClass      `json:"class"`
    ErrorCode    string          `json:"error_code"`
    Retryable    bool            `json:"retryable"`
    Fingerprint  string          `json:"fingerprint"`
    TraceID      string          `json:"trace_id,omitempty"`
    Message      string          `json:"message"`
    Hint         string          `json:"hint,omitempty"`
    Cause        *BrainError     `json:"cause,omitempty"`
    InternalOnly *InternalDetail `json:"-"`
    BrainID      string          `json:"brain_id,omitempty"`
    SidecarPID   int             `json:"sidecar_pid,omitempty"`
    OccurredAt   time.Time       `json:"occurred_at"`
    Attempt      int             `json:"attempt,omitempty"`
    Suggestions  []string        `json:"suggestions,omitempty"`
}

type InternalDetail struct {
    Stack      string
    RawStderr  string
    DebugFlags map[string]string
}

// 必须实现 error interface
func (e *BrainError) Error() string

// decide.go
type Decision struct {
    Retry        bool
    BackoffHint  time.Duration
    Reason       string
}
func Decide(err *BrainError, attempt int) Decision  // panic unimplemented

// fingerprint.go
func Fingerprint(err *BrainError) string  // panic unimplemented
```

### 5.2 llm/（02 §5 + 22）

```go
// provider.go
type Provider interface {
    Name() string
    Complete(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
    Stream(ctx context.Context, req *ChatRequest) (StreamReader, error)
}

type StreamReader interface {
    Next(ctx context.Context) (StreamEvent, error)
    Close() error
}

type StreamEvent struct {
    Type StreamEventType
    Data json.RawMessage
}

type StreamEventType string
const (
    EventMessageStart  StreamEventType = "message.start"
    EventContentDelta  StreamEventType = "content.delta"
    EventToolCallDelta StreamEventType = "tool_call.delta"
    EventMessageDelta  StreamEventType = "message.delta"
    EventMessageEnd    StreamEventType = "message.end"
)

// chat_request.go
type ChatRequest struct {
    RunID        string
    TurnIndex    int
    BrainID      string
    System       []SystemBlock
    Messages     []Message
    Tools        []ToolSchema  // 从参数传入，不 import tool 包（避免循环）
    ToolChoice   string
    Model        string
    MaxTokens    int
    Stream       bool
    CacheControl []CachePoint    // 三层 cache_control
    TurnTimeout  time.Duration
    RemainingBudget BudgetSnapshot // 简化：也可以用 map
}

type SystemBlock struct {
    Text string
    Cache bool  // cache_control: ephemeral
}

type Message struct {
    Role    string // user/assistant/tool
    Content []ContentBlock
}

type ContentBlock struct {
    Type string  // text / tool_use / tool_result
    Text string  `json:",omitempty"`
    ToolUseID string `json:",omitempty"`
    ToolName  string `json:",omitempty"`
    Input     json.RawMessage `json:",omitempty"`
    Output    json.RawMessage `json:",omitempty"`
    IsError   bool `json:",omitempty"`
}

type CachePoint struct {
    Layer string  // L1_system / L2_task / L3_history
    Index int
}

type BudgetSnapshot struct {
    TurnsRemaining      int
    CostUSDRemaining    float64
    TokensRemaining     int
}

type ToolSchema struct {  // 轻量版，避免 import 循环
    Name        string
    Description string
    InputSchema json.RawMessage
}

// chat_response.go
type ChatResponse struct {
    ID           string
    Model        string
    StopReason   string
    Content      []ContentBlock
    Usage        Usage
    FinishedAt   time.Time
}

// usage.go
type Usage struct {
    InputTokens          int
    OutputTokens         int
    CacheReadTokens      int
    CacheCreationTokens  int
    CostUSD              float64
}
```

**注意**：`llm/` 和 `tool/` 会互相需要对方的类型（`ChatRequest.Tools` 要 `ToolSchema`，`Tool.Execute` 要 `ChatResponse` 吗？）。解法是 `llm/` 自己定义一个轻量版 `ToolSchema`（上面已写），让它和 `tool/` 独立，避免循环依赖。

### 5.3 tool/（02 §6）

```go
// tool.go
type Tool interface {
    Name() string
    Schema() Schema
    Risk() Risk
    Execute(ctx context.Context, args json.RawMessage) (*Result, error)
}

type Result struct {
    Output  json.RawMessage
    IsError bool
}

// registry.go
type Registry interface {
    Register(tool Tool) error
    Lookup(name string) (Tool, bool)
    List() []Tool
    ListByBrain(brainKind string) []Tool
}

// schema.go
type Schema struct {
    Name        string
    Description string
    InputSchema json.RawMessage  // JSON schema of args
    Brain       string           // which brain registered it
}

// risk.go
type Risk string
const (
    RiskSafe     Risk = "safe"
    RiskLow      Risk = "low"
    RiskMedium   Risk = "med"
    RiskHigh     Risk = "high"
    RiskCritical Risk = "critical"
)
```

### 5.4 observability/（24）

```go
// metrics.go
type Registry interface {
    Counter(name string, labels Labels) Counter
    Histogram(name string, labels Labels, buckets []float64) Histogram
    Gauge(name string, labels Labels) Gauge
}

type Labels map[string]string

type Counter interface {
    Inc()
    Add(n float64)
}

type Histogram interface {
    Observe(v float64)
}

type Gauge interface {
    Set(v float64)
    Add(n float64)
}

// trace.go
type TraceExporter interface {
    StartSpan(ctx context.Context, name string, attrs Labels) (context.Context, Span)
}

type Span interface {
    SetAttr(key, value string)
    SetError(err error)
    End()
}

// logs.go
type LogExporter interface {
    Emit(ctx context.Context, level LogLevel, msg string, attrs Labels)
}

type LogLevel string
const (
    LogTrace LogLevel = "trace"
    LogDebug LogLevel = "debug"
    LogInfo  LogLevel = "info"
    LogWarn  LogLevel = "warn"
    LogError LogLevel = "error"
)
```

### 5.5 security/（23）

```go
// zones.go
type Zone int
const (
    ZoneKernel     Zone = 1  // Kernel 自身代码
    ZoneBuiltin    Zone = 2  // 内置 brain
    ZoneThirdParty Zone = 3  // 第三方 brain
    ZoneTool       Zone = 4  // 工具执行
    ZoneLLMOutput  Zone = 5  // LLM 输出
)

// vault.go
type Vault interface {
    Get(ctx context.Context, key string) (string, error)
    Put(ctx context.Context, key, value string) error
    Delete(ctx context.Context, key string) error
}

// sandbox.go
type Sandbox interface {
    FS() FSPolicy
    Net() NetPolicy
    Proc() ProcPolicy
    Sys() SysPolicy
}

type FSPolicy struct {
    ReadAllowed  []string  // 允许读的路径
    WriteAllowed []string
    Denied       []string
}

type NetPolicy struct {
    AllowedHosts []string
    DeniedHosts  []string
    AllowedPorts []int
}

type ProcPolicy struct {
    MaxProcs   int
    AllowedExe []string
}

type SysPolicy struct {
    MaxMemoryMB int
    MaxCPUTime  time.Duration
}

// llm_access.go
type LLMAccessStrategy interface {
    Mode() string  // proxied / direct / hybrid
    Credentials(ctx context.Context, provider string) (map[string]string, error)
}

// audit.go
type AuditEvent struct {
    EventID    string
    Actor      string
    Action     string
    Resource   string
    Timestamp  time.Time
    PrevHash   string  // hash chain
    SelfHash   string
    Payload    map[string]interface{}
}

type AuditLogger interface {
    Emit(ctx context.Context, ev *AuditEvent) error
}
```

### 5.6 cli/（27 常量部分，**不含 dispatcher**）

```go
// exit_codes.go
const (
    ExitOK                = 0
    ExitFailed            = 1
    ExitCanceled          = 2
    ExitBudgetExhausted   = 3
    ExitNotFound          = 4
    ExitInvalidState      = 5
    ExitUsage             = 64
    ExitDataErr           = 65
    ExitNoInput           = 66
    ExitNoPerm            = 67
    ExitSoftware          = 70
    ExitOSErr             = 71
    ExitCredMissing       = 77
    ExitSignalInt         = 130
    ExitSignalTerm        = 143
)

// output.go
type OutputFormat string
const (
    FormatHuman OutputFormat = "human"
    FormatJSON  OutputFormat = "json"
)

// version_info.go
// 27 §17.3 JSON schema
type VersionInfo struct {
    CLIVersion      string `json:"cli_version"`
    ProtocolVersion string `json:"protocol_version"`
    KernelVersion   string `json:"kernel_version"`
    SDKLanguage     string `json:"sdk_language"`
    SDKVersion      string `json:"sdk_version"`
    Commit          string `json:"commit"`
    BuiltAt         string `json:"built_at"`
    OS              string `json:"os"`
    Arch            string `json:"arch"`
}
```

### 5.7 protocol/（20）

```go
// methods.go  — 所有 JSON-RPC 方法名常量
const (
    MethodInitialize          = "initialize"
    MethodShutdown            = "shutdown"
    MethodHeartbeat           = "heartbeat"
    MethodLLMComplete         = "llm.complete"
    MethodLLMStream           = "llm.stream"
    MethodLLMRequestDirect    = "llm.requestDirectAccess"
    MethodToolInvoke          = "tool.invoke"
    MethodPlanCreate          = "plan.create"
    MethodPlanUpdate          = "plan.update"
    MethodArtifactPut         = "artifact.put"
    MethodArtifactGet         = "artifact.get"
    MethodTraceEmit           = "trace.emit"
    MethodAuditEmit           = "audit.emit"
)

// frame.go
type Frame struct {
    ContentLength int
    ContentType   string
    Body          []byte
}

type FrameReader interface {
    ReadFrame(ctx context.Context) (*Frame, error)
}

type FrameWriter interface {
    WriteFrame(ctx context.Context, frame *Frame) error
}

// rpc.go
type RPCMessage struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      string          `json:"id,omitempty"` // k:xxx / s:xxx
    Method  string          `json:"method,omitempty"`
    Params  json.RawMessage `json:"params,omitempty"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
    Code    int             `json:"code"`
    Message string          `json:"message"`
    Data    json.RawMessage `json:"data,omitempty"`
}

type BidirRPC interface {
    Call(ctx context.Context, method string, params interface{}, result interface{}) error
    Notify(ctx context.Context, method string, params interface{}) error
    Handle(method string, handler HandlerFunc)
}

type HandlerFunc func(ctx context.Context, params json.RawMessage) (interface{}, error)

// lifecycle.go
type InitializeRequest struct {
    ProtocolVersion string
    KernelVersion   string
    Capabilities    map[string]bool
    LLMConfig       map[string]interface{}
    LLMCredentials  map[string]string // 仅 direct/hybrid
    WorkspacePath   string
    RunContext      map[string]interface{}
}

type InitializeResponse struct {
    ProtocolVersion string
    BrainVersion    string
    BrainCapabilities map[string]bool
    SupportedTools  []string
}

// errors.go
// protocol 层错误（import errors 子包）
func NewProtocolError(code string, msg string) *braninerrors.BrainError  // 示意
```

**注意**：`protocol/errors.go` 是"把 protocol 层的失败包装成 BrainError 的 helper"，import `errors` 子包。

### 5.8 loop/（22）

```go
// run.go
type Run struct {
    ID         string
    BrainID    string
    State      State
    Budget     Budget
    StartedAt  time.Time
    EndedAt    *time.Time
    CurrentTurn int
}

type State string
const (
    StatePending      State = "pending"
    StateRunning      State = "running"
    StateWaitingTool  State = "waiting_tool"
    StatePaused       State = "paused"
    StateCompleted    State = "completed"
    StateFailed       State = "failed"
    StateCanceled     State = "canceled"
    StateCrashed      State = "crashed"
)

// turn.go
type Turn struct {
    Index     int
    RunID     string
    UUID      string  // 幂等键，用于 resume
    StartedAt time.Time
    EndedAt   *time.Time
    LLMCalls  int
    ToolCalls int
}

type Executor interface {
    Execute(ctx context.Context, run *Run, req *llm.ChatRequest) (*TurnResult, error)
}

type TurnResult struct {
    Turn      *Turn
    Response  *llm.ChatResponse
    NextState State
    Error     *braninerrors.BrainError
}

// budget.go
type Budget struct {
    MaxTurns       int
    MaxCostUSD     float64
    MaxToolCalls   int
    MaxLLMCalls    int
    MaxDuration    time.Duration

    UsedTurns      int
    UsedCostUSD    float64
    UsedToolCalls  int
    UsedLLMCalls   int
    ElapsedTime    time.Duration
}

func (b *Budget) CheckTurn() error
func (b *Budget) CheckCost() error
func (b *Budget) Remaining() llm.BudgetSnapshot

// cache.go
type CacheBuilder interface {
    BuildL1System(system []llm.SystemBlock) []llm.CachePoint
    BuildL2Task(messages []llm.Message, taskBoundary int) []llm.CachePoint
    BuildL3History(messages []llm.Message) []llm.CachePoint
}

// stream.go
type StreamConsumer interface {
    OnMessageStart(ctx context.Context, run *Run, turn *Turn)
    OnContentDelta(ctx context.Context, run *Run, turn *Turn, text string)
    OnToolCallDelta(ctx context.Context, run *Run, turn *Turn, toolName string, argsPartial string)
    OnMessageDelta(ctx context.Context, run *Run, turn *Turn, delta json.RawMessage)
    OnMessageEnd(ctx context.Context, run *Run, turn *Turn, usage llm.Usage)
}

// sanitizer.go
type ToolResultSanitizer interface {
    Sanitize(ctx context.Context, raw *tool.Result, meta SanitizeMeta) (*llm.ContentBlock, error)
}

type SanitizeMeta struct {
    ToolName string
    Risk     tool.Risk
    RunID    string
}

// loop_detector.go
type LoopDetector interface {
    Observe(ctx context.Context, run *Run, event LoopEvent) (LoopVerdict, error)
}

type LoopEvent struct {
    Type       string // tool_call / llm_call / content
    ToolName   string
    ContentHash string
    TraceID    string
}

type LoopVerdict struct {
    IsLoop    bool
    Pattern   string // repeated_tool_call / empty_delta / same_trace_id / ...
    Confidence float64
}
```

**注意**：
- `braninerrors "github.com/leef-l/brain/errors"` 的 alias 是为了避免和 std `errors` 包冲突
- `loop/` 依赖 `llm/` 和 `tool/`（波 1），依赖 `errors/`（波 1）
- 如果某个 field 需要，可以 import `agent/` 获取 `agent.Kind`

### 5.9 persistence/（26）

```go
// plan_store.go
type BrainPlan struct {
    ID           int64
    RunID        int64
    BrainID      string
    Version      int64
    CurrentState json.RawMessage
    Archived     bool
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type BrainPlanDelta struct {
    ID        int64
    PlanID    int64
    Version   int64
    OpType    string
    Payload   json.RawMessage
    Actor     string
    CreatedAt time.Time
}

type PlanStore interface {
    Create(ctx context.Context, plan *BrainPlan) (int64, error)
    Get(ctx context.Context, id int64) (*BrainPlan, error)
    Update(ctx context.Context, id int64, delta *BrainPlanDelta) error
    ListByRun(ctx context.Context, runID int64) ([]*BrainPlan, error)
    Archive(ctx context.Context, id int64) error
}

// artifact_store.go
type Artifact struct {
    Kind    string
    Content []byte
    Caption string
}

type Ref string  // "sha256/<hex>"

type ArtifactStore interface {
    Put(ctx context.Context, runID int64, artifact Artifact) (Ref, error)
    Get(ctx context.Context, ref Ref) (io.ReadCloser, error)
    Exists(ctx context.Context, ref Ref) (bool, error)
}

// run_checkpoint.go
type Checkpoint struct {
    RunID          int64
    TurnIndex      int
    BrainID        string
    State          string
    MessagesRef    Ref
    SystemRef      Ref
    ToolsRef       Ref
    CostSnapshot   json.RawMessage
    TokenSnapshot  json.RawMessage
    BudgetRemain   json.RawMessage
    TraceParent    string
    TurnUUID       string
    ResumeAttempts int
    UpdatedAt      time.Time
}

type RunCheckpointStore interface {
    Save(ctx context.Context, cp *Checkpoint) error
    Get(ctx context.Context, runID int64) (*Checkpoint, error)
    MarkResumeAttempt(ctx context.Context, runID int64) error
}

// usage_ledger.go
type UsageRecord struct {
    RunID          int64
    TurnIndex      int
    Provider       string
    Model          string
    InputTokens    int64
    OutputTokens   int64
    CacheRead      int64
    CacheCreation  int64
    CostUSD        float64
    IdempotencyKey string
    CreatedAt      time.Time
}

type UsageLedger interface {
    Record(ctx context.Context, rec *UsageRecord) error
    Sum(ctx context.Context, runID int64) (*UsageRecord, error)  // aggregate
}

// artifact_meta.go
type ArtifactMeta struct {
    Ref        Ref
    MimeType   string
    SizeBytes  int64
    RunID      *int64
    TurnIndex  *int
    Caption    string
    RefCount   int64
    Tags       map[string]string
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

type ArtifactMetaStore interface {
    Put(ctx context.Context, meta *ArtifactMeta) error
    Get(ctx context.Context, ref Ref) (*ArtifactMeta, error)
    IncRefCount(ctx context.Context, ref Ref) error
    DecRefCount(ctx context.Context, ref Ref) error
}

// resume.go
type ResumeCoordinator interface {
    Resume(ctx context.Context, runID int64) (*Checkpoint, error)
    CanResume(ctx context.Context, runID int64) (bool, error)
}

// cas.go
func ComputeKey(data []byte) Ref  // 实现 sha256 计算（这个可以真写）
func ParseRef(s string) (algo string, hex string, err error)
```

**特殊说明**：`cas.go` 的 `ComputeKey` 函数**可以真实实现**（用 `crypto/sha256`），因为它是纯函数无副作用，作为第一个"有实际逻辑的代码"。

### 5.10 testing/（25）

```go
// compliance_runner.go
type ComplianceTest struct {
    ID          string  // C-01 / C-E-01 / ...
    Description string
    Category    string  // protocol / error / loop / security / ...
}

type ComplianceRunner interface {
    List() []ComplianceTest
    Run(ctx context.Context, testID string) (*TestResult, error)
    RunAll(ctx context.Context) (*ComplianceReport, error)
}

type TestResult struct {
    TestID     string
    Status     string // pass / fail / skipped
    DurationMS int64
    Error      string
}

type ComplianceReport struct {
    SDKLanguage    string
    SDKVersion     string
    KernelVersion  string
    ProtocolVersion string
    RunAt          time.Time
    Results        map[string]*TestResult
    Summary        Summary
}

type Summary struct {
    Total   int
    Passed  int
    Failed  int
    Skipped int
}

// cassette_recorder.go
type CassetteRecorder interface {
    Start(ctx context.Context, name string) error
    Record(ctx context.Context, event CassetteEvent) error
    Finish(ctx context.Context) error
}

type CassetteEvent struct {
    Type      string // llm_request / llm_response / tool_call / tool_result
    Timestamp time.Time
    Payload   json.RawMessage
}

// cassette_player.go
type CassettePlayer interface {
    Load(ctx context.Context, name string) error
    Next(ctx context.Context) (CassetteEvent, error)
    Rewind(ctx context.Context) error
}

// fake_sidecar.go
type FakeSidecar interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    SendFrame(ctx context.Context, frame interface{}) error
    ReceiveFrame(ctx context.Context) (interface{}, error)
}
```

### 5.11 kernel/（02 §12，主线）

```go
// kernel.go
type Kernel struct {
    // 持有所有 store / registry / provider 的引用
    PlanStore        persistence.PlanStore
    ArtifactStore    persistence.ArtifactStore
    RunCheckpoint    persistence.RunCheckpointStore
    UsageLedger      persistence.UsageLedger
    ToolRegistry     tool.Registry
    Vault            security.Vault
    AuditLogger      security.AuditLogger
    Metrics          observability.Registry
    Trace            observability.TraceExporter
    Logs             observability.LogExporter
}

func NewKernel(opts ...Option) *Kernel

type Option func(*Kernel)
// 各种 WithXxx 函数

// runner.go
type BrainRunner interface {
    Start(ctx context.Context, kind agent.Kind, desc agent.Descriptor) (agent.Agent, error)
    Stop(ctx context.Context, kind agent.Kind) error
}

// transport.go
type BrainTransport interface {
    Send(ctx context.Context, msg *protocol.RPCMessage) error
    Receive(ctx context.Context) (*protocol.RPCMessage, error)
    Close() error
}
```

---

## 6. 子 agent 任务规格（派发模板）

**主线发给每个子 agent 的提示词模板**：

```
你是 brain-skel-<subpkg> agent，负责在 /www/wwwroot/project/easymvp-claude/brain/<subpkg>/ 下写 Go 骨架代码。

必读（按这个顺序）：
1. /www/wwwroot/project/easymvp-claude/docs/next-gen-executor/brain骨架实施计划.md
   — 重点看 §4 代码风格约束 和 §5.<section-num> 你的子包规格速查

2. /www/wwwroot/project/easymvp-claude/docs/next-gen-executor/<规格文档>.md
   — 你的子包对应的原始规格（如有疑问以此为准）

任务：
- 在 brain/<subpkg>/ 下创建 <N> 个 .go 文件（具体列表见 §1.1 和 §3）
- 严格遵守 §4 的代码风格（panic 格式、import 前缀、命名、不引外部依赖）
- 每个导出类型/函数必须有 godoc 注释并包含规格引用
- 未实现方法体使用 panic("unimplemented: <spec-ref>")
- 某些纯函数可以真实实现（例如 cas.go 的 ComputeKey），但不强求
- 写完后自行运行：cd /www/wwwroot/project/easymvp-claude/admin-go && go build ./brain/<subpkg>/...
  必须编译通过才算完成

完成后在 200 字以内汇报：
- 创建了哪些文件
- 哪些方法/函数是真实实现、哪些是 panic stub
- 编译是否通过
- 有无对规格的疑问或建议修正的地方

严禁：
- 不得修改其他子包（brain/<其他目录>）下的任何文件
- 不得引入外部依赖（只用标准库）
- 不得写 TODO 以外的假数据
- 不得跳过编译验证
```

每个波次结束时，主线会：
1. 汇总所有子 agent 的汇报
2. 对整体跑一次 `go build ./brain/...` 验证交叉依赖
3. 如有编译错误，修正后提交下一波

---

## 7. 进度勾选表

**主线负责维护**。每完成一个文件打勾。

### 阶段 0：准备（已完成）

- [x] `brain/doc.go`
- [x] `brain/version.go`
- [x] `brain/VERSION.json`
- [x] `brain/agent/agent.go`
- [x] `docs/next-gen-executor/brain骨架实施计划.md`（本文档）

### 阶段 1：波 1 并行（6 个 agent）

- [x] `brain/errors/` — agent-errors
- [x] `brain/llm/` — agent-llm
- [x] `brain/tool/` — agent-tool
- [x] `brain/observability/` — agent-obs
- [x] `brain/security/` — agent-sec
- [x] `brain/cli/` — agent-cli-const
- [x] 波 1 集成 `go build ./brain/...` 通过

### 阶段 2：波 2 并行（4 个 agent）

- [x] `brain/protocol/` — agent-protocol
- [x] `brain/loop/` — agent-loop
- [x] `brain/persistence/` — agent-persist
- [x] `brain/testing/` — agent-test
- [x] 波 2 集成 `go build ./brain/...` 通过

### 阶段 3：波 3 主线

- [x] `brain/kernel/kernel.go`
- [x] `brain/kernel/runner.go`
- [x] `brain/kernel/transport.go`
- [x] `cmd/main.go`
- [x] `cmd/dispatcher.go`
- [x] `cmd/cmd_version.go`（完整实现）
- [x] `cmd/cmd_doctor.go`（完整实现）
- [x] `cmd/cmd_stub.go`（10 个占位子命令）
- [x] 波 3 集成 `go build ./brain/... ./cmd/brain/` 通过

### 阶段 4：运行验证

- [x] `./brain version`（human 输出）
- [x] `./brain version --short`（只输出版本号）
- [x] `./brain version --json`（结构化输出）
- [x] `./brain doctor`（8 项检查，1 项实跑 + 7 项 skipped，退出 0）
- [x] `./brain unknown-cmd`（退出码 64）
- [x] `./brain --help`（usage 打印）
- [x] `./brain run`（stub 子命令退出码 70）

### 阶段 5：汇报

- [x] 写 F 完成报告（文件清单、编译状态、实际运行输出、后续建议）

---

## 8. 决策日志

> 任何偏离本文档原计划的决策在这里记录，不覆盖原文。

- **2026-04-11 · 初版** · 采用 Agent Teams 3 波并行策略
- **2026-04-11 · cas.go 允许真实实现** · §5.9 特殊说明：`ComputeKey` 因为是纯函数无副作用，允许波 2 的 agent-persist 真写而不是 panic stub
- **2026-04-11 · cmd/brain/ 独立路径** · §1.1 注释：`cmd/brain/` 放在 `cmd/` 而不是 `brain/cmd/`，遵循 Go 惯例
- **2026-04-11 · 零外部依赖铁律** · §4.6：波 1-3 全程不引入 cobra / otel-go / zap，全部使用标准库；将来有需要再通过 minor bump 引入
- **2026-04-12 · v0.1.0 冻结** · 计划 §7 所有勾选项完成；go build / go vet / go test ./brain/... -race 全部 clean；cmd/brain version · version --short · version --json · doctor · run --prompt · unknown-cmd · --help · 10 个 stub 子命令行为全部符合 27-CLI命令契约.md §16 §17。实际交付超出骨架计划：agent-persist/agent-loop/agent-obs/agent-sec/agent-llm/agent-tool/agent-protocol/agent-test 在 panic stub 之外补上了 Mem* 参考实现、MockProvider、hash_chain_audit、file_cassette、run cmd，供 kernel.NewMemKernel 一站式装配，让 `brain run` / `brain doctor` 在无外部依赖的前提下可真跑端到端最小 Run。新增文件未纳入 §1.1 清单但 go 包拓扑仍然成立——延续 §4.6 铁律，零新外部依赖。

---

## 9. 恢复指南（如果对话中断）

如果你看到这份文档是因为之前的对话被打断或上下文被压缩：

1. **先读 §0 ~ §4**：理解计划的目的、产物、分工、风格约束
2. **读 §7 进度勾选表**：看到哪些项目已经打勾，确定当前阶段
3. **读 §5 里**对应当前波次的子包规格速查
4. **检查实际文件状态**：`ls -R /www/wwwroot/project/easymvp-claude/brain/`
   对照 §1.1 的目录结构，找出已创建和缺失的文件
5. **继续执行**：按 §7 的顺序推进到下一个未打勾项
6. **不确定时**：读对应子包的原始规格文档（`docs/next-gen-executor/XX-*.md`）
7. **决策变更**：任何偏离原计划的决策都要记到 §8 决策日志

**关键原则**：不要重新设计、不要改变分工、不要引入新依赖。本文档定义的是"一次性把骨架写完的最短路径"，任何绕路都会增加总时长。

---

## 10. 完成定义

本计划达成"完成"状态的硬指标：

1. §7 所有勾选项打勾
2. `cd admin-go && go build ./brain/... ./cmd/brain/` 零警告通过
3. `cd admin-go && go vet ./brain/...` 零警告通过（推荐）
4. 实际运行 `./brain version` 输出符合 27 §17.2 human 格式
5. 实际运行 `./brain version --json` 输出符合 27 §17.3 JSON schema
6. 实际运行 `./brain doctor` 至少 6/8 项检查有返回（其中网络相关 2 项可为 skipped）
7. F 完成报告已发给用户

达成后可选：
- 本计划文档是"留还是删"由用户决定；建议保留作为"如何起 Go SDK 骨架"的方法论参考（按 CLAUDE.md 十五的长期保留策略）

---

## 版本历史

| 版本 | 日期 | 变更 |
|------|------|------|
| v1.0 | 2026-04-11 | 首版：Agent Teams 3 波并行策略 + 60 文件清单 + 11 子包接口速查 + 风格约束 + 进度勾选表 + 恢复指南 |
| v1.1 | 2026-04-12 | v0.1.0 冻结：§7 全勾 + §8 追加冻结记录；实际交付 99 文件（含 Mem*/Mock/Test 真实实现） |
