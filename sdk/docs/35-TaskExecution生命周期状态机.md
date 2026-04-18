# 35. §7.7.1 TaskExecution 生命周期状态机 — 设计细化方案

> **状态**：v1 · 2026-04-16
> **所属规格**：[32-v3-Brain架构.md](./32-v3-Brain架构.md) §7.7.1
> **依赖**：[22-Agent-Loop规格.md](./22-Agent-Loop规格.md) · [sdk/loop/run.go](../loop/run.go)
> **作者**：Brain v3 架构组

---

## 0. 概述

本文档是 §7.7.1 TaskExecution 唯一执行对象的**状态机设计细化方案**，覆盖：

1. 完整状态枚举（在现有 8 状态基础上新增/合并决策）
2. 状态转移图（所有合法迁移 + 触发条件）
3. ExecutionMode 运行时切换规则
4. Lifecycle 终止条件（oneshot / daemon / watch 三路）
5. RestartPolicy 执行逻辑（判断时机、退避策略、最大重试、状态回转）
6. Budget 与状态的交互
7. 父子 TaskExecution 状态传播
8. 从 loop/run.go 的 Run 到 TaskExecution 的具体迁移路径

---

## 1. 完整状态枚举

### 1.1 继承现有 8 个状态的决策

现有 `loop/run.go` 中已定义：

```
pending → running → waiting_tool → paused → completed
                                          → failed
                                          → canceled
                                          → crashed
```

这 8 个状态对 `oneshot` 场景是足够的。问题在于 `daemon` 和 `watch` 需要额外语义：

| 场景 | 现有状态的问题 | 需要新增 |
|------|--------------|---------|
| daemon 正在优雅停止 | `running` 和 `canceled` 之间没有过渡状态，外部无法区分"正在清理"和"已取消" | `draining` |
| daemon 崩溃后等待重启计时器 | `crashed` 是终态，`pending` 是初态，两者之间没有"正在退避等待重启"的语义 | `restarting` |
| watch 等待触发事件 | `paused` 的语义是"人为暂停"，而 watch 的等待是正常运行态 | `waiting_event` |

### 1.2 最终状态枚举（12 个）

```go
// ExecutionState 是 TaskExecution 的状态机标签。
// 合法迁移图见 §2。不允许出现枚举外的字符串值。
type ExecutionState string

const (
    // ── 初始态 ──────────────────────────────────────────────────────────

    // StatePending：已创建，等待 Runner 调度。
    // 所有 TaskExecution 必须从此状态开始。
    StatePending ExecutionState = "pending"

    // ── 活跃态 ──────────────────────────────────────────────────────────

    // StateRunning：Runner 正在执行当前 Turn，LLM 可以随时被调用。
    StateRunning ExecutionState = "running"

    // StateWaitingTool：当前 Turn 已发出 tool_use，等待异步工具返回结果。
    // 此时 LLM 未被调用，Budget 的时间维度仍在计时。
    StateWaitingTool ExecutionState = "waiting_tool"

    // StateWaitingEvent：watch lifecycle 专用。
    // 上一次执行完成，正在等待下一个触发事件（如文件变更、定时信号、外部推送）。
    // 不同于 StatePaused（人为暂停），WaitingEvent 是 watch 的正常空闲态。
    StateWaitingEvent ExecutionState = "waiting_event"

    // StatePaused：人工控制面命令显式暂停。
    // Runner 不得启动新 Turn，直到收到 Resume 指令。
    // 适用于 interactive 和 daemon；watch 任务建议用 stop/restart 而非 pause。
    StatePaused ExecutionState = "paused"

    // StateDraining：daemon/watch 收到停止指令，正在优雅退出。
    // Runner 完成当前 Turn 和工具调用后进入此状态，然后执行 on-stop 钩子，
    // 最终迁移到 StateCanceled。
    // 与 StateCanceled 区别：Draining 可被监控，外部知道"正在关闭"。
    StateDraining ExecutionState = "draining"

    // StateInterrupted：被 LeaseManager 强制撤销 lease 后进入的中间状态。
    // 收到 LeaseRevokedError 后的 graceful 中断，允许清理资源。
    // 前驱状态：running, waiting_tool, waiting_event（任何持有 lease 的活跃状态）。
    // 后继状态：restarting（RestartPolicy 允许）或 failed（重试耗尽或 policy=never）。
    StateInterrupted ExecutionState = "interrupted"

    // StateRestarting：RestartPolicy 触发重启，正在退避等待计时器。
    // 等待期结束后迁移回 StatePending，由 Runner 重新调度。
    // 此状态期间 Budget 不计 Turn，但 ElapsedTime 继续计时。
    StateRestarting ExecutionState = "restarting"

    // ── 终态（terminal） ─────────────────────────────────────────────────

    // StateCompleted：成功完成。
    // oneshot：LLM 返回最终答复且 Budget 有余量。
    // daemon/watch：仅在被显式 stop 且 draining 完成后才会进入（通常走 canceled）。
    StateCompleted ExecutionState = "completed"

    // StateFailed：不可恢复失败。BrainError 已附加到最后一个 Turn。
    // RestartPolicy 触发重启时，会先离开此状态进入 StateRestarting，
    // 因此 StateFailed 作为终态仅在重启耗尽或 policy=never 时才最终落地。
    StateFailed ExecutionState = "failed"

    // StateCanceled：外部取消。
    // 来源：context 取消、控制面 stop 指令、draining 完成。
    StateCanceled ExecutionState = "canceled"

    // StateCrashed：进程崩溃且无法通过标准路径恢复。
    // 崩溃后若 RestartPolicy 允许，先进入 StateRestarting；
    // 若不允许或重启已耗尽，则落在 StateCrashed。
    StateCrashed ExecutionState = "crashed"
)

// IsTerminal 返回 true 表示状态机已无法继续迁移。
func (s ExecutionState) IsTerminal() bool {
    switch s {
    case StateCompleted, StateFailed, StateCanceled, StateCrashed:
        return true
    }
    return false
}

// IsActive 返回 true 表示 Runner 正在持有资源（需要 Lease）。
func (s ExecutionState) IsActive() bool {
    switch s {
    case StateRunning, StateWaitingTool, StateWaitingEvent, StatePaused, StateDraining, StateInterrupted:
        return true
    }
    return false
}
```

### 1.3 合并/排除决策说明

| 候选 | 决策 | 原因 |
|------|------|------|
| 把 `crashed` 和 `failed` 合并 | **不合并** | `crashed` 明确表示进程级崩溃（信号 9/11），有 crash recovery 路径；`failed` 是业务错误，语义不同 |
| 为 `interactive→background` 切换加一个 `demoting` 状态 | **不加** | mode 切换是元数据变更，不影响状态机路径；切换完成前 Runner 仍在 `running`，外部看不到差异 |
| 把 `paused` 拆成 `paused_human` / `paused_system` | **不拆** | 过度设计，用 `PauseReason` 字段区分即可 |
| 为 `waiting_event` 加超时后的 `idle_timeout` 状态 | **不加** | 超时直接迁移到 `completed`（watch 的一种正常终止条件），不需要中间态 |
| 新增 `interrupted` 中间状态 | **新增** | LeaseManager 强制撤销 lease 后需要 graceful 中断语义，区别于直接进入 `failed`/`crashed`；允许资源清理后再由 RestartPolicy 决定去向 |

---

## 2. 状态转移图

### 2.1 ASCII 总图

```
                    ┌─────────────────────────────────────────────────┐
                    │               触发条件图例                        │
                    │  [A] Runner.Execute() 调度                        │
                    │  [B] LLM 返回 tool_use                           │
                    │  [C] 工具执行完成                                  │
                    │  [D] LLM 返回最终答复 / StopReason=end_turn       │
                    │  [E] Budget 耗尽 (CheckTurn/CheckCost)            │
                    │  [F] context.Cancel / 控制面 stop                 │
                    │  [G] daemon/watch 收到 stop，draining 完成        │
                    │  [H] 进程崩溃 (signal/panic)                      │
                    │  [I] 人工 Pause 指令                               │
                    │  [J] 人工 Resume 指令                              │
                    │  [K] watch 触发事件到达                            │
                    │  [L] RestartPolicy 允许重启，退避计时器到期         │
                    │  [M] RestartPolicy 耗尽或 never，无法再重启        │
                    │  [N] oneshot 完成 / watch idle_timeout 到达        │
                    │  [O] LeaseManager 强制撤销 lease (LeaseRevokedError)│
                    └─────────────────────────────────────────────────┘

                          [A]
  (create) ──────────▶ pending ──────────────────────────────┐
                          │                                   │ [F]
                          │ [A]                               ▼
                          ▼                               canceled ◀── [G] ── draining ◀── [F] ──┐
                       running ◀──────────────────── [J] ── paused                               │
                          │  ▲                                                                    │
                     [B]  │  │ [C]                                                                │
                          ▼  │                                                                    │
                     waiting_tool                                                                  │
                          │                                                                       │
                          │ [D] oneshot 完成                                                       │
                          ▼                                                                        │
                       completed ◀── [N] ── waiting_event ◀── [D] ── running                     │
                                                  │ [K]                  │                        │
                                                  └──────────────────────┘                        │
                                                                                                   │
  running ──[E]──▶ failed ──[L]──▶ restarting ──[L 到期]──▶ pending                             │
  running ──[H]──▶ crashed ──[L]──▶ restarting                                                   │
                                         │ [M]                                                    │
                                         ▼                                                        │
                                      failed / crashed (终态)                                     │
                                                                                                   │
  running ──────[O]──▶ interrupted ──[L]──▶ restarting                                            │
  waiting_tool ─[O]──▶ interrupted ──[M]──▶ failed (终态)                                         │
  waiting_event [O]──▶ interrupted                                                                │
                                                                                                   │
  running ──[I]──▶ paused ──[J]──▶ running                                                       │
  running ──[F]──▶ draining ──────────────────────────────────────────────────────────────────────┘
```

### 2.2 合法迁移表（规范版本）

| 当前状态 | 目标状态 | 触发条件 | 约束 |
|---------|---------|---------|------|
| `pending` | `running` | Runner 调度此任务 | 必须先检查 Budget |
| `pending` | `canceled` | context 取消 / 控制面 stop | 尚未消耗任何资源 |
| `running` | `waiting_tool` | LLM 返回 tool_use，工具分发开始 | Lease 已通过 AcquireSet |
| `running` | `waiting_event` | watch lifecycle，当前轮次结束，等待事件 | 仅 Watch lifecycle 合法 |
| `running` | `completed` | LLM 返回最终答复，StopReason≠tool_use | 仅 oneshot 合法；daemon/watch 不进 completed |
| `running` | `paused` | 人工 Pause 指令 | interactive / daemon 皆可；paused 期间 Budget 计时仍继续 |
| `running` | `draining` | daemon/watch 收到 stop 指令 | 仅 daemon/watch；完成当前 Turn 后进入 |
| `running` | `failed` | Budget 耗尽 / BrainError ClassPermanent | 先尝试 RestartPolicy |
| `running` | `crashed` | 进程崩溃，Runner 检测到信号 | 先尝试 RestartPolicy |
| `waiting_tool` | `running` | 所有工具结果返回，下一 Turn 开始 | Lease 在 ScopeTurn 时此处释放 |
| `waiting_tool` | `failed` | 工具执行全部失败且不可重试 | |
| `waiting_tool` | `draining` | 收到 stop 指令，等待工具返回后退出 | 工具必须超时或完成后再迁移 |
| `waiting_tool` | `canceled` | context 取消 | 立即取消，不等工具返回 |
| `waiting_event` | `running` | watch 触发事件到达 | 重置 Turn Budget（可选配置） |
| `waiting_event` | `completed` | idle_timeout 到达（可选）或 watch 收到 stop | |
| `waiting_event` | `draining` | 收到 stop 指令 | |
| `paused` | `running` | 人工 Resume 指令 | |
| `paused` | `canceled` | 控制面 stop 或 context 取消 | |
| `paused` | `failed` | Budget 超时在暂停期间耗尽 | |
| `draining` | `canceled` | on-stop 钩子完成，优雅退出 | 正常终止路径 |
| `draining` | `crashed` | draining 超时（默认 30s）仍未完成 | 强制终止 |
| `restarting` | `pending` | 退避计时器到期，可以重启 | Runner 重置 Turn 计数，但保留 Budget 累计 |
| `restarting` | `failed` | 重启次数耗尽 / RestartPolicy=never | 终态 |
| `restarting` | `crashed` | 重启次数耗尽且原因是崩溃 | 终态 |
| `failed` | `restarting` | RestartPolicy 允许 | 见 §5 |
| `crashed` | `restarting` | RestartPolicy 允许 | 见 §5 |

---

## 3. ExecutionMode 切换规则

### 3.1 语义定义

```go
type ExecutionMode string

const (
    // ModeInteractive：前台模式。
    // - 输出流直连用户（SSE / WebSocket / stdout）
    // - 控制面可以实时 Pause / Resume / Stop
    // - 一般由用户显式触发（brain run / chat）
    ModeInteractive ExecutionMode = "interactive"

    // ModeBackground：后台模式。
    // - 输出通过 EventBus 异步推送
    // - 用户不阻塞在结果上
    // - 一般由 Central delegate 或 /background 命令触发
    ModeBackground ExecutionMode = "background"
)
```

### 3.2 运行时切换：interactive → background

用户在前台任务执行中途发出 `/background` 指令，或 Central delegate 时降级模式。

**条件**：

- TaskExecution 处于 `running`、`waiting_tool` 或 `paused` 之一
- Lifecycle 为 `oneshot` 或 `watch`（daemon 默认后台，无需切换）
- 不处于 `draining`、`restarting` 或任何终态

**执行步骤**：

```go
func (te *TaskExecution) DemoteToBackground(ctx context.Context) error {
    // 1. 检查当前状态是否允许切换
    if !te.Status.State.IsActive() || te.Status.State == StateDraining {
        return ErrInvalidModeTransition
    }
    if te.Mode == ModeBackground {
        return nil // 幂等
    }

    // 2. 解除前台 StreamConsumer 绑定
    // （将 StreamConsumer 指向 EventBus，不再直连用户输出）
    te.streamConsumer = te.eventBus.AsStreamConsumer(te.ID)

    // 3. 更新 Mode 字段（原子写，持久化）
    te.Mode = ModeBackground

    // 4. 通知用户：任务已转入后台，通过 EventBus 跟踪
    te.eventBus.Publish(TaskDemotedEvent{
        ExecutionID: te.ID,
        OldMode:     ModeInteractive,
        NewMode:     ModeBackground,
        At:          time.Now().UTC(),
    })

    // 5. 状态不变（仍然是 running/waiting_tool/paused）
    return nil
}
```

**约束**：

- Mode 切换是**元数据操作**，不产生状态转移，不影响 Budget 计时
- 切换后当前 Turn 继续执行，不中断
- **background → interactive 的反向切换**：v3.0 不支持（会引入 stream 重放复杂度）。用户可以通过 `/attach {id}` 订阅 EventBus 历史事件来"重连"后台任务，但不改变 Mode 字段

### 3.3 Mode 对行为的影响矩阵

| 行为 | interactive | background |
|------|-------------|------------|
| LLM 输出流 | 直连用户（SSE/stdout） | 写入 EventBus |
| Pause/Resume | 用户可直接发指令 | 仅控制面 API |
| Budget 超时通知 | 实时终端输出 | EventBus 推送 |
| 崩溃重启 | 用户看到重启消息 | EventBus 事件 |
| 完成通知 | 终端打印结果 | EventBus + 推送通知 |

---

## 4. Lifecycle 终止条件

三种 Lifecycle 对"任务结束"的定义完全不同：

### 4.1 OneShot — 执行一次即结束

```
完成条件（any）：
  ① LLM 返回最终答复（StopReason ≠ tool_use）
  ② Budget 任意维度耗尽（→ StateFailed）
  ③ context 取消（→ StateCanceled）
  ④ 进程崩溃且 RestartPolicy=never（→ StateCrashed）
```

**实现**：现有 `Runner.Execute` 的主循环逻辑直接对应 OneShot + Interactive。

### 4.2 Daemon — 持续运行直到被明确停止

```
完成条件（any）：
  ① 控制面 stop 指令 → StateDraining → StateCanceled
  ② context 取消 → StateCanceled
  ③ RestartPolicy 耗尽 → StateFailed / StateCrashed
  
注意：daemon 不因"单轮 LLM 返回最终答复"而结束。
      一轮完成后 Runner 立刻重新发起下一轮（或等待新输入）。
```

**关键设计**：daemon 的每一"工作周期"是一个 Turn 序列，周期之间 Budget 如何重置？

```go
// DaemonCyclePolicy 控制 daemon 每个工作周期的 Budget 行为
type DaemonCyclePolicy struct {
    // ResetBudgetPerCycle：每个工作周期是否重置 Turn/Cost/ToolCalls 计数
    // true  = 每轮独立计费（适合周期性采集任务）
    // false = 累计计费（适合长驻对话任务，Budget 是总量上限）
    ResetBudgetPerCycle bool

    // CycleMaxDuration：单个工作周期的最大持续时间，超时后视为本周期失败
    CycleMaxDuration time.Duration

    // IdleTimeout：daemon 空闲等待下一个触发的最大时间，0 表示无限等待
    IdleTimeout time.Duration
}
```

### 4.3 Watch — 事件驱动，有事件时执行，无事件时等待

```
完成条件（any）：
  ① 控制面 stop 指令 → StateDraining → StateCanceled
  ② IdleTimeout 到达（没有新事件）→ StateCompleted
  ③ context 取消 → StateCanceled
  ④ RestartPolicy 耗尽 → StateFailed

正常流转：
  running（处理事件）→ completed（本次处理）→ waiting_event（等待下一事件）
  waiting_event ──[新事件]──▶ running（处理新事件）
  waiting_event ──[timeout]──▶ completed（终止）
```

**Watch 触发器接口**：

```go
// WatchTrigger 是 watch lifecycle 的事件源接口
type WatchTrigger interface {
    // Wait 阻塞直到有事件到来或 ctx 取消。
    // 返回事件描述和触发消息（注入 initialMessages 或追加）。
    Wait(ctx context.Context) (event WatchEvent, messages []llm.Message, err error)
}

type WatchEvent struct {
    Type    string          // "file_change" / "schedule" / "signal" / "push"
    Payload json.RawMessage // 事件原始数据，注入 system prompt 或首条 user message
}
```

### 4.4 Lifecycle 对比表

| 维度 | oneshot | daemon | watch |
|------|---------|--------|-------|
| 正常终态 | `completed` | `canceled` | `completed` / `canceled` |
| "一轮结束"后行为 | 终止 | 立即重启下一轮 | 进入 `waiting_event` |
| Budget 重置 | 不重置 | 可配置 | 每次触发可重置 Turn 计数 |
| 空闲时状态 | 无（已终止） | `running`（等待输入） | `waiting_event` |
| stop 触发 | → `canceled` | → `draining` → `canceled` | → `draining` → `canceled` |

---

## 5. RestartPolicy 执行逻辑

### 5.1 触发时机

RestartPolicy 在 TaskExecution 到达以下状态时**立即评估**（在终态落地之前）：

```
StateRunning → StateFailed   [Budget 耗尽 / BrainError]
StateRunning → StateCrashed  [进程崩溃]
StateWaitingTool → StateFailed
StateDraining → StateCrashed [draining 超时]
```

评估逻辑：

```go
func (te *TaskExecution) evaluateRestart(exitState ExecutionState, cause error) (shouldRestart bool) {
    switch te.Restart {
    case RestartNever:
        return false
    case RestartOnFailure:
        // 仅在非正常退出时重启；context.Canceled 视为正常取消，不重启
        if exitState == StateFailed || exitState == StateCrashed {
            return !errors.Is(cause, context.Canceled)
        }
        return false
    case RestartAlways:
        // 任何退出（含 failed/crashed，但不含 canceled）都重启
        return exitState != StateCanceled
    }
    return false
}
```

### 5.2 退避策略（指数退避 + 抖动）

```go
// RestartConfig 是 RestartPolicy 的详细配置，挂在 TaskExecution 上
type RestartConfig struct {
    // MaxRetries：最大重启次数，0 表示无限（daemon+Always 的典型配置）
    MaxRetries int

    // InitialBackoff：首次退避时间（默认 1s）
    InitialBackoff time.Duration

    // MaxBackoff：退避上限（默认 5min）
    MaxBackoff time.Duration

    // BackoffMultiplier：每次退避的倍率（默认 2.0）
    BackoffMultiplier float64

    // JitterFraction：在退避时间基础上加 ±JitterFraction 的随机抖动（默认 0.2）
    // 防止大规模崩溃时所有 task 同时重启造成雷群效应
    JitterFraction float64
}

// NextBackoff 根据已重启次数计算下次等待时间
func (rc *RestartConfig) NextBackoff(retryCount int) time.Duration {
    if rc.InitialBackoff == 0 {
        rc.InitialBackoff = time.Second
    }
    if rc.MaxBackoff == 0 {
        rc.MaxBackoff = 5 * time.Minute
    }
    if rc.BackoffMultiplier == 0 {
        rc.BackoffMultiplier = 2.0
    }

    // 指数退避
    backoff := float64(rc.InitialBackoff) * math.Pow(rc.BackoffMultiplier, float64(retryCount))
    if backoff > float64(rc.MaxBackoff) {
        backoff = float64(rc.MaxBackoff)
    }

    // 加抖动
    jitter := rc.JitterFraction
    if jitter == 0 {
        jitter = 0.2
    }
    delta := backoff * jitter
    backoff += (rand.Float64()*2 - 1) * delta // [-delta, +delta]

    return time.Duration(backoff)
}
```

### 5.3 状态回转路径

```
StateFailed ──[evaluateRestart=true]──▶ StateRestarting
StateCrashed ──[evaluateRestart=true]──▶ StateRestarting
     │
     │  (退避等待 NextBackoff(retryCount) 时间)
     │  (Runner 阻塞在 time.After，可被 ctx 取消)
     ▼
StatePending ──[Runner 重新调度]──▶ StateRunning
```

重启时 TaskExecution 的哪些字段被重置：

```go
func (te *TaskExecution) prepareForRestart(now time.Time) {
    // ✅ 重置：执行状态
    te.Status.State = StatePending
    te.Status.StartedAt = time.Time{}  // 等待下次 Start() 设置
    te.Status.EndedAt = nil

    // ✅ 重置：Turn 相关计数（视 DaemonCyclePolicy.ResetBudgetPerCycle 配置）
    if te.shouldResetBudget() {
        te.Budget.UsedTurns = 0
        te.Budget.UsedLLMCalls = 0
        te.Budget.UsedToolCalls = 0
        // 注意：UsedCostUSD 永远不重置（计费是累计的）
        // 注意：ElapsedTime 永远不重置（总时间计费）
    }

    // ✅ 重置：消息历史（视配置；daemon 通常保留对话历史）
    if te.restartConfig.ClearMessages {
        te.Messages = te.initialMessages // 回到初始消息
    }

    // ❌ 不重置：ID、ParentID、BrainID、Mode、Lifecycle、Restart
    // ❌ 不重置：RetryCount（需要递增）
    // ❌ 不重置：UsedCostUSD、总 ElapsedTime

    te.Status.RetryCount++
    te.Status.LastRestartAt = &now
    te.Status.LastFailCause = te.Status.LastError // 保留上次失败原因供观测
}
```

### 5.4 重启耗尽逻辑

```go
func (te *TaskExecution) checkRetryExhausted() bool {
    if te.restartConfig.MaxRetries == 0 {
        return false // 0 = 无限重启
    }
    return te.Status.RetryCount >= te.restartConfig.MaxRetries
}
```

耗尽后：

- 原始退出状态是 `StateFailed` → 终态为 `StateFailed`
- 原始退出状态是 `StateCrashed` → 终态为 `StateCrashed`
- 附加 `BrainError{Code: CodeRestartExhausted, RetryCount: n, LastCause: ...}`

---

## 6. Budget 与状态的交互

### 6.1 Budget 检查时机

```
Loop 入口（每个 Turn 开始前）：
  CheckTurn() → 检查 turns / cost / llmCalls / toolCalls / timeout
  
Turn 中途（LLM 返回后）：
  CheckCost() → 检查 cost / llmCalls
  
状态机层面：
  Budget 耗尽 → StateFailed（不是 StateCanceled）
  evaluateRestart() → 可能转 StateRestarting
```

### 6.2 各 Lifecycle 下 Budget 耗尽的处理

| Lifecycle | Budget 耗尽时 | 下一步 |
|-----------|--------------|--------|
| oneshot | → StateFailed | evaluateRestart → 重启或终止 |
| daemon | 本周期 → StateFailed | evaluateRestart（几乎总是 always）→ StateRestarting → 下个周期 Budget 重置后继续 |
| watch | 本次触发处理 → StateFailed | evaluateRestart → 重启或跳过本次，等下次事件 |

**关键约束**：

```go
// Budget.UsedCostUSD 永远累计，不随重启重置
// 这是计费的硬约束：用户的钱不会因为崩溃重启而"退款"

// MaxCostUSD 是整个 TaskExecution 生命期的上限
// 如果希望"每天最多花 X 元"，用 DaemonCyclePolicy + 外部调度器在每天创建新 TaskExecution

// Budget 耗尽通知路径
func (te *TaskExecution) onBudgetExhausted(cause error) {
    be := toBrainError(cause)
    te.Status.LastError = be
    
    // daemon/watch：通过 EventBus 推送，不强制终止（由 evaluateRestart 决定）
    te.eventBus.Publish(BudgetExhaustedEvent{
        ExecutionID: te.ID,
        Cause:       be,
        RetryCount:  te.Status.RetryCount,
    })
}
```

### 6.3 新增 Budget 字段

在现有 `loop.Budget` 基础上，TaskExecution 级别追加：

```go
// TaskBudget 扩展了 loop.Budget，增加 lifecycle 维度的计费控制
type TaskBudget struct {
    loop.Budget // 嵌入现有字段（MaxTurns, MaxCostUSD 等）

    // ── daemon/watch 专用 ─────────────────────────────

    // MaxTotalCostUSD：整个 TaskExecution 生命期的总费用上限（跨重启累计）
    // 0 = 不限制总费用（但单周期的 MaxCostUSD 仍有效）
    MaxTotalCostUSD float64

    // TotalUsedCostUSD：跨重启的累计费用（只增不减）
    TotalUsedCostUSD float64

    // MaxCycles：daemon 允许运行的最大周期数，0 = 无限
    MaxCycles int

    // UsedCycles：已完成的周期数
    UsedCycles int
}
```

---

## 7. 父子 TaskExecution 状态传播

### 7.1 数据结构

```go
// ExecutionStatus 是 TaskExecution 运行时状态快照
type ExecutionStatus struct {
    State          ExecutionState
    RetryCount     int
    LastError      *brainerrors.BrainError
    LastRestartAt  *time.Time
    LastFailCause  *brainerrors.BrainError
    StartedAt      time.Time
    EndedAt        *time.Time
    ChildrenStates map[string]ExecutionState // childID → state，实时快照
}
```

### 7.2 传播方向：子 → 父

子 TaskExecution 的状态变化需要通知父任务，父任务根据自身的 Lifecycle 决定是否联动。

```
子任务完成（StateCompleted）→ 父任务收到 childCompleted 事件
                           → 父任务判断：所有子任务完成？→ 父任务可以 Complete
                                        还有子任务？   → 父任务继续 Running

子任务失败（StateFailed）  → 父任务收到 childFailed 事件
                           → 父任务根据 ChildFailurePolicy 决定：
                             PropagateImmediately → 父任务立刻 Fail
                             WaitAll              → 等其他子任务完成再综合判断
                             Ignore               → 忽略，子任务失败不影响父任务

子任务取消（StateCanceled）→ 父任务收到 childCanceled 事件
                           → 通常 Ignore（子任务是可选的）

子任务崩溃（StateCrashed）→ 父任务收到 childCrashed 事件
                           → 通常 PropagateImmediately（崩溃比失败更严重）
```

### 7.3 ChildFailurePolicy

```go
// ChildFailurePolicy 定义父任务在子任务失败时的响应策略
type ChildFailurePolicy string

const (
    // PropagateImmediately：任何子任务失败立即令父任务失败并取消所有兄弟任务
    // 适用于：所有子任务是"AND"关系，缺一不可
    PropagateImmediately ChildFailurePolicy = "propagate_immediately"

    // WaitAll：等待所有子任务完成（不论成功失败），再综合判断父任务结果
    // 若有任何子任务失败，父任务最终 Failed；全部成功，父任务 Completed
    // 适用于：数据收集类任务，希望尽量多拿结果
    WaitAll ChildFailurePolicy = "wait_all"

    // Ignore：子任务失败不影响父任务
    // 适用于：子任务是"OR"关系，有一个成功就够
    Ignore ChildFailurePolicy = "ignore"
)
```

### 7.4 父任务取消时的级联取消

```go
// 父任务进入 StateCanceled 或 StateDraining 时，必须级联取消所有子任务
func (te *TaskExecution) cancelChildren(ctx context.Context, reason string) error {
    var errs []error
    for _, childID := range te.childIDs {
        child, err := te.registry.Get(childID)
        if err != nil {
            errs = append(errs, err)
            continue
        }
        // 只取消活跃的子任务
        if !child.Status.State.IsTerminal() {
            if err := child.Cancel(ctx, CancelReason{Source: "parent_canceled", ParentID: te.ID, Reason: reason}); err != nil {
                errs = append(errs, err)
            }
        }
    }
    return errors.Join(errs...)
}
```

### 7.5 状态传播的层级约束

```
规则 1：父任务必须比子任务"活得更长"
        → 父任务 Complete 前，必须等所有子任务达到终态

规则 2：父任务取消，立即级联取消所有未完成子任务（无论深度）

规则 3：子任务的 Budget 是父任务 Budget 的子集
        → 子任务 Budget 耗尽不超过父任务 Budget 剩余量
        → 父任务 Budget 耗尽时，所有子任务必须立即取消

规则 4：子任务的 Mode 不能高于父任务
        → 父是 background → 子必须是 background
        → 父是 interactive → 子可以是 interactive 或 background

规则 5：子任务的 Lifecycle 不受父任务约束（可以比父活得长，但取消会级联）
```

---

## 8. 从 loop/run.go 的 Run 到 TaskExecution 的迁移路径

### 8.1 迁移原则

- **兼容优先**：`loop.Run` 继续存在，作为"底层 Turn 执行引擎"的内部结构
- **适配层**：`TaskExecution` 组合 `loop.Run`，而不是替换它
- **不破坏现有测试**：`loop/runner_test.go`、`loop/loop_test.go` 不需要修改

### 8.2 目标分层架构

```
TaskExecution（状态机、policy、lifecycle 管理）
    └── loop.Runner（每个 Turn 的 LLM 调用、工具分发、Budget 检查）
            └── loop.Run（Turn 计数、Budget 字段、时间戳）
```

### 8.3 具体重构步骤

#### Step 1：定义 ExecutionState 和 Policy 类型（新文件）

**文件**：`sdk/execution/state.go`

```go
package execution

// ExecutionState — 见 §1
// ExecutionMode — 见 §3
// LifecyclePolicy — 见 §4
// RestartPolicy — 见 §5
// （所有类型定义，不依赖 loop 包）
```

#### Step 2：定义 TaskExecution 结构体

**文件**：`sdk/execution/task.go`

```go
package execution

import (
    "context"
    "time"

    "github.com/leef-l/brain/sdk/llm"
    "github.com/leef-l/brain/sdk/loop"
)

// TaskExecution 是 Brain v3 的唯一执行对象。
// 它包装了 loop.Run（底层执行引擎），并在其上添加：
// - 三个 policy 维度（Mode × Lifecycle × Restart）
// - 父子关系（ParentID）
// - Flow Edge（输入/输出引用）
// - 生命周期扩展状态（Draining / Restarting / WaitingEvent）
type TaskExecution struct {
    // 身份
    ID       string
    ParentID string    // 顶层任务为空
    BrainID  string    // 由哪个 Brain 执行

    // 上下文与消息
    Context         context.Context
    Messages        []llm.Message
    initialMessages []llm.Message // 重启时恢复用

    // Policy 三维
    Mode      ExecutionMode
    Lifecycle LifecyclePolicy
    Restart   RestartPolicy

    // 配置
    restartConfig    RestartConfig
    cyclePolicy      DaemonCyclePolicy   // daemon/watch 专用
    childFailPolicy  ChildFailurePolicy

    // 状态
    Status ExecutionStatus

    // Budget（扩展版，组合 loop.Budget）
    Budget TaskBudget

    // Flow Edges
    Inputs  []EdgeRef
    Outputs []EdgeRef

    // 内部引用（运行时注入）
    run      *loop.Run        // 底层 Run，由 Runner.Execute 管理
    registry ExecutionRegistry // 父子任务查找
    eventBus EventBus
    childIDs []string
}
```

#### Step 3：实现 TaskExecution 状态机方法

**文件**：`sdk/execution/task_fsm.go`

```go
package execution

// 在 loop.Run 的方法之上，增加 TaskExecution 专有转移

func (te *TaskExecution) StartDraining(now time.Time) error {
    if te.Lifecycle == OneShot {
        return ErrInvalidLifecycleOp
    }
    switch te.Status.State {
    case StateRunning, StateWaitingTool, StateWaitingEvent, StatePaused:
    default:
        return ErrInvalidStateTransition{From: te.Status.State, To: StateDraining}
    }
    te.Status.State = StateDraining
    te.Status.DrainingAt = &now
    return nil
}

func (te *TaskExecution) EnterWaitingEvent(now time.Time) error {
    if te.Lifecycle != Watch {
        return ErrInvalidLifecycleOp
    }
    if te.Status.State != StateRunning {
        return ErrInvalidStateTransition{From: te.Status.State, To: StateWaitingEvent}
    }
    te.Status.State = StateWaitingEvent
    return nil
}

func (te *TaskExecution) EnterRestarting(now time.Time, cause error) error {
    switch te.Status.State {
    case StateFailed, StateCrashed:
    default:
        return ErrInvalidStateTransition{From: te.Status.State, To: StateRestarting}
    }
    if te.checkRetryExhausted() {
        return ErrRestartExhausted
    }
    te.Status.State = StateRestarting
    te.Status.RestartingAt = &now
    te.Status.LastFailCause = toBrainError(cause)
    return nil
}

func (te *TaskExecution) PrepareRestart(now time.Time) error {
    if te.Status.State != StateRestarting {
        return ErrInvalidStateTransition{From: te.Status.State, To: StatePending}
    }
    te.prepareForRestart(now)
    return nil
}
```

#### Step 4：实现 TaskRunner（包装 loop.Runner）

**文件**：`sdk/execution/task_runner.go`

```go
package execution

// TaskRunner 驱动 TaskExecution 完整生命周期。
// 它包装了 loop.Runner（负责单次 LLM 调用序列），
// 并在其上实现 Lifecycle × RestartPolicy 的控制逻辑。
type TaskRunner struct {
    inner        *loop.Runner
    registry     ExecutionRegistry
    eventBus     EventBus
    watchTrigger WatchTrigger // 仅 Watch lifecycle 使用
    now          func() time.Time
}

// Run 驱动 TaskExecution 从 pending 到 terminal state。
// 这是主入口，对应不同 Lifecycle 有不同的循环结构。
func (tr *TaskRunner) Run(ctx context.Context, te *TaskExecution, opts loop.RunOptions) error {
    switch te.Lifecycle {
    case OneShot:
        return tr.runOneShot(ctx, te, opts)
    case Daemon:
        return tr.runDaemon(ctx, te, opts)
    case Watch:
        return tr.runWatch(ctx, te, opts)
    default:
        return ErrUnknownLifecycle
    }
}

// runOneShot：执行一次，完成即返回
func (tr *TaskRunner) runOneShot(ctx context.Context, te *TaskExecution, opts loop.RunOptions) error {
    for {
        // 初始化底层 Run
        run := loop.NewRun(te.ID, te.BrainID, te.Budget.Budget)
        te.run = run

        // 调用 loop.Runner.Execute
        result, err := tr.inner.Execute(ctx, run, te.Messages, opts)
        if err != nil {
            return err
        }

        // 更新 TaskExecution 状态
        te.Status.State = ExecutionState(run.State)

        // 检查是否需要重启
        if run.State == loop.StateFailed || run.State == loop.StateCrashed {
            if te.evaluateRestart(ExecutionState(run.State), nil) {
                if err := te.EnterRestarting(tr.now(), nil); err != nil {
                    break // 重启耗尽，终止
                }
                backoff := te.restartConfig.NextBackoff(te.Status.RetryCount)
                select {
                case <-time.After(backoff):
                    te.PrepareRestart(tr.now())
                    continue // 重启
                case <-ctx.Done():
                    te.Status.State = StateCanceled
                    return nil
                }
            }
            break
        }

        // 成功完成
        te.Messages = result.FinalMessages
        break
    }
    return nil
}

// runDaemon：持续运行，每轮完成后立即重启，直到收到 stop 信号
func (tr *TaskRunner) runDaemon(ctx context.Context, te *TaskExecution, opts loop.RunOptions) error {
    for {
        // 检查 ctx（stop 信号）
        if ctx.Err() != nil {
            if err := te.drainAndCancel(ctx); err != nil {
                te.Status.State = StateCrashed
            }
            return nil
        }

        run := loop.NewRun(te.ID, te.BrainID, te.Budget.Budget)
        te.run = run

        result, _ := tr.inner.Execute(ctx, run, te.Messages, opts)

        // 保留消息历史（daemon 持续对话）
        if result != nil {
            te.Messages = result.FinalMessages
        }

        // 周期结束，Budget 按配置决定是否重置
        if te.cyclePolicy.ResetBudgetPerCycle {
            te.Budget.resetCycleBudget()
        }
        te.Status.UsedCycles++

        // 检查 MaxCycles
        if te.Budget.MaxCycles > 0 && te.Status.UsedCycles >= te.Budget.MaxCycles {
            te.Status.State = StateCompleted
            return nil
        }

        // 若本轮失败，evaluateRestart
        if run.State == loop.StateFailed || run.State == loop.StateCrashed {
            if !te.evaluateRestart(ExecutionState(run.State), nil) {
                te.Status.State = ExecutionState(run.State)
                return nil
            }
            if err := te.EnterRestarting(tr.now(), nil); err != nil {
                te.Status.State = StateFailed
                return nil
            }
            backoff := te.restartConfig.NextBackoff(te.Status.RetryCount)
            select {
            case <-time.After(backoff):
                te.PrepareRestart(tr.now())
                continue
            case <-ctx.Done():
                te.Status.State = StateCanceled
                return nil
            }
        }

        // 本轮正常完成，立即开始下一轮（daemon 不停止）
    }
}

// runWatch：等待事件 → 处理事件 → 回到等待
func (tr *TaskRunner) runWatch(ctx context.Context, te *TaskExecution, opts loop.RunOptions) error {
    for {
        // 进入 WaitingEvent 状态
        _ = te.EnterWaitingEvent(tr.now())

        // 等待触发器
        var waitCtx context.Context
        var cancel context.CancelFunc
        if te.cyclePolicy.IdleTimeout > 0 {
            waitCtx, cancel = context.WithTimeout(ctx, te.cyclePolicy.IdleTimeout)
        } else {
            waitCtx, cancel = context.WithCancel(ctx)
        }

        event, messages, waitErr := tr.watchTrigger.Wait(waitCtx)
        cancel()

        if waitErr != nil {
            if errors.Is(waitErr, context.DeadlineExceeded) {
                // idle timeout → 正常完成
                te.Status.State = StateCompleted
                return nil
            }
            if errors.Is(waitErr, context.Canceled) {
                te.Status.State = StateCanceled
                return nil
            }
            // 触发器错误
            te.Status.State = StateFailed
            return nil
        }

        // 收到事件，转入 Running
        te.Status.State = StateRunning
        _ = event // 可将 event 注入 messages

        // 执行本次触发的处理
        run := loop.NewRun(te.ID, te.BrainID, te.Budget.Budget)
        te.run = run
        result, _ := tr.inner.Execute(ctx, run, messages, opts)

        if result != nil && te.cyclePolicy.ResetBudgetPerCycle {
            te.Budget.resetCycleBudget()
        }

        // 本次处理失败 → evaluateRestart
        if run.State == loop.StateFailed || run.State == loop.StateCrashed {
            if !te.evaluateRestart(ExecutionState(run.State), nil) {
                te.Status.State = ExecutionState(run.State)
                return nil
            }
            if err := te.EnterRestarting(tr.now(), nil); err != nil {
                te.Status.State = StateFailed
                return nil
            }
            backoff := te.restartConfig.NextBackoff(te.Status.RetryCount)
            select {
            case <-time.After(backoff):
                te.PrepareRestart(tr.now())
                te.Status.State = StateRunning // 重启后回到 Running，再次进入等待循环
            case <-ctx.Done():
                te.Status.State = StateCanceled
                return nil
            }
        }
        // 继续下一轮等待
    }
}
```

#### Step 5：迁移 kernel/orchestrator.go 中的 runManager

**当前实现**：`runManager` 是 map[string]*loop.Run + sync.Mutex，直接存 Run。

**迁移目标**：改为 `ExecutionRegistry`，存 *TaskExecution。

```go
// 迁移前（orchestrator.go 片段）
type runManager struct {
    mu   sync.Mutex
    runs map[string]*loop.Run
}

// 迁移后
type ExecutionRegistry interface {
    Register(te *execution.TaskExecution) error
    Get(id string) (*execution.TaskExecution, error)
    List() []*execution.TaskExecution
    Delete(id string) error
}

// MemExecutionRegistry：Phase A 的内存实现
type MemExecutionRegistry struct {
    mu         sync.RWMutex
    executions map[string]*execution.TaskExecution
}
```

#### Step 6：更新 API Handler

**迁移前**：`POST /v1/runs` → 创建 `loop.Run` + 启动 `loop.Runner`

**迁移后**：`POST /v1/executions` → 创建 `TaskExecution` + 启动 `TaskRunner`

```go
// handler 层的 policy 解析
func createExecution(w http.ResponseWriter, r *http.Request) {
    var req CreateExecutionRequest
    // ...

    te := &execution.TaskExecution{
        ID:        newULID(),
        BrainID:   req.BrainID,
        Mode:      execution.ExecutionMode(req.Mode),      // 默认 interactive
        Lifecycle: execution.LifecyclePolicy(req.Lifecycle), // 默认 oneshot
        Restart:   execution.RestartPolicy(req.Restart),   // 默认 never
        Messages:  req.Messages,
        Budget:    parseBudget(req.Budget),
    }

    registry.Register(te)
    
    // 异步启动（background mode）或同步驱动（interactive mode）
    if te.Mode == execution.ModeBackground {
        go taskRunner.Run(r.Context(), te, opts)
        writeJSON(w, 202, te.Status)
    } else {
        // interactive：流式响应
        taskRunner.RunStreaming(w, r.Context(), te, opts)
    }
}
```

### 8.4 迁移阶段计划

| 阶段 | 工作内容 | 目标文件 | 影响范围 |
|------|---------|---------|---------|
| **M1**（兼容层） | 定义 `execution` 包的所有类型（State/Policy/TaskExecution），不修改 loop 包 | `sdk/execution/*.go` | 零破坏 |
| **M2**（适配器） | 实现 `TaskRunner`，内部调用 `loop.Runner.Execute`；OneShot 路径优先 | `sdk/execution/task_runner.go` | 零破坏 |
| **M3**（Registry 迁移） | 将 `orchestrator.runManager` 从 `map[*loop.Run]` 改为 `ExecutionRegistry` | `sdk/kernel/orchestrator.go` | 需要 orchestrator 测试重跑 |
| **M4**（API 迁移） | 新增 `/v1/executions` 端点；`/v1/runs` 保留并内部路由到 TaskExecution(Interactive+OneShot) | `cmd/serve/*.go` | API 兼容 |
| **M5**（Daemon/Watch）| 实现 `runDaemon` / `runWatch`；实现 `WatchTrigger` 接口 | `sdk/execution/task_runner.go` | 需要集成测试 |
| **M6**（状态持久化） | TaskExecution 状态写入持久化存储（复用 persistence.PlanStore 或新建） | `sdk/execution/store.go` | 需要数据库迁移 |

---

## 9. 接口全景（设计契约汇总）

```go
// === 核心类型 ===

type TaskExecution struct { ... }   // sdk/execution/task.go
type ExecutionState string          // sdk/execution/state.go
type ExecutionMode string           // sdk/execution/state.go
type LifecyclePolicy string         // sdk/execution/state.go
type RestartPolicy string           // sdk/execution/state.go
type TaskBudget struct { ... }      // sdk/execution/budget.go
type RestartConfig struct { ... }   // sdk/execution/restart.go
type DaemonCyclePolicy struct { ... }

// === 接口 ===

type ExecutionRegistry interface {
    Register(*TaskExecution) error
    Get(id string) (*TaskExecution, error)
    List() []*TaskExecution
    Delete(id string) error
}

type WatchTrigger interface {
    Wait(ctx context.Context) (WatchEvent, []llm.Message, error)
}

type EventBus interface {
    Publish(event any)
    AsStreamConsumer(executionID string) loop.StreamConsumer
}

// === TaskRunner 主入口 ===

type TaskRunner struct {
    Inner        *loop.Runner
    Registry     ExecutionRegistry
    EventBus     EventBus
    WatchTrigger WatchTrigger
    Now          func() time.Time
}

func (tr *TaskRunner) Run(ctx context.Context, te *TaskExecution, opts loop.RunOptions) error
```

---

## 10. 设计约束与不变式（铁律）

以下约束在代码中必须以 panic 或 BrainError{CodeInvariantViolated} 强制执行：

1. **状态机只前向迁移**：终态不能迁移到任何其他状态（包括 Restarting）。`EnterRestarting` 必须先于落地 terminal state 调用。

2. **UsedCostUSD 只增不减**：任何重置逻辑中，必须保留 `TotalUsedCostUSD` 的累计。

3. **子任务不能比父任务活得更长**：父任务取消时，`cancelChildren` 必须在父任务迁移到 terminal state 之前完成（或 goroutine 异步保证）。

4. **Mode 切换不产生状态转移**：`DemoteToBackground` 只能修改 Mode 字段，不能修改 State 字段。

5. **Draining 有超时兜底**：`StateDraining` 最长持续时间由 `DrainTimeout`（默认 30s）控制，超时后强制迁移到 `StateCrashed`。

6. **WaitingEvent 只允许 Watch lifecycle**：`EnterWaitingEvent` 在非 Watch lifecycle 下必须返回错误，不允许降级为 Paused。

7. **Budget.CheckTurn 在 Restarting 期间不调用**：退避等待期间不消耗 Turn 预算，但 ElapsedTime 仍计时（MaxDuration 是总上限）。
