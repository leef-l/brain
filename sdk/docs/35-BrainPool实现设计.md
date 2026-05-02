# 35. BrainPool 实现设计

> **版本**: v2.0(全量对齐代码)
> **更新日期**: 2026-05-02
> **实现位置**: `sdk/kernel/pool.go`(主体) + `sdk/kernel/pool_entry.go`(实例) + `sdk/kernel/pool_health.go`(健康/扩缩容)
> **MACCS 关联**: 多实例并发(AcquireBrain) + 6.1 HealthManager + 6.6 MultiProjectManager 槽位
> **设计原则**: **只管进程,不管锁**——锁由 LeaseManager 负责,冲突由 ConflictDetector / Arbiter 负责

---

## 0. 设计边界

```
BrainPool         → 进程的启动 / 复用 / 健康 / 扩缩容 / 关闭
LeaseManager      → 资源锁的获取 / 释放(canonical-order 防死锁)
ConflictDetector  → 任务级资源冲突检测(blocker 严重度)
Arbiter           → 死锁仲裁(选 victim 中止)
ExecutionScheduler → 把以上能力组装到 RunPlan 派发链路
```

四者**互不知道对方的内部实现**,通过明确的接口/事件协作。

---

## 1. 真实接口(`pool.go:22-42`)

```go
type BrainPool interface {
    // GetBrain 返回一个正在运行的 sidecar agent,如果不存在则启动。
    // 内部使用默认负载均衡策略(LatencyAwareStrategy)选择最优实例。
    GetBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error)

    // AcquireBrain 是多实例并发感知的获取入口(MACCS 设计):
    //   - 优先选 load=0 的空闲实例
    //   - 全忙 + 资源预算允许 → 自动扩容启动新实例
    //   - 全忙 + 预算到顶 → 共享负载最低的现有实例
    AcquireBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error)

    Status() map[agent.Kind]BrainStatus
    AutoStart(ctx context.Context)
    Shutdown(ctx context.Context) error
}
```

> **与原文档的差异**:原文档写有 `ReturnBrain / HealthCheck / Drain / WarmUp / Register` 5 个方法在接口里,**实际接口只有 5 个方法**(GetBrain, AcquireBrain, Status, AutoStart, Shutdown),其余在 `*ProcessBrainPool` 具体类型上以**方法**形式暴露,不是 interface 约束。这是**有意的窄接口设计**:消费者(Orchestrator/Workflow)只需 GetBrain/AcquireBrain,管理面板(Dashboard/CLI)按需取具体类型调高级方法。

---

## 2. ProcessBrainPool 完整方法表(`pool.go`)

### 2.1 接口实现(必须)

| 方法 | 行号 | 用途 |
|------|-----|------|
| `GetBrain(ctx, kind)` | 256 | 单实例语义:有就返回最优,无就启动 |
| `AcquireBrain(ctx, kind)` | 177 | **多实例语义**:空闲优先 / 扩容 / 共享 |
| `Status()` | 533 | 所有 kind 的 BrainStatus 快照 |
| `AutoStart(ctx)` | 580 | 启动所有 `AutoStart=true` 的 brain |
| `Shutdown(ctx)` | 599 | 优雅关停全部 |

### 2.2 高级运维(具体类型方法)

| 方法 | 行号 | 用途 |
|------|-----|------|
| `BrainDetail(kind)` | 546 | 单 brain 详细状态 |
| `SelectBrain(ctx, kind, strategy)` | 355 | 用指定策略选实例 |
| `ScaleBrain(ctx, kind, n)` | 378 | 扩容到 n 个实例 |
| `InstanceCount(kind)` | 412 | 存活实例计数 |
| `RestartBrain(ctx, kind)` | 670 | 移除 + 重启 |
| `RemoveBrain(kind)` | 651 | 移除全部实例 |
| `WarmPool(ctx, kinds...)` | 730 | 后台预热 |
| `HealthCheck()` | 747 | 全 kind 存活快照 |
| `Drain(ctx, keep...)` | 759 | 关闭非保留 kind |
| `Register(reg)` | 787 | 动态注册新 kind |
| `Available(kind)` / `AvailableKinds()` | 625 / 630 | 探测结果 |
| `Registrations()` | 639 | 配置驱动的注册列表 |
| `ReleaseAgent(ag)` | 818 | 调用结束减载 |
| `SetLoadBalanceStrategy(s)` | 130 | 切换默认策略 |
| `SetNotifyCh(ch)` | 724 | 订阅 BrainEvent 生命周期 |

---

## 3. 多实例数据结构(`pool_entry.go`)

```go
type poolEntry struct {
    agent     agent.Agent
    id        string         // "{kind}-{seq}"
    load      atomic.Int64   // 当前并发请求数
    lastUsed  atomic.Value   // time.Time
    latency   *EWMAScore     // 延迟 EWMA(秒)
    latencyMu sync.RWMutex
    createdAt time.Time
}
```

| 方法 | 行号 | 语义 |
|------|-----|------|
| `Acquire()` | 44 | load+1, 更新 lastUsed |
| `Release()` | 50 | load-1(下界 0) |
| `CurrentLoad()` | 58 | atomic 读 |
| `RecordLatency(d)` | 63 | 写 EWMA |
| `LatencyEWMA()` | 70 | 读 EWMA(秒) |
| `LastUsed()` | 77 | 最后使用时间 |

### 3.1 ProcessBrainPool 字段(`pool.go:64`)

```go
type ProcessBrainPool struct {
    runner       BrainRunner   // 启动 sidecar 的 Runner
    binResolver  func(agent.Kind) (string, error)
    available    map[agent.Kind]bool          // 磁盘探测结果
    registrations map[agent.Kind]*BrainRegistration

    mu      sync.Mutex
    active  map[agent.Kind][]*poolEntry        // 同 kind 多实例
    starting map[agent.Kind]bool                // 防并发重启
    entrySeq map[agent.Kind]int                 // 实例编号
    pendingSpawn map[agent.Kind]int             // 锁外启动期间的 reserve 计数(防并发突破上限)

    defaultStrategy LoadBalanceStrategy         // GetBrain 默认策略
    notifyCh        chan<- BrainEvent           // 可选订阅
    warmKinds       []agent.Kind
}
```

---

## 4. 负载均衡策略(`pool_entry.go:86-180`)

| 策略 | 类型 | 选择依据 |
|------|------|---------|
| `RoundRobinStrategy` | 轮询 | 按 kind 维护 `next` 计数,简单循环 |
| `LeastLoadedStrategy` | 最小负载 | `CurrentLoad()` 最小 |
| `LatencyAwareStrategy` | 延迟感知 | `score = latencyEWMA + load*0.5`,score 最小 |

**默认策略**:`DefaultLoadBalanceStrategy() = NewLatencyAwareStrategy()`(`pool_entry.go:178`)。

> **理由**:延迟感知 + 负载惩罚兼顾响应速度和均衡性;0.5s/请求 的惩罚权重让"延迟低但已经堆 N 个请求"的实例不会一直被选。

---

## 5. AcquireBrain 多实例并发流程(`pool.go:177-251`)

这是 MACCS 多实例并发的**核心算法**,与 `GetBrain` 的关键区别在于**主动扩容**。

```
AcquireBrain(kind):
  1. 持锁,过滤存活实例 alive
  2. 优先选 alive 中 load=0 的空闲 → Acquire 后返回
  3. 全忙:
     a. 持锁内统计 currentSameKind = len(alive) + pendingSpawn[kind]
        currentTotal = Σ(各 kind 的 alive + pendingSpawn)
     b. 持锁内调 CanSpawnSidecar(currentTotal, currentSameKind, hardMax)
        → 决定能否扩容(50% 资源水位 + hardMax 配置)
     c. 若可:
        - 持锁 reserve 一个 instanceID + pendingSpawn[kind]++
        - 释放锁,锁外启动 sidecar(耗时操作不能持锁)
        - 启动后持锁 pendingSpawn[kind]--, 包装 poolEntry, append, Acquire 后返回
     d. 若不可(或扩容失败):
        - 用 LeastLoadedStrategy 选最低负载的现有实例,共享
  4. 一个实例都没有(冷启动) → 走 GetBrain 标准路径
```

### 5.1 Critical Bug 修复(2026-04-30 c4fe85b)

> **原 bug**:`entrySeq[kind]++` 与 `available[kind]` 等检查不在同一锁内,N 个 goroutine 并发 AcquireBrain 同 kind 时:
> - 全部通过 CanSpawnSidecar 检查(只看到旧的 active 数)
> - 同时 ++ entrySeq → 拿到相同 instanceID
> - 全部启动 → 实例数突破 hardMax + ID 冲突

> **修复**:
> 1. 引入 `pendingSpawn map[Kind]int` 在锁内 reserve 启动槽位,CanSpawnSidecar 用 `currentSameKind = active + pendingSpawn` 判断
> 2. 持锁内一次性完成 `instanceID := entrySeq[kind]; entrySeq[kind]++`,把 ID 传给锁外启动函数,避免 newEntryLocked 重复 ++
> 3. 失败路径在 `pendingSpawn[kind]--` 后才返回,保证计数一致性

---

## 6. GetBrain 单实例并发安全(`pool.go:256-329`)

`GetBrain` 用**双重检查 + starting 标记**避免重复启动:

```
GetBrain(kind):
  1. 持锁过滤 alive。有则用 defaultStrategy 选并 Acquire 返回。
  2. 锁内查 starting[kind]:
     - 已是 true: 释放锁,调 waitForSidecar 等(最多 5s = 50 次 100ms 轮询)
       - 等到了: Select + Acquire 返回
       - 等启动者失败: 自己接手启动
     - 是 false: 设 starting[kind]=true, 释放锁
  3. 锁外 startWithRegistration → 启动 sidecar
  4. 持锁 newPoolEntry, append, delete starting, 释放,通知 BrainEvent{start}
```

> 5s 超时时长在 `waitForSidecar()` 行号 684:`for attempts := 0; attempts < 50; attempts++ { time.Sleep(100ms) }`。

---

## 7. 健康监控与弹性伸缩(`pool_health.go`)

```go
type HealthMonitor struct {
    pool        *ProcessBrainPool
    policy      PoolHealthPolicy
    lastScaleUp / lastScaleDown map[Kind]time.Time   // 冷却时间记录
    stopCh chan / wg sync.WaitGroup
}
```

### 7.1 PoolHealthPolicy 默认值(`pool_health.go:54`)

| 字段 | 默认 | 含义 |
|------|------|------|
| `CheckInterval` | 30s | 健康检查间隔 |
| `MaxLoadPerInstance` | 5 | 单实例最大并发 |
| `MinInstancesPerKind` | 1 | 每 kind 最少 1 实例 |
| `MaxInstancesPerKind` | 5 | 每 kind 最多 5 实例 |
| `IdleTimeout` | 5min | 实例空闲超时考虑缩容 |
| `ScaleUpCooldown` | 60s | 扩容冷却 |
| `ScaleDownCooldown` | 120s | 缩容冷却 |

### 7.2 后台循环 `runCheck` 流程(`pool_health.go:120`)

```
每 CheckInterval:
  for kind in active:
    1. 对每个 entry 跑 checkEntry → 进程是否存活(processChecker.ProcessExited)
    2. 移除 unhealthy entry(removeEntry: 关 sidecar 进程 + 从 active 切片删)
    3. 计算 healthy 实例的 avgLoad
    4. 扩容判断:
       - avgLoad >= MaxLoadPerInstance 且 healthy < MaxInstancesPerKind
       - 距上次扩容 > ScaleUpCooldown
       → 调 ScaleBrain(kind, n+1),记 lastScaleUp[kind]
    5. 缩容判断:
       - healthy > MinInstancesPerKind
       - 存在 load=0 且空闲 > IdleTimeout 的实例
       - 距上次缩容 > ScaleDownCooldown
       → 移除其中一个空闲实例,记 lastScaleDown[kind]
```

> **MACCS 6.1 接入**:`HealthManager`(`sdk/kernel/health_check.go`)注册 `brainPool` checker,`GET /v1/health` 返回。

---

## 8. BrainEvent 生命周期通知(`pool.go:55-61`)

```go
type BrainEvent struct {
    Kind   agent.Kind
    Action string // "start" | "stop" | "restart"
    Agent  agent.Agent
    Error  error
    Time   time.Time
}
```

通过 `SetNotifyCh(ch)` 订阅。**非阻塞发送**(`notify` 用 `select default`),订阅者落后丢弃,不阻塞 pool 主流程。

> **典型订阅者**:Dashboard `/v1/dashboard/ws` WebSocket 推送、AuditLog 审计。

---

## 9. 与 LeaseManager 的边界

**Pool 不知道 Lease 的存在**。Lease 在更高层(turn_executor / ExecutionScheduler)调用,失败 → 任务排队/重排,**不影响 Pool 的实例数**。

> **设计权衡**:有人会建议"Pool 拿不到 Lease 就少启一个实例"——拒绝。原因:Lease 失败可能是临时(几秒)冲突,Pool 启停实例是高成本(秒级)操作,**冷启动一次 sidecar > 100ms,等待 Lease < 10ms**,把锁等待和进程启停耦合是浪费。

---

## 10. 与 MACCS Wave 4 / 7 的关联

| MACCS 任务 | BrainPool 角色 |
|-----------|---------------|
| 4.2 ConflictDetector | 不参与:Pool 不感知冲突 |
| 4.3 DeadlockDetector(Wave 7 接入) | 不参与:wait-for graph 在 ExecutionScheduler 层 |
| 4.4 Arbiter ResolveDeadlock(Wave 7) | 不参与:victim 选择在 Arbiter |
| 4.5 SmartScheduler | 不参与:重排在 BatchPlanner / ExecutionScheduler |
| 6.1 HealthManager | **被注册**:`brainPool` checker 调 `HealthCheck()` |
| 6.6 MultiProjectManager | **共享资源**:多项目 quota 与 Pool 实例数互不干扰 |

---

## 11. 配置入口

```yaml
# ~/.brain/config.json
brains:
  - kind: "code"
    binary: "/usr/local/bin/brain-code-sidecar"
    args: ["--mode", "daemon"]
    env: ["BRAIN_LOG_LEVEL=info"]
    auto_start: true
    max_instances: 5         # MaxInstances → ProcessBrainPool.registrations 中读取
    model: "claude-sonnet-4-6"
    min_approval_level: "warning"
```

* `max_instances` → `BrainRegistration.MaxInstances` → `AcquireBrain` 中作为 hardMax 参数
* `auto_start` → `AutoStart()` 启动入口
* `binary` 优先级高于 `binResolver` 的默认路径(`pool.go:142`)

---

## 12. 已知局限 / 待优化

1. **健康检查仅 process-alive**:`isAlive` 只检查 sidecar 进程是否退出,不做 RPC ping/pong。计划:`HealthCheck` 增强为发 `heartbeat` RPC。
2. **跨节点不感知**:`ProcessBrainPool` 是单节点本地池,跨节点 brain 走 `RemoteBrainPool`(见 `discovery.go` + 37-远程专精大脑调用说明)。
3. **PoolHealthPolicy 不可热加载**:目前需重启 brain 才能改 CheckInterval 等。计划:加 `UpdatePolicy(p)` 方法。

---

## 13. 引用代码位置速查

```
sdk/kernel/pool.go               # ProcessBrainPool 主体(862 行)
sdk/kernel/pool_entry.go         # poolEntry + 3 种 LoadBalanceStrategy(180 行)
sdk/kernel/pool_health.go        # HealthMonitor + PoolHealthPolicy(254 行)
sdk/kernel/discovery.go          # 跨节点 brain 发现(配合 RemoteBrainPool)
sdk/kernel/orchestrator.go:84    # Orchestrator 持有 brainPool
cmd/brain/cmd_serve.go:343       # health 字段引用 HealthManager
```

## 14. 相关文档

- 上位:`32-v3-Brain架构.md` §7.11 Brain Pool
- 平级:`35-LeaseManager实现设计.md`(锁管理)
- 平级:`35-Dispatch-Policy-冲突图与Batch分组算法.md`(冲突重排)
- 上层:`docs/MACCS-架构总纲-v2.md`
- 远程扩展:`37-远程专精大脑调用说明.md`
