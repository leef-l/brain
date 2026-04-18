# 35. 统一 Dashboard — §7.2 详细设计规格

> **状态**：v1 · 2026-04-16
> **上位规格**：[32-v3-Brain架构.md](./32-v3-Brain架构.md) §3.7 Control Plane
> **实现目标**：把 Quant WebUI（单脑）扩展为全脑控制面，覆盖 Brains / Tasks / Events / Providers / Leases 五大模块
> **依赖实现**：`cmd/brain/cmd_serve.go`、`brains/quant/webui/`、`sdk/runtimeaudit/context.go`、`sdk/kernel/orchestrator.go`

---

## 0. 设计边界

Dashboard 是 **Control Plane 的前端入口**，不是任何专精大脑的业务页面。它的职责是：

1. **可见性**：展示所有 brain 的健康状态、工具列表、权限状态
2. **可控性**：启停脑、取消任务、重置 lease
3. **可审计性**：统一事件流，不丢失跨脑通信记录
4. **不侵入业务**：Quant WebUI 的量化业务面板继续在 `:8380` 运行，不合并

Dashboard 跑在 `brain serve` 的主端口（默认 `:7701`）上，前端走 `embed.FS` 内嵌。

---

## 1. 完整 REST API 列表

### 1.1 端点总览

| Method | Path | 说明 |
|--------|------|------|
| GET | `/v1/dashboard/overview` | 全系统总览快照 |
| GET | `/v1/dashboard/brains` | 所有注册 Brain 的状态列表 |
| GET | `/v1/dashboard/brains/:kind` | 单个 Brain 详情（工具/权限/健康） |
| POST | `/v1/dashboard/brains/:kind/restart` | 重启某个 Brain sidecar |
| GET | `/v1/dashboard/executions` | TaskExecution 列表（替代 `/v1/runs`） |
| GET | `/v1/dashboard/executions/:id` | 单个 Execution 详情（含事件日志） |
| DELETE | `/v1/dashboard/executions/:id` | 取消运行中的 Execution |
| GET | `/v1/dashboard/events` | 事件流查询（支持过滤和分页） |
| GET | `/v1/dashboard/providers` | Provider 认证状态列表 |
| GET | `/v1/dashboard/leases` | 当前活跃 Capability Lease 列表 |
| DELETE | `/v1/dashboard/leases/:id` | 强制释放某个 Lease |

---

### 1.2 GET /v1/dashboard/overview

**描述**：返回当前系统全局快照，用于 Dashboard 首屏和心跳轮询。

**Request**：无参数

**Response 200**：

```json
{
  "snapshot_at": "2026-04-16T10:30:00Z",
  "kernel_version": "0.7.0",
  "protocol_version": "1",
  "uptime_seconds": 3600,
  "brains": {
    "total": 5,
    "active": 3,
    "error": 1,
    "idle": 1
  },
  "executions": {
    "running": 2,
    "queued": 0,
    "completed_today": 14,
    "failed_today": 1
  },
  "providers": {
    "healthy": 2,
    "degraded": 0,
    "unavailable": 1
  },
  "leases": {
    "active": 2
  },
  "events": {
    "total_today": 312,
    "errors_today": 3
  }
}
```

**字段说明**：

| 字段 | 类型 | 说明 |
|------|------|------|
| `snapshot_at` | string (RFC3339) | 快照时间戳 |
| `kernel_version` | string | Brain kernel 版本 |
| `uptime_seconds` | int | 进程启动至今秒数 |
| `brains.active` | int | 状态为 `running` 的 brain 数 |
| `brains.error` | int | 状态为 `error` 或 `crashed` 的 brain 数 |
| `executions.running` | int | 当前 `running` 状态的 TaskExecution 数 |

---

### 1.3 GET /v1/dashboard/brains

**描述**：返回所有注册 Brain 的状态列表（含可用但未运行的）。

**Query 参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `status` | string | 过滤状态：`running`/`idle`/`error`/`all`（默认 `all`） |
| `kind` | string | 按 kind 过滤，如 `quant` |

**Response 200**：

```json
{
  "brains": [
    {
      "kind": "quant",
      "name": "Quant Brain",
      "version": "0.7.0",
      "status": "running",
      "pid": 12345,
      "started_at": "2026-04-16T09:00:00Z",
      "last_heartbeat_at": "2026-04-16T10:29:58Z",
      "health_score": 0.98,
      "auto_start": true,
      "binary": "/usr/local/bin/brain-quant-sidecar",
      "capabilities": ["trading", "risk-management", "backtesting"],
      "tools_count": 16,
      "active_executions": 1,
      "model": "claude-opus-4-6",
      "llm_access": "allowed"
    },
    {
      "kind": "data",
      "name": "Data Brain",
      "version": "0.7.0",
      "status": "running",
      "pid": 12346,
      "started_at": "2026-04-16T09:00:01Z",
      "last_heartbeat_at": "2026-04-16T10:29:59Z",
      "health_score": 1.0,
      "auto_start": true,
      "binary": "/usr/local/bin/brain-data-sidecar",
      "capabilities": ["market-data", "feature-engineering"],
      "tools_count": 8,
      "active_executions": 0,
      "model": "",
      "llm_access": "none"
    },
    {
      "kind": "browser",
      "name": "Browser Brain",
      "version": "0.7.0",
      "status": "idle",
      "pid": 0,
      "started_at": null,
      "last_heartbeat_at": null,
      "health_score": -1,
      "auto_start": false,
      "binary": "/usr/local/bin/brain-browser",
      "capabilities": ["web-browse", "screenshot", "form-fill"],
      "tools_count": 0,
      "active_executions": 0,
      "model": "claude-sonnet-4-6",
      "llm_access": "allowed"
    }
  ],
  "total": 3
}
```

---

### 1.4 GET /v1/dashboard/brains/:kind

**描述**：单个 Brain 的完整详情，包含工具列表、权限策略、健康历史。

**Path 参数**：`kind` — Brain 类型标识（`quant`/`data`/`code`/`browser`/`fault`/`central`）

**Response 200**：

```json
{
  "kind": "quant",
  "name": "Quant Brain",
  "version": "0.7.0",
  "status": "running",
  "pid": 12345,
  "binary": "/usr/local/bin/brain-quant-sidecar",
  "started_at": "2026-04-16T09:00:00Z",
  "last_heartbeat_at": "2026-04-16T10:29:58Z",
  "health_score": 0.98,
  "health_history": [
    {"at": "2026-04-16T10:29:00Z", "score": 0.97},
    {"at": "2026-04-16T10:28:00Z", "score": 0.98}
  ],
  "capabilities": ["trading", "risk-management", "backtesting"],
  "tools": [
    {
      "name": "quant.global_portfolio",
      "description": "查询所有账户的总持仓和净值",
      "category": "query"
    },
    {
      "name": "quant.pause_trading",
      "description": "暂停所有交易单元",
      "category": "control"
    }
  ],
  "policy": {
    "llm_access": "allowed",
    "model": "claude-opus-4-6",
    "tool_scope": "restricted",
    "sandbox": "none",
    "approval_mode": "auto"
  },
  "active_executions": 1,
  "total_executions_today": 4,
  "auto_start": true,
  "manifest_path": "~/.brain/brains/quant/brain.yaml"
}
```

**Response 404**：

```json
{"error": "brain not found: unknown_kind"}
```

---

### 1.5 POST /v1/dashboard/brains/:kind/restart

**描述**：强制重启某个 Brain sidecar（先发 SIGTERM，等待退出，再重新启动）。

**Request Body**：

```json
{
  "reason": "手动重置，清除内存泄漏",
  "grace_seconds": 5
}
```

| 字段 | 类型 | 必选 | 默认值 |
|------|------|------|--------|
| `reason` | string | 否 | `"manual restart"` |
| `grace_seconds` | int | 否 | `5` |

**Response 202**（异步接受）：

```json
{
  "kind": "quant",
  "action": "restart",
  "accepted_at": "2026-04-16T10:30:00Z",
  "estimated_ready_at": "2026-04-16T10:30:10Z"
}
```

**Response 409**：Brain 当前有活跃 Execution，需先取消。

```json
{"error": "brain has active executions, cancel them first or use force=true"}
```

---

### 1.6 GET /v1/dashboard/executions

**描述**：TaskExecution 列表，替代 `/v1/runs`，支持过滤和分页。

**Query 参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `status` | string | `running`/`completed`/`failed`/`cancelled`/`all`（默认 `all`） |
| `brain` | string | 按 brain kind 过滤 |
| `limit` | int | 最多返回条数，默认 50，最大 500 |
| `offset` | int | 分页偏移，默认 0 |
| `since` | string (RFC3339) | 只返回此时间之后创建的记录 |

**Response 200**：

```json
{
  "executions": [
    {
      "id": "run-20260416-001",
      "status": "running",
      "brain": "central",
      "prompt": "分析当前市场，给出交易建议",
      "mode": "auto",
      "workdir": "/workspace",
      "created_at": "2026-04-16T10:00:00Z",
      "updated_at": "2026-04-16T10:05:00Z",
      "duration_ms": 300000,
      "turns": 5,
      "max_turns": 20,
      "subtasks": [
        {
          "task_id": "sub-001",
          "brain": "data",
          "status": "completed",
          "instruction": "获取 BTC-USDT 最新数据"
        }
      ]
    }
  ],
  "total": 16,
  "running": 1,
  "limit": 50,
  "offset": 0
}
```

---

### 1.7 GET /v1/dashboard/executions/:id

**描述**：单个 Execution 的完整详情，含事件日志。

**Response 200**：

```json
{
  "id": "run-20260416-001",
  "status": "running",
  "brain": "central",
  "prompt": "分析当前市场，给出交易建议",
  "mode": "auto",
  "workdir": "/workspace",
  "created_at": "2026-04-16T10:00:00Z",
  "updated_at": "2026-04-16T10:05:00Z",
  "duration_ms": 300000,
  "turns": 5,
  "max_turns": 20,
  "result": null,
  "error": "",
  "subtasks": [
    {
      "task_id": "sub-001",
      "brain": "data",
      "status": "completed",
      "instruction": "获取 BTC-USDT 最新数据",
      "started_at": "2026-04-16T10:01:00Z",
      "completed_at": "2026-04-16T10:01:05Z",
      "duration_ms": 5000,
      "output": {"snapshot_count": 3}
    }
  ],
  "events": [
    {
      "id": "evt-001",
      "at": "2026-04-16T10:00:01Z",
      "type": "run.accepted",
      "source": "kernel",
      "message": "run accepted by serve API",
      "data": null
    },
    {
      "id": "evt-002",
      "at": "2026-04-16T10:01:00Z",
      "type": "subtask.delegate",
      "source": "central",
      "message": "delegating to data brain",
      "data": {"target_kind": "data", "task_id": "sub-001"}
    }
  ]
}
```

---

### 1.8 DELETE /v1/dashboard/executions/:id

**描述**：取消运行中的 Execution，等价于原 `DELETE /v1/runs/:id`。

**Response 200**：

```json
{"id": "run-20260416-001", "status": "cancelled"}
```

**Response 404**：Execution 不存在。
**Response 409**：Execution 已结束（非 `running`）。

---

### 1.9 GET /v1/dashboard/events

**描述**：统一事件流查询，支持类型过滤、时间范围、来源过滤。

**Query 参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `types` | string | 逗号分隔的事件类型，如 `run.accepted,subtask.delegate` |
| `source` | string | 来源 brain kind，如 `central`/`quant` |
| `level` | string | `info`/`warn`/`error`，默认 `info`（包含所有） |
| `since` | string (RFC3339) | 时间下界 |
| `until` | string (RFC3339) | 时间上界 |
| `run_id` | string | 关联到特定 Execution |
| `limit` | int | 最多返回条数，默认 100，最大 1000 |
| `cursor` | string | 分页游标（从上次响应的 `next_cursor` 取） |

**Response 200**：

```json
{
  "events": [
    {
      "id": "evt-20260416-0312",
      "at": "2026-04-16T10:29:00Z",
      "type": "brain.heartbeat",
      "level": "info",
      "source": "quant",
      "run_id": "",
      "message": "quant brain heartbeat",
      "data": {"health_score": 0.98, "active_executions": 1}
    },
    {
      "id": "evt-20260416-0311",
      "at": "2026-04-16T10:28:55Z",
      "type": "subtask.completed",
      "level": "info",
      "source": "data",
      "run_id": "run-20260416-001",
      "message": "subtask sub-001 completed",
      "data": {"task_id": "sub-001", "duration_ms": 5000}
    }
  ],
  "total": 312,
  "has_more": true,
  "next_cursor": "cursor-abc123"
}
```

---

### 1.10 GET /v1/dashboard/providers

**描述**：所有已配置 LLM Provider 的认证状态。

**Response 200**：

```json
{
  "providers": [
    {
      "name": "anthropic",
      "display_name": "Anthropic Claude",
      "models": ["claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-6"],
      "status": "healthy",
      "auth_method": "api_key",
      "auth_masked": "sk-ant-***...abc",
      "last_checked_at": "2026-04-16T10:29:00Z",
      "latency_ms": 320,
      "error": "",
      "assigned_brains": ["central", "quant", "browser"]
    },
    {
      "name": "deepseek",
      "display_name": "DeepSeek",
      "models": ["deepseek-coder-v2"],
      "status": "unavailable",
      "auth_method": "api_key",
      "auth_masked": "sk-***...xyz",
      "last_checked_at": "2026-04-16T10:29:00Z",
      "latency_ms": -1,
      "error": "connection timeout",
      "assigned_brains": ["code"]
    },
    {
      "name": "hunyuan",
      "display_name": "腾讯混元",
      "models": ["hunyuan-pro"],
      "status": "degraded",
      "auth_method": "secret_id_key",
      "auth_masked": "AKID***...def",
      "last_checked_at": "2026-04-16T10:25:00Z",
      "latency_ms": 2100,
      "error": "high latency",
      "assigned_brains": []
    }
  ],
  "total": 3,
  "healthy": 1,
  "degraded": 1,
  "unavailable": 1
}
```

---

### 1.11 GET /v1/dashboard/leases

**描述**：当前所有活跃的 Capability Lease。（Lease 是 Brain 对某种能力的排他/共享占用声明，防止并发冲突。）

**Query 参数**：

| 参数 | 类型 | 说明 |
|------|------|------|
| `holder` | string | 按 brain kind 过滤 |
| `resource` | string | 按资源类型过滤，如 `exchange.okx` |

**Response 200**：

```json
{
  "leases": [
    {
      "id": "lease-abc123",
      "holder": "quant",
      "run_id": "run-20260416-001",
      "resource": "exchange.okx",
      "scope": "exclusive",
      "acquired_at": "2026-04-16T10:00:00Z",
      "expires_at": "2026-04-16T10:30:00Z",
      "ttl_seconds": 1800,
      "remaining_seconds": 1740,
      "auto_release": true
    }
  ],
  "total": 1
}
```

---

### 1.12 DELETE /v1/dashboard/leases/:id

**描述**：强制释放 Lease（管理员紧急操作）。

**Response 200**：

```json
{"id": "lease-abc123", "released": true}
```

**Response 404**：Lease 不存在或已过期。

---

## 2. Go 数据模型（DTO 定义）

所有 DTO 定义建议放在 `cmd/brain/dashboard_dto.go` 或独立包 `internal/dashboard/dto.go`。

```go
package dashboard

import (
    "encoding/json"
    "time"
)

// -----------------------------------------------------------------------
// OverviewDTO — /v1/dashboard/overview 响应
// -----------------------------------------------------------------------

// OverviewDTO 是系统全局快照，用于 Dashboard 首屏和心跳轮询。
type OverviewDTO struct {
    SnapshotAt      time.Time            `json:"snapshot_at"`
    KernelVersion   string               `json:"kernel_version"`
    ProtocolVersion string               `json:"protocol_version"`
    UptimeSeconds   int64                `json:"uptime_seconds"`
    Brains          OverviewBrainStats   `json:"brains"`
    Executions      OverviewExecStats    `json:"executions"`
    Providers       OverviewProvStats    `json:"providers"`
    Leases          OverviewLeaseStats   `json:"leases"`
    Events          OverviewEventStats   `json:"events"`
}

type OverviewBrainStats struct {
    Total  int `json:"total"`
    Active int `json:"active"`
    Error  int `json:"error"`
    Idle   int `json:"idle"`
}

type OverviewExecStats struct {
    Running        int `json:"running"`
    Queued         int `json:"queued"`
    CompletedToday int `json:"completed_today"`
    FailedToday    int `json:"failed_today"`
}

type OverviewProvStats struct {
    Healthy     int `json:"healthy"`
    Degraded    int `json:"degraded"`
    Unavailable int `json:"unavailable"`
}

type OverviewLeaseStats struct {
    Active int `json:"active"`
}

type OverviewEventStats struct {
    TotalToday  int `json:"total_today"`
    ErrorsToday int `json:"errors_today"`
}

// -----------------------------------------------------------------------
// BrainStatusDTO — /v1/dashboard/brains 列表项 和 /v1/dashboard/brains/:kind 详情
// -----------------------------------------------------------------------

// BrainStatusDTO 描述一个 Brain 的完整运行时状态。
// 列表端点返回不含 Tools/HealthHistory 的精简版（omitempty 控制）。
type BrainStatusDTO struct {
    Kind               string              `json:"kind"`
    Name               string              `json:"name"`
    Version            string              `json:"version"`
    Status             BrainStatus         `json:"status"`
    PID                int                 `json:"pid"`                          // 0 = 未运行
    Binary             string              `json:"binary"`
    StartedAt          *time.Time          `json:"started_at"`                   // null = 未运行
    LastHeartbeatAt    *time.Time          `json:"last_heartbeat_at"`            // null = 未收到
    HealthScore        float64             `json:"health_score"`                 // -1 = 未知
    HealthHistory      []HealthPoint       `json:"health_history,omitempty"`     // 详情页才包含
    Capabilities       []string            `json:"capabilities"`
    Tools              []ToolSummaryDTO    `json:"tools,omitempty"`              // 详情页才包含
    ToolsCount         int                 `json:"tools_count"`
    Policy             *BrainPolicyDTO     `json:"policy,omitempty"`             // 详情页才包含
    ActiveExecutions   int                 `json:"active_executions"`
    TotalExecToday     int                 `json:"total_executions_today"`
    AutoStart          bool                `json:"auto_start"`
    Model              string              `json:"model"`                        // LLM 模型 ID，空=无 LLM
    LLMAccess          string              `json:"llm_access"`                   // "allowed"/"none"
    ManifestPath       string              `json:"manifest_path,omitempty"`
}

// BrainStatus 枚举 Brain 的运行状态。
type BrainStatus string

const (
    BrainStatusRunning BrainStatus = "running"  // sidecar 进程存活且正常响应
    BrainStatusIdle    BrainStatus = "idle"      // 未运行（可按需启动）
    BrainStatusStarting BrainStatus = "starting" // 启动中，等待首次 handshake
    BrainStatusError   BrainStatus = "error"     // 进程异常或心跳超时
    BrainStatusCrashed BrainStatus = "crashed"   // 进程意外退出
)

// HealthPoint 是健康评分的时序采样点。
type HealthPoint struct {
    At    time.Time `json:"at"`
    Score float64   `json:"score"`
}

// ToolSummaryDTO 是工具的展示摘要（不含完整 schema）。
type ToolSummaryDTO struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Category    string `json:"category"` // "query"/"control"/"execute"
}

// BrainPolicyDTO 描述 Brain 的权限策略配置。
type BrainPolicyDTO struct {
    LLMAccess    string `json:"llm_access"`    // "allowed"/"restricted"/"none"
    Model        string `json:"model"`
    ToolScope    string `json:"tool_scope"`    // "full"/"restricted"
    Sandbox      string `json:"sandbox"`       // "none"/"workdir"/"container"
    ApprovalMode string `json:"approval_mode"` // "auto"/"plan"/"manual"
}

// -----------------------------------------------------------------------
// ExecutionDTO — /v1/dashboard/executions 列表项和详情
// -----------------------------------------------------------------------

// ExecutionDTO 是 TaskExecution 的完整快照，对应原 runEntry/persistedRunRecord。
type ExecutionDTO struct {
    ID          string           `json:"id"`
    Status      string           `json:"status"`    // running/completed/failed/cancelled
    Brain       string           `json:"brain"`     // brain kind
    Prompt      string           `json:"prompt"`
    Mode        string           `json:"mode"`      // auto/plan/accept-edits/restricted
    Workdir     string           `json:"workdir"`
    CreatedAt   time.Time        `json:"created_at"`
    UpdatedAt   time.Time        `json:"updated_at"`
    DurationMS  int64            `json:"duration_ms"`
    Turns       int              `json:"turns"`
    MaxTurns    int              `json:"max_turns"`
    Result      json.RawMessage  `json:"result,omitempty"`
    Error       string           `json:"error,omitempty"`
    Subtasks    []SubtaskDTO     `json:"subtasks,omitempty"`   // 详情页才包含
    Events      []EventDTO       `json:"events,omitempty"`     // 详情页才包含
}

// SubtaskDTO 描述一个跨脑委托子任务。
type SubtaskDTO struct {
    TaskID      string          `json:"task_id"`
    Brain       string          `json:"brain"`
    Status      string          `json:"status"`
    Instruction string          `json:"instruction"`
    StartedAt   *time.Time      `json:"started_at"`
    CompletedAt *time.Time      `json:"completed_at"`
    DurationMS  int64           `json:"duration_ms"`
    Output      json.RawMessage `json:"output,omitempty"`
    Error       string          `json:"error,omitempty"`
}

// -----------------------------------------------------------------------
// EventDTO — /v1/dashboard/events 列表项
// -----------------------------------------------------------------------

// EventDTO 是统一事件总线的单条事件。
// 它聚合了三个来源：runtimeaudit.Event、protocol 事件、BrainPool 状态变更。
type EventDTO struct {
    ID      string          `json:"id"`
    At      time.Time       `json:"at"`
    Type    string          `json:"type"`    // 见事件类型枚举
    Level   string          `json:"level"`   // "info"/"warn"/"error"
    Source  string          `json:"source"`  // brain kind 或 "kernel"
    RunID   string          `json:"run_id"`  // 关联的 Execution ID，空=全局事件
    Message string          `json:"message"`
    Data    json.RawMessage `json:"data,omitempty"`
}

// 已定义的事件类型常量（不穷举，extensible）
const (
    EventTypeRunAccepted      = "run.accepted"
    EventTypeRunCompleted     = "run.completed"
    EventTypeRunFailed        = "run.failed"
    EventTypeRunCancelled     = "run.cancelled"
    EventTypeSubtaskDelegate  = "subtask.delegate"
    EventTypeSubtaskCompleted = "subtask.completed"
    EventTypeSubtaskFailed    = "subtask.failed"
    EventTypeBrainStarted     = "brain.started"
    EventTypeBrainStopped     = "brain.stopped"
    EventTypeBrainCrashed     = "brain.crashed"
    EventTypeBrainHeartbeat   = "brain.heartbeat"
    EventTypeProviderHealthy  = "provider.healthy"
    EventTypeProviderDegraded = "provider.degraded"
    EventTypeLeaseAcquired    = "lease.acquired"
    EventTypeLeaseReleased    = "lease.released"
    EventTypeLeaseExpired     = "lease.expired"
)

// -----------------------------------------------------------------------
// ProviderDTO — /v1/dashboard/providers 列表项
// -----------------------------------------------------------------------

// ProviderDTO 描述一个 LLM Provider 的认证和健康状态。
type ProviderDTO struct {
    Name           string     `json:"name"`           // "anthropic"/"deepseek"/"hunyuan"
    DisplayName    string     `json:"display_name"`
    Models         []string   `json:"models"`
    Status         string     `json:"status"`         // "healthy"/"degraded"/"unavailable"
    AuthMethod     string     `json:"auth_method"`    // "api_key"/"secret_id_key"
    AuthMasked     string     `json:"auth_masked"`    // 脱敏后的凭证，如 "sk-***...abc"
    LastCheckedAt  *time.Time `json:"last_checked_at"`
    LatencyMS      int64      `json:"latency_ms"`     // -1 = 不可达
    Error          string     `json:"error,omitempty"`
    AssignedBrains []string   `json:"assigned_brains"` // 使用该 provider 的 brain kinds
}

// -----------------------------------------------------------------------
// LeaseDTO — /v1/dashboard/leases 列表项
// -----------------------------------------------------------------------

// LeaseDTO 描述一个 Capability Lease 的当前状态。
// Lease 表示 Brain 对某类资源的排他或共享占用，防止并发冲突。
type LeaseDTO struct {
    ID               string    `json:"id"`
    Holder           string    `json:"holder"`           // brain kind
    RunID            string    `json:"run_id"`           // 关联的 Execution ID
    Resource         string    `json:"resource"`         // 如 "exchange.okx"/"filesystem./workspace"
    Scope            string    `json:"scope"`            // "exclusive"/"shared"
    AcquiredAt       time.Time `json:"acquired_at"`
    ExpiresAt        time.Time `json:"expires_at"`
    TTLSeconds       int64     `json:"ttl_seconds"`
    RemainingSeconds int64     `json:"remaining_seconds"`
    AutoRelease      bool      `json:"auto_release"`     // 是否在 execution 结束后自动释放
}
```

---

## 3. WebSocket 推送协议

### 3.1 端点

```
GET /ws/dashboard
```

复用 gorilla/websocket（已在 `brains/quant/webui/ws.go` 引入），最大连接数建议 **20**（Quant WebUI 是 5，全脑 Dashboard 面向更多客户端）。

### 3.2 握手与订阅

客户端在连接建立后发送 **订阅消息**，不发送则默认订阅所有频道：

```json
{
  "action": "subscribe",
  "channels": ["brains", "executions", "events", "providers"],
  "filters": {
    "events": {
      "level": "warn",
      "sources": ["quant", "kernel"]
    },
    "executions": {
      "status": "running"
    }
  }
}
```

服务端回应确认：

```json
{
  "type": "subscribed",
  "channels": ["brains", "executions", "events", "providers"],
  "ts": 1744779000000
}
```

客户端可随时发送 `unsubscribe` 取消部分频道。

### 3.3 服务端推送消息格式

所有服务端推送消息共享同一信封结构：

```go
// WSMessage 是所有 WebSocket 推送消息的信封。
type WSMessage struct {
    Type    string      `json:"type"`    // 消息类型，见下表
    Channel string      `json:"channel"` // 所属频道
    Data    interface{} `json:"data"`
    TS      int64       `json:"ts"`      // 服务端 Unix 毫秒时间戳
}
```

### 3.4 消息类型与推送时机

| `type` | `channel` | 推送时机 | `data` 类型 |
|--------|-----------|----------|-------------|
| `brain_tick` | `brains` | 每 2 秒轮询一次，全量推送所有 brain 状态 | `[]BrainStatusDTO`（精简版） |
| `brain_state_change` | `brains` | Brain 状态变化时立即推送 | `BrainStatusDTO` |
| `execution_update` | `executions` | Execution 状态变化时立即推送 | `ExecutionDTO`（含 subtasks） |
| `execution_tick` | `executions` | 有 running 任务时每 1 秒推送 | `[]ExecutionDTO`（精简版） |
| `event_stream` | `events` | 有新事件时立即推送（批量，最多 20 条/次） | `[]EventDTO` |
| `provider_tick` | `providers` | 每 30 秒检查一次 provider 健康 | `[]ProviderDTO` |
| `overview_tick` | `overview` | 每 5 秒推送一次总览 | `OverviewDTO` |
| `error` | `*` | 推送失败或内部错误时 | `{"code": "ERR_xxx", "message": "..."}` |
| `pong` | `*` | 响应客户端 ping | `{"latency_ms": 12}` |

### 3.5 推送示例

```json
{
  "type": "brain_state_change",
  "channel": "brains",
  "data": {
    "kind": "quant",
    "status": "error",
    "health_score": 0.0,
    "last_heartbeat_at": "2026-04-16T10:28:00Z"
  },
  "ts": 1744779000000
}
```

```json
{
  "type": "event_stream",
  "channel": "events",
  "data": [
    {
      "id": "evt-20260416-0313",
      "at": "2026-04-16T10:30:01Z",
      "type": "brain.crashed",
      "level": "error",
      "source": "quant",
      "run_id": "",
      "message": "quant sidecar process exited unexpectedly",
      "data": {"exit_code": 1, "signal": "SIGSEGV"}
    }
  ],
  "ts": 1744779001000
}
```

### 3.6 客户端 Ping / Pong

客户端每 30 秒发一次 Ping：

```json
{"action": "ping", "ts": 1744779000000}
```

服务端回：

```json
{"type": "pong", "ts": 1744779000000, "latency_ms": 8}
```

---

## 4. 事件聚合层（EventBus）

### 4.1 问题陈述

当前系统有三个互相独立的事件来源：

1. **`runtimeaudit.Sink`**：通过 `context.WithValue` 注入，记录 `run.accepted`、`run.cancel.requested` 等运行时事件（`sdk/runtimeaudit/context.go`）
2. **`persistedRunEvent`**（`runtime_store.go`）：写入文件持久化的事件日志
3. **Orchestrator 内部状态变更**：Brain 启停、心跳、委托——当前没有统一事件出口

这三个来源没有汇聚点，Dashboard 无法统一查询。

### 4.2 方案：中央 EventBus

引入 `EventBus` 作为全局事件汇聚器，实现 `runtimeaudit.Sink`：

```go
// EventBus 聚合所有来源的事件，供 Dashboard 查询和 WebSocket 推送。
// 实现 runtimeaudit.Sink 接口，可直接注入 context。
type EventBus struct {
    mu     sync.RWMutex
    buf    []EventDTO     // 环形内存缓冲，最多保留 N 条
    cap    int
    seq    atomic.Int64   // 全局事件序号，生成 ID 用
    subs   []chan EventDTO // WebSocket 订阅者
    subMu  sync.RWMutex
}

// AppendEvent 实现 runtimeaudit.Sink，接收 runtimeaudit 注入的事件。
func (b *EventBus) AppendEvent(ctx context.Context, ev runtimeaudit.Event) {
    dto := EventDTO{
        ID:      b.nextID(),
        At:      time.Now().UTC(),
        Type:    ev.Type,
        Level:   inferLevel(ev.Type),
        Source:  sourceFromCtx(ctx),
        RunID:   runIDFromCtx(ctx),
        Message: ev.Message,
        Data:    ev.Data,
    }
    b.store(dto)
    b.fan(dto)
}

// EmitBrainEvent 由 Orchestrator 调用，发布 Brain 生命周期事件。
func (b *EventBus) EmitBrainEvent(kind string, evType string, msg string, data json.RawMessage) {
    dto := EventDTO{
        ID:      b.nextID(),
        At:      time.Now().UTC(),
        Type:    evType,
        Level:   inferLevel(evType),
        Source:  kind,
        Message: msg,
        Data:    data,
    }
    b.store(dto)
    b.fan(dto)
}

// Query 按过滤条件查询历史事件（最多返回 limit 条）。
func (b *EventBus) Query(f EventFilter) []EventDTO { ... }

// Subscribe 返回一个单向 channel，接收实时事件推送。
// 调用方在不需要时必须调用 Unsubscribe 避免 goroutine 泄漏。
func (b *EventBus) Subscribe() (<-chan EventDTO, func()) { ... }

// EventFilter 控制 Query 的返回范围。
type EventFilter struct {
    Types   []string
    Sources []string
    Level   string     // "info"/"warn"/"error"
    Since   time.Time
    Until   time.Time
    RunID   string
    Limit   int
    Cursor  string
}
```

### 4.3 与现有 runtimeaudit 的集成

**修改 `runServe()`（`cmd_serve.go`）**：

```go
// 创建全局 EventBus，注入 context
bus := dashboard.NewEventBus(dashboard.EventBusConfig{
    MaxEvents: 10000, // 内存保留 1 万条
})

// 将 EventBus 注入 serve context，让所有 runtimeaudit.Emit() 都流入 bus
serveCtx = runtimeaudit.WithSink(serveCtx, bus)

// 将 bus 传给 Dashboard handler
dashHandler := dashboard.NewHandler(mgr, orch, bus, ...)
```

**修改 Orchestrator** 在 Brain 状态变更时调用 `bus.EmitBrainEvent()`：

```go
// 在 getOrStartSidecar 成功后
bus.EmitBrainEvent(string(kind), EventTypeBrainStarted, 
    fmt.Sprintf("%s sidecar started", kind), nil)

// 在心跳超时后
bus.EmitBrainEvent(string(kind), EventTypeBrainCrashed,
    fmt.Sprintf("%s sidecar crashed", kind), 
    jsonMarshal(map[string]any{"exit_code": exitCode}))
```

### 4.4 数据流总图

```text
  runtimeaudit.Emit(ctx, ev)          Orchestrator.brain_lifecycle
           │                                      │
           ▼                                      ▼
     EventBus.AppendEvent()          EventBus.EmitBrainEvent()
           │                                      │
           └───────────────┬──────────────────────┘
                           │
                    EventBus（内存环形缓冲）
                    ├── Query()  ──▶  GET /v1/dashboard/events
                    └── Subscribe() ─▶  /ws/dashboard push
```

### 4.5 持久化策略

- **默认**：内存环形缓冲（10000 条），进程重启后丢失历史
- **可选**：写入 `runtimeStore`（已有 `appendEvent` 方法），Dashboard 查询时优先读内存，Miss 时读文件
- **不做**：不引入外部数据库，保持零依赖原则

---

## 5. 向后兼容 — /v1/runs 迁移策略

### 5.1 原则

不 Breaking Change。`/v1/runs` 端点**保留运行**，但标记为 deprecated，响应中加 `X-Deprecated-Use` header 提示迁移。

### 5.2 实现方式

在 `cmd_serve.go` 中，让原有 handler 直接代理到新的 Dashboard handler：

```go
// 保留旧端点，内部代理到 dashboard handler
mux.HandleFunc("/v1/runs", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("X-Deprecated-Use", "/v1/dashboard/executions")
    switch r.Method {
    case http.MethodPost:
        // POST /v1/runs → POST /v1/dashboard/executions（同一个 handleCreateRun）
        handleCreateRun(w, r, mgr, runtime, cfg, *maxRuns, mode, env.workdir, runWorkdirPolicy)
    case http.MethodGet:
        // GET /v1/runs → GET /v1/dashboard/executions，转换响应格式
        handleListExecutionsCompat(w, r, mgr)
    default:
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
    }
})

mux.HandleFunc("/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("X-Deprecated-Use", "/v1/dashboard/executions/:id")
    id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
    switch r.Method {
    case http.MethodGet:
        handleGetRun(w, r, mgr, id) // 格式兼容，无需改动
    case http.MethodDelete:
        handleCancelRun(w, r, mgr, id)
    }
})
```

### 5.3 响应格式映射

| 旧字段（`/v1/runs`） | 新字段（`/v1/dashboard/executions`） | 说明 |
|---------------------|--------------------------------------|------|
| `run_id` | `id` | 改名 |
| `status` | `status` | 不变 |
| `brain` | `brain` | 不变 |
| `prompt` | `prompt` | 不变 |
| `result` | `result` | 不变 |
| `created_at` | `created_at` | 不变 |
| 无 | `updated_at` | 新增 |
| 无 | `duration_ms` | 新增 |
| 无 | `turns` | 新增 |
| 无 | `subtasks` | 新增（详情页） |
| 无 | `events` | 新增（详情页） |

`/v1/runs` 的 `GET` 响应保留原有的 `{"runs": [...]}` 格式，不改变 `run_id` 字段名，避免现有 SDK 和脚本 Breaking。

---

## 6. 前端嵌入方案

### 6.1 现有基础

Quant WebUI 的嵌入方式（`brains/quant/webui/embed.go`）：

```go
//go:embed static
var staticFS embed.FS
```

静态文件在 `brains/quant/webui/static/` 目录，路由在 `server.go` 中用 `fs.Sub` 挂载到 `/`。

### 6.2 全脑 Dashboard 的嵌入方案

建议在 `cmd/brain/` 目录下新建 `dashboard/` 子目录，与主命令一起编译：

```
cmd/brain/
├── cmd_serve.go          # 现有，注册 /v1/runs 等端点
├── dashboard/
│   ├── embed.go          # //go:embed static
│   ├── handler.go        # HTTP handler（注册到 /v1/dashboard/* 和 /ws/dashboard）
│   ├── dto.go            # DTO 定义（上方 Go 结构体）
│   ├── eventbus.go       # EventBus 实现
│   ├── brains.go         # Brain 状态采集（从 Orchestrator 读）
│   ├── providers.go      # Provider 健康检查
│   ├── leases.go         # Lease 管理
│   └── static/           # 前端构建产物（SPA）
│       ├── index.html
│       ├── app.js
│       └── app.css
```

**embed.go**：

```go
package dashboard

import "embed"

//go:embed static
var staticFS embed.FS
```

**在 cmd_serve.go 中挂载 Dashboard**：

```go
import "github.com/leef-l/brain/cmd/brain/dashboard"

// 创建 Dashboard handler
bus := dashboard.NewEventBus(dashboard.DefaultConfig())
serveCtx = runtimeaudit.WithSink(serveCtx, bus)

dashHandler := dashboard.NewHandler(dashboard.Config{
    Manager:    mgr,
    Orchestrator: startupOrch,
    EventBus:   bus,
    Runtime:    runtime,
    StartedAt:  time.Now(),
})

// 挂载 Dashboard API
dashHandler.Register(mux)  // 注册 /v1/dashboard/* 和 /ws/dashboard

// 挂载 Dashboard 静态文件（SPA）
dashHandler.RegisterStatic(mux)  // 挂载 /dashboard/ → embed.FS
```

**Register 方法实现**：

```go
func (h *Handler) Register(mux *http.ServeMux) {
    mux.HandleFunc("/v1/dashboard/overview",        h.handleOverview)
    mux.HandleFunc("/v1/dashboard/brains",          h.handleBrains)
    mux.HandleFunc("/v1/dashboard/brains/",         h.handleBrain)  // 含 :kind 和 :kind/restart
    mux.HandleFunc("/v1/dashboard/executions",      h.handleExecutions)
    mux.HandleFunc("/v1/dashboard/executions/",     h.handleExecution)
    mux.HandleFunc("/v1/dashboard/events",          h.handleEvents)
    mux.HandleFunc("/v1/dashboard/providers",       h.handleProviders)
    mux.HandleFunc("/v1/dashboard/leases",          h.handleLeases)
    mux.HandleFunc("/v1/dashboard/leases/",         h.handleLease)
    mux.HandleFunc("/ws/dashboard",                 h.handleWS)
}

func (h *Handler) RegisterStatic(mux *http.ServeMux) {
    sub, _ := fs.Sub(staticFS, "static")
    fileServer := http.FileServer(http.FS(sub))
    mux.Handle("/dashboard/", http.StripPrefix("/dashboard/",
        http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
            fileServer.ServeHTTP(w, r)
        }),
    ))
}
```

### 6.3 端口策略

| 服务 | 端口 | 说明 |
|------|------|------|
| Brain Kernel API + Dashboard | `:7701` | `brain serve` 默认端口，含 REST + WebSocket + 静态 SPA |
| Quant WebUI | `:8380` | 量化专用业务面板，独立端口，继续保留 |

两个服务**不合并**，职责分离：

- `:7701` = 全脑控制面（运维视角）
- `:8380` = Quant 业务面板（交易员视角）

### 6.4 前端技术选型建议

延续 Quant WebUI 的轻量化路线（纯 HTML + Vanilla JS 或 Preact），**不引入 React/Vue/Webpack**，原因：

1. 与现有 Quant WebUI 一致，`embed.FS` 中不需要 node_modules
2. 避免引入 pnpm/npm 构建步骤（当前环境约束）
3. Dashboard 的 UI 复杂度（表格 + 图表 + WebSocket）用 Vanilla JS + Chart.js 完全可以覆盖

如需更现代的开发体验，可以在 `npm/` 目录下（已有）单独构建，产物 copy 到 `dashboard/static/`，不影响 Go 编译。

---

## 7. 实现优先级与 Milestone

### P0（核心骨架，优先落地）

1. `EventBus` 实现 + 接入 `runtimeaudit.WithSink`
2. `GET /v1/dashboard/brains` — 从 Orchestrator 读 `active` map
3. `GET /v1/dashboard/executions` — 从 `runManager` 读，字段扩展
4. `/ws/dashboard` — 连接管理 + EventBus fan-out
5. `GET /v1/dashboard/overview` — 聚合上面所有来源

### P1（完整可用）

6. `GET /v1/dashboard/events` — 支持过滤和分页
7. `GET /v1/dashboard/brains/:kind` — 详情页，含工具列表和健康历史
8. `GET /v1/dashboard/providers` — Provider 健康检查
9. 前端 SPA 骨架（Brains / Executions / Events 三个标签页）

### P2（进阶能力）

10. `GET /v1/dashboard/leases` — Lease 管理（依赖 §7.7 AcquireSet 实现）
11. `POST /v1/dashboard/brains/:kind/restart` — Brain 重启（依赖 Orchestrator 支持）
12. Provider 30 秒健康轮询 + WS 推送
13. Quant 业务数据嵌入 Dashboard（iframe 或 API 代理）

---

## 8. 关键设计决策记录

| 决策 | 选择 | 理由 |
|------|------|------|
| Dashboard 端口 | `:7701`（与 brain serve 共用） | 不增加运维复杂度，SPA 跟 API 同源，无 CORS |
| Quant WebUI 保留 | `:8380` 独立运行 | 职责分离，量化业务面板面向不同受众 |
| EventBus 存储 | 内存环形缓冲（默认）+ 可选文件持久化 | 零依赖，进程内低延迟；持久化需求由 runtimeStore 覆盖 |
| WebSocket 订阅模型 | 频道 + 过滤器，连接建立后发 subscribe 消息 | 灵活，Dashboard 只接收关心的频道，节省带宽 |
| `/v1/runs` 兼容 | 保留端点 + `X-Deprecated-Use` header | 不 Breaking，给已有脚本迁移窗口（建议 v3.1 移除） |
| 前端框架 | Vanilla JS + Chart.js | 不引入 npm 构建，embed.FS 友好 |
| Lease 实现依赖 | `/v1/dashboard/leases` 为 P2 | Lease 机制（§7.7）尚未实现，API 先定义，后端实现跟上 |

---

*文档版本 v1 · 2026-04-16 · 作者：Brain v3 架构师（Claude Sonnet 4.6）*
