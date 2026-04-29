# 35. Brain Pool 实现设计

> **⚠️ 实现简化说明（2026-04-24）：** BrainPool 接口实际只有 4 个方法（GetBrain/Status/AutoStart/Shutdown），ReturnBrain/HealthCheck/Drain/WarmUp/Register 未实现。EntryState 状态机未实现，用更简单的 agent 状态管理替代。

> **状态**：v1 · 2026-04-16
> **对应路线**：[32-v3-Brain架构.md](./32-v3-Brain架构.md) §7.11 Phase A-1
> **依赖规格**：[20-协议规格.md](./20-协议规格.md) / [02-BrainKernel设计.md](./02-BrainKernel设计.md)
> **实现目标**：`sdk/kernel/pool.go`（新增）

---

## 0. 设计原则

Brain Pool 从 Orchestrator 中抽取出来，职责边界极度清晰：

> **⚠️ WorkflowEngine 使用说明（2026-04-26）：** `WorkflowEngine`（`sdk/kernel/workflow.go`）通过 `Orchestrator.Delegate()` 调用 `BrainPool.GetBrain()` 获取 sidecar RPC 连接。Workflow 的 DAG 节点执行是 BrainPool 的典型消费者场景——每个节点可能调用不同 kind 的 brain，BrainPool 负责进程的启动、复用和回收。

> **只管进程，不管锁。**

- Pool 负责：sidecar 进程的启动、复用、健康检查、回收、关闭
- Pool 不负责：并发控制、工具权限、任务调度（全部由 Capability Lease 负责）

这个边界是 §7.11 的核心约束，必须贯穿整个实现，不能被破坏。

---

## 1. BrainPool 完整接口

```go
// BrainPool 管理所有专精大脑的 sidecar 进程生命周期。
// 只管进程，不管锁——并发控制由 Capability Lease 负责。
//
// 位置：sdk/kernel/pool.go
type BrainPool interface {
    // GetBrain 返回一个可用的 sidecar RPC 连接。
    // 对 shared-service brain：立即返回已有连接。
    // 对 exclusive-session brain：返回唯一实例（Lease 在外层控制并发）。
    // 对 ephemeral-worker brain：从预热池取或冷启动新实例。
    // 返回的 BidirRPC 调用方不得持有超过 task 生命周期。
    GetBrain(ctx context.Context, kind agent.Kind) (protocol.BidirRPC, error)

    // ReturnBrain 将 ephemeral-worker 实例归还给 idle 池。
    // shared-service / exclusive-session 调用此方法是 no-op。
    // rpc 必须来自同一 Pool 的 GetBrain 调用，否则 panic。
    ReturnBrain(kind agent.Kind, rpc protocol.BidirRPC)

    // Status 返回所有已注册 kind 的当前状态快照。
    // 用于 Dashboard /v1/brains API 和健康检查端点。
    Status() map[agent.Kind]BrainPoolStatus

    // HealthCheck 主动对指定 kind 做一次 ping/pong 健康探测。
    // 返回 nil 表示健康，返回 error 包含详细原因。
    // 若探测失败会触发内部替换流程（不保证同步完成）。
    HealthCheck(ctx context.Context, kind agent.Kind) error

    // Drain 使指定 kind 停止接受新的 GetBrain 请求，
    // 等待在途请求全部完成，然后关闭进程。
    // 用于滚动升级、brain 下线等场景。
    Drain(ctx context.Context, kind agent.Kind) error

    // WarmUp 预热指定 kind 的 sidecar 实例。
    // 对 ephemeral-worker：提前启动 n 个实例放入 idle 池。
    // 对 shared-service / exclusive-session：确保实例已启动并 Ready。
    // n <= 0 表示使用 BrainPoolConfig 中的默认预热数量。
    WarmUp(ctx context.Context, kind agent.Kind, n int) error

    // Register 动态注册一个新的 brain kind 及其策略。
    // 已存在的 kind 会更新策略（不影响正在运行的实例）。
    Register(reg BrainPoolRegistration) error

    // Shutdown 优雅关闭所有 brain 实例。
    // 先 Drain 所有 kind，超时后强制 kill。
    Shutdown(ctx context.Context) error
}
```

---

## 2. 内部数据结构

### 2.1 Pool Entry 状态机

每个 brain kind 对应一个 `kindPool`，内部按策略管理一个或多个 entry。

```
EntryState 状态机：

  ┌──────────┐  启动成功  ┌──────────┐
  │ starting │──────────▶│   idle   │
  └──────────┘           └────┬─────┘
       │                      │ GetBrain
       │ 启动失败              ▼
       ▼              ┌──────────────┐
  ┌──────────┐        │   in-use     │
  │   dead   │        └──────┬───────┘
  └──────────┘               │ ReturnBrain
       ▲                     ▼
       │              ┌──────────────┐
       │ 进程崩溃      │   idle       │
       └──────────────┘
       
  任意状态 ──Drain──▶ draining ──完成──▶ closed
```

```go
// EntryState 是单个 sidecar 实例的状态
type EntryState int

const (
    EntryStarting  EntryState = iota // 正在启动，等待 initialize 握手
    EntryIdle                        // 空闲，可被 GetBrain 取走
    EntryInUse                       // 正在被某个 task 使用
    EntryDraining                    // 等待当前使用方结束，然后关闭
    EntryClosed                      // 已关闭，不可再用
    EntryDead                        // 进程意外退出
)

// poolEntry 是单个 sidecar 实例的 Pool 内部记录
type poolEntry struct {
    kind      agent.Kind
    agent     agent.Agent      // 实现 RPCAgent，持有 processAgent
    rpc       protocol.BidirRPC
    state     EntryState
    startedAt time.Time
    lastUsed  time.Time        // 用于 ephemeral 的空闲超时回收
    useCount  int64            // 复用次数，用于 metrics

    mu sync.Mutex
}

// kindPool 管理单个 kind 的所有 sidecar 实例
type kindPool struct {
    kind     agent.Kind
    strategy ProcessStrategy
    cfg      StrategyConfig

    mu      sync.Mutex
    entries []*poolEntry      // 所有实例（idle + in-use）
    waiters []chan *poolEntry  // exclusive-session 等待队列

    // ephemeral-worker 专用：空闲实例快速查找
    idleStack []*poolEntry

    draining bool             // Drain() 调用后设为 true，拒绝新 GetBrain

    // health check 相关
    consecutiveFailures int
    lastHealthCheck     time.Time
}
```

### 2.2 Pool 顶层结构

```go
// ProcessStrategy 是进程管理策略（不是并发语义）
type ProcessStrategy string

const (
    StrategySharedService   ProcessStrategy = "shared-service"
    StrategyExclusiveSession ProcessStrategy = "exclusive-session"
    StrategyEphemeralWorker  ProcessStrategy = "ephemeral-worker"
)

// BrainPoolRegistration 注册一个 brain 到 Pool
type BrainPoolRegistration struct {
    Kind     agent.Kind
    Binary   string          // sidecar 二进制路径（空则由 BinResolver 解析）
    Strategy ProcessStrategy
    Config   StrategyConfig
}

// StrategyConfig 三种策略的共用配置结构（按字段区分）
type StrategyConfig struct {
    // === shared-service ===
    HealthCheckInterval time.Duration // 默认 30s
    HealthCheckTimeout  time.Duration // 默认 5s
    MaxConsecFailures   int           // 连续失败多少次触发重启，默认 3

    // === exclusive-session ===
    MaxInstances    int           // 最大实例数（0 表示不限），默认 1
    WaitTimeout     time.Duration // 等待可用实例的超时，默认 30s

    // === ephemeral-worker ===
    WarmPoolSize    int           // 预热池大小，默认 2
    MaxInstances    int           // 最大并发实例数，默认 8
    IdleTimeout     time.Duration // 空闲超时后回收，默认 5 分钟
    StartTimeout    time.Duration // 单次冷启动超时，默认 30s
}

// BrainPoolStatus 是 Status() 返回的单个 kind 状态
type BrainPoolStatus struct {
    Kind            agent.Kind
    Strategy        ProcessStrategy
    TotalInstances  int
    IdleInstances   int
    InUseInstances  int
    Draining        bool
    LastHealthCheck time.Time
    HealthOK        bool
    ConsecFailures  int
}

// brainPool 是 BrainPool 接口的实现
type brainPool struct {
    runner      BrainRunner            // 底层进程管理（ProcessRunner）
    binResolver func(agent.Kind) (string, error)

    mu    sync.RWMutex
    kinds map[agent.Kind]*kindPool

    // 后台 goroutine 控制
    ctx    context.Context
    cancel context.CancelFunc
    wg     sync.WaitGroup
}
```

### 2.3 状态快照的完整性

`BrainPoolStatus` 设计为只读快照，调用 `Status()` 时加 RLock 快速生成，不需要等待进程状态刷新：

```go
func (p *brainPool) Status() map[agent.Kind]BrainPoolStatus {
    p.mu.RLock()
    defer p.mu.RUnlock()

    result := make(map[agent.Kind]BrainPoolStatus, len(p.kinds))
    for kind, kp := range p.kinds {
        kp.mu.Lock()
        status := BrainPoolStatus{
            Kind:           kind,
            Strategy:       kp.strategy,
            TotalInstances: len(kp.entries),
            Draining:       kp.draining,
            HealthOK:       kp.consecutiveFailures < kp.cfg.MaxConsecFailures,
            ConsecFailures: kp.consecutiveFailures,
            LastHealthCheck: kp.lastHealthCheck,
        }
        for _, e := range kp.entries {
            switch e.state {
            case EntryIdle:
                status.IdleInstances++
            case EntryInUse:
                status.InUseInstances++
            }
        }
        kp.mu.Unlock()
        result[kind] = status
    }
    return result
}
```

---

## 3. 三种策略的详细行为

### 3.1 shared-service（Data Brain、Quant Brain）

**进程模型**：单例长驻进程，多 task 并发共享同一 sidecar。

#### 启动时机

```go
// shared-service 在以下时机启动：
// 1. Register() 时若设置了 AutoStart = true
// 2. 第一次 GetBrain() 被调用（懒启动）
// 3. WarmUp() 被显式调用
// 4. health check 检测到死亡后自动重启（重启逻辑见下）

func (kp *kindPool) getSharedService(ctx context.Context) (protocol.BidirRPC, error) {
    kp.mu.Lock()
    defer kp.mu.Unlock()

    // 找到唯一实例
    for _, e := range kp.entries {
        if e.state == EntryIdle || e.state == EntryInUse {
            e.state = EntryInUse
            e.lastUsed = time.Now()
            e.useCount++
            return e.rpc, nil
        }
        if e.state == EntryStarting {
            // 等待启动完成（释放锁后轮询，或用 chan 通知）
            return kp.waitForStarting(ctx, e)
        }
    }

    // 无实例，启动新实例
    return kp.startNew(ctx)
}
```

**关键点**：`GetBrain` 对 shared-service 返回同一个 RPC 连接，调用方不需要（也不应该）独占该连接。多个并发 task 通过 BidirRPC 的内部请求 ID 复用连接——这是协议层面的多路复用，Pool 层不管。

#### Health Check 机制

```go
// 后台 goroutine，每隔 HealthCheckInterval 探测一次
func (kp *kindPool) runHealthCheck(ctx context.Context) {
    ticker := time.NewTicker(kp.cfg.HealthCheckInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            kp.doHealthCheck(ctx)
        }
    }
}

func (kp *kindPool) doHealthCheck(ctx context.Context) {
    kp.mu.Lock()
    entry := kp.findActiveEntry()
    kp.lastHealthCheck = time.Now()
    kp.mu.Unlock()

    if entry == nil {
        return
    }

    // 发送 ping，等待 pong（详见 §4）
    pingCtx, cancel := context.WithTimeout(ctx, kp.cfg.HealthCheckTimeout)
    defer cancel()

    err := kp.ping(pingCtx, entry)
    if err != nil {
        kp.mu.Lock()
        kp.consecutiveFailures++
        failures := kp.consecutiveFailures
        kp.mu.Unlock()

        if failures >= kp.cfg.MaxConsecFailures {
            // 触发自动替换
            kp.triggerReplace(entry)
        }
    } else {
        kp.mu.Lock()
        kp.consecutiveFailures = 0
        kp.mu.Unlock()
    }
}
```

#### 崩溃重启逻辑

```go
func (kp *kindPool) triggerReplace(dead *poolEntry) {
    go func() {
        // 1. 把死亡 entry 标记为 dead
        dead.mu.Lock()
        dead.state = EntryDead
        dead.mu.Unlock()

        // 2. 尝试优雅关闭（可能已死，忽略错误）
        shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        _ = dead.agent.Shutdown(shutCtx)

        // 3. 从 entries 移除 dead entry
        kp.mu.Lock()
        kp.removeEntry(dead)
        kp.mu.Unlock()

        // 4. 启动新实例（重连）
        startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer startCancel()
        _, err := kp.startNew(startCtx)
        if err != nil {
            // 记录错误，等下次 health check 再试
            fmt.Fprintf(os.Stderr, "brain pool: replace %s failed: %v\n", kp.kind, err)
        }
    }()
}
```

#### Graceful Shutdown Drain

```go
// shared-service drain 步骤：
// 1. 标记 draining = true（新 GetBrain 调用返回 ErrPoolDraining）
// 2. 等待所有 in-use 引用释放（通过 ReturnBrain 或超时）
// 3. 发送 shutdown 通知，等待进程退出
// 4. 超时则强制 kill

func (kp *kindPool) drain(ctx context.Context) error {
    kp.mu.Lock()
    kp.draining = true
    kp.mu.Unlock()

    // 等待所有 in-use entry 变为 idle 或 closed
    deadline := time.Now().Add(30 * time.Second)
    for time.Now().Before(deadline) {
        kp.mu.Lock()
        inUse := kp.countInUse()
        kp.mu.Unlock()
        if inUse == 0 {
            break
        }
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(100 * time.Millisecond):
        }
    }

    // 关闭所有实例
    kp.mu.Lock()
    entries := append([]*poolEntry(nil), kp.entries...)
    kp.mu.Unlock()

    for _, e := range entries {
        shutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
        _ = e.agent.Shutdown(shutCtx)
        cancel()
    }
    return nil
}
```

---

### 3.2 exclusive-session（Browser Brain）

**进程模型**：最多 MaxInstances 个实例（默认 1），每个 task 独占一个实例，有等待队列。

**重要说明**：exclusive-session 的"独占"指进程复用语义（一个 browser 会话不能被两个 task 同时驱动），实际的并发控制仍由 Capability Lease 的 `ExclusiveSession` 模式负责。Pool 层只管理进程数量，不做业务级锁。

```go
func (kp *kindPool) getExclusiveSession(ctx context.Context) (protocol.BidirRPC, error) {
    for {
        kp.mu.Lock()

        // 尝试取一个 idle 实例
        for _, e := range kp.entries {
            if e.state == EntryIdle {
                e.state = EntryInUse
                e.lastUsed = time.Now()
                e.useCount++
                kp.mu.Unlock()
                return e.rpc, nil
            }
        }

        // 没有 idle 实例，检查是否可以新建
        if kp.cfg.MaxInstances <= 0 || len(kp.entries) < kp.cfg.MaxInstances {
            // 可以新建
            kp.mu.Unlock()
            return kp.startNew(ctx)
        }

        // 实例数已满，进入等待队列
        if kp.draining {
            kp.mu.Unlock()
            return nil, ErrPoolDraining
        }

        waiter := make(chan *poolEntry, 1)
        kp.waiters = append(kp.waiters, waiter)
        kp.mu.Unlock()

        // 等待被唤醒
        select {
        case <-ctx.Done():
            kp.removeWaiter(waiter)
            return nil, ctx.Err()
        case entry, ok := <-waiter:
            if !ok || entry == nil {
                return nil, ErrPoolDraining
            }
            return entry.rpc, nil
        }
    }
}

// ReturnBrain 对 exclusive-session：唤醒等待队列里的下一个 waiter
func (kp *kindPool) returnExclusiveSession(entry *poolEntry) {
    kp.mu.Lock()
    defer kp.mu.Unlock()

    if kp.draining || len(kp.waiters) == 0 {
        // 没有等待者，entry 回到 idle
        entry.state = EntryIdle
        entry.lastUsed = time.Now()
        return
    }

    // 唤醒第一个等待者，直接把 entry 交给它
    waiter := kp.waiters[0]
    kp.waiters = kp.waiters[1:]
    entry.state = EntryInUse
    entry.lastUsed = time.Now()
    waiter <- entry
}
```

#### 最大实例数与等待队列

```go
// StrategyConfig 对 exclusive-session 的相关字段：
//   MaxInstances int           // 默认 1。0 表示无上限（不推荐用于 browser）
//   WaitTimeout  time.Duration // 等待队列超时，默认 30s
//
// 等待队列是 FIFO channel 列表，GetBrain 超时后自动从列表移除。
// 如果 draining = true，所有 waiter 立即收到关闭通知。
```

---

### 3.3 ephemeral-worker（Code Brain、Verifier Brain、Fault Brain）

**进程模型**：按需启动，用完归还，空闲超时后回收，预热池减少冷启动。

```go
func (kp *kindPool) getEphemeralWorker(ctx context.Context) (protocol.BidirRPC, error) {
    kp.mu.Lock()

    // 1. 优先从 idleStack 取
    if len(kp.idleStack) > 0 {
        entry := kp.idleStack[len(kp.idleStack)-1]
        kp.idleStack = kp.idleStack[:len(kp.idleStack)-1]
        entry.state = EntryInUse
        entry.lastUsed = time.Now()
        entry.useCount++
        kp.mu.Unlock()
        return entry.rpc, nil
    }

    // 2. 检查是否超过并发上限
    inUse := kp.countInUse()
    if kp.cfg.MaxInstances > 0 && inUse >= kp.cfg.MaxInstances {
        kp.mu.Unlock()
        // 超过上限，等待或返回错误
        return kp.waitForIdleEphemeral(ctx)
    }
    kp.mu.Unlock()

    // 3. 冷启动新实例
    return kp.startNew(ctx)
}

// ReturnBrain 对 ephemeral-worker：放回 idleStack，触发空闲超时计时
func (kp *kindPool) returnEphemeralWorker(entry *poolEntry) {
    kp.mu.Lock()
    defer kp.mu.Unlock()

    if kp.draining {
        // drain 中，直接关闭
        go entry.agent.Shutdown(context.Background())
        kp.removeEntry(entry)
        return
    }

    // 进入等待队列中的 waiter 优先
    if len(kp.waiters) > 0 {
        waiter := kp.waiters[0]
        kp.waiters = kp.waiters[1:]
        entry.state = EntryInUse
        entry.lastUsed = time.Now()
        waiter <- entry
        return
    }

    // 没有等待者，推入 idleStack
    entry.state = EntryIdle
    entry.lastUsed = time.Now()
    kp.idleStack = append(kp.idleStack, entry)
}
```

#### 空闲超时回收

```go
// 后台 goroutine：扫描 ephemeral-worker 的 idle 实例，超时则关闭
func (kp *kindPool) runIdleReaper(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second) // 每 30s 扫描一次
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            kp.reapIdleEntries(ctx)
        }
    }
}

func (kp *kindPool) reapIdleEntries(ctx context.Context) {
    kp.mu.Lock()
    now := time.Now()
    var toClose []*poolEntry
    var remaining []*poolEntry

    for _, e := range kp.idleStack {
        if now.Sub(e.lastUsed) > kp.cfg.IdleTimeout {
            toClose = append(toClose, e)
        } else {
            remaining = append(remaining, e)
        }
    }
    kp.idleStack = remaining

    // 也从 entries 里移除
    for _, e := range toClose {
        e.state = EntryClosed
        kp.removeEntry(e)
    }
    kp.mu.Unlock()

    // 在锁外关闭进程（避免持锁时阻塞）
    for _, e := range toClose {
        shutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        _ = e.agent.Shutdown(shutCtx)
        cancel()
    }
}
```

---

## 4. Health Check 机制

### 4.1 Ping/Pong 协议

使用协议层已有的 `$/ping` 方法（不需要新增协议方法）：

```go
// protocol 层已有 BidirRPC.Call，用它发 ping
func (kp *kindPool) ping(ctx context.Context, entry *poolEntry) error {
    // 方案 A：使用进程存活检测（ProcessExited）——零网络开销
    type processChecker interface {
        ProcessExited() bool
    }
    if pc, ok := entry.agent.(processChecker); ok {
        if pc.ProcessExited() {
            return fmt.Errorf("process exited")
        }
        return nil
    }

    // 方案 B：对无法做进程检测的 agent（如 StdioRunner/pipeAgent），
    // 发一次轻量 RPC ping（需要 sidecar 实现 $/ping 方法）
    var pong struct{}
    return entry.rpc.Call(ctx, "$/ping", nil, &pong)
}
```

**设计决策**：对 `processAgent` 优先用 `ProcessExited()` 做进程级存活检测（已有，`process_runner.go L519-532`），零 RPC 开销。只有 `pipeAgent`（测试用）才降级到 RPC ping。

### 4.2 Unhealthy 判定阈值

```go
// StrategyConfig 相关字段：
//   MaxConsecFailures int  // 默认 3：连续 3 次 health check 失败才判定 unhealthy
//   HealthCheckInterval     // 默认 30s
//   HealthCheckTimeout      // 默认 5s
//
// 计算：默认配置下最多 3 × 30s = 90s 才触发替换
// 对 Data/Quant 这类关键 brain，可调短到 10s 间隔 + 2 次失败
```

### 4.3 自动替换策略

```go
// 触发条件：consecutiveFailures >= MaxConsecFailures
// 替换流程（异步执行，不阻塞 health check goroutine）：
//
// 1. 标记旧实例为 EntryDead
// 2. 对 shared-service：立即尝试启动新实例
//    - 如果新实例启动中，GetBrain 会等待（EntryStarting 状态）
// 3. 对 exclusive-session：同 shared-service
// 4. 对 ephemeral-worker：
//    - dead 实例不再归还到 idleStack
//    - 下次 GetBrain 会冷启动或从预热池取
// 5. 记录 metrics：替换时间、替换原因、连续失败次数
```

---

## 5. Graceful Shutdown 完整流程

```go
func (p *brainPool) Shutdown(ctx context.Context) error {
    // 1. 取消所有后台 goroutine（health check、idle reaper、warm pool）
    p.cancel()

    // 2. 并发 Drain 所有 kind
    p.mu.RLock()
    kinds := make([]agent.Kind, 0, len(p.kinds))
    for kind := range p.kinds {
        kinds = append(kinds, kind)
    }
    p.mu.RUnlock()

    var wg sync.WaitGroup
    errs := make(chan error, len(kinds))

    for _, kind := range kinds {
        wg.Add(1)
        go func(k agent.Kind) {
            defer wg.Done()
            if err := p.Drain(ctx, k); err != nil {
                errs <- fmt.Errorf("drain %s: %w", k, err)
            }
        }(kind)
    }

    // 3. 等待所有 Drain 完成，或 ctx 超时
    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
    case <-ctx.Done():
        // 超时：强制 kill 所有剩余进程
        p.forceKillAll()
        return ctx.Err()
    }

    // 4. 等待后台 goroutine 退出
    p.wg.Wait()

    close(errs)
    var lastErr error
    for err := range errs {
        lastErr = err
    }
    return lastErr
}
```

### 5.1 Drain 策略详解

```
Drain 三阶段：

Phase 1 — 停止接受新请求（立即）
  draining = true
  waiter 队列：所有等待者收到 ErrPoolDraining，立即返回

Phase 2 — 等待在途请求完成（最长 DrainTimeout，默认 30s）
  轮询 countInUse() == 0
  每 100ms 检查一次
  收到 ctx.Done() 则跳过等待，进入 Phase 3

Phase 3 — 关闭进程（每个实例最长 ShutdownTimeout，默认 10s）
  遍历所有 entries
  发送 $/shutdown 通知
  等待进程退出（via processAgent.exited channel）
  超时则 SIGKILL
```

### 5.2 强制 Kill 兜底

```go
func (p *brainPool) forceKillAll() {
    p.mu.RLock()
    defer p.mu.RUnlock()

    for _, kp := range p.kinds {
        kp.mu.Lock()
        for _, e := range kp.entries {
            if e.agent != nil {
                // processAgent 实现了 forceKill
                type forceKiller interface {
                    ForceKill() error
                }
                if fk, ok := e.agent.(forceKiller); ok {
                    _ = fk.ForceKill()
                }
            }
        }
        kp.mu.Unlock()
    }
}
```

---

## 6. 冷启动优化——预热池设计

### 6.1 预热池结构

预热池只用于 ephemeral-worker。shared-service 和 exclusive-session 是单例，本身就常驻，不需要预热概念。

```go
// 预热池就是 idleStack 的一部分——两者共享同一数据结构
// WarmPoolSize 定义了"最少维持多少个 idle 实例"
// idleReaper 回收实例时，若 idle 数量 < WarmPoolSize，
// 立即补充新实例（保持最低水位）

func (kp *kindPool) reapIdleEntries(ctx context.Context) {
    // ... 回收超时实例 ...

    // 补充预热池到最低水位
    kp.mu.Lock()
    shortfall := kp.cfg.WarmPoolSize - len(kp.idleStack)
    kp.mu.Unlock()

    for i := 0; i < shortfall; i++ {
        go func() {
            startCtx, cancel := context.WithTimeout(ctx, kp.cfg.StartTimeout)
            defer cancel()
            entry, err := kp.startEntry(startCtx)
            if err != nil {
                return
            }
            kp.mu.Lock()
            entry.state = EntryIdle
            kp.idleStack = append(kp.idleStack, entry)
            kp.mu.Unlock()
        }()
    }
}
```

### 6.2 预热数量配置

```
默认值设计：

Code Brain（ephemeral-worker）：
  WarmPoolSize = 2    -- 常备 2 个 idle 实例，应对突发委托
  MaxInstances = 8    -- 最多同时跑 8 个
  IdleTimeout  = 5min -- 5 分钟无使用则回收（保留预热水位）

Verifier Brain（ephemeral-worker）：
  WarmPoolSize = 1    -- 备 1 个，验证任务通常在 code 之后触发
  MaxInstances = 4
  IdleTimeout  = 3min

Fault Brain（ephemeral-worker）：
  WarmPoolSize = 0    -- 故障注入是低频操作，不预热，按需冷启动
  MaxInstances = 2
  IdleTimeout  = 2min
```

### 6.3 预测性预热（Phase B 扩展，当前 Phase A 不实现）

```go
// 预留接口，Phase A 时此方法是 no-op
type WarmUpPredictor interface {
    // ShouldWarmUp 返回 true 表示预测即将有委托，应提前预热
    ShouldWarmUp(ctx context.Context, kind agent.Kind) bool
}

// Phase A 默认实现：仅靠时间窗口维持水位，不做预测
type staticWarmUpPredictor struct{}
func (s *staticWarmUpPredictor) ShouldWarmUp(_ context.Context, _ agent.Kind) bool {
    return false
}
```

### 6.4 WarmUp() 方法实现

```go
func (p *brainPool) WarmUp(ctx context.Context, kind agent.Kind, n int) error {
    p.mu.RLock()
    kp, ok := p.kinds[kind]
    p.mu.RUnlock()
    if !ok {
        return fmt.Errorf("brain pool: kind %s not registered", kind)
    }

    switch kp.strategy {
    case StrategySharedService, StrategyExclusiveSession:
        // 确保单例已启动并 Ready
        _, err := p.GetBrain(ctx, kind)
        if err != nil {
            return err
        }
        // 对 shared-service，GetBrain 后立即 ReturnBrain（no-op）
        p.ReturnBrain(kind, nil)
        return nil

    case StrategyEphemeralWorker:
        warmN := n
        if warmN <= 0 {
            warmN = kp.cfg.WarmPoolSize
        }
        return kp.warmEphemeral(ctx, warmN)
    }
    return nil
}

func (kp *kindPool) warmEphemeral(ctx context.Context, n int) error {
    var wg sync.WaitGroup
    errs := make(chan error, n)

    for i := 0; i < n; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            entry, err := kp.startEntry(ctx)
            if err != nil {
                errs <- err
                return
            }
            kp.mu.Lock()
            entry.state = EntryIdle
            kp.idleStack = append(kp.idleStack, entry)
            kp.mu.Unlock()
        }()
    }

    wg.Wait()
    close(errs)

    for err := range errs {
        if err != nil {
            return err // 返回第一个错误
        }
    }
    return nil
}
```

---

## 7. 从 Orchestrator 抽取的迁移步骤

### 7.1 现状分析

当前 `getOrStartSidecar()`（`orchestrator.go L511-590`）混合了以下职责：

1. 进程存活检测（`isAlive`）
2. 死进程清理
3. 并发启动防护（nil placeholder 技巧）
4. 启动新 sidecar（`runner.Start`）
5. 注册 reverse RPC handlers
6. 缓存到 `active` map

Brain Pool 抽取后，这些职责重新分配：

| 原代码 | 新位置 |
|--------|--------|
| 进程存活检测（`isAlive`） | `Pool.ping()` / health check |
| 死进程清理 | Pool 内部 `triggerReplace()` |
| 并发启动防护（nil placeholder） | `kindPool.mu` + `EntryStarting` 状态 |
| 启动新 sidecar（`runner.Start`） | Pool 内部 `startEntry()` |
| 注册 reverse RPC handlers | 保留在 Orchestrator（Pool 不知道业务逻辑） |
| 缓存到 `active` map | Pool 内部 `entries` slice |

### 7.2 具体重构路径

**Step 1：新增 `sdk/kernel/pool.go`**

```go
// 新文件，实现 BrainPool 接口
// 复制 ProcessRunner 的进程管理逻辑，去掉 active map
// 内部直接调用已有的 BrainRunner.Start() / Stop()
```

**Step 2：Orchestrator 注入 BrainPool**

```go
// 修改 Orchestrator 结构体（替换 active map）
type Orchestrator struct {
    pool          BrainPool          // 新增，替代 active map + runner 调用
    llmProxy      *LLMProxy
    binResolver   func(agent.Kind) (string, error)
    toolCalls     SpecialistToolCallAuthorizer
    available     map[agent.Kind]bool
    registrations map[agent.Kind]*BrainRegistration
    // 移除：runner BrainRunner（Pool 内部管理）
    // 移除：mu sync.Mutex（Pool 内部管理）
    // 移除：active map[agent.Kind]agent.Agent（Pool 替代）
}
```

**Step 3：替换 `getOrStartSidecar()` 调用**

```go
// 原来：
func (o *Orchestrator) delegateOnce(ctx context.Context, req *SubtaskRequest, start time.Time) {
    ag, err := o.getOrStartSidecar(ctx, req.TargetKind)
    // 从 ag 取 rpc...
}

// 替换为：
func (o *Orchestrator) delegateOnce(ctx context.Context, req *SubtaskRequest, start time.Time) {
    rpc, err := o.pool.GetBrain(ctx, req.TargetKind)
    if err != nil {
        return failResult(req, err)
    }
    // rpc 直接可用，不需要再从 agent 类型断言
    return callBrainExecute(ctx, rpc, req)
}
```

**Step 4：reverse RPC handler 注册时机调整**

原来在 `getOrStartSidecar()` 内部完成启动后立即注册。抽取后，Pool 不知道业务逻辑，需要通过回调解决：

```go
// Pool 支持 OnStart 钩子（仅用于 handler 注册）
type BrainPoolRegistration struct {
    Kind     agent.Kind
    Strategy ProcessStrategy
    Config   StrategyConfig
    // OnStart 在新实例启动成功后、放入 idle 池之前调用
    // 用于注册 reverse RPC handlers（LLMProxy、subtask.delegate、specialist.call_tool）
    OnStart func(kind agent.Kind, rpc protocol.BidirRPC) error
}

// Pool 在 startEntry() 末尾调用：
func (kp *kindPool) startEntry(ctx context.Context) (*poolEntry, error) {
    ag, err := kp.runner.Start(ctx, kp.kind, kp.desc)
    // ...
    if kp.reg.OnStart != nil {
        if err := kp.reg.OnStart(kp.kind, entry.rpc); err != nil {
            ag.Shutdown(context.Background())
            return nil, err
        }
    }
    return entry, nil
}
```

**Step 5：移除 `Orchestrator.active` + `Orchestrator.mu`**

```go
// 原 AutoStartBrains、StartBrain、StopBrain、ListBrains
// 全部委托给 Pool：
func (o *Orchestrator) StartBrain(ctx context.Context, kind agent.Kind) error {
    return o.pool.WarmUp(ctx, kind, 1)
}

func (o *Orchestrator) StopBrain(ctx context.Context, kind agent.Kind) error {
    return o.pool.Drain(ctx, kind)
}

func (o *Orchestrator) ListBrains() []BrainStatus {
    status := o.pool.Status()
    // 转换格式
}
```

**Step 6：`Orchestrator.Shutdown()` 委托给 Pool**

```go
func (o *Orchestrator) Shutdown(ctx context.Context) error {
    return o.pool.Shutdown(ctx)
}
```

**Step 7：保留 `Orchestrator.Delegate` 中的重试逻辑**

重试逻辑（`removeSidecar` + retry）不应下沉到 Pool。Pool 负责进程管理，重试策略属于 Orchestrator 的业务逻辑：

```go
func (o *Orchestrator) Delegate(ctx context.Context, req *SubtaskRequest) (*SubtaskResult, error) {
    result, err := o.delegateOnce(ctx, req, time.Now())
    if err == nil && result.Status != "failed" {
        return result, nil
    }

    // 通知 Pool 该 kind 可能有问题，触发 health check（不是立即替换）
    go o.pool.HealthCheck(context.Background(), req.TargetKind)

    // 直接重试（Pool 会在后台处理替换，这次重试可能拿到同一个或新实例）
    return o.delegateOnce(ctx, req, time.Now())
}
```

### 7.3 迁移顺序

```
Week 1：
  1. 实现 brainPool 结构体 + kindPool（不接入 Orchestrator）
  2. 单元测试：三种策略的 GetBrain/ReturnBrain
  3. 测试 Shutdown/Drain 流程

Week 2：
  4. 接入 Orchestrator（并行保留旧 active map，双写）
  5. 集成测试：serve 模式下多并发 run 共享 Pool
  6. 确认 sidecar 进程数从 N×run 降低到固定数量

Week 3：
  7. 移除旧 active map 和相关代码
  8. 接入 Dashboard /v1/brains 端点（复用 Pool.Status()）
  9. 性能基准对比
```

---

## 8. 与 ProcessRunner 的关系

### 8.1 结论：BrainPool 包装 ProcessRunner，不替代它

```
关系层次：

  BrainPool（进程生命周期 + 复用策略 + health check）
      │
      │ 调用 BrainRunner 接口
      ▼
  ProcessRunner（fork/exec + stdio transport + initialize 握手）
      │
      │ 产生
      ▼
  processAgent（持有 *exec.Cmd + BidirRPC）
```

**ProcessRunner 不需要修改**。它已经足够好：
- `Start()` 负责启动进程 + 完成 initialize 握手，返回 `agent.Agent`
- `Stop()` 负责优雅 shutdown
- `processAgent.ProcessExited()` 提供进程存活检测

**BrainPool 做的是 ProcessRunner 不做的事**：
- 多实例管理（entries slice vs ProcessRunner 的单 kind/单实例 map）
- 策略区分（shared / exclusive / ephemeral）
- Health check 后台 goroutine
- idle 超时回收
- 等待队列（waiter channel）
- 预热池维持
- Drain / Shutdown 语义

### 8.2 ProcessRunner 现有问题

ProcessRunner 内部有一个 `processes map[agent.Kind]*processAgent`，**这个 map 和 BrainPool 的 entries 会重复**。

解决方案：BrainPool 内部不再使用 ProcessRunner 的 `processes` map，改为直接调用 `runner.Start()` 并自己管理返回的 `agent.Agent`：

```go
// Pool 内部 startEntry 的核心逻辑：
func (kp *kindPool) startEntry(ctx context.Context) (*poolEntry, error) {
    // 直接调用 runner.Start，拿到 agent
    ag, err := kp.runner.Start(ctx, kp.kind, kp.desc)
    if err != nil {
        return nil, err
    }

    // 取出 RPC session
    rpcAgent, ok := ag.(agent.RPCAgent)
    if !ok {
        ag.Shutdown(context.Background())
        return nil, fmt.Errorf("agent does not implement RPCAgent")
    }
    rpc, ok := rpcAgent.RPC().(protocol.BidirRPC)
    if !ok {
        ag.Shutdown(context.Background())
        return nil, fmt.Errorf("agent RPC is not BidirRPC")
    }

    return &poolEntry{
        kind:      kp.kind,
        agent:     ag,
        rpc:       rpc,
        state:     EntryIdle,
        startedAt: time.Now(),
        lastUsed:  time.Now(),
    }, nil
}

// Pool 内部关闭 entry：
func (kp *kindPool) closeEntry(ctx context.Context, entry *poolEntry) {
    entry.state = EntryClosed
    // 直接调用 agent.Shutdown，不经过 runner.Stop
    // （runner.Stop 会从 runner.processes 删除，但 Pool 已经自己管理了）
    _ = entry.agent.Shutdown(ctx)
}
```

> **注意**：这意味着 ProcessRunner 的 `processes` map 和 Pool 的 `entries` 会出现**不一致**——Pool 创建的实例不会出现在 `runner.processes` 里。这没有问题，因为 `runner.processes` 只是 ProcessRunner 自己的内部追踪，Pool 接管后可以不使用它。
>
> 如果担心泄漏，可以在迁移完成后给 ProcessRunner 新增一个 `StartUntracked()` 方法，或者在 Pool 测试完毕后直接废弃 `runner.processes`。

### 8.3 接口关系图

```
sdk/kernel/
├── runner.go          BrainRunner interface（不变）
├── process_runner.go  ProcessRunner（不变，只改：可选废弃 processes map）
├── pool.go            BrainPool interface + brainPool 实现（新增）
├── orchestrator.go    Orchestrator（修改：注入 Pool，移除 active map）
└── lease.go           LeaseManager（另一个设计文档，与 Pool 正交）

依赖方向：
orchestrator.go → pool.go（通过 BrainPool 接口）
pool.go         → runner.go（通过 BrainRunner 接口）
pool.go 不依赖  orchestrator.go（Pool 不知道业务逻辑）
```

---

## 9. 错误类型定义

```go
var (
    // ErrPoolDraining 在 Drain() 进行中时由 GetBrain 返回
    ErrPoolDraining = errors.New("brain pool: pool is draining")

    // ErrPoolNotRegistered 请求了未注册的 kind
    ErrPoolNotRegistered = errors.New("brain pool: kind not registered")

    // ErrAcquireTimeout 等待可用实例超时（exclusive-session 等待队列）
    ErrAcquireTimeout = errors.New("brain pool: timeout waiting for available instance")

    // ErrMaxInstancesReached 超过 ephemeral-worker 最大并发实例数且无等待位
    ErrMaxInstancesReached = errors.New("brain pool: max instances reached")
)
```

---

## 10. 构造函数

```go
// NewBrainPool 创建一个新的 BrainPool。
// runner 是底层进程管理器（通常是 ProcessRunner）。
// registrations 初始化时注册的 brain 列表；也可以后续调用 Register() 追加。
func NewBrainPool(
    runner BrainRunner,
    registrations []BrainPoolRegistration,
) (BrainPool, error) {
    ctx, cancel := context.WithCancel(context.Background())
    pool := &brainPool{
        runner: runner,
        kinds:  make(map[agent.Kind]*kindPool),
        ctx:    ctx,
        cancel: cancel,
    }

    for _, reg := range registrations {
        if err := pool.Register(reg); err != nil {
            cancel()
            return nil, fmt.Errorf("brain pool: register %s: %w", reg.Kind, err)
        }
    }

    return pool, nil
}

func (p *brainPool) Register(reg BrainPoolRegistration) error {
    p.mu.Lock()
    defer p.mu.Unlock()

    cfg := reg.Config
    // 填充默认值
    if cfg.HealthCheckInterval <= 0 {
        cfg.HealthCheckInterval = 30 * time.Second
    }
    if cfg.HealthCheckTimeout <= 0 {
        cfg.HealthCheckTimeout = 5 * time.Second
    }
    if cfg.MaxConsecFailures <= 0 {
        cfg.MaxConsecFailures = 3
    }
    if cfg.IdleTimeout <= 0 {
        cfg.IdleTimeout = 5 * time.Minute
    }
    if cfg.StartTimeout <= 0 {
        cfg.StartTimeout = 30 * time.Second
    }
    // exclusive-session 默认 MaxInstances = 1
    if reg.Strategy == StrategyExclusiveSession && cfg.MaxInstances <= 0 {
        cfg.MaxInstances = 1
    }
    if cfg.WaitTimeout <= 0 {
        cfg.WaitTimeout = 30 * time.Second
    }

    kp := &kindPool{
        kind:     reg.Kind,
        strategy: reg.Strategy,
        cfg:      cfg,
        reg:      &reg,
        runner:   p.runner,
    }

    p.kinds[reg.Kind] = kp

    // 启动后台 goroutine
    p.wg.Add(1)
    go func() {
        defer p.wg.Done()
        kp.runBackground(p.ctx)
    }()

    return nil
}

// kindPool.runBackground 统一后台任务入口
func (kp *kindPool) runBackground(ctx context.Context) {
    var wg sync.WaitGroup

    switch kp.strategy {
    case StrategySharedService, StrategyExclusiveSession:
        wg.Add(1)
        go func() {
            defer wg.Done()
            kp.runHealthCheck(ctx)
        }()

    case StrategyEphemeralWorker:
        wg.Add(1)
        go func() {
            defer wg.Done()
            kp.runIdleReaper(ctx)
        }()
    }

    wg.Wait()
}
```

---

## 11. 并发安全保证

```
加锁规则（严格遵守，避免死锁）：

1. brainPool.mu：只用于保护 kinds map 的读写
   - 任何 kindPool 的方法都不在持 brainPool.mu 时调用
   - 只做快速 map 操作，不做 IO

2. kindPool.mu：保护 entries、idleStack、waiters、draining、consecutiveFailures
   - 不在持锁时调用 runner.Start() / agent.Shutdown()
   - 不在持锁时等待 channel（会死锁）
   - 取 entry 后立即 Unlock，再做 IO 操作

3. poolEntry.mu：保护 entry.state
   - 只用于状态转换的原子性检查

4. 锁顺序：brainPool.mu > kindPool.mu > poolEntry.mu
   - 任何时候都按此顺序获取，避免死锁
```

---

## 12. 可观测性集成点

```go
// Pool 在以下时机发射 metrics/events（通过 EventBus，Phase A 先用 stderr）：

// 时机 1：新实例启动
pool_event{kind="code", event="start", duration_ms=1240}

// 时机 2：实例因 health check 失败被替换
pool_event{kind="quant", event="replace", reason="health_check_failed", consec_failures=3}

// 时机 3：空闲实例被回收
pool_event{kind="code", event="reap", idle_duration_s=312}

// 时机 4：等待队列超时
pool_event{kind="browser", event="wait_timeout", waited_ms=30000}

// 时机 5：Drain 完成
pool_event{kind="data", event="drained", duration_ms=850}
```

---

## 13. 完整文件骨架

```go
// sdk/kernel/pool.go

package kernel

import (
    "context"
    "fmt"
    "os"
    "sort"
    "sync"
    "time"

    "github.com/leef-l/brain/sdk/agent"
    "github.com/leef-l/brain/sdk/protocol"
)

// ─── 接口 ──────────────────────────────────────────────────────────────────

type BrainPool interface { /* 见 §1 */ }

// ─── 类型定义 ───────────────────────────────────────────────────────────────

type ProcessStrategy string
type EntryState int
type BrainPoolRegistration struct { /* 见 §2 */ }
type StrategyConfig struct { /* 见 §2 */ }
type BrainPoolStatus struct { /* 见 §2 */ }

// ─── 错误 ──────────────────────────────────────────────────────────────────

var (
    ErrPoolDraining      = /* ... */
    ErrPoolNotRegistered = /* ... */
    ErrAcquireTimeout    = /* ... */
    ErrMaxInstancesReached = /* ... */
)

// ─── 内部结构 ───────────────────────────────────────────────────────────────

type poolEntry struct { /* 见 §2 */ }
type kindPool struct { /* 见 §2 */ }
type brainPool struct { /* 见 §2 */ }

// ─── 构造函数 ───────────────────────────────────────────────────────────────

func NewBrainPool(runner BrainRunner, registrations []BrainPoolRegistration) (BrainPool, error)

// ─── 接口实现 ───────────────────────────────────────────────────────────────

func (p *brainPool) GetBrain(ctx context.Context, kind agent.Kind) (protocol.BidirRPC, error)
func (p *brainPool) ReturnBrain(kind agent.Kind, rpc protocol.BidirRPC)
func (p *brainPool) Status() map[agent.Kind]BrainPoolStatus
func (p *brainPool) HealthCheck(ctx context.Context, kind agent.Kind) error
func (p *brainPool) Drain(ctx context.Context, kind agent.Kind) error
func (p *brainPool) WarmUp(ctx context.Context, kind agent.Kind, n int) error
func (p *brainPool) Register(reg BrainPoolRegistration) error
func (p *brainPool) Shutdown(ctx context.Context) error

// ─── 策略内部方法（kindPool 方法）──────────────────────────────────────────

func (kp *kindPool) getSharedService(ctx context.Context) (protocol.BidirRPC, error)
func (kp *kindPool) getExclusiveSession(ctx context.Context) (protocol.BidirRPC, error)
func (kp *kindPool) getEphemeralWorker(ctx context.Context) (protocol.BidirRPC, error)
func (kp *kindPool) returnSharedService(entry *poolEntry)
func (kp *kindPool) returnExclusiveSession(entry *poolEntry)
func (kp *kindPool) returnEphemeralWorker(entry *poolEntry)
func (kp *kindPool) startEntry(ctx context.Context) (*poolEntry, error)
func (kp *kindPool) closeEntry(ctx context.Context, entry *poolEntry)
func (kp *kindPool) removeEntry(entry *poolEntry)
func (kp *kindPool) findActiveEntry() *poolEntry
func (kp *kindPool) countInUse() int
func (kp *kindPool) runBackground(ctx context.Context)
func (kp *kindPool) runHealthCheck(ctx context.Context)
func (kp *kindPool) doHealthCheck(ctx context.Context)
func (kp *kindPool) ping(ctx context.Context, entry *poolEntry) error
func (kp *kindPool) triggerReplace(dead *poolEntry)
func (kp *kindPool) runIdleReaper(ctx context.Context)
func (kp *kindPool) reapIdleEntries(ctx context.Context)
func (kp *kindPool) warmEphemeral(ctx context.Context, n int) error
func (kp *kindPool) waitForIdleEphemeral(ctx context.Context) (protocol.BidirRPC, error)
func (kp *kindPool) removeWaiter(waiter chan *poolEntry)
func (kp *kindPool) drain(ctx context.Context) error

// ─── 编译时断言 ─────────────────────────────────────────────────────────────

var _ BrainPool = (*brainPool)(nil)
```

---

## 14. 设计决策汇总

| 决策 | 选择 | 原因 |
|------|------|------|
| BrainPool 与 ProcessRunner 的关系 | 包装，不替代 | ProcessRunner 的 fork/exec + initialize 握手已经很好，不重复造 |
| shared-service 的 RPC 多路复用 | 协议层（BidirRPC 内部请求 ID）处理 | Pool 层不做业务级多路复用 |
| Health check 实现 | 优先 ProcessExited() 进程检测，降级 RPC ping | 零 RPC 开销，跨平台 |
| 等待队列实现 | chan *poolEntry（FIFO） | 简单可靠，避免 polling |
| 预热池数据结构 | idleStack（LIFO） | 最近启动的实例最热，优先复用 |
| 并发启动防护 | EntryStarting 状态 + mu | 替代旧 nil placeholder 技巧，语义更清晰 |
| OnStart 回调 | Pool 注册时声明 | 保持 Pool 不知道业务逻辑（LLM proxy 等） |
| ProcessRunner.processes map | 迁移完成后可废弃 | Pool 接管后产生重复追踪 |
| Drain 超时策略 | 等待 30s + 强制 kill | 与 processAgent.Shutdown 的 3s grace period 分层 |
