# 35. LeaseManager 实现设计

> **版本**: v2.0(全量对齐代码 + Wave 7 边界澄清)
> **更新日期**: 2026-05-02
> **实现位置**: `sdk/kernel/lease.go`(423 行)
> **MACCS 关联**: 4.1 资源访问追踪(并入 LeaseManager) + Wave 7 边界澄清(为何 LeaseManager 不动)

---

## 0. 核心结论

> **LeaseManager 只管"非死锁的并发互斥"**,**不管死锁仲裁**。
> **死锁路径在 ExecutionScheduler 层,通过 ConflictDetector → DeadlockDetector → Arbiter 接入**(见 §10)。

这是 Wave 7 的关键设计决策——绕开"LeaseManager 改造为可竞态模型"的复杂改造,从更高层接入死锁仲裁,LeaseManager 保持简洁稳定。

---

## 1. 真实接口(`lease.go:67-76`)

```go
type LeaseManager interface {
    // AcquireSet 原子地获取一组租约。全部成功才返回;任一失败则
    // 回滚已获取的租约并重试(最多 3 次,指数退避+jitter)。
    // ctx 取消时返回 ErrAcquireTimeout。
    AcquireSet(ctx context.Context, reqs []LeaseRequest) ([]Lease, error)

    // ReleaseAll 释放一组租约。
    ReleaseAll(leases []Lease)
}
```

> **与原文档差异**:原文设计了 `Release / Query / Renew / ForceRevoke / Subscribe / Close` 等 8 个方法。**实际接口只有 2 个**。其余作为 `*MemLeaseManager` 的具体类型方法存在(见 §3.4),不进 interface。**这是有意的窄接口**:消费者(turn_executor / ExecutionScheduler)只需要 AcquireSet + ReleaseAll,管理面板按需取具体类型。

---

## 2. 数据结构

### 2.1 LeaseRequest(`lease.go:51-57`)

```go
type LeaseRequest struct {
    Capability  string     // 能力名,如 "file-write"
    ResourceKey string     // 资源标识,如 "/tmp/foo.txt"
    AccessMode  AccessMode // 访问模式
    Scope       LeaseScope // 生命周期范围
    HolderID    string     // 持有者 ID(brain ID 或 turn ID,普通字符串)
}
```

### 2.2 AccessMode 枚举(`lease.go:25-34`)

| 值 | 字符串 | 兼容性 |
|---|--------|-------|
| `AccessSharedRead` | `shared-read` | 多读者并发 |
| `AccessSharedWriteAppend` | `shared-write-append` | 多追加写并发,兼容 SharedRead |
| `AccessExclusiveWrite` | `exclusive-write` | 不兼容任何模式 |
| `AccessExclusiveSession` | `exclusive-session` | 不兼容任何模式 |

### 2.3 LeaseScope(`lease.go:38-46`)

| 值 | 含义 |
|---|------|
| `ScopeTurn` | 一个 turn 结束自动过期 |
| `ScopeTask` | task 完成过期 |
| `ScopeDaemon` | brain 退出前持续 |

### 2.4 Lease(`lease.go:60-65`)

```go
type Lease interface {
    ID() string      // 租约唯一标识
    Release()        // 释放租约,唤醒等待者
}
```

---

## 3. MemLeaseManager 内存实现(`lease.go:128`)

```go
type MemLeaseManager struct {
    mu      sync.Mutex
    cond    *sync.Cond
    counter atomic.Int64
    slots   map[string][]*memLease   // key = "Capability\x00ResourceKey"
}
```

### 3.1 兼容性矩阵(`lease.go:94`)

```
                 SR    SWA   EW    ES
SharedRead       ✓     ✓     ✗     ✗
SharedWriteAppend✓     ✓     ✗     ✗
ExclusiveWrite   ✗     ✗     ✗     ✗
ExclusiveSession ✗     ✗     ✗     ✗
```

实现是 `compatible(a, b)`:任一为 `Exclusive*` 即不兼容,否则兼容。

### 3.2 AcquireSet 算法(`lease.go:165`)

```
1. 按 canonical order 排序请求(Capability + ResourceKey 字典序) ←── 防死锁的核心
2. 重试循环(最多 3 次):
   a. 尝试 tryAcquireAll:逐个检查兼容性,任一冲突就回滚已获取的部分
   b. 全成功 → 返回 []Lease
   c. 失败 → 指数退避 + jitter:base = 2^attempt × 10ms + rand(0..base/2)
      并发等待 cond.Broadcast 或 ctx.Done
3. 超 maxRetries → ErrAcquireTimeout
```

> **canonical-order 是 LeaseManager 防死锁的全部依赖**:多个任务请求重叠资源集合,**无论顺序**都按字典序加锁,**不可能**形成"A 持有 X 等待 Y / B 持有 Y 等待 X"循环。

### 3.3 等待机制(`lease.go:205-227`)

```
当冲突时:
  1. 启动 goroutine 拿锁、设 timer(wait), 调 cond.Wait()
  2. select { 等到 broadcast → 重试 ; 等到 ctx.Done → ErrAcquireTimeout }
  3. ctx.Done 时主动 cond.Broadcast 防 goroutine 泄漏
```

### 3.4 高级方法(`*MemLeaseManager` 上的具体类型方法)

| 方法 | 行号 | 用途 |
|------|-----|------|
| `Snapshot()` | 320 | 所有活跃租约快照(Dashboard 用) |
| `ActiveCount()` | 339 | 活跃租约总数 |
| `UniqueResourceCount()` | 350 | 被占用的独立资源数 |
| `Query(cap, resKey)` | 357 | 按 cap+resKey 查租约 |
| `Renew(leases)` | 375 | 续期(内存版仅做存在性校验) |
| `ForceRevoke(cap, resKey)` | 401 | 强制撤销指定资源所有租约 |
| `Close()` | 418 | 关闭 + 清理 + Broadcast |

---

## 4. 失败路径与超时

| 错误 | 触发条件 |
|------|---------|
| `ErrAcquireTimeout` | ctx 被取消/超时,或 maxRetries=3 用完 |

**重试间隔**:`10ms * 2^attempt + rand(0..base/2)` → 第 1 次 ~10-15ms,第 2 次 ~20-30ms,第 3 次 ~40-60ms。**总最坏等待 < 100ms**。

> **设计取舍**:不实现"持锁等待 + 死锁检测"模型(见 §10)。如果 100ms 内拿不到锁,直接报错让上层(scheduler)决定重排,不在锁层面卡死。

---

## 5. memLease 释放语义(`lease.go:121`)

```go
func (l *memLease) Release() {
    l.once.Do(func() { l.mgr.removeLease(l) })
}
```

`sync.Once` 保证 **Release() 多次调用安全**——重复释放是 no-op,不 panic。

`removeLease` 内部:
1. 持锁从 `slots[key]` 切片删除该 lease
2. 释放锁
3. `cond.Broadcast()` 唤醒所有等待者重试

---

## 6. 接入点

### 6.1 Orchestrator 持有(`orchestrator.go:84` 字段 / `orchestrator.go:301` WithLeaseManager option)

```go
func WithLeaseManager(lm LeaseManager) OrchestratorOption {
    return func(o *Orchestrator) { o.leaseManager = lm }
}
```

### 6.2 turn_executor / ExecutionScheduler 调用 AcquireSet

实际调用路径(待 §10 接入死锁前):
- `turn_executor` 派发 batch 前,从 ToolConcurrencySpec 推导 LeaseRequest 列表
- `AcquireSet` 全成功 → 派发 batch
- `ReleaseAll` 在 batch 完成或失败的 defer 中

---

## 7. 与原 v1 Orchestrator.active 的差异

| v1 active | v3 LeaseManager |
|----------|-----------------|
| brain 级粗粒度锁 | Capability × ResourceKey × AccessMode 细粒度 |
| 无 SharedRead 概念 | 兼容矩阵区分 4 种模式 |
| 无 scope | turn / task / daemon 三种 scope |
| 无死锁防护 | canonical-order 排序 |
| per-run 实例 | 全局单例,跨 run 共享 |

迁移已完成(2026-04 Phase A):`active map` 移除,LeaseManager 替代。

---

## 8. 健康监控接入(MACCS 6.1)

`HealthManager` 注册 `leaseManager` checker,`GET /v1/health` 返回:

```json
{
  "checkers": {
    "leaseManager": {
      "healthy": true,
      "active_leases": 3,
      "unique_resources": 2
    }
  }
}
```

实现见 `cmd/brain/cmd_serve_health_checkers.go`。

---

## 9. 当前限制

1. **内存版**:`MemLeaseManager` 重启丢失。生产部署需自行做持久化包装(`persistence/` 中暂未提供 LeaseStore)。
2. **HolderID 是字符串**:无强类型约束,误用可能导致租约归属错乱(由调用方保证)。
3. **`maxRetries=3` 写死**:`lease.go:179` 不可配。**100ms 上限是有意的**——见 §10 解释。

---

## 10. Wave 7 边界:为什么 LeaseManager 不接入死锁检测

### 10.1 原计划(已废弃)

文档原本规划"P0 AcquireSet + P1 canonical ordering + **P2 wait-for graph**"三道防线。MACCS Wave 7 启动时本来要做 P2:LeaseManager 改造为可竞态模型(持锁 + waitFor + DeadlockDetector + Arbiter 介入)。

### 10.2 实际决策(0139b5e 后 Wave 7 调整)

**否决了 LeaseManager 改造**,理由:

1. **当前 AcquireSet 是原子语义**——拿不到全部就立刻回滚,**不存在"持有 A 等待 B"状态**。canonical-order 排序已经数学上保证不会死锁,DeadlockDetector 在 LeaseManager 层无数据可吃(空图)。
2. **改造成本高**:LeaseManager 改成"持锁 + waitFor"会引入新的复杂状态机、新的并发 bug 风险、新的测试矩阵。
3. **真实死锁场景在更高层**:Brain v3 实际可能死锁的是**任务级**资源依赖(任务 T1 写文件 a 还在跑、T2 等 a;T2 又被依赖循环卡住等 T1 产出),这不在锁层面能看到——**它在 ConflictDetector 报告的 blocker 级别**。

### 10.3 真正的接入路径(Wave 7 实现)

```
ExecutionScheduler.RunPlan(派发前):
  1. ConflictDetector.Detect(batchDecls) → ConflictReport
  2. 报告中 blocker 冲突 → 翻译为 wait-for 边写入 DeadlockDetector
  3. DeadlockDetector.Detect() 检环
  4. 命中环 → DefaultArbiter.ResolveDeadlock(cycle, priorities) → 选 victim
  5. victim 强制 RetryCount=RetryLimit+1 + MarkFailed("deadlock-victim") 不重试以打破环
```

详细见 `35-Dispatch-Policy-冲突图与Batch分组算法.md` §6.

### 10.4 LeaseManager 与 Wave 7 的最终边界

| 责任 | 模块 |
|------|------|
| 单批内资源互斥(canonical-order) | LeaseManager |
| 任务级资源冲突检测 | ConflictDetector |
| 跨任务循环依赖死锁检测 | DeadlockDetector |
| 死锁仲裁(选 victim) | Arbiter |
| 把以上接到 RunPlan 派发链 | ExecutionScheduler.AttachConflictControl + AttachDeadlockControl |

LeaseManager **保持不动**,Wave 7 工作量 ~150 行新代码 + 文档对齐,而非原本预估的"~2 天 LeaseManager 重构"。

---

## 11. 引用代码位置速查

```
sdk/kernel/lease.go              # 全部实现(423 行)
sdk/kernel/orchestrator.go:84    # Orchestrator 持有 leaseManager 字段
sdk/kernel/orchestrator.go:301   # WithLeaseManager option
cmd/brain/cmd_serve_health_checkers.go  # 6.1 HealthManager 注册 leaseManager checker
```

## 12. 相关文档

- 上位:`32-v3-Brain架构.md` §7.7.2 / §7.9
- 平级:`35-BrainPool实现设计.md`(进程边界)
- 平级:`35-Dispatch-Policy-冲突图与Batch分组算法.md`(冲突 + 死锁主线接入)
- 状态机:`35-TaskExecution生命周期状态机.md`
- 上层:`docs/MACCS-架构总纲-v2.md`
