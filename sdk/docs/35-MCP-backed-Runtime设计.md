# 35. MCP-backed Runtime 设计

> **状态**：v1 · 2026-04-16
> **对应路线**：[32-v3-Brain架构.md](./32-v3-Brain架构.md) §6 Phase B-7
> **依赖规格**：[20-协议规格.md](./20-协议规格.md) / [33-Brain-Manifest规格.md](./33-Brain-Manifest规格.md) / [35-BrainPool实现设计.md](./35-BrainPool实现设计.md)
> **代码基础**：`sdk/kernel/mcpadapter/adapter.go`（已有）
> **实现目标**：`sdk/kernel/mcphost/`（新增），第一个 mcp-backed brain 参考实现

---

## 0. 定位与设计原则

### 0.1 MCP 在 v3 中的位置

```text
┌──────────────────────────────────────────────────────────────┐
│  用户 / Central Brain                                         │
│                                                               │
│    delegate ──────────────────────────────────▶ MCP Brain    │
│                                          （brain sidecar 进程）│
│                                                    │          │
│                                        ┌───────────▼────────┐ │
│                                        │  MCP Brain Host    │ │
│                                        │  ┌──────────────┐  │ │
│                                        │  │ Agent Loop   │  │ │
│                                        │  │ LLM + Tools  │  │ │
│                                        │  └──────┬───────┘  │ │
│                                        │         │           │ │
│                                        │  mcpadapter × N    │ │
│                                        └──────────┬─────────┘ │
│                                                   │           │
│                                     MCP transport (stdio)     │
│                                                   │           │
│                              ┌────────────────────▼──────────┐│
│                              │  MCP Server 1  MCP Server 2  ││
│                              │  (npx/binary)  (npx/binary)  ││
│                              └───────────────────────────────┘│
└──────────────────────────────────────────────────────────────┘
```

三个不可混淆的层级：

| 层级 | 对象 | 身份 |
|------|------|------|
| 顶层产品对象 | `Brain`（mcp-backed runtime） | Central 的 delegate 目标 |
| 中间层 | MCP Brain Host | Brain sidecar 进程内部的运行时宿主 |
| 底层 | MCP Server | capability backend，**不是** brain |

### 0.2 核心约束（Delegate 语义冻结规则 #3）

> **MCP Server 不能直接成为 delegate 目标。**

`central` 只 delegate 给 `brain`。`brain/execute` 是唯一执行入口。MCP Server 是 brain host 进程内部的能力后端，对外不可见。

这不是实现细节，是不可打破的架构约束。违反它会导致授权边界崩溃、健康检查语义混乱、Lease 管理无法收敛。

### 0.3 设计目标

1. **完整性**：mcp-backed brain 在 BrainPool、Dispatch、LeaseManager、Dashboard 中的行为与 native brain 一致
2. **透明性**：调用方（Central/turn_executor）无需知道 brain 内部是否用了 MCP
3. **可观测性**：每个 MCP server 连接状态、工具调用延迟、错误率可见
4. **故障隔离**：单个 MCP server 崩溃不影响 brain 其他工具的正常工作

---

## 1. MCP Brain Host 架构

### 1.1 进程内部结构

mcp-backed brain 与 native brain 的**对外接口完全相同**：都是通过 stdio 与 BrainPool 通信的 sidecar 进程，都实现 `brain/execute`、`brain/tools_list`、`ping` 等协议方法。

差异在进程内部：

```text
┌─────────────────────────────────────────────────────────────┐
│  MCP Brain Sidecar 进程                                      │
│                                                             │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  stdio 传输层（与 BrainPool / Kernel 通信）            │  │
│  │  FrameReader / FrameWriter / BidirRPC               │  │
│  └───────────────────────┬──────────────────────────────┘  │
│                          │                                  │
│  ┌───────────────────────▼──────────────────────────────┐  │
│  │  BrainHost                                           │  │
│  │                                                      │  │
│  │  ┌──────────────────────────────────────────────┐   │  │
│  │  │  Agent Loop（loop.Runner）                    │   │  │
│  │  │  LLM 推理 + 工具调度                           │   │  │
│  │  └──────────────────────────────────────────────┘   │  │
│  │                                                      │  │
│  │  ┌──────────────────────────────────────────────┐   │  │
│  │  │  MCPToolRegistry（tool.Registry 扩展）        │   │  │
│  │  │  持有所有 MCP server 注册的工具               │   │  │
│  │  └──────────────────────────────────────────────┘   │  │
│  │                                                      │  │
│  │  ┌──────────────────────────────────────────────┐   │  │
│  │  │  AdapterManager                              │   │  │
│  │  │  管理 N 个 mcpadapter.Adapter 实例            │   │  │
│  │  └──────────────────────────────────────────────┘   │  │
│  └──────────────────────────────────────────────────────┘  │
│                                                             │
│  子进程 / 子 goroutine：                                     │
│  ┌─────────────────┐  ┌─────────────────┐  ┌────────────┐  │
│  │ MCP Server 1    │  │ MCP Server 2    │  │ MCP Srv N  │  │
│  │ (exec/npx)      │  │ (exec/npx)      │  │ (exec/npx) │  │
│  │ stdio pipe      │  │ stdio pipe      │  │ stdio pipe │  │
│  └─────────────────┘  └─────────────────┘  └────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### 1.2 BrainHost 核心结构

```go
// BrainHost 是 mcp-backed brain 的运行时宿主。
// 位置：sdk/kernel/mcphost/host.go
//
// 职责：
//  1. 启动并管理 N 个 MCP server 进程（通过 AdapterManager）
//  2. 把 MCP tools 注册进 MCPToolRegistry
//  3. 驱动 Agent Loop（LLM 推理 + 工具路由）
//  4. 响应 Kernel 的 brain/execute、brain/tools_list、ping 方法
type BrainHost struct {
    // manifest 来自 brain 启动时加载的配置
    manifest BrainManifest

    // adapterMgr 管理所有 MCP server 连接
    adapterMgr *AdapterManager

    // registry 持有从 MCP server 发现的所有工具
    registry *MCPToolRegistry

    // loop 是 Agent Loop，负责 LLM 推理和工具分发
    loop *loop.Runner

    // transport 是与 Kernel/BrainPool 通信的 stdio 传输
    transport protocol.BidirRPC

    // healthCh 接收各 adapter 的健康事件
    healthCh chan AdapterHealthEvent

    // ctx / cancel 管理整个 host 的生命周期
    ctx    context.Context
    cancel context.CancelFunc

    mu      sync.RWMutex
    started bool
}
```

### 1.3 AdapterManager 结构

```go
// AdapterManager 管理 N 个 mcpadapter.Adapter 的生命周期。
// 位置：sdk/kernel/mcphost/adapter_manager.go
type AdapterManager struct {
    adapters map[string]*managedAdapter // key = mcp_server.name
    mu       sync.RWMutex
}

type managedAdapter struct {
    name    string
    adapter *mcpadapter.Adapter
    spec    MCPServerSpec
    status  AdapterStatus
    retries int
    mu      sync.Mutex
}

type AdapterStatus int

const (
    AdapterStarting AdapterStatus = iota
    AdapterReady
    AdapterDegraded  // 部分工具不可用，brain 继续服务
    AdapterFailed    // 完全不可用，等待重连
    AdapterStopped
)
```

---

## 2. Manifest 中的 mcp-backed Runtime 声明

### 2.1 完整 Manifest 格式

```json
{
  "schema_version": 1,
  "kind": "filesystem",
  "name": "Filesystem Brain",
  "brain_version": "1.0.0",
  "description": "Provides file system read/write capabilities via MCP",
  "capabilities": ["fs.read", "fs.write", "fs.search"],
  "task_patterns": ["read file", "write file", "list directory", "search files"],
  "runtime": {
    "type": "mcp-backed",
    "entrypoint": "bin/brain-mcp-host",
    "mcp_servers": [
      {
        "name": "filesystem",
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
        "env": {
          "NODE_ENV": "production"
        },
        "tool_prefix": "mcp.fs.",
        "startup_timeout_ms": 10000,
        "health_check_interval_ms": 30000,
        "restart_policy": "on-failure",
        "max_restarts": 3
      }
    ]
  },
  "policy": {
    "approval_class": "workspace-write",
    "tool_scope": "delegate.filesystem"
  },
  "health": {
    "startup_timeout_ms": 15000,
    "ping_interval_ms": 30000
  }
}
```

### 2.2 mcp_servers 字段规格

```go
// MCPServerSpec 描述 manifest 中一个 MCP server 的声明。
// 位置：sdk/kernel/mcphost/manifest.go
type MCPServerSpec struct {
    // Name 是这个 MCP server 的逻辑名，在 brain 内唯一。
    // 用于日志、metrics、健康检查报告。
    Name string `json:"name"`

    // Command 是要执行的命令（绝对路径或 PATH 中可查找到的命令）。
    Command string `json:"command"`

    // Args 是传给 Command 的参数列表。
    Args []string `json:"args"`

    // Env 是额外注入的环境变量。nil 则继承父进程环境。
    Env map[string]string `json:"env,omitempty"`

    // ToolPrefix 是注册到工具注册表时的前缀。
    // 例如 "mcp.fs." 会使 MCP 工具 "read_file" 变成 "mcp.fs.read_file"。
    // 不填则默认为 "mcp.<name>."。
    ToolPrefix string `json:"tool_prefix,omitempty"`

    // StartupTimeoutMs 是等待 MCP server 完成 initialize 握手的超时。
    // 默认 10000（10秒）。
    StartupTimeoutMs int `json:"startup_timeout_ms,omitempty"`

    // HealthCheckIntervalMs 是健康检查 ping 的间隔。
    // 默认 30000（30秒）。
    HealthCheckIntervalMs int `json:"health_check_interval_ms,omitempty"`

    // RestartPolicy 决定 MCP server 崩溃时的行为。
    // 可选值：never / on-failure / always
    // 默认 "on-failure"。
    RestartPolicy string `json:"restart_policy,omitempty"`

    // MaxRestarts 是最大自动重启次数。超过后 adapter 进入 Failed 状态。
    // 默认 3。
    MaxRestarts int `json:"max_restarts,omitempty"`
}
```

### 2.3 多 MCP server 声明示例（浏览器场景）

```json
{
  "schema_version": 1,
  "kind": "browser-mcp",
  "name": "Browser MCP Brain",
  "brain_version": "1.0.0",
  "capabilities": ["web.browse", "web.extract", "web.screenshot"],
  "runtime": {
    "type": "mcp-backed",
    "entrypoint": "bin/brain-mcp-host",
    "mcp_servers": [
      {
        "name": "puppeteer",
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-puppeteer"],
        "tool_prefix": "mcp.puppeteer.",
        "restart_policy": "on-failure",
        "max_restarts": 5
      },
      {
        "name": "fetch",
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-fetch"],
        "tool_prefix": "mcp.fetch.",
        "restart_policy": "always"
      }
    ]
  },
  "policy": {
    "approval_class": "external-network"
  }
}
```

### 2.4 Manifest 校验规则

BrainHost 启动时必须校验以下内容：

| 字段 | 校验规则 | 失败行为 |
|------|---------|---------|
| `runtime.type` | 必须是 `"mcp-backed"` | 拒绝启动 |
| `mcp_servers` | 至少有 1 个元素 | 拒绝启动 |
| `mcp_servers[].name` | 在列表内唯一，非空 | 拒绝启动 |
| `mcp_servers[].command` | 非空字符串 | 拒绝启动 |
| `mcp_servers[].tool_prefix` | 如为空则自动填充 `"mcp.<name>."` | 自动修复 |
| `mcp_servers[].restart_policy` | 枚举值校验 | 默认 `"on-failure"` |
| `mcp_servers[].max_restarts` | >= 0 | 默认 `3` |

---

## 3. 工具发现与注册

### 3.1 启动序列

```text
BrainHost.Start()
  │
  ├── 1. 并行启动所有 MCP server（AdapterManager.StartAll）
  │      ├── Adapter.Start() → exec command → stdio pipes
  │      ├── MCP initialize 握手（protocol version, clientInfo）
  │      └── notifications/initialized 通知
  │
  ├── 2. 工具发现（并行）
  │      └── Adapter.DiscoverTools() → "tools/list" → []MCPToolSpec
  │
  ├── 3. 工具注册（MCPToolRegistry.RegisterFromAdapter）
  │      ├── 添加 ToolPrefix 前缀
  │      ├── 转换 MCP inputSchema → tool.Schema
  │      ├── 为每个工具创建 ToolConcurrencySpec（默认规则）
  │      └── 注册进 MCPToolRegistry
  │
  ├── 4. 上报工具列表给 Kernel（brain/tools_list 响应就绪）
  │
  └── 5. 启动健康检查 goroutine（per adapter）
```

### 3.2 MCPToolSpec → tool.Schema 转换

MCP server 的 `tools/list` 响应格式（标准 MCP 协议）：

```json
{
  "tools": [
    {
      "name": "read_file",
      "description": "Read the contents of a file",
      "inputSchema": {
        "type": "object",
        "properties": {
          "path": { "type": "string", "description": "File path to read" }
        },
        "required": ["path"]
      }
    }
  ]
}
```

转换规则：

```go
// convertMCPTool 将 MCPToolSpec 转换为 brain 的 tool.Schema。
// 位置：sdk/kernel/mcphost/converter.go
func convertMCPTool(spec mcpadapter.MCPToolSpec, prefix string, brainKind string) tool.Schema {
    return tool.Schema{
        // MCP 工具名加前缀。
        // 例如："read_file" → "mcp.fs.read_file"
        Name: prefix + spec.Name,

        // 直接使用 MCP 描述，可能为空。
        Description: spec.Description,

        // MCP inputSchema 与 JSON Schema draft 兼容，直接使用。
        // 若为 nil，则填充空 object schema。
        InputSchema: coalesceSchema(spec.InputSchema),

        // Brain 字段标记为 mcp-backed brain 的 kind。
        Brain: brainKind,
    }
}

func coalesceSchema(s json.RawMessage) json.RawMessage {
    if len(s) == 0 {
        return json.RawMessage(`{"type":"object","properties":{}}`)
    }
    return s
}
```

### 3.3 ToolConcurrencySpec 自动生成

mcp-backed brain 无法要求所有第三方 MCP server 预先声明 ToolConcurrencySpec。因此 BrainHost 为 MCP 工具按**保守策略**自动生成：

```go
// defaultMCPToolConcurrencySpec 为 MCP 工具生成默认的并发规格。
// 策略：SharedRead（最保守）+ ScopeTurn（最短 scope）。
// 理由：MCP 工具的资源使用是未知的，保守策略防止数据竞争。
// brain 开发者可通过 mcp_servers[].concurrency_overrides 覆盖。
func defaultMCPToolConcurrencySpec(toolName string, prefix string) tool.ToolConcurrencySpec {
    return tool.ToolConcurrencySpec{
        Capability:          "mcp.tool",
        ResourceKeyTemplate: "mcp:" + prefix + "*",
        AccessMode:          tool.SharedRead,
        Scope:               tool.ScopeTurn,
        AcquireTimeout:      5 * time.Second,
    }
}
```

**覆盖机制**：manifest 中可以为具体工具声明 concurrency override：

```json
{
  "mcp_servers": [
    {
      "name": "filesystem",
      "tool_prefix": "mcp.fs.",
      "concurrency_overrides": [
        {
          "tool": "write_file",
          "capability": "fs.write",
          "resource_key_template": "workdir:{{path}}",
          "access_mode": "exclusive-write",
          "scope": "turn"
        }
      ]
    }
  ]
}
```

### 3.4 工具命名冲突处理

当多个 MCP server 注册了同名工具时（加前缀后），BrainHost 的处理策略：

```go
// RegisterFromAdapter 注册来自单个 adapter 的所有工具。
// 若工具名已存在，记录 WARNING 并跳过（不覆盖），保证先启动的 server 优先。
func (r *MCPToolRegistry) RegisterFromAdapter(
    adapterName string,
    schemas []tool.Schema,
) (registered int, skipped int, err error) {
    r.mu.Lock()
    defer r.mu.Unlock()
    for _, schema := range schemas {
        if _, exists := r.tools[schema.Name]; exists {
            r.logger.Warn("MCP tool name conflict, skipping",
                "tool", schema.Name,
                "adapter", adapterName)
            skipped++
            continue
        }
        r.tools[schema.Name] = &mcpToolEntry{schema: schema, adapter: adapterName}
        registered++
    }
    return
}
```

---

## 4. mcpadapter 完整路由逻辑

### 4.1 tool_call 路由路径

```text
Central Brain
  │
  │  brain/execute（SubtaskRequest）
  ▼
BrainHost（stdio 接收）
  │
  │  Agent Loop turn → LLM 返回 tool_use
  ▼
turn_executor.dispatchTools()
  │
  ├── 从 MCPToolRegistry 查找工具
  │      └── registry.Lookup(toolName) → (MCPTool, adapterName)
  │
  ├── 从 tool.Schema 读取 ToolConcurrencySpec
  │      └── 推导 LeaseRequest
  │
  ├── AcquireSet（LeaseManager，如果 host 内集成了 lease）
  │
  └── MCPTool.Execute(ctx, args)
         │
         ▼
      MCPTool.adapter.Invoke(ctx, mcpName, args)
         │
         │  tools/call 请求（JSON-RPC over stdio）
         ▼
      MCP Server 进程
         │
         │  MCPToolResult（content blocks + isError）
         ▼
      adapter.Invoke 返回 *tool.Result
         │
         ▼
      turn_executor 收集结果 → 回填 tool_result 消息
         │
         ▼
      下一个 LLM turn（或结束）
```

### 4.2 Invoke 详细流程

```go
// Invoke 是 tool_call 路由的核心方法。
// 位置：sdk/kernel/mcpadapter/adapter.go（已有，本节补充说明）
func (a *Adapter) Invoke(ctx context.Context, toolName string, args json.RawMessage) (*tool.Result, error) {
    // 1. 状态检查（非阻塞）
    a.mu.Lock()
    if !a.started || a.shutdown {
        a.mu.Unlock()
        return nil, brainerrors.New(brainerrors.CodeShuttingDown,
            brainerrors.WithMessage("mcpadapter: not available"))
    }
    rpc := a.rpc
    a.mu.Unlock()

    // 2. 构造 MCP tools/call 请求
    //    注意：toolName 是 MCP 原始名（无前缀），由 MCPTool 持有
    callReq := map[string]interface{}{
        "name":      toolName,  // 无前缀的原始 MCP 工具名
        "arguments": args,      // 直接透传 LLM 生成的参数（已通过 InputSchema 验证）
    }

    // 3. 发送 JSON-RPC 请求，等待响应
    var result mcpadapter.MCPToolResult
    if err := rpc.Call(ctx, "tools/call", callReq, &result); err != nil {
        return nil, brainerrors.Wrap(err, brainerrors.CodeToolExecutionFailed,
            brainerrors.WithMessage(fmt.Sprintf("mcpadapter: tools/call %s failed", toolName)))
    }

    // 4. 聚合 content blocks
    //    当前版本：只聚合 text 类型
    //    未来版本：支持 image、resource 等类型
    output, _ := json.Marshal(aggregateContent(result.Content))

    return &tool.Result{
        Output:  output,
        IsError: result.IsError,
    }, nil
}

// aggregateContent 将 MCP content blocks 聚合为单个字符串。
// text 类型直接拼接；其他类型记录 WARN 并跳过（Phase 1 限制）。
func aggregateContent(blocks []mcpadapter.MCPContent) string {
    var sb strings.Builder
    for _, b := range blocks {
        switch b.Type {
        case "text":
            sb.WriteString(b.Text)
        case "image":
            // Phase 2 支持，当前以 placeholder 替代
            sb.WriteString("[image content not supported in text mode]")
        case "resource":
            // Phase 2 支持
            sb.WriteString(fmt.Sprintf("[resource: %s]", b.Text))
        }
    }
    return sb.String()
}
```

### 4.3 参数验证策略

MCP server 的 inputSchema 在工具发现时已存入 tool.Schema。Agent Loop 在调用工具前会对 LLM 生成的参数做 schema 验证：

```go
// validateMCPArgs 在工具调用前验证 LLM 生成的参数是否符合 inputSchema。
// 位置：sdk/kernel/mcphost/validator.go
func validateMCPArgs(schema json.RawMessage, args json.RawMessage) error {
    // Phase B-7 实现：使用 jsonschema 库做 draft 2020-12 验证
    // Phase A 的最简实现：只检查 required 字段是否存在
    var schemaDef struct {
        Required []string `json:"required"`
    }
    if err := json.Unmarshal(schema, &schemaDef); err != nil {
        return nil // schema 解析失败，放行（不阻塞工具调用）
    }
    var argMap map[string]json.RawMessage
    if err := json.Unmarshal(args, &argMap); err != nil {
        return fmt.Errorf("args is not a JSON object: %w", err)
    }
    for _, req := range schemaDef.Required {
        if _, ok := argMap[req]; !ok {
            return fmt.Errorf("missing required field: %s", req)
        }
    }
    return nil
}
```

### 4.4 错误分类与映射

MCP 工具调用可能出现多种错误，需要映射到 Brain 错误体系：

| 错误场景 | MCP 层表现 | Brain 错误码 | 处理方式 |
|---------|-----------|-------------|---------|
| MCP server 进程已退出 | rpc.Call 返回 io.EOF | `CodeShuttingDown` | 触发 adapter 重连，向 LLM 返回工具错误 |
| MCP server 返回 isError=true | MCPToolResult.IsError = true | 无错误码，透传 | tool.Result.IsError=true，由 LLM 决定后续 |
| JSON-RPC 超时（ctx.Done） | rpc.Call 返回 context.DeadlineExceeded | `CodeToolExecutionFailed` | 向 LLM 返回超时错误 |
| tools/call 不存在的工具名 | MCP server 返回 JSON-RPC error | `CodeToolNotFound` | 记录错误，向 LLM 返回工具不存在 |
| args 不符合 schema | validateMCPArgs 返回错误 | `CodeInvalidRequest` | 拒绝调用，向 LLM 返回参数错误 |
| adapter 未启动 | started=false | `CodeInvariantViolated` | 内部错误，不应暴露给 LLM |

---

## 5. MCP Brain 在 BrainPool 中的行为

### 5.1 复用策略选择

mcp-backed brain 的进程复用策略取决于其语义，不是固定的：

| 业务场景 | 推荐策略 | 理由 |
|---------|---------|-----|
| 无状态文件系统访问（filesystem brain） | `shared-service` | MCP server 无会话状态，多 task 共享效率最高 |
| 有会话的浏览器操作（browser-mcp brain） | `exclusive-session` | puppeteer MCP server 持有浏览器会话，需要独占 |
| 按需临时计算（git-mcp brain） | `ephemeral-worker` | 用完即可回收，冷启动代价低 |

manifest 中通过 `policy.pool_strategy` 声明（如未声明则 BrainPool 按 `shared-service` 默认处理）：

```json
{
  "policy": {
    "pool_strategy": "shared-service"
  }
}
```

### 5.2 BrainPool 中 mcp-backed brain 的注册

```go
// RegisterMCPBrain 将 mcp-backed brain 注册进 BrainPool。
// 与 native brain 注册的区别：BrainRunner 内部会启动 BrainHost，
// BrainHost 负责启动 MCP server 子进程。
// 位置：sdk/kernel/pool.go（扩展）
func (p *brainPool) RegisterMCPBrain(manifest MCPBrainManifest) error {
    spec := BrainPoolRegistration{
        Kind:     agent.Kind(manifest.Kind),
        Strategy: parsePoolStrategy(manifest.Policy.PoolStrategy),
        // Launcher 启动 BrainHost 进程（而非直接的工具 sidecar）
        Launcher: func(ctx context.Context) (protocol.BidirRPC, error) {
            return launchMCPBrainHost(ctx, manifest)
        },
        // HealthProber 通过 ping 探测 BrainHost，BrainHost 内部聚合所有 MCP server 状态
        HealthProber: defaultPingProber,
        Config: BrainPoolConfig{
            StartupTimeout:      time.Duration(manifest.Health.StartupTimeoutMs) * time.Millisecond,
            HealthCheckInterval: 30 * time.Second,
            IdleTimeout:         5 * time.Minute,
        },
    }
    return p.Register(spec)
}
```

### 5.3 shared-service 策略下的行为

```text
BrainPool                     MCP Brain Host                MCP Server
    │                              │                             │
    │── GetBrain(filesystem) ─────▶│                             │
    │                              │  (已就绪，直接返回)           │
    │◀─ BidirRPC handle ───────────│                             │
    │                              │                             │
    │── GetBrain(filesystem) ─────▶│                             │
    │  （另一个 task）               │  (共享同一进程)              │
    │◀─ 同一个 BidirRPC handle ────│                             │
    │                              │                             │
    │  两个 task 并发调用工具：       │                             │
    │  task-1: mcp.fs.read_file   │──── tools/call read_file ──▶│
    │  task-2: mcp.fs.list_dir    │──── tools/call list_dir ───▶│
    │                              │                             │
    │                              │◀─── result (read_file) ─────│
    │                              │◀─── result (list_dir) ──────│
```

**重要**：BidirRPC 本身支持并发 in-flight 请求（JSON-RPC ID 复用），所以 shared-service 下多个 task 共享同一个 BrainHost 进程时，工具调用天然是并发安全的。并发控制（需要时）仍由 Capability Lease 负责，不在 BrainPool 层做。

### 5.4 exclusive-session 策略下的行为

```text
BrainPool                     MCP Brain Host (browser-mcp)
    │                              │
    │── GetBrain(browser-mcp) ────▶│  ← task-1 获取，持有 ExclusiveSession lease
    │◀─ BidirRPC handle ───────────│
    │                              │
    │── GetBrain(browser-mcp) ────▶│  ← task-2 等待（pool 层 waiter channel）
    │  （另一个 task）               │
    │  [阻塞，直到 task-1 完成]      │
    │                              │
    │── ReturnBrain(browser-mcp) ─▶│  ← task-1 完成归还
    │                              │
    │◀─ BidirRPC handle ───────────│  ← task-2 获取成功
```

### 5.5 ephemeral-worker 策略下的行为

```text
BrainPool                     MCP Brain Host 实例池
    │                              │
    │                         [idle 池: 2 个预热实例]
    │── GetBrain(git-mcp) ────────▶│  ← 从 idle 池取一个
    │◀─ BidirRPC handle ───────────│
    │                              │
    │  task 完成后：                 │
    │── ReturnBrain(git-mcp) ──────▶│  ← 归还到 idle 池
    │                              │
    │  空闲超过 5 分钟时：             │
    │                              │──── 回收（Stop MCP server 子进程）
```

---

## 6. Dispatch/Lease 层的差异

### 6.1 MCP 工具的 ToolConcurrencySpec 生成

当 Manifest 没有声明 concurrency_overrides 时，BrainHost 使用**自动推导规则**为 MCP 工具生成 ToolConcurrencySpec：

```go
// inferMCPToolConcurrencySpec 根据工具名和 inputSchema 推导并发规格。
// 推导依据：
//   1. 工具名中包含 write/create/delete/update → ExclusiveWrite
//   2. 工具名中包含 read/get/list/search/query → SharedRead
//   3. 无法判断 → SharedRead（保守策略）
//
// ResourceKeyTemplate 推导：
//   1. inputSchema 中有 "path" 字段 → "workdir:{{path}}"
//   2. inputSchema 中有 "url" 字段 → "url:{{url}}"
//   3. 无法推导 → "mcp:<prefix>*"（宽泛的 key，等效于 brain 级锁）
func inferMCPToolConcurrencySpec(
    toolName string,
    inputSchema json.RawMessage,
    prefix string,
) tool.ToolConcurrencySpec {
    accessMode := inferAccessMode(toolName)
    resourceKey := inferResourceKey(inputSchema, prefix)

    return tool.ToolConcurrencySpec{
        Capability:          "mcp." + sanitizeName(prefix),
        ResourceKeyTemplate: resourceKey,
        AccessMode:          accessMode,
        Scope:               tool.ScopeTurn,
        AcquireTimeout:      5 * time.Second,
    }
}

func inferAccessMode(name string) tool.AccessMode {
    lower := strings.ToLower(name)
    for _, kw := range []string{"write", "create", "delete", "update", "move", "rename", "patch"} {
        if strings.Contains(lower, kw) {
            return tool.ExclusiveWrite
        }
    }
    return tool.SharedRead
}

func inferResourceKey(schema json.RawMessage, prefix string) string {
    var s struct {
        Properties map[string]json.RawMessage `json:"properties"`
    }
    if err := json.Unmarshal(schema, &s); err != nil {
        return "mcp:" + prefix + "*"
    }
    if _, ok := s.Properties["path"]; ok {
        return "workdir:{{path}}"
    }
    if _, ok := s.Properties["url"]; ok {
        return "url:{{url}}"
    }
    return "mcp:" + prefix + "*"
}
```

### 6.2 LeaseRequest 推导示例

| MCP 工具名 | inputSchema 有何字段 | 推导的 AccessMode | 推导的 ResourceKey |
|-----------|--------------------|-----------------|--------------------|
| `read_file` | `path` | SharedRead | `workdir:{{path}}` |
| `write_file` | `path` | ExclusiveWrite | `workdir:{{path}}` |
| `delete_file` | `path` | ExclusiveWrite | `workdir:{{path}}` |
| `list_directory` | `path` | SharedRead | `workdir:{{path}}` |
| `fetch_url` | `url` | SharedRead | `url:{{url}}` |
| `navigate` | `url` | ExclusiveWrite（含 navigate） | `url:{{url}}` |
| `search_files` | `query`, `path` | SharedRead | `workdir:{{path}}` |

### 6.3 turn_executor 处理 MCP 工具的流程

```text
LLM 返回 tool_use 列表（包含 MCP 工具）：
  call_1: mcp.fs.read_file(path="/tmp/a.txt")
  call_2: mcp.fs.read_file(path="/tmp/b.txt")
  call_3: mcp.fs.write_file(path="/tmp/c.txt", content="...")

BatchPlanner（冲突分析）：
  call_1：SharedRead，ResourceKey = "workdir:/tmp/a.txt"
  call_2：SharedRead，ResourceKey = "workdir:/tmp/b.txt"
  call_3：ExclusiveWrite，ResourceKey = "workdir:/tmp/c.txt"

冲突图：
  call_1 ↔ call_2：不同 ResourceKey → 不冲突
  call_1 ↔ call_3：不同 ResourceKey → 不冲突
  call_2 ↔ call_3：不同 ResourceKey → 不冲突
  → 全部并行！

Batch 1（并行）：call_1 + call_2 + call_3
  AcquireSet([SharedRead workdir:/tmp/a.txt, SharedRead workdir:/tmp/b.txt,
              ExclusiveWrite workdir:/tmp/c.txt])
  ↓
  并行调用 3 个 MCP 工具
  ↓
  结果按原顺序回填 tool_result
```

---

## 7. 健康检查机制

### 7.1 双层健康检查

mcp-backed brain 的健康检查分两层：

```text
BrainPool 层（外层）
  └── 每 30s 向 BrainHost 发送 ping
       └── BrainHost 响应 pong（聚合内部 MCP server 状态）

BrainHost 内层（per-adapter）
  └── 每 30s 向每个 MCP server 发送 ping
       └── MCP ping：发送空的 tools/list 请求（复用现有连接）
```

### 7.2 MCP Server Ping 实现

MCP 协议没有标准 ping 方法。BrainHost 使用如下 ping 策略：

```go
// pingMCPServer 对 MCP server 做一次轻量健康探测。
// 策略：发送 tools/list 请求（通常很快返回），超时视为不健康。
// 不使用 JSON-RPC ping，因为大多数 MCP server 不实现该方法。
func (a *managedAdapter) ping(ctx context.Context) error {
    pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
    defer cancel()

    var result struct {
        Tools []json.RawMessage `json:"tools"`
    }
    if err := a.adapter.RPC().Call(pingCtx, "tools/list", map[string]interface{}{}, &result); err != nil {
        return fmt.Errorf("MCP ping failed: %w", err)
    }
    return nil
}
```

### 7.3 重连策略

```go
// reconnectLoop 是每个 adapter 的重连主循环。
// 位置：sdk/kernel/mcphost/adapter_manager.go
func (m *managedAdapter) reconnectLoop(ctx context.Context, spec MCPServerSpec) {
    backoff := 1 * time.Second
    maxBackoff := 60 * time.Second

    for attempt := 0; attempt <= spec.MaxRestarts; attempt++ {
        if ctx.Err() != nil {
            return // BrainHost 已关闭
        }

        if attempt > 0 {
            // 指数退避 + ±20% 抖动
            jitter := time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64()))
            select {
            case <-time.After(jitter):
            case <-ctx.Done():
                return
            }
            if backoff < maxBackoff {
                backoff *= 2
            }
        }

        m.setStatus(AdapterStarting)
        if err := m.adapter.Start(ctx); err != nil {
            m.logger.Error("MCP server restart failed",
                "adapter", m.name, "attempt", attempt, "error", err)
            m.setStatus(AdapterFailed)
            continue
        }

        // 重新发现工具（MCP server 重启后工具集可能变化）
        schemas, err := m.adapter.DiscoverTools(ctx)
        if err != nil {
            m.logger.Error("MCP tool re-discovery failed", "adapter", m.name, "error", err)
            m.setStatus(AdapterDegraded)
            continue
        }

        // 更新工具注册表（替换旧工具）
        m.registry.ReRegisterAdapter(m.name, schemas)
        m.setStatus(AdapterReady)
        m.retries = attempt

        // 等待 adapter 再次失败
        <-m.adapter.DoneCh() // Adapter.DoneCh() 在 adapter 关闭时关闭
    }

    // 超过最大重启次数
    m.setStatus(AdapterFailed)
    m.logger.Error("MCP server exceeded max restarts, giving up",
        "adapter", m.name, "maxRestarts", spec.MaxRestarts)
    // 通知 BrainHost 汇总健康状态
    m.healthCh <- AdapterHealthEvent{Name: m.name, Status: AdapterFailed}
}
```

### 7.4 BrainHost 向 BrainPool 上报的健康状态聚合

```go
// aggregateHealthStatus 聚合所有 MCP server 的状态，返回 BrainHost 整体健康。
// 规则：
//   - 所有 adapter Ready → Healthy
//   - 部分 adapter Degraded，剩余 Ready → Degraded（仍可服务）
//   - 全部 adapter Failed → Unhealthy（BrainPool 应触发 BrainHost 重启）
//   - 无 adapter → Unhealthy（配置错误）
func (h *BrainHost) aggregateHealthStatus() BrainHealthStatus {
    h.adapterMgr.mu.RLock()
    defer h.adapterMgr.mu.RUnlock()

    if len(h.adapterMgr.adapters) == 0 {
        return BrainHealthUnhealthy
    }

    readyCount, failedCount := 0, 0
    for _, a := range h.adapterMgr.adapters {
        switch a.status {
        case AdapterReady:
            readyCount++
        case AdapterFailed:
            failedCount++
        }
    }

    total := len(h.adapterMgr.adapters)
    if failedCount == total {
        return BrainHealthUnhealthy
    }
    if readyCount == total {
        return BrainHealthHealthy
    }
    return BrainHealthDegraded // 部分可用
}
```

### 7.5 健康状态对 BrainPool 行为的影响

| BrainHost 健康状态 | BrainPool 行为 |
|-------------------|----------------|
| Healthy | 正常服务，继续接受 GetBrain 请求 |
| Degraded | 继续服务，Dashboard 显示告警，减少工具集（失败的 adapter 的工具标记为不可用） |
| Unhealthy | BrainPool 触发重启序列；重启超限后标记 brain 为 unavailable |

---

## 8. 参考实现：filesystem MCP Brain

### 8.1 完整 Manifest

文件：`brains/filesystem/manifest.json`

```json
{
  "schema_version": 1,
  "kind": "filesystem",
  "name": "Filesystem Brain",
  "brain_version": "1.0.0",
  "description": "File system read/write/search via MCP filesystem server",
  "capabilities": ["fs.read", "fs.write", "fs.search", "fs.list"],
  "task_patterns": [
    "read file",
    "write file",
    "list directory",
    "search files",
    "file content",
    "directory listing"
  ],
  "runtime": {
    "type": "mcp-backed",
    "entrypoint": "bin/brain-mcp-host",
    "mcp_servers": [
      {
        "name": "filesystem",
        "command": "npx",
        "args": [
          "-y",
          "@modelcontextprotocol/server-filesystem",
          "/tmp"
        ],
        "tool_prefix": "mcp.fs.",
        "startup_timeout_ms": 10000,
        "health_check_interval_ms": 30000,
        "restart_policy": "on-failure",
        "max_restarts": 3,
        "concurrency_overrides": [
          {
            "tool": "write_file",
            "capability": "fs.write",
            "resource_key_template": "workdir:{{path}}",
            "access_mode": "exclusive-write",
            "scope": "turn"
          },
          {
            "tool": "create_directory",
            "capability": "fs.write",
            "resource_key_template": "workdir:{{path}}",
            "access_mode": "exclusive-write",
            "scope": "turn"
          },
          {
            "tool": "delete_file",
            "capability": "fs.write",
            "resource_key_template": "workdir:{{path}}",
            "access_mode": "exclusive-write",
            "scope": "turn"
          }
        ]
      }
    ]
  },
  "policy": {
    "approval_class": "workspace-write",
    "pool_strategy": "shared-service",
    "tool_scope": "delegate.filesystem"
  },
  "health": {
    "startup_timeout_ms": 15000,
    "ping_interval_ms": 30000
  }
}
```

### 8.2 BrainHost 代码骨架

文件：`sdk/kernel/mcphost/host.go`

```go
// Package mcphost 提供 mcp-backed brain 的运行时宿主实现。
// 一个 BrainHost 进程对外表现为标准 brain sidecar（实现 brain/execute 等协议方法），
// 对内管理 N 个 MCP server 子进程。
//
// 使用方式：
//
//	host, err := mcphost.New(manifest, transport)
//	if err != nil { ... }
//	if err := host.Start(ctx); err != nil { ... }
//	defer host.Stop(ctx)
//	host.Serve() // 阻塞，直到 ctx 取消或 transport 关闭
package mcphost

import (
    "context"
    "encoding/json"
    "fmt"
    "sync"

    "github.com/leef-l/brain/sdk/kernel/mcpadapter"
    "github.com/leef-l/brain/sdk/loop"
    "github.com/leef-l/brain/sdk/protocol"
    "github.com/leef-l/brain/sdk/tool"
)

// BrainHost 是 mcp-backed brain sidecar 的核心。
type BrainHost struct {
    manifest   BrainManifest
    transport  protocol.BidirRPC
    adapterMgr *AdapterManager
    registry   *MCPToolRegistry
    runner     *loop.Runner

    ctx    context.Context
    cancel context.CancelFunc
    mu     sync.RWMutex

    started bool
    stopped bool
}

// New 创建一个 BrainHost 实例。
// manifest 来自 brain 启动时解析的配置文件。
// transport 是与 BrainPool/Kernel 通信的 stdio 传输（已建立连接）。
func New(manifest BrainManifest, transport protocol.BidirRPC) (*BrainHost, error) {
    if manifest.Runtime.Type != "mcp-backed" {
        return nil, fmt.Errorf("mcphost: manifest runtime.type must be 'mcp-backed', got %q",
            manifest.Runtime.Type)
    }
    if len(manifest.Runtime.MCPServers) == 0 {
        return nil, fmt.Errorf("mcphost: manifest.runtime.mcp_servers must not be empty")
    }

    ctx, cancel := context.WithCancel(context.Background())
    host := &BrainHost{
        manifest:   manifest,
        transport:  transport,
        adapterMgr: newAdapterManager(),
        registry:   newMCPToolRegistry(),
        ctx:        ctx,
        cancel:     cancel,
    }
    return host, nil
}

// Start 启动所有 MCP server，发现并注册工具，完成 Agent Loop 初始化。
// 必须在 Serve 之前调用。
func (h *BrainHost) Start(ctx context.Context) error {
    h.mu.Lock()
    defer h.mu.Unlock()

    if h.started {
        return fmt.Errorf("mcphost: already started")
    }

    // 1. 启动所有 MCP server（并行）
    startErrs := h.adapterMgr.StartAll(ctx, h.manifest.Runtime.MCPServers)
    if len(startErrs) > 0 {
        // 若有启动失败，记录警告并继续——部分失败不阻塞 brain 启动
        for name, err := range startErrs {
            h.logWarn("MCP server start failed (will retry)", "server", name, "error", err)
        }
    }

    // 2. 工具发现（对已启动的 adapter 并行执行）
    h.adapterMgr.DiscoverAndRegisterAll(ctx, h.registry, h.manifest.Kind)

    // 3. 初始化 Agent Loop
    h.runner = h.buildAgentLoop()

    // 4. 注册协议处理函数
    h.registerProtocolHandlers()

    h.started = true
    return nil
}

// buildAgentLoop 构建 Agent Loop，工具集来自 MCPToolRegistry。
func (h *BrainHost) buildAgentLoop() *loop.Runner {
    // Agent Loop 配置：复用 native brain 的 loop.Runner，
    // 只替换 ToolRegistry 为 MCPToolRegistry
    tools := h.registry.AllTools()
    return loop.NewRunner(loop.Config{
        BrainKind: h.manifest.Kind,
        Tools:     tools,
        // LLM 配置从环境变量或 central 下发的 SubtaskRequest 读取
    })
}

// registerProtocolHandlers 注册响应 Kernel 协议消息的 handler。
func (h *BrainHost) registerProtocolHandlers() {
    // brain/execute：主执行入口
    h.transport.Handle("brain/execute", h.handleExecute)

    // brain/tools_list：返回当前工具集（含来自 MCP server 的工具）
    h.transport.Handle("brain/tools_list", h.handleToolsList)

    // ping：BrainPool 健康探测
    h.transport.Handle("ping", h.handlePing)
}

// handleExecute 处理来自 Central 的 brain/execute 请求。
// 这是 delegate 的执行入口，与 native brain 完全一致。
func (h *BrainHost) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
    var req protocol.SubtaskRequest
    if err := json.Unmarshal(params, &req); err != nil {
        return nil, fmt.Errorf("mcphost: parse brain/execute params: %w", err)
    }

    // 使用 Agent Loop 执行（工具调用会被路由到对应的 MCP server）
    result, err := h.runner.Execute(ctx, &req)
    if err != nil {
        return nil, err
    }
    return result, nil
}

// handleToolsList 返回所有已注册工具（来自所有就绪的 MCP server）。
func (h *BrainHost) handleToolsList(ctx context.Context, _ json.RawMessage) (interface{}, error) {
    tools := h.registry.AllTools()
    schemas := make([]tool.Schema, 0, len(tools))
    for _, t := range tools {
        schemas = append(schemas, t.Schema())
    }
    return map[string]interface{}{"tools": schemas}, nil
}

// handlePing 响应 BrainPool 的健康探测，聚合内部 MCP server 状态。
func (h *BrainHost) handlePing(ctx context.Context, _ json.RawMessage) (interface{}, error) {
    status := h.adapterMgr.AggregateStatus()
    return map[string]interface{}{
        "status":   status.String(),
        "adapters": h.adapterMgr.StatusSnapshot(),
    }, nil
}

// Stop 优雅关闭所有 MCP server 子进程。
func (h *BrainHost) Stop(ctx context.Context) error {
    h.cancel()
    return h.adapterMgr.StopAll(ctx)
}

// Serve 阻塞，驱动 transport 消息循环，直到 ctx 取消。
func (h *BrainHost) Serve() error {
    return h.transport.Serve(h.ctx)
}
```

### 8.3 main 入口骨架

文件：`cmd/brain-mcp-host/main.go`

```go
// brain-mcp-host 是 mcp-backed brain 的通用 sidecar 入口。
// 它从工作目录或 --manifest 标志加载 manifest.json，
// 启动 BrainHost，然后通过 stdio 与 BrainPool 通信。
//
// 编译方式：
//   go build -o bin/brain-mcp-host ./cmd/brain-mcp-host
//
// 启动方式（由 BrainPool/ProcessRunner 负责，不需要手动执行）：
//   brain-mcp-host --manifest /path/to/manifest.json
package main

import (
    "context"
    "flag"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/leef-l/brain/sdk/kernel/mcphost"
    "github.com/leef-l/brain/sdk/protocol"
)

func main() {
    manifestPath := flag.String("manifest", "manifest.json", "path to brain manifest JSON")
    flag.Parse()

    // 加载 manifest
    manifest, err := mcphost.LoadManifest(*manifestPath)
    if err != nil {
        log.Fatalf("brain-mcp-host: load manifest: %v", err)
    }

    // 建立与 Kernel 的 stdio 传输
    reader := protocol.NewFrameReader(os.Stdin)
    writer := protocol.NewFrameWriter(os.Stdout)
    transport := protocol.NewBidirRPC(protocol.RoleSidecar, reader, writer)

    // 创建 BrainHost
    host, err := mcphost.New(manifest, transport)
    if err != nil {
        log.Fatalf("brain-mcp-host: create host: %v", err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
    defer stop()

    // 启动（并行启动 MCP servers，发现工具，注册 handler）
    if err := host.Start(ctx); err != nil {
        log.Fatalf("brain-mcp-host: start: %v", err)
    }

    // 阻塞运行
    if err := host.Serve(); err != nil {
        log.Printf("brain-mcp-host: serve ended: %v", err)
    }

    // 优雅关闭
    if err := host.Stop(ctx); err != nil {
        log.Printf("brain-mcp-host: stop: %v", err)
    }
}
```

### 8.4 测试骨架

文件：`sdk/kernel/mcphost/host_test.go`

```go
package mcphost_test

import (
    "context"
    "testing"
    "time"

    "github.com/leef-l/brain/sdk/kernel/mcpadapter"
    "github.com/leef-l/brain/sdk/kernel/mcphost"
)

// TestBrainHostToolDiscovery 验证 BrainHost 能正确发现并注册 MCP 工具。
func TestBrainHostToolDiscovery(t *testing.T) {
    // 使用 NewPipeAdapter 创建测试用 adapter（不启动真实子进程）
    serverReader, hostWriter := io.Pipe()
    hostReader, serverWriter := io.Pipe()

    // 启动假 MCP server（与 mcpadapter_test 中的 fakeMCPServer 相同）
    go fakeMCPServer(t, serverReader, serverWriter, []mcpadapter.MCPToolSpec{
        {
            Name:        "read_file",
            Description: "Read a file",
            InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
        },
        {
            Name:        "write_file",
            Description: "Write a file",
            InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
        },
    })

    manifest := mcphost.BrainManifest{
        Kind: "test-filesystem",
        Runtime: mcphost.RuntimeSpec{
            Type: "mcp-backed",
            MCPServers: []mcphost.MCPServerSpec{
                {
                    Name:       "filesystem",
                    ToolPrefix: "mcp.fs.",
                },
            },
        },
    }

    // 创建带 pipe adapter 的 BrainHost
    host := mcphost.NewWithPipeAdapters(manifest, map[string]io.ReadWriter{
        "filesystem": &readWriter{hostReader, hostWriter},
    })

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    if err := host.Start(ctx); err != nil {
        t.Fatalf("Start: %v", err)
    }
    defer host.Stop(ctx)

    tools := host.Registry().AllTools()
    if len(tools) != 2 {
        t.Fatalf("expected 2 tools, got %d", len(tools))
    }

    // 验证工具名前缀
    names := make(map[string]bool)
    for _, tool := range tools {
        names[tool.Name()] = true
    }
    if !names["mcp.fs.read_file"] {
        t.Error("expected tool mcp.fs.read_file")
    }
    if !names["mcp.fs.write_file"] {
        t.Error("expected tool mcp.fs.write_file")
    }
}
```

---

## 9. native vs mcp-backed 功能对比矩阵

| 能力维度 | native brain | mcp-backed brain | 差异说明 |
|---------|-------------|-----------------|---------|
| **对外接口** | `brain/execute`、`brain/tools_list`、`ping` | **完全相同** | 调用方无感知 |
| **Delegate 支持** | ✅ 是 Central 的 delegate 目标 | ✅ **完全相同** | MCP Server 不可见 |
| **工具来源** | 本地 Go 代码注册 | MCP server 动态发现 | mcp-backed 工具集在运行时确定 |
| **工具数量** | 编译时固定 | 启动时动态（MCP server 决定） | mcp-backed 更灵活，但无法静态分析 |
| **工具命名** | `<brain>.<tool>` 直接注册 | `mcp.<prefix>.<tool>` 加前缀 | 需注意名称冲突 |
| **工具 schema** | Go 代码内硬定义，精确 | MCP server 提供，质量由 server 决定 | 可能不准确，需降级处理 |
| **ToolConcurrencySpec** | 注册时精确声明 | 自动推导（保守策略）或 override | mcp-backed 默认保守，可手动优化 |
| **BrainPool 复用策略** | 支持三种（shared/exclusive/ephemeral） | **完全相同** | 由 manifest policy.pool_strategy 声明 |
| **LeaseManager 集成** | ✅ 完整集成 | ✅ **完整集成**（通过推导规格） | 并发控制语义相同 |
| **Dispatch/BatchPlanner** | ✅ 参与 | ✅ **完整参与** | MCP 工具和 native 工具共享批次 |
| **健康检查** | 单层 ping | 双层（BrainHost + per-adapter ping） | mcp-backed 更复杂，故障定位更细 |
| **故障隔离** | 进程崩溃 → brain 失效 | MCP server 崩溃 → adapter 降级，brain 继续运行 | mcp-backed 部分可用性更好 |
| **重启策略** | ProcessRunner 管理 | 双层：BrainPool 管 BrainHost，AdapterManager 管 MCP server | mcp-backed 有内层自动重连 |
| **工具调用延迟** | 进程内函数调用，极低 | 额外一跳 stdio IPC（MCP server 是子进程） | mcp-backed 延迟更高（~1-5ms overhead） |
| **可观测性** | brain 级 metrics | brain 级 + adapter 级 metrics | mcp-backed metrics 更丰富 |
| **Dashboard 显示** | brain 状态 + 工具列表 | brain 状态 + MCP server 状态 + 工具列表 | Dashboard 需要显示 MCP server 子状态 |
| **开发复杂度** | 高（需要写 Go 工具代码） | 低（复用社区 MCP server） | mcp-backed 接入成本显著更低 |
| **工具更新** | 需要重新编译 brain | 更换 MCP server 版本即可 | mcp-backed 更新更快 |
| **安全隔离** | 工具代码在 sidecar 进程内 | MCP server 是独立子进程，有额外进程边界 | mcp-backed 隔离性更强 |
| **Context sharing** | SubtaskContext 直接传递 | SubtaskContext 由 BrainHost 持有，不传给 MCP server | MCP server 无法感知 brain 上下文 |
| **四层学习（L0）** | 可实现 BrainLearner 接口 | BrainHost 可观测工具调用结果并实现 BrainLearner | mcp-backed 学习粒度粗于 native |
| **manifest 声明** | `runtime.type = "native"` | `runtime.type = "mcp-backed"` + `mcp_servers` | mcp-backed manifest 更复杂 |
| **进程数量** | 1 sidecar 进程 | 1 BrainHost + N 个 MCP server 子进程 | mcp-backed 资源占用更多 |
| **启动时间** | 快（单进程） | 较慢（需等待所有 MCP server 就绪） | 可通过预热（WarmUp）缓解 |

### 9.1 选择指南

**选 native brain 的场景**：
- 工具逻辑高度定制化，无对应 MCP server
- 对延迟极度敏感（高频工具调用）
- 需要深度集成 brain 内部状态（如 Quant brain 的 WeightAdapter）
- 需要精确的 ToolConcurrencySpec（如 `quant.place_order` 的交易级别隔离）

**选 mcp-backed brain 的场景**：
- 存在高质量的社区 MCP server（filesystem、GitHub、Slack、Puppeteer 等）
- 快速验证新能力（无需写 Go 代码）
- 工具集需要频繁更新或版本化
- 需要接入外部生态（MCP 生态正在快速扩展）

---

## 10. 与其他 35 系列文档的接口边界

### 10.1 与 BrainPool（35-BrainPool实现设计.md）

- mcp-backed brain 通过 `BrainPool.Register(BrainPoolRegistration)` 注册，接口与 native brain 完全一致
- `BrainPool.HealthCheck()` 向 BrainHost 发 ping，BrainHost 内部聚合 MCP server 状态再响应
- mcp-backed brain 的 `Launcher` 函数启动的是 `brain-mcp-host` 二进制，而非工具 sidecar

### 10.2 与 Dispatch Policy（35-Dispatch-Policy-冲突图与Batch分组算法.md）

- MCP 工具的 `ToolConcurrencySpec` 由 BrainHost 在工具注册时自动推导或从 manifest override 读取
- BatchPlanner 处理 MCP 工具时，行为与 native 工具**完全一致**（按 ResourceKey 冲突分析）
- MCP 工具和 native 工具可以出现在同一个 BatchPlanner 的 tool_call 列表中，共享分组算法

### 10.3 与 TaskExecution（35-TaskExecution生命周期状态机.md）

- TaskExecution 调用 `Orchestrator.Delegate()` 时，目标是 mcp-backed brain（通过 BrainPool 获取），与 native brain 无区别
- brain/execute 返回的 `SubtaskResult` 格式完全相同，状态机转换不需要区分 runtime 类型

### 10.4 与 Dashboard（35-统一Dashboard设计规格.md）

Dashboard 的 `/v1/brains/:kind` 端点对 mcp-backed brain 需要额外字段：

```json
{
  "kind": "filesystem",
  "status": "healthy",
  "runtime_type": "mcp-backed",
  "tools_count": 8,
  "mcp_servers": [
    {
      "name": "filesystem",
      "status": "ready",
      "tools_count": 8,
      "uptime_seconds": 3600,
      "last_ping_latency_ms": 2,
      "restart_count": 0
    }
  ]
}
```

### 10.5 与 Context Engine（35-Context-Engine详细设计.md）

- SubtaskContext 由 Orchestrator 传入 brain/execute 请求
- BrainHost 接收 SubtaskContext，注入 Agent Loop 的 AssembleRequest
- MCP server **不接收也不感知** SubtaskContext——Context 边界止于 BrainHost
- 这保证了 Context 的隐私边界：MCP server（可能是第三方）无法读取跨脑上下文

### 10.6 Hybrid Runtime 概述

Hybrid Runtime（`runtime_type: "hybrid"`）结合了 native 和 mcp-backed 两种运行时，适用于"核心工具用 Go 原生实现、扩展工具通过 MCP 接入"的场景。

**Manifest 声明**：

```yaml
runtime:
  type: hybrid
  native_tools:     # Go 原生实现的工具列表
    - code_search
    - code_edit
  mcp_servers:      # MCP 扩展工具
    - name: lsp
      command: ["brain-mcp-lsp"]
      tools_prefix: "lsp_"
```

**路由规则**（由 `HybridToolRouter` 实现）：

1. 工具名匹配 `native_tools` 注册表 → native Go 函数直接执行
2. 工具名前缀匹配 MCP server 的 `tools/list` → MCPAdapter 路由到 MCP server
3. 两者都匹配 → **native 优先**（延迟更低、无 IPC 开销）
4. 都不匹配 → 返回 `ToolNotFound` 错误

**故障降级**：MCP server 不可用时，仅 MCP 工具受影响，native 工具不受影响。
详细的降级策略（熔断器、三层 fallback）见 [35-跨脑通信协议设计](./35-跨脑通信协议设计.md) §6。

> Hybrid 的完整通信模型在 35-14 §6 中定义，本文档仅覆盖 MCP 侧的实现。
> Hybrid Runtime 计划在 Phase B 落地，依赖 mcp-backed runtime（Phase B-7）先行完成。

---

## 11. 实现路径与里程碑

### 11.1 B-7 工作项分解

| 编号 | 工作内容 | 文件 | 工时估算 | 前置条件 |
|------|---------|------|---------|---------|
| B-7-1 | MCPServerSpec / BrainManifest 结构定义 + 校验 | `sdk/kernel/mcphost/manifest.go` | 0.5 天 | 无 |
| B-7-2 | AdapterManager（并行启动、状态机、重连循环） | `sdk/kernel/mcphost/adapter_manager.go` | 1 天 | B-7-1 |
| B-7-3 | MCPToolRegistry（注册、查找、重注册） | `sdk/kernel/mcphost/registry.go` | 0.5 天 | 无 |
| B-7-4 | 工具发现 + ToolConcurrencySpec 推导 + override 解析 | `sdk/kernel/mcphost/converter.go` | 1 天 | B-7-1、B-7-3 |
| B-7-5 | BrainHost 核心（Start、Serve、协议 handler） | `sdk/kernel/mcphost/host.go` | 1.5 天 | B-7-2、B-7-3、B-7-4 |
| B-7-6 | brain-mcp-host 二进制入口 | `cmd/brain-mcp-host/main.go` | 0.5 天 | B-7-5 |
| B-7-7 | filesystem brain 参考实现（manifest + 集成测试） | `brains/filesystem/` | 0.5 天 | B-7-6 |
| B-7-8 | Dashboard mcp_servers 字段扩展 | `sdk/kernel/` 相关 | 0.5 天 | B-7-5 |

**Phase B-7 合计**：6 天

### 11.2 不做的事（明确排除范围）

- **MCP server 的进程沙箱化**：Phase C 考虑（与 native brain 工具沙箱策略对齐）
- **MCP over HTTP transport**：当前只支持 stdio。HTTP transport 是 P2（remote runtime）
- **MCP server 的 resources/prompts 能力**：只处理 tools，resources 和 prompts 后置
- **MCP server 的 tool 流式输出**：当前聚合所有 content block 后一次性返回
- **动态 MCP server 增删**（运行时 hot-add）：重启 BrainHost 来更新 MCP server 配置
- **MCP Authentication**：第三方 MCP server 的 OAuth/token 认证，Phase C 集成 Vault

---

*文档版本：v1 · 2026-04-16*
*作者：Brain v3 架构组*
*上位规格：[32-v3-Brain架构.md](./32-v3-Brain架构.md) §6 Phase B-7*
