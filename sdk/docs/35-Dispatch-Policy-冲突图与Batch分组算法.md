# 35. §7.8 Dispatch Policy — 冲突图构建与 Batch 分组算法

> **⚠️ 包路径说明（2026-04-24）：** 实际实现在 `sdk/kernel/dispatch.go`（ConflictGraph/BatchPlanner）+ `sdk/loop/batch_planner.go`（接口定义），非文档设计的 `sdk/loop/dispatch/` 子包。

> **状态**：v1 · 2026-04-16
> **归属**：32-v3-Brain架构.md §7.8 的下位规格
> **作用域**：`sdk/loop/turn_executor.go` + 新增 `sdk/loop/dispatch/`
> **依赖**：`ToolConcurrencySpec`（§7.7.4）、`LeaseManager`（§7.7.2）、`AcquireSet`（§7.9）

---

## 0. 设计约束回顾

Dispatch Policy **不是独立的 Scheduler 服务**，是 `turn_executor` 的内部策略（Batch Planner）。

核心约束：

1. **并发性由资源冲突推导出来**，不靠人工硬标 parallel/serial/exclusive
2. **AcquireSet 原子申请**，任何 batch 内的 lease 整体申请、整体成功或整体回滚
3. **结果按原 tool_call 顺序回填**，对 LLM 透明
4. **禁止普通工具执行过程中再隐式追加跨资源 lease**（§7.9 P0 约束）

---

## 1. 冲突图数据结构

### 1.1 核心类型定义

```go
// Package dispatch 实现 turn_executor 的内部 Batch Planner。
// 外部只需要调用 Plan()，其余类型是内部实现细节。
package dispatch

import (
    "encoding/json"
    "time"
)

// ToolCallNode 是冲突图的节点，对应 LLM 一次返回中的一个 tool_use block。
type ToolCallNode struct {
    // Index 是在原始 toolUseBlocks 列表中的下标（0-based），
    // 用于最终结果按原顺序回填。
    Index int

    // ToolUseID 是 LLM 分配的 tool_use_id（如 "toolu_abc123"）。
    ToolUseID string

    // ToolName 是工具名（如 "quant.place_order"）。
    ToolName string

    // Input 是 LLM 传入的 JSON 参数（原始 bytes，不解析）。
    Input json.RawMessage

    // Lease 是从 ToolConcurrencySpec + Input 推导出的 LeaseRequest。
    // 如果工具没有声明 ToolConcurrencySpec，则 Lease 为 nil（无资源约束，可自由并行）。
    Lease *LeaseRequest
}

// ConflictGraph 是冲突图的完整表示。
// 节点是 tool_call，边表示"两个 tool_call 不能在同一 batch 中并行执行"。
//
// 实现选择：邻接矩阵（N × N bit 矩阵）。
// 理由：LLM 单次 tool_call 数量通常 N ≤ 32，矩阵操作 O(N²) 可接受；
// 邻接矩阵比邻接表在 batch 分组时的遍历更缓存友好。
type ConflictGraph struct {
    // Nodes 是所有 tool_call 节点，按原始 Index 排列。
    Nodes []*ToolCallNode

    // Edges[i][j] == true 表示 Nodes[i] 和 Nodes[j] 存在冲突，
    // 不能放入同一并行 batch。矩阵是对称的，Edges[i][i] 永远为 false。
    Edges [][]bool

    // n 是节点数量，缓存 len(Nodes)。
    n int
}

// NewConflictGraph 创建一个 n 节点的空冲突图（无边）。
func NewConflictGraph(nodes []*ToolCallNode) *ConflictGraph {
    n := len(nodes)
    edges := make([][]bool, n)
    for i := range edges {
        edges[i] = make([]bool, n)
    }
    return &ConflictGraph{Nodes: nodes, Edges: edges, n: n}
}

// AddEdge 在节点 i 和 j 之间添加冲突边（对称）。
func (g *ConflictGraph) AddEdge(i, j int) {
    g.Edges[i][j] = true
    g.Edges[j][i] = true
}

// Conflicts 判断节点 i 和 j 是否有冲突边。
func (g *ConflictGraph) Conflicts(i, j int) bool {
    return g.Edges[i][j]
}
```

### 1.2 LeaseRequest（引用 §7.7.2 定义）

```go
// LeaseRequest 是从 ToolConcurrencySpec 推导出的具体租约请求。
// 对应架构文档 §7.7.2 中的三元组：Capability × ResourceKey × AccessMode。
type LeaseRequest struct {
    Capability  string     // "execution.order" / "fs.write" / "session.browser"
    ResourceKey string     // "account:paper-main" / "workdir:/repo-a" 等
    AccessMode  AccessMode // SharedRead / SharedWriteAppend / ExclusiveWrite / ExclusiveSession
    Scope       LeaseScope // turn / task / daemon
    AcquireTimeout time.Duration
}

type AccessMode string
const (
    SharedRead        AccessMode = "shared-read"
    SharedWriteAppend AccessMode = "shared-write-append"
    ExclusiveWrite    AccessMode = "exclusive-write"
    ExclusiveSession  AccessMode = "exclusive-session"
)

type LeaseScope string
const (
    ScopeTurn   LeaseScope = "turn"
    ScopeTask   LeaseScope = "task"
    ScopeDaemon LeaseScope = "daemon"
)
```

---

## 2. 冲突判定规则

### 2.1 精确冲突矩阵

两个 `LeaseRequest` A 和 B **冲突**（即不能并行执行）的充要条件：

```
Conflicts(A, B) = true
  当且仅当满足以下所有条件：
  1. A.ResourceKey == B.ResourceKey   （同一资源）
  2. A 或 B 中至少有一个是独占模式（ExclusiveWrite 或 ExclusiveSession）
```

完整判定矩阵：

| A \ B             | SharedRead | SharedWriteAppend | ExclusiveWrite | ExclusiveSession |
|-------------------|:----------:|:-----------------:|:--------------:|:----------------:|
| **SharedRead**    | 不冲突      | 不冲突             | **冲突**        | **冲突**          |
| **SharedWriteAppend** | 不冲突 | 不冲突             | **冲突**        | **冲突**          |
| **ExclusiveWrite** | **冲突**   | **冲突**           | **冲突**        | **冲突**          |
| **ExclusiveSession** | **冲突** | **冲突**           | **冲突**        | **冲突**          |

> **设计说明**：SharedWriteAppend 之间不冲突，是因为 append 语义天然无序兼容（日志追加、队列入队等场景）。如果业务上不允许，tool 应声明 ExclusiveWrite。

### 2.2 特殊情况处理

```go
// conflicts 判断两个节点是否冲突。
// 任一节点没有 Lease（工具无资源约束）→ 永不冲突。
// ResourceKey 不同 → 永不冲突（即使同 Capability）。
func conflicts(a, b *ToolCallNode) bool {
    // 无 Lease 约束的工具可以随意并行。
    if a.Lease == nil || b.Lease == nil {
        return false
    }

    // ResourceKey 不同 → 无论 AccessMode 如何都不冲突。
    // 例：两个 ExclusiveWrite 操作不同账户 → 可以并行。
    if a.Lease.ResourceKey != b.Lease.ResourceKey {
        return false
    }

    // 同一 ResourceKey，按矩阵判定。
    return isExclusive(a.Lease.AccessMode) || isExclusive(b.Lease.AccessMode)
}

func isExclusive(mode AccessMode) bool {
    return mode == ExclusiveWrite || mode == ExclusiveSession
}
```

> **注意**：冲突判定不区分 `Capability`。两个不同 Capability 的工具如果操作同一个 ResourceKey 且其中一个是独占，也会冲突。这是正确行为——ResourceKey 就是资源的唯一标识，不管通过什么能力访问。

---

## 3. ResourceKey 模板解析

### 3.1 模板语法

`ToolConcurrencySpec.ResourceKeyTemplate` 使用 `{{arg_name}}` 占位符，从 tool_call 的 JSON 参数中提取值。

```
"account:{{account_id}}"         → "account:paper-main"
"workdir:{{path}}"               → "workdir:/repo-a/x.go"
"symbol:{{symbol}}"              → "symbol:BTC-USDT"
"browser:{{session_id}}"         → "browser:session-1"
"fs:{{base_dir}}"                → "fs:/home/user/project"
```

### 3.2 解析算法

```go
import (
    "encoding/json"
    "fmt"
    "regexp"
    "strings"
)

// templatePattern 匹配 {{field_name}} 占位符。
var templatePattern = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// ResolveResourceKey 从 tool_call 的 JSON 参数和 ResourceKeyTemplate
// 生成具体的 ResourceKey。
//
// 算法步骤：
//   1. 将 args JSON 解析为 map[string]any（只解一层，不递归）
//   2. 用正则找到所有 {{field}} 占位符
//   3. 逐一从 map 中取值，替换占位符
//   4. 如果某个占位符对应的字段不存在 → 使用 "*" 作为通配符（代表"所有该资源"）
//   5. 如果 ResourceKeyTemplate 为空 → 返回 Capability 本身作为 ResourceKey
//
// 通配符语义：ResourceKey 含 "*" 时，与任何同 Capability 的 ExclusiveWrite 冲突。
// 这用于处理"不指定具体资源实例"的工具（如批量操作）。
func ResolveResourceKey(template string, args json.RawMessage) (string, error) {
    // 空模板：资源 key 就是 capability 本身（单例资源）。
    if template == "" {
        return "", nil
    }

    // 解析 args 为 flat map。
    var argMap map[string]any
    if err := json.Unmarshal(args, &argMap); err != nil {
        // args 解析失败时用通配符，而不是返回错误。
        // tool 执行时会再次验证参数，此处只影响冲突判断。
        return replaceWithWildcard(template), nil
    }

    result := templatePattern.ReplaceAllStringFunc(template, func(match string) string {
        // 提取字段名，去掉 {{ 和 }}。
        fieldName := match[2 : len(match)-2]
        fieldName = strings.TrimSpace(fieldName)

        val, ok := argMap[fieldName]
        if !ok {
            // 字段缺失 → 通配符，保守估计为冲突资源。
            return "*"
        }
        return fmt.Sprintf("%v", val)
    })

    return result, nil
}

// replaceWithWildcard 将模板中所有占位符替换为 "*"。
func replaceWithWildcard(template string) string {
    return templatePattern.ReplaceAllString(template, "*")
}

// wildcardConflicts 判断含通配符的 ResourceKey 是否与另一个冲突。
// 规则：只要其中一个含 "*"，且不是 SharedRead，就视为冲突（保守策略）。
func wildcardConflicts(a, b string) bool {
    return strings.Contains(a, "*") || strings.Contains(b, "*")
}
```

### 3.3 嵌套参数提取（Phase B 扩展位）

Phase A 只支持一层平铺的 JSON field。Phase B 预留 JSONPath 扩展：

```go
// ToolConcurrencySpec Phase B 扩展字段（当前留空即可）。
type LeaseTemplate struct {
    // Phase A：模板替换，如 "account:{{account_id}}"
    ResourceKeyTemplate string

    // Phase B 可选：JSONPath，当参数嵌套时使用。
    // 例："$.order.account_id" 从 {"order": {"account_id": "paper-main"}} 提取。
    // 空字符串表示使用 ResourceKeyTemplate。
    ResourceKeyExtractor string
}
```

---

## 4. 图着色 / 分组算法

### 4.1 算法选择

**选择：贪心图着色（Greedy Graph Coloring）**，而不是最大独立集（NP-hard）。

理由：
- 最大独立集是 NP-hard，对 N ≤ 32 虽然可接受，但贪心着色已经足够优（最多 Δ+1 色，Δ 是最大度数）
- 贪心着色的"颜色"天然对应 "batch 编号"，算法直觉清晰
- Phase A 不需要全局最优（最少 batch 数），需要的是"正确"（同 batch 内无冲突）
- 贪心着色时间复杂度 O(N²)，N 个 tool_call 下可忽略不计

### 4.2 贪心图着色算法

```go
// BatchPlan 是分组结果：有序的 batch 列表，每个 batch 内的节点可并行执行。
// Batches[0] 先执行，Batches[1] 在 Batches[0] 完成后执行，以此类推。
type BatchPlan struct {
    // Batches 是分组后的执行批次，顺序敏感。
    Batches []Batch
}

// Batch 是一组可以并行执行的 tool_call 节点。
type Batch struct {
    // Nodes 是这个 batch 内的所有 tool_call。
    // 执行时并行发起，等所有完成后再进入下一 batch。
    Nodes []*ToolCallNode
    // Color 是图着色时分配的颜色（= batch 编号），调试用。
    Color int
}

// ColorGraph 对冲突图执行贪心图着色，返回 BatchPlan。
//
// 贪心着色算法（Welsh-Powell 变体）：
//   1. 按节点度数降序排列（度数高的先着色，减少回溯）
//   2. 对每个节点，找最小的未被邻居使用的颜色（batch 编号）
//   3. 分配该颜色
//   4. 将同色节点收集成 Batch
//
// 正确性保证：贪心着色保证同一颜色（batch）内无冲突边。
// 批次顺序：颜色编号即执行顺序（color 0 最先）。
func ColorGraph(g *ConflictGraph) *BatchPlan {
    n := g.n
    if n == 0 {
        return &BatchPlan{}
    }

    // Step 1：计算每个节点的度数（冲突边数量）。
    degree := make([]int, n)
    for i := 0; i < n; i++ {
        for j := 0; j < n; j++ {
            if g.Conflicts(i, j) {
                degree[i]++
            }
        }
    }

    // Step 2：按度数降序排列节点索引（高度数节点先着色）。
    order := make([]int, n)
    for i := range order {
        order[i] = i
    }
    sort.Slice(order, func(a, b int) bool {
        return degree[order[a]] > degree[order[b]]
    })

    // Step 3：贪心着色。
    colors := make([]int, n)
    for i := range colors {
        colors[i] = -1 // 未着色
    }

    maxColor := -1
    for _, nodeIdx := range order {
        // 收集邻居已使用的颜色。
        usedColors := make(map[int]bool)
        for neighbor := 0; neighbor < n; neighbor++ {
            if g.Conflicts(nodeIdx, neighbor) && colors[neighbor] >= 0 {
                usedColors[colors[neighbor]] = true
            }
        }

        // 找最小未使用颜色。
        color := 0
        for usedColors[color] {
            color++
        }
        colors[nodeIdx] = color
        if color > maxColor {
            maxColor = color
        }
    }

    // Step 4：按颜色分组成 Batch。
    batchCount := maxColor + 1
    batches := make([]Batch, batchCount)
    for i := range batches {
        batches[i].Color = i
    }
    for nodeIdx, color := range colors {
        batches[color].Nodes = append(batches[color].Nodes, g.Nodes[nodeIdx])
    }

    return &BatchPlan{Batches: batches}
}
```

### 4.3 算法示例（对应 §7.8 中的 5 个 tool_call 例子）

```
输入冲突图（5 个节点）：
  call_1: SharedRead  symbol:BTC
  call_2: SharedRead  symbol:ETH
  call_3: ExclusiveWrite account:paper-main
  call_4: ExclusiveWrite workdir:/repo-a
  call_5: ExclusiveWrite account:paper-main   ← 与 call_3 冲突

冲突边：
  (3, 5)：同 ResourceKey "account:paper-main"，且 call_3 是 ExclusiveWrite

度数排列：
  call_3: degree=1, call_5: degree=1, 其余 degree=0

贪心着色顺序（任意打平顺序）：call_3, call_5, call_1, call_2, call_4

着色过程：
  call_3 → 邻居无颜色 → color=0
  call_5 → 邻居 call_3 用了 0 → color=1
  call_1 → 无邻居 → color=0
  call_2 → 无邻居 → color=0
  call_4 → 无邻居 → color=0

结果：
  Batch 0（并行）: call_1, call_2, call_3, call_4
  Batch 1（等 Batch 0 完成）: call_5
```

---

## 5. Batch 执行顺序

### 5.1 顺序决定原则

**颜色编号即执行顺序**（color=0 最先，color=1 次之，以此类推）。这是贪心着色的天然结果，不需要额外的拓扑排序。

理由：
- 不同 batch 之间不存在数据依赖（同一 turn 内 LLM 只有一次响应，批次间无因果关系）
- 唯一排序约束来自资源冲突：冲突的 tool_call 被着上不同颜色，编号小的先执行
- 如果未来引入 tool_call 间的显式依赖（Phase C），再在 BatchPlan 上加 DAG 边

### 5.2 无冲突时的退化

当所有节点都是颜色 0（无任何冲突边），整个 BatchPlan 就是一个 Batch，所有 tool_call 全部并行执行。这是最优情况。

```go
// AllParallel 返回 true 表示所有 tool_call 可以在一个 batch 内并行执行。
func (p *BatchPlan) AllParallel() bool {
    return len(p.Batches) <= 1
}
```

---

## 6. 结果回填

### 6.1 设计原则

LLM 期望 tool_result 与 tool_use 一一对应，且**顺序必须和 tool_use_id 对齐**。并行执行打乱了完成顺序，必须在回填时还原。

### 6.2 结果收集结构

```go
// ToolCallResult 是单个 tool_call 的执行结果，携带原始 Index 用于排序。
type ToolCallResult struct {
    // OriginalIndex 是节点在原始 toolUseBlocks 列表中的下标（0-based）。
    // 回填时按此字段升序排列，恢复原始顺序。
    OriginalIndex int

    // ToolUseID 对应 LLM 分配的 tool_use_id，用于构造 tool_result block。
    ToolUseID string

    // Output 是工具执行的原始结果。
    Output json.RawMessage

    // IsError 表示工具执行是否失败（工具级别的失败，非基础设施失败）。
    IsError bool
}
```

### 6.3 并行收集 + 有序回填

```go
import (
    "context"
    "sort"
    "sync"
)

// executeBatch 并行执行一个 Batch 内的所有 tool_call，收集结果。
// 保证：所有 tool_call 完成后（无论成功失败）才返回。
func executeBatch(
    ctx context.Context,
    batch Batch,
    registry tool.Registry,
    errorPolicy BatchErrorPolicy,
) []ToolCallResult {
    results := make([]ToolCallResult, len(batch.Nodes))
    var wg sync.WaitGroup

    // 用于 fail-fast 的 context 取消（FailBatch 策略时使用）。
    batchCtx, cancelBatch := context.WithCancel(ctx)
    defer cancelBatch()

    for i, node := range batch.Nodes {
        wg.Add(1)
        go func(i int, node *ToolCallNode) {
            defer wg.Done()
            result := executeSingleTool(batchCtx, node, registry)
            results[i] = result

            // FailBatch 策略：第一个失败时取消 batch 内其他调用。
            if result.IsError && errorPolicy == FailBatch {
                cancelBatch()
            }
        }(i, node)
    }

    wg.Wait()
    return results
}

// mergeResults 将多个 batch 的结果合并，按 OriginalIndex 升序排序，
// 恢复原始 tool_call 顺序，用于构造 tool_result message。
func mergeResults(allResults [][]ToolCallResult) []ToolCallResult {
    var merged []ToolCallResult
    for _, batchResults := range allResults {
        merged = append(merged, batchResults...)
    }
    sort.Slice(merged, func(i, j int) bool {
        return merged[i].OriginalIndex < merged[j].OriginalIndex
    })
    return merged
}

// toToolResultBlocks 将排序后的结果转换为 LLM 要求的 tool_result ContentBlocks。
func toToolResultBlocks(results []ToolCallResult) []llm.ContentBlock {
    blocks := make([]llm.ContentBlock, len(results))
    for i, r := range results {
        blocks[i] = llm.ContentBlock{
            Type:      "tool_result",
            ToolUseID: r.ToolUseID,
            Output:    r.Output,
            IsError:   r.IsError,
        }
    }
    return blocks
}
```

---

## 7. 错误处理

### 7.1 三种错误策略

```go
// BatchErrorPolicy 定义单个 tool_call 失败时的 batch 级处理策略。
type BatchErrorPolicy string

const (
    // ContinueBatch：失败的 tool_call 继续作为 IsError=true 的结果回填，
    // 不影响同 batch 内其他 tool_call 的执行。
    // 适用：大多数普通工具（读文件、查数据等）。
    // 语义：让 LLM 自行处理部分失败，LLM 可以决定是否重试或调整策略。
    ContinueBatch BatchErrorPolicy = "continue"

    // FailBatch：第一个 tool_call 失败时，立即取消 batch 内剩余的并行执行。
    // 已完成的结果正常回填，被取消的以 IsError=true + "canceled" 回填。
    // 适用：同 batch 内结果有强依赖的场景（如一组必须全部成功的写操作）。
    // 语义：保守策略，避免部分写入导致状态不一致。
    FailBatch BatchErrorPolicy = "fail-batch"

    // FailAll：任何 batch 内的 tool_call 失败，整个 turn 的 tool dispatch 终止，
    // 后续 batch 不再执行，Run 转入 StateFailed。
    // 适用：关键路径工具（如 ExclusiveWrite 的金融交易）。
    // 语义：fail-fast，宁可终止也不继续在可能不一致的状态上操作。
    FailAll BatchErrorPolicy = "fail-all"
)
```

### 7.2 策略选择建议

| 工具类型 | 建议策略 | 理由 |
|----------|----------|------|
| `SharedRead`（查询、读取） | `ContinueBatch` | 读操作失败无副作用，LLM 可自行处理 |
| `SharedWriteAppend`（日志追加） | `ContinueBatch` | append 语义幂等，部分失败可接受 |
| `ExclusiveWrite`（文件写入、数据库写） | `FailBatch` | 同 batch 内多个写操作可能有前置关系 |
| `ExclusiveWrite`（金融交易下单） | `FailAll` | 状态不一致风险不可接受 |
| `ExclusiveSession`（浏览器会话操作） | `FailBatch` | 会话状态可能被污染，保守处理 |

### 7.3 策略来源

策略从哪里来？优先级从高到低：

```go
// 1. ToolConcurrencySpec 声明（工具注册时）：最精确
tool.Schema{
    Concurrency: ToolConcurrencySpec{
        ErrorPolicy: FailAll,  // 该工具自己声明 fail-all
    },
}

// 2. batch 内所有工具策略取最严格的（取 max severity）：
//    fail-all > fail-batch > continue
// 如果 batch 内有任一工具声明 fail-all，整个 batch 用 fail-all。

// 3. 全局默认：ContinueBatch（宽松策略）
```

### 7.4 Lease 申请失败的处理

```go
// AcquireSet 失败（超时或资源被锁定）时的处理：
// - 整个 batch 不执行（AcquireSet 是原子的，要么全部获取，要么全部回滚）
// - 记录失败原因到每个 node 的 result（IsError=true，Output 含 lease 失败信息）
// - 告知 LLM lease 申请超时，由 LLM 决定是否重试
// - RunState 不进入 StateFailed（这不是工具执行失败，而是资源暂时不可用）
```

---

## 8. 与现有 turn_executor 的集成

### 8.1 集成位置

在 `runner.go` 的 `dispatchTools()` 调用处插入 Batch Planner。

**当前代码位置（`sdk/loop/runner.go:231`）**：

```go
// 现有代码（串行逐个执行）：
toolResultBlocks, toolCallCount := r.dispatchTools(ctx, run, turn, toolUseBlocks)
```

**修改后**：

```go
// 新代码（先分析冲突，再 batch 并行执行）：
toolResultBlocks, toolCallCount := r.dispatchToolsWithBatchPlanning(ctx, run, turn, toolUseBlocks)
```

### 8.2 完整集成伪代码

```go
// dispatchToolsWithBatchPlanning 替换现有的串行 dispatchTools。
// 它先做冲突分析和 batch 分组，再按 batch 顺序并行执行。
func (r *Runner) dispatchToolsWithBatchPlanning(
    ctx context.Context,
    run *Run,
    turn *Turn,
    toolUseBlocks []llm.ContentBlock,
) ([]llm.ContentBlock, int) {

    // ── Step 1：构建 ToolCallNode 列表，推导 LeaseRequest ──────────────────
    nodes := make([]*dispatch.ToolCallNode, len(toolUseBlocks))
    for i, tb := range toolUseBlocks {
        node := &dispatch.ToolCallNode{
            Index:     i,
            ToolUseID: tb.ToolUseID,
            ToolName:  tb.ToolName,
            Input:     tb.Input,
        }

        // 从 ToolRegistry 查询 ToolConcurrencySpec。
        if spec, ok := r.ToolRegistry.ConcurrencySpec(tb.ToolName); ok {
            resourceKey, _ := dispatch.ResolveResourceKey(spec.ResourceKeyTemplate, tb.Input)
            node.Lease = &dispatch.LeaseRequest{
                Capability:     spec.Capability,
                ResourceKey:    resourceKey,
                AccessMode:     spec.AccessMode,
                Scope:          spec.Scope,
                AcquireTimeout: spec.AcquireTimeout,
            }
        }
        nodes[i] = node
    }

    // ── Step 2：构建冲突图 ─────────────────────────────────────────────────
    g := dispatch.NewConflictGraph(nodes)
    for i := 0; i < len(nodes); i++ {
        for j := i + 1; j < len(nodes); j++ {
            if dispatch.ConflictsNodes(nodes[i], nodes[j]) {
                g.AddEdge(i, j)
            }
        }
    }

    // ── Step 3：贪心图着色，生成 BatchPlan ──────────────────────────────────
    plan := dispatch.ColorGraph(g)

    // ── Step 4：按 batch 顺序执行 ─────────────────────────────────────────
    var allResults [][]dispatch.ToolCallResult
    totalCount := 0

    for _, batch := range plan.Batches {
        // Step 4a：语义审批（在 AcquireSet 之前，避免获取了 lease 但被审批拒绝）。
        // 门禁顺序：approve → acquireSet → execute（见 35-端到端时序 §7）。
        if r.SemanticApprover != nil {
            decision, _ := r.SemanticApprover.Approve(ctx, batch)
            if !decision.Granted {
                batchResults := failBatchWithApprovalDenied(batch, decision.Reason)
                allResults = append(allResults, batchResults)
                totalCount += len(batch.Nodes)
                continue
            }
        }

        // Step 4b：AcquireSet 原子申请 batch 内所有 lease。
        leaseReqs := collectLeaseRequests(batch)
        leases, acquireErr := r.LeaseManager.AcquireSet(ctx, leaseReqs)
        if acquireErr != nil {
            // Lease 申请失败 → 将 batch 内所有 node 标记为失败，继续下一 batch。
            // （具体策略由 BatchErrorPolicy 决定，这里是 ContinueBatch 语义）
            batchResults := failBatchWithLeaseError(batch, acquireErr)
            allResults = append(allResults, batchResults)
            totalCount += len(batch.Nodes)
            continue
        }

        // Step 4c：并行执行 batch 内所有 tool_call。
        batchResults := dispatch.ExecuteBatch(ctx, batch, r.ToolRegistry, r.batchErrorPolicy(batch))
        allResults = append(allResults, batchResults)
        totalCount += len(batch.Nodes)

        // Step 4d：释放 turn scope 的 lease。
        // task scope / daemon scope 的 lease 由 LeaseManager 跟踪，不在此释放。
        for _, lease := range leases {
            if lease.Scope == dispatch.ScopeTurn {
                _ = r.LeaseManager.Release(ctx, lease)
            }
        }

        // Step 4e：通知 ToolObserver（如果有）。
        if r.ToolObserver != nil {
            for _, result := range batchResults {
                r.ToolObserver.OnToolEnd(ctx, run, turn, result.ToolName, !result.IsError, result.Output)
            }
        }
    }

    // ── Step 5：按原始顺序回填结果 ───────────────────────────────────────
    merged := dispatch.MergeResults(allResults)
    blocks := dispatch.ToToolResultBlocks(merged)

    return blocks, totalCount
}
```

### 8.3 改动范围评估

| 文件 | 改动类型 | 说明 |
|------|----------|------|
| `sdk/loop/runner.go` | **修改** | `dispatchTools()` 替换为 `dispatchToolsWithBatchPlanning()`，添加 `LeaseManager` 字段 |
| `sdk/loop/dispatch/planner.go` | **新增** | `ConflictGraph`、`ColorGraph`、`BatchPlan`、`ExecuteBatch` |
| `sdk/loop/dispatch/conflict.go` | **新增** | `conflicts()`、`ResolveResourceKey()` |
| `sdk/loop/dispatch/result.go` | **新增** | `ToolCallResult`、`MergeResults`、`ToToolResultBlocks` |
| `sdk/tool/registry.go` | **扩展** | 新增 `ConcurrencySpec(name string) (ToolConcurrencySpec, bool)` 接口方法 |
| `sdk/tool/schema.go` | **扩展** | `Schema` 新增 `Concurrency ToolConcurrencySpec` 字段 |
| `sdk/kernel/lease.go` | **新增**（Phase A 前置） | `LeaseManager`、`AcquireSet`，Dispatch Policy 依赖此接口 |

**不需要修改的文件**：`turn_executor.go`（TurnExecutor 只是 Executor 接口的一个实现，批量分发逻辑在 `runner.go` 的 `dispatchTools` 阶段，不在 TurnExecutor.Execute 内）

> **更新**：查阅代码后发现工具分发逻辑实际在 `runner.go:dispatchTools()`（L334），而非 `turn_executor.go`。`TurnExecutor.Execute()` 只做 LLM 调用，不做 tool dispatch。Batch Planner 集成在 `Runner.dispatchTools` 替换处，改动更精确。

---

## 9. 性能考量

### 9.1 时间复杂度分析

| 阶段 | 时间复杂度 | 说明 |
|------|-----------|------|
| LeaseRequest 推导 | O(N) | N 个 tool_call，每个做一次模板解析 |
| 模板解析（正则替换） | O(K) | K 是模板中占位符数量，通常 ≤ 3 |
| 冲突图构建 | O(N²) | N×N 对比较 |
| 贪心图着色 | O(N² + N·C) | C 是颜色数（≤ Δ+1，Δ 是最大度数） |
| 并行 batch 执行 | O(B × max_tool_latency) | B 是 batch 数（≤ N/2），工具延迟主导 |
| 结果排序回填 | O(N log N) | sort.Slice |
| **总计（冲突分析部分）** | **O(N²)** | 工具延迟远大于分析开销 |

**实际性能**：N ≤ 32（LLM 单次返回的工具数），N² = 1024 次操作，冲突分析耗时 < 100μs，可以忽略。

### 9.2 ToolConcurrencySpec 缓存

```go
// SpecCache 缓存工具名到 ToolConcurrencySpec 的映射，避免每次 turn 都查 registry。
// 工具注册是启动时一次性完成的，spec 不会变化，直接用 sync.Map 做只读缓存。
type SpecCache struct {
    cache sync.Map // map[string]*ToolConcurrencySpec
}

func (c *SpecCache) Get(toolName string, registry tool.Registry) (*ToolConcurrencySpec, bool) {
    if v, ok := c.cache.Load(toolName); ok {
        spec, _ := v.(*ToolConcurrencySpec)
        return spec, spec != nil
    }
    // miss：查 registry，回填缓存。
    if spec, ok := registry.ConcurrencySpec(toolName); ok {
        c.cache.Store(toolName, &spec)
        return &spec, true
    }
    // 工具无 spec：缓存 nil，下次不再查 registry。
    c.cache.Store(toolName, (*ToolConcurrencySpec)(nil))
    return nil, false
}
```

**结论**：SpecCache 是小优化，对于 N ≤ 32 的场景可以不加。但对于高频 serve 模式（大量并发 task 复用同一组工具），缓存有意义，建议 Phase A 一起实现。

### 9.3 资源冲突的极端场景

```
最差情况：N 个 tool_call 两两冲突（完全图）
  → N 个 batch，每个 batch 一个 tool_call → 完全串行
  → 退化为 v1 的串行行为，功能正确，性能与当前持平

正常情况：稀疏冲突图（绝大多数 tool_call 不冲突）
  → 1-2 个 batch，大部分并行 → 性能显著提升

典型场景（§7.8 示例的 4 个 tool_call）：
  → 1 个 batch，完全并行 → 接近 max(tool_latency) 而非 sum(tool_latency)
```

---

## 10. 完整流程图

```text
LLM 返回 N 个 tool_use blocks
         │
         ▼
[Step 1] 推导 LeaseRequest
  ToolRegistry.ConcurrencySpec(name)
  ResolveResourceKey(template, args)
  → N 个 ToolCallNode（部分有 Lease，部分无）
         │
         ▼
[Step 2] 构建冲突图
  for i, j in N×N:
    if conflicts(node_i, node_j): AddEdge(i, j)
  → ConflictGraph（N 节点，M 冲突边）
         │
         ▼
[Step 3] 贪心图着色
  ColorGraph(g)
  → BatchPlan（有序 Batch 列表）
         │
         ▼
[Step 4] 按 Batch 顺序执行            ← 主循环
  for each Batch:
    AcquireSet(batch.LeaseRequests)    ← 原子申请
    ├── 失败 → failBatch, continue
    └── 成功 → ExecuteBatch（goroutine 并行）
                ├── 每个 tool_call 独立 goroutine
                ├── FailBatch 策略：第一个失败 → cancelBatch()
                └── 所有 goroutine 完成 → 收集 []ToolCallResult
    Release turn-scope leases
    Notify ToolObserver
         │
         ▼
[Step 5] 结果回填
  MergeResults（按 OriginalIndex 升序排序）
  ToToolResultBlocks（构造 tool_result ContentBlocks）
  → 追加到 messages，进入下一 turn
```

---

## 11. 设计取舍说明

| 设计决策 | 选择 | 被放弃的方案 | 理由 |
|----------|------|-------------|------|
| 分组算法 | 贪心图着色 | 最大独立集（精确解） | N≤32 下最优解和贪心解差距可忽略，贪心 O(N²) 已够 |
| 冲突图表示 | 邻接矩阵 | 邻接表 | N≤32 矩阵比表遍历更缓存友好，且实现简单 |
| ResourceKey 通配符 | `*` 作为保守通配 | 报错 / 跳过约束 | 参数缺失时宁可多冲突也不漏冲突，保安全 |
| batch 顺序 | 颜色编号即顺序 | 单独的拓扑排序 | 同 turn 内无因果依赖，颜色即顺序天然正确 |
| 并行执行 | `sync.WaitGroup` + goroutine | 协程池 | N≤32，goroutine 开销可忽略，不需要池化 |
| Lease 申请失败 | 记为 IsError，继续 | 终止整个 turn | 资源暂时不可用是可恢复状态，让 LLM 决策更灵活 |
| 错误策略来源 | ToolConcurrencySpec 声明 + batch 内取最严 | 全局配置 | 工具自己最清楚自己的错误语义 |
```
