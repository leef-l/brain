# 35. Dispatch Policy — 冲突图、Batch 分组与死锁仲裁

> **版本**: v2.0(全量对齐代码 + Wave 4 全 + Wave 7 死锁路径)
> **更新日期**: 2026-05-02
> **实现位置**:
> - `sdk/kernel/dispatch.go` — BatchPlanner + ConflictGraph + Welsh-Powell 着色
> - `sdk/kernel/conflict_detector.go` — 资源声明级冲突检测(MACCS 4.2)
> - `sdk/kernel/smart_scheduler.go` — 冲突感知重排(MACCS 4.5)
> - `sdk/kernel/deadlock_detector.go` — Wait-For graph(MACCS 4.3)
> - `sdk/kernel/arbiter.go` — 死锁仲裁(MACCS 4.4)
> - `sdk/kernel/execution_scheduler.go` — 把以上组装到派发链(MACCS 3.6 + Wave 7)
> - `sdk/loop/batch_planner.go` — 接口定义 + tool 层入口

---

## 0. 范围澄清

Dispatch Policy 不是单一组件,是**两条层级互补的并发控制链**:

```
工具调用层(LLM 一轮 N 个 tool_use):
  loop/turn_executor → BatchPlanner(dispatch.go)
    → ConflictGraph + Welsh-Powell 着色
    → AcquireSet 拿锁 → 并行执行 → 结果回填

任务调度层(MACCS 多 brain 多 task):
  PlanOrchestrator → ExecutionScheduler.RunPlan
    → ConflictDetector.Detect 资源声明级冲突
    → SmartScheduler.Reschedule 冲突感知重排
    → AttachDeadlockControl: DeadlockDetector + Arbiter(Wave 7)
    → DelegateBatch 派发
```

二者**互不替代**:工具层管 LLM 单轮内的 N 个 tool_use,任务层管多 brain 多 task 的整体编排。

---

## 1. 工具层:BatchPlanner + 冲突图(`sdk/kernel/dispatch.go`)

### 1.1 输入输出

```go
type ToolCallNode struct {
    Index    int                       // 原始 tool_call 数组下标
    ToolName string                    // 如 "quant.place_order"
    Args     json.RawMessage           // LLM 原始 JSON
    Spec     *tool.ToolConcurrencySpec // 工具并发约束(可能 nil)
}

type ToolBatch struct {
    Calls  []ToolCallNode
    Leases []LeaseRequest
}

type BatchPlan struct {
    Batches       []ToolBatch
    ErrorStrategy ErrorStrategy
}
```

### 1.2 ErrorStrategy 三种(`dispatch.go:38`)

| 值 | 含义 |
|---|------|
| `ErrorContinueBatch` | 失败标 IsError=true,不影响其他 |
| `ErrorFailBatch` | 第一个失败即取消 batch 内剩余 |
| `ErrorFailAll` | 任何失败终止整个 dispatch |

### 1.3 ResourceKey 模板解析(`dispatch.go:62-`)

工具声明 `ResourceKeyTemplate = "{{file_path}}"` →
- 解析 `Args` 为 `map[string]any`
- 用正则 `{{[^}]+}}` 找占位符
- 缺失字段 → `*` 通配(保守)
- 模板为空 → 用 Capability 兜底

### 1.4 冲突图构建

* 节点 = `ToolCallNode`
* 边:两节点的 `LeaseRequest` 不兼容(参考 `lease.go::compatible`)
* 表示:`ConflictGraph.Edges [][]bool` 邻接矩阵(N≤32 时缓存友好)

### 1.5 Welsh-Powell 贪心着色

```
按节点度从大到小排序
颜色 = 0
for 节点 v:
  for 颜色 c = 0..k:
    if v 与 c 中所有已着色节点都无冲突:
      给 v 染 c, break
  否则: 新颜色 c+1, 给 v 染 c+1

同色节点 → 同 batch(可并行)
```

输出按 batch 顺序执行,每 batch 内 `AcquireSet` 拿全部 Lease,并行调用工具。

### 1.6 实现位置说明(原文档已修正)

> 原文档说"实现在 `sdk/loop/dispatch/`"。**实际实现在 `sdk/kernel/dispatch.go`**(类型层,跨 loop/kernel 边界)+ `sdk/loop/batch_planner.go`(loop 侧接口适配)。

---

## 2. 任务层:ConflictDetector(`sdk/kernel/conflict_detector.go`)— MACCS 4.2

### 2.1 接口

```go
type ConflictDetector interface {
    Detect(declarations []TaskResourceDecl) *ConflictReport
    DetectPair(a, b TaskResourceDecl) []Conflict
    CheckNewTask(existing []TaskResourceDecl, newTask TaskResourceDecl) []Conflict
}
```

### 2.2 TaskResourceDecl 输入(`conflict_detector.go:80-88`)

```go
type TaskResourceDecl struct {
    TaskID       string
    BrainKind    string
    ReadPaths    []string
    WritePaths   []string
    Ports        []int
    Dependencies []string  // 任务级依赖(用于循环依赖检测)
}
```

### 2.3 检测的 4 类冲突

| 类型 | Severity | 触发 |
|------|----------|------|
| `ConflictFileWrite` | **Blocker** | 两任务 WritePaths 路径重叠 |
| `ConflictFileReadWrite` | Critical | 一方 Read,另一方 Write,路径重叠 |
| `ConflictPortBind` | **Blocker** | 两任务 Ports 数字重叠 |
| `ConflictDependency`(循环) | **Blocker** | DFS 着色检测 Dependencies 中循环 |

### 2.4 路径重叠规则(`pathOverlaps`,line 249)

```
"/a/b" 等于 "/a/b" → 重叠
"/a"   覆盖 "/a/b" → 重叠(目录前缀)
"/a/b" 覆盖 "/a"   → 重叠(对称)
"/a/x" vs "/a/b"  → 不重叠
```

### 2.5 循环依赖 DFS(`hasCyclicDeps`,line 270)

DFS 着色法:0=未访问,1=访问中(灰),2=完成(黑)。访问中遇灰 → 环。

### 2.6 ConflictID 原子分配(`line 110`)

```go
seq atomic.Int64
nextID() = "cfl-{seq.Add(1)}"
```

> **0139b5e 修复**:原版 seq 是普通 int + mu Lock,ExecutionScheduler 并发派发多层时 ID 重复。改 atomic.Int64.Add 后线性安全。

### 2.7 ConflictReport 结构(`line 58`)

```go
type ConflictReport struct {
    ReportID      string
    Conflicts     []Conflict
    TotalCount    int
    BlockerCount  int
    CriticalCount int
    WarningCount  int
    HasBlockers   bool
    GeneratedAt   time.Time
}
func (r *ConflictReport) Summary() string  // 一行摘要
```

---

## 3. 任务层:SmartScheduler(`sdk/kernel/smart_scheduler.go`)— MACCS 4.5

### 3.1 定位

> **不是独立的第三套调度器**——是 `DefaultTaskScheduler.Plan()` 的**可选辅助/增强**。

主线调度路径仍是 `DefaultTaskScheduler`(`scheduler.go`)的拓扑排序 + L1 brain 选择 + 优先级批次。SmartScheduler 提供两类增强:

| 方法 | 用途 |
|------|-----|
| `Reschedule(layers, decls)` | 拓扑分层后做"贪心冲突分离"——同层冲突任务挤到下一层 |
| `OptimizePlan(plan, decls)` | 一站式:Plan.ComputeParallelLayers + Reschedule |
| `SuggestParallelism(decls)` | 根据冲突比例建议并行度 |
| `ValidateSchedule(layers, decls)` | 重排后再次验证(派发前安全网) |

### 3.2 Reschedule 算法(`smart_scheduler.go:70`)

```
对每一层(从前到后):
  splitConflictingTasks 贪心分离:
    依次把任务加入 safe 组,与已 safe 用 CheckNewTask 检测
    无冲突 → 加 safe;有冲突 → 加 deferred
  当前层 = safe;deferred 插入下一层(若到尾则新增层)
  当前层 > maxParallel → 多余的也挤下一层
最后清理空层
```

### 3.3 SuggestParallelism 启发式(`smart_scheduler.go:173`)

```
冲突比例 = conflictPairs / (n*(n-1)/2)
比例 = 0     → n(全并行)
比例 > 0.5   → 2(几乎全冲突,建议串行/低并行)
否则         → n * (1 - 比例)(裁掉冲突部分,下界 2,上界 maxParallel)
```

### 3.4 ScheduleConstraint 输出

每条 blocker / critical 冲突生成一条 `ScheduleConstraint{TaskA, TaskB, MustSerialize, Reason}`,作为 SmartScheduleResult.Constraints,供上层审计 / Dashboard 展示。

---

## 4. 死锁检测:DeadlockDetector(`sdk/kernel/deadlock_detector.go`)— MACCS 4.3

### 4.1 数据结构

```go
type DeadlockDetector struct {
    edges map[string][]WaitEdge   // waiterTaskID → 出边
    tasks map[string]bool          // 节点集
}

type WaitEdge struct {
    WaiterTaskID string
    HolderTaskID string
    ResourcePath string
    WaitingSince time.Time
}
```

### 4.2 关键方法

| 方法 | 行号 | 用途 |
|------|-----|------|
| `AddWaitEdge(waiter, holder, resource)` | 56 | 加边:waiter 等待 holder 释放 resource |
| `RemoveEdge(waiter, holder)` | 67 | 删指定边 |
| `RemoveTask(taskID)` | 85 | 删该 task 的全部入边出边 |
| `Clear()` | 106 | 完全清空 |
| `Detect()` | 113 | 跑一次 DFS 检环 → DeadlockReport |
| `WouldDeadlock(waiter, holder, resource)` | 186 | "若加这条边会形成环吗"(干跑探查) |
| `GetWaitChain(taskID)` | 203 | BFS 取该 task 的等待链 |
| `SuggestVictim(cycle)` | 223 | 选环中等待时间最长(最新)的任务作 victim |
| `Stats()` | 245 | TotalTasks / TotalEdges / MaxWaitDepth |

### 4.3 DFS 检环算法(`detectLocked`,line 119)

```
visited[v] = false 全部
recStack[v] = false 全部
parent / edgeRes 记录路径

DFS(v):
  visited[v] = true; recStack[v] = true
  for each (waiter→holder) edge from v:
    if 未访问 holder:  parent[holder]=v; DFS(holder)
    elif holder in recStack:  发现环 → extractCycle(holder, v, parent, edgeRes)
  recStack[v] = false

收集所有环(去重 by sig = "{排序的 TaskIDs}")
Severity = "critical" 当 |TaskIDs|<=2 否则 "warning"
```

### 4.4 单 blocker 不构成环(设计意图)

`Conflict.TaskIDs` 通常 = `[a, b]` → 翻译为单边 `b → a`,**不形成环**。
真正触发死锁仲裁需要**多个 blocker 跨资源相互引用**(例如 `a→b、b→c、c→a`)。

> 这是有意的——绝大多数真冲突由 SmartScheduler 重排即可消化,**仅在多任务多资源相互锁死时**由本路径介入仲裁。

---

## 5. 仲裁:DefaultArbiter(`sdk/kernel/arbiter.go`)— MACCS 4.4

### 5.1 接口与策略

```go
type Arbiter interface {
    Arbitrate(conflict, priorities) *ArbiterDecision
    ArbitrateAll(report, priorities) []*ArbiterDecision
    ResolveDeadlock(cycle, priorities) *ArbiterDecision  // Wave 7 关键
}

const (
    StrategySerialize  = "serialize"  // 串行排队
    StrategyPriority   = "priority"   // 优先级抢占
    StrategyMerge      = "merge"      // 合并
    StrategyPartition  = "partition"  // 分区
    StrategyAbort      = "abort"      // 中止低优先级
)
```

### 5.2 默认策略映射(`arbiter.go:83`)

| ConflictType | Strategy |
|--------------|----------|
| `ConflictFileWrite` | `Serialize` |
| `ConflictFileReadWrite` | `Priority` |
| `ConflictPortBind` | `Abort` |
| `ConflictDependency` | `Serialize` |
| `ConflictResource` | `Priority` |

### 5.3 选 victim 算法(`pickVictim`,line 305)

```
victim = taskIDs[0]
for i in 1..n:
  if higherPriority(victim, taskIDs[i]):
    victim = taskIDs[i]    # 翻转:把当前 victim 换成更低优先级的 i
return victim
```

`higherPriority(a, b)` 规则(`arbiter.go:218`):
1. **Critical 优先**:Critical 任务非 victim
2. **已开始优先**:`StartedAt!=nil` 不被中止(避免丢失进度)
3. **Priority 数值小者优先**(数值 1 = 最高)

### 5.4 ResolveDeadlock 输出(`arbiter.go:144`)

```
1. pickVictim(cycle.TaskIDs, priorities)
2. winners = cycle 中除 victim
3. actions:
   - {abort, victim, "abort to break deadlock {cycle.CycleID}"}
   - {retry, w} for each w in winners
4. 返回 ArbiterDecision{Strategy: Abort, LoserTasks=[victim], WinnerTasks=winners, Actions}
```

---

## 6. ExecutionScheduler 接入(Wave 4 全 + Wave 7 死锁)— `sdk/kernel/execution_scheduler.go`

### 6.1 注入点

```go
func (s *ExecutionScheduler) AttachConflictControl(
    detector ConflictDetector,
    smart *SmartScheduler,
    dryRun bool,
    resolver func(taskID string) TaskResourceDecl,
)

// Wave 7
func (s *ExecutionScheduler) AttachDeadlockControl(
    detector *DeadlockDetector,
    arbiter Arbiter,
    dryRun bool,
)
```

### 6.2 BuildExecutionPlan 路径(`execution_scheduler.go:214`)

```
plan.ComputeParallelLayers()           # 拓扑分层
构建 ScheduledTask map
若 SmartScheduler 注入:
  decls := resourceDecls(execPlan)
  result := smart.Reschedule(layers, decls)
  if result.ConflictsAvoided > 0:
    diaglog.Info(...)
    if !dryRun: ep.Layers = result.OptimizedLayers
返回 ExecutionPlan
```

### 6.3 RunPlan 派发流程(`execution_scheduler.go:475`)

```
for layer:
  batch := NextBatch(execPlan)        # 当前层 queued 任务,最多 MaxParallel

  # === ConflictDetector 二次校验(派发前安全网)===
  if detector != nil:
    report := detector.Detect(batchDecls)
    if report.HasBlockers:
      diaglog.Warn(blocker conflicts)

      # === Wave 7 死锁仲裁路径 ===
      if ddet != nil && arb != nil:
        victims := resolveDeadlocksFromConflicts(
          execPlan, batch, report, ddet, arb, ddDryRun)
        # victims 中的 task 已被 MarkFailed("deadlock-victim")

  # 过滤掉 victim,仅派发剩余
  liveBatch := batch \ victims
  if len(liveBatch) == 0: AdvanceLayer; continue

  for t in liveBatch: MarkRunning(t)
  result := orch.DelegateBatch(ctx, batchReq)
  按 result 处理 succeeded / retried / failed
  if !retried: AdvanceLayer
```

### 6.4 resolveDeadlocksFromConflicts 算法(`execution_scheduler.go:693`)

```
victims := {}
touched := {}
for c in report.Conflicts:
  if c.Severity != Blocker: continue
  ids := sort(c.TaskIDs, 字典序)
  for k in 0..len(ids)-1:
    holder, waiter := ids[k], ids[k+1]
    ddet.AddWaitEdge(waiter, holder, c.ResourcePath)
    touched.add(holder, waiter)

defer:
  for tid in touched: ddet.RemoveTask(tid)  # 每批结束清理 wait-for graph

deadReport := ddet.Detect()
if !deadReport.HasDeadlock: return {}

priorities := buildBatchPriorities(execPlan, batch)
retryCap := s.config.RetryLimit  # 加锁后取一次,避免循环内裸读

for cycle in deadReport.Cycles:
  decision := arb.ResolveDeadlock(cycle, priorities)
  diaglog.Warn(cycle resolved)
  if dryRun: continue
  for vtid in decision.LoserTasks:
    victims[vtid] = true
    st := execPlan.Tasks[vtid]
    st.RetryCount = retryCap + 1   # 强制不可重试
    s.MarkFailed(st, "deadlock-victim: cycle="+cycle.CycleID)

return victims
```

### 6.5 buildBatchPriorities(`execution_scheduler.go:794`)

```go
out := {}
for tid, st in execPlan.Tasks:
  startedAt := st.StartedAt
  prio := st.Task.EstimatedTurns
  kind := string(st.Task.Kind)
  out[tid] = TaskPriorityInfo{
    Priority:  prio,                # EstimatedTurns 短的优先级高(短任务先跑完释放锁)
    StartedAt: startedAt,
    Critical:  startedAt != nil,    # 已开始 → Critical
  }
# batch 中未在 Tasks 里的也补一条默认
```

> **设计**:`Priority = EstimatedTurns`——短任务优先级高,因为让它先跑完释放资源,解锁长任务。

---

## 7. 双开关安全网

| 开关 | 默认 | 配置位置 |
|------|------|----------|
| `maccs.conflict.enabled` | true | `MACCSConflictConfig.Enabled` |
| `maccs.conflict.dry_run` | true | `MACCSConflictConfig.DryRun` — 仅日志,不真重排 |
| `maccs.deadlock.enabled` | true | `MACCSDeadlockConfig.Enabled` |
| `maccs.deadlock.dry_run` | true | `MACCSDeadlockConfig.DryRun` — 仅日志,不真中止 victim |

> **生产部署建议**:首周观察 `dry_run=true` 看 diaglog 报告是否合理(误报率)。无误报后切 `dry_run=false` 启用强制重排 / victim 中止。

---

## 8. 全链路示例(单次 RunPlan)

```
[Build]
  TaskPlan.ComputeParallelLayers              # 拓扑 → [layer0:[t1,t2], layer1:[t3,t4]]
  SmartScheduler.Reschedule(layers, decls)     # t2 与 t1 写冲突 → 挤到 layer1: [layer0:[t1], layer1:[t2,t3,t4]]
  ExecutionPlan.Layers = optimized

[Run]
  Layer 0:
    batch = [t1] (NextBatch)
    ConflictDetector.Detect([t1.decl]) → 0 conflicts
    DelegateBatch([t1]) → success
    AdvanceLayer

  Layer 1:
    batch = [t2,t3,t4]
    ConflictDetector.Detect → blocker on /tmp/x: [t2,t3]
    DeadlockDetector.AddWaitEdge(t3→t2)        # 单边不成环
    DeadlockDetector.Detect() → no cycle
    victims = {}
    DelegateBatch([t2,t3,t4])
      → t2 OK,t3 retried,t4 OK
    !retried 时推进;有 retried 留本层重跑
```

---

## 9. 引用代码位置速查

```
sdk/kernel/dispatch.go           # 工具层 BatchPlanner + 冲突图(~300 行)
sdk/kernel/conflict_detector.go  # MACCS 4.2(330 行)
sdk/kernel/smart_scheduler.go    # MACCS 4.5(305 行)
sdk/kernel/deadlock_detector.go  # MACCS 4.3(278 行)
sdk/kernel/arbiter.go            # MACCS 4.4(316 行)
sdk/kernel/execution_scheduler.go # 派发主链 + Wave 7 接入(~830 行)
sdk/loop/batch_planner.go        # loop 侧适配
cmd/brain/cmd_serve_projects.go  # newProjectService 注入 AttachConflictControl + AttachDeadlockControl
cmd/brain/config/config.go       # MACCSConflictConfig + MACCSDeadlockConfig
```

## 10. 相关文档

- 上位:`32-v3-Brain架构.md` §7.8 Dispatch Policy
- 平级:`35-LeaseManager实现设计.md`(锁层语义,§10 解释为什么 LM 不接死锁)
- 平级:`35-BrainPool实现设计.md`(进程边界)
- 上层:`docs/MACCS-架构总纲-v2.md` §4 并发控制
- 上层:`docs/MACCS-实施进度追踪.md` Wave 4 + Wave 7 接入记录
