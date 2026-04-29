# 35. Context Engine 详细设计

> **⚠️ 实现差异说明（2026-04-24）：** 实际实现在 `sdk/kernel/context_engine.go`（非独立 sdk/contextengine/ 包）。Compress 签名为 `(ctx, messages, budget int)`（多了 budget 参数）。Persist 方法改为 SharedFor/ClearShared。
>
> **✅ 实现状态（2026-04-26）：** `Compress` 已实现完整三阶段压缩：① 窗口裁剪 → ② 截断最老消息 → ③ **LLM 摘要（当 `e.Summarizer != nil`）** → 硬截断兜底。`summarizeMessages` 保留最新 2 条消息，其余由 LLM 生成摘要。chat/run/buildOrchestrator 三处均注入带 Summarizer 的 ContextEngine。

> **状态**：v1 · 2026-04-16  
> **所属阶段**：Phase B-4（见 32-v3-Brain架构.md §6）  
> **上位规格**：[32-v3-Brain架构.md](./32-v3-Brain架构.md) §7.4  
> **依赖规格**：[22-Agent-Loop规格.md](./22-Agent-Loop规格.md) §3（三层 Prompt Cache）

---

## 0. 概述

Context Engine 是在现有三层 Prompt Cache（L1 System / L2 Task / L3 History）之上新增的**统一上下文装配层**。它解决三个问题：

1. **装配**：根据 brain 类型、任务类型、token 预算，选择性拼装当前 turn 应该看到什么上下文
2. **压缩**：长对话中 token 爆炸时，用摘要、窗口裁剪、预算约束三种策略降低体积
3. **共享**：central delegate 给专精 brain 时传递必要上下文；专精 brain 执行完后结果 merge 回 central

```text
┌─────────────────────────────────────────────────────────────┐
│                      Context Engine                          │
│                                                             │
│   ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐ │
│   │  Assembler   │  │  Compressor  │  │  CrossBrain      │ │
│   │  上下文装配  │  │  上下文压缩  │  │  Sharer 跨脑共享 │ │
│   │              │  │              │  │                  │ │
│   │ brain 类型   │  │ 摘要压缩     │  │ central→spec     │ │
│   │ 任务类型     │  │ 窗口裁剪     │  │ result merge     │ │
│   │ token budget │  │ 预算约束     │  │ 隐私边界         │ │
│   └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘ │
│          │                 │                   │            │
│          └─────────────────┼───────────────────┘            │
│                            ▼                                │
│   ┌─────────────────────────────────────────────────────┐   │
│   │           三层 Prompt Cache（已有）                   │   │
│   │    L1 System  │  L2 Task  │  L3 History             │   │
│   └─────────────────────────────────────────────────────┘   │
│                            │                                │
│   ┌─────────────────────────────────────────────────────┐   │
│   │           跨 Turn 记忆持久化（新增）                  │   │
│   │    MemoryStore: 摘要 + 里程碑 + 用户档案             │   │
│   └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

---

## 1. 核心接口定义

```go
// package contextengine
// 路径: sdk/contextengine/engine.go

package contextengine

import (
    "context"
    "time"

    "github.com/leef-l/brain/sdk/agent"
    "github.com/leef-l/brain/sdk/llm"
)

// ContextEngine 是上下文装配层的主接口。
// 所有方法对于相同输入必须是幂等的（不产生副作用），
// 副作用（写 MemoryStore）由调用方在适当时机触发。
type ContextEngine interface {
    // Assemble 根据 AssembleRequest 返回装配好的消息列表。
    // 返回的消息已经过 Compress（若 token 超预算）和 Share 路由处理。
    // 调用方直接将返回值作为 llm.ChatRequest.Messages 使用。
    Assemble(ctx context.Context, req AssembleRequest) ([]llm.Message, error)

    // Compress 对已有消息列表执行压缩。
    // 先尝试窗口裁剪（无 LLM 调用，低延迟），
    // 若仍超预算则调用 LLM 生成摘要（高延迟，高质量）。
    // policy 为 nil 时使用引擎默认策略。
    Compress(ctx context.Context, messages []llm.Message, policy *CompressPolicy) ([]llm.Message, error)

    // Share 将 from brain 的上下文按共享协议投递给 to brain。
    // 实现必须遵守隐私边界（§4.4），过滤不可跨脑传递的内容。
    // 该方法是异步友好的：调用方可以 fire-and-forget，
    // 也可以 await 确认对端已接收。
    Share(ctx context.Context, from agent.Kind, to agent.Kind, msgs []llm.Message, opts ...ShareOption) error

    // Persist 将当前对话的关键内容提炼后写入 MemoryStore。
    // 由 Runner 在 turn 结束时调用，不阻塞主流程。
    Persist(ctx context.Context, runID string, brainKind agent.Kind, msgs []llm.Message) error
}
```

---

## 2. AssembleRequest 数据模型

```go
// AssembleRequest 是 Assemble() 的完整入参。
// 字段分为五个语义组：身份、任务、预算、记忆、控制。
type AssembleRequest struct {
    // ── 身份组 ──────────────────────────────────────────────

    // RunID 是当前 TaskExecution 的唯一 ID，用于关联 MemoryStore。
    RunID string

    // BrainKind 是发起请求的 brain 类型。
    // 装配器依据此字段决定包含哪些层的上下文。
    BrainKind agent.Kind

    // TurnIndex 是当前 Turn 的索引（0-based）。
    // 用于判断是否需要触发压缩（例如每 N turn 执行一次摘要）。
    TurnIndex int

    // ── 任务组 ──────────────────────────────────────────────

    // TaskType 描述当前任务的语义类型，影响装配策略的选择。
    // 例如 "analysis" / "execution" / "verification" / "delegation"。
    TaskType TaskType

    // Instruction 是当前 turn 的自然语言指令。
    // 装配器可能将其用于语义相关性过滤（未来扩展）。
    Instruction string

    // DelegatedFrom 非空时表示这是被委托的子任务，来自指定 brain。
    // 装配器将把委托上下文（SharedContext）注入到消息头部。
    DelegatedFrom agent.Kind

    // SharedContext 是 central 委托时随同传递的上下文消息。
    // 见 §4 跨脑共享协议。
    SharedContext []llm.Message

    // ── 预算组 ──────────────────────────────────────────────

    // TokenBudget 是本次 Assemble 允许使用的最大 token 数。
    // 0 表示使用引擎默认值（通常为模型上下文窗口的 80%）。
    TokenBudget int

    // TokenBudgetStrategy 控制在预算紧张时如何分配 token。
    // 见 §5 Token 预算管理。
    TokenBudgetStrategy TokenBudgetStrategy

    // ── 记忆组 ──────────────────────────────────────────────

    // IncludeMemory 控制是否将 MemoryStore 中的历史记忆注入。
    // 默认 true；对短任务可以设为 false 节省 token。
    IncludeMemory bool

    // MemorySlots 指定要注入的记忆槽位名称。
    // 空列表时注入所有相关记忆。
    MemorySlots []string

    // ── 控制组 ──────────────────────────────────────────────

    // CompressPolicy 覆盖引擎默认压缩策略。nil 使用默认值。
    CompressPolicy *CompressPolicy

    // ForceCompress 为 true 时跳过 token 检查，强制执行压缩。
    // 用于主动维护（如任务结束前的最终压缩）。
    ForceCompress bool

    // MaxHistoryMessages 限制 L3 History 层最多包含多少条消息。
    // 0 表示无限制（由 TokenBudget 兜底）。
    MaxHistoryMessages int

    // ExcludePrivate 为 true 时剔除标记为 private 的消息块。
    // 跨脑 Share 时自动置 true（见 §4.4 隐私边界）。
    ExcludePrivate bool
}

// TaskType 枚举任务语义类型。
type TaskType string

const (
    TaskTypeAnalysis     TaskType = "analysis"     // 分析、研究类
    TaskTypeExecution    TaskType = "execution"     // 执行、操作类
    TaskTypeVerification TaskType = "verification"  // 验证、审查类
    TaskTypeDelegation   TaskType = "delegation"    // 委托给专精脑
    TaskTypeConversation TaskType = "conversation"  // 普通对话类
    TaskTypeLongRunning  TaskType = "long_running"  // 长时间任务（Data/Quant daemon）
)

// TokenBudgetStrategy 控制 token 不足时的分配策略。
type TokenBudgetStrategy string

const (
    // StrategyRecency 优先保留最近的消息（默认）。
    StrategyRecency TokenBudgetStrategy = "recency"

    // StrategyMilestone 优先保留里程碑消息和最近消息。
    StrategyMilestone TokenBudgetStrategy = "milestone"

    // StrategyRelevance 基于语义相关性评分保留（需要额外计算）。
    StrategyRelevance TokenBudgetStrategy = "relevance"

    // StrategyBalanced 在 Recency 和 Milestone 之间取平衡。
    StrategyBalanced TokenBudgetStrategy = "balanced"
)
```

---

## 3. Assemble 装配策略

### 3.1 装配流水线

```go
// Assemble 的内部执行流水线（伪代码）
func (e *engine) Assemble(ctx context.Context, req AssembleRequest) ([]llm.Message, error) {
    // Step 1: 构建基础消息列表（按层次）
    var msgs []llm.Message

    // Step 1a: 注入委托上下文（若为子任务）
    if req.DelegatedFrom != "" && len(req.SharedContext) > 0 {
        delegationHeader := buildDelegationHeader(req.DelegatedFrom, req.SharedContext)
        msgs = append(msgs, delegationHeader...)
    }

    // Step 1b: 注入跨 Turn 记忆（L0 层，在 L2 之前）
    if req.IncludeMemory {
        memMsgs, err := e.memory.Recall(ctx, req.RunID, req.BrainKind, req.MemorySlots)
        if err == nil && len(memMsgs) > 0 {
            msgs = append(msgs, memMsgs...)
        }
    }

    // Step 1c: 加载 L3 History（原始历史消息）
    history, err := e.historyStore.Load(ctx, req.RunID)
    if err != nil {
        return nil, err
    }

    // Step 1d: 按 BrainKind 和 TaskType 过滤历史
    history = e.filterHistory(history, req)

    msgs = append(msgs, history...)

    // Step 2: token 计数
    tokenCount := e.tokenCounter.Count(msgs, req.BrainKind)

    budget := req.TokenBudget
    if budget == 0 {
        budget = e.defaultBudget(req.BrainKind)
    }

    // Step 3: 如需压缩则压缩
    if req.ForceCompress || tokenCount > budget {
        policy := req.CompressPolicy
        if policy == nil {
            policy = e.defaultCompressPolicy(req.BrainKind, req.TaskType)
        }
        msgs, err = e.Compress(ctx, msgs, policy)
        if err != nil {
            return nil, err
        }
    }

    // Step 4: 截断到 MaxHistoryMessages（硬性上限）
    if req.MaxHistoryMessages > 0 && len(msgs) > req.MaxHistoryMessages {
        msgs = applyMaxMessages(msgs, req.MaxHistoryMessages)
    }

    // Step 5: 过滤私有内容（跨脑场景）
    if req.ExcludePrivate {
        msgs = filterPrivateBlocks(msgs)
    }

    return msgs, nil
}
```

### 3.2 按 BrainKind 的装配矩阵

| BrainKind  | L1 System | L2 Task | L3 History | 跨脑记忆 | 默认 token 预算 |
|------------|-----------|---------|------------|----------|----------------|
| `central`  | 全量      | 全量    | 全量       | 读写     | 模型窗口 × 85% |
| `quant`    | 全量      | 全量    | 最近 50 条 | 只读     | 模型窗口 × 75% |
| `data`     | 全量      | 全量    | 最近 20 条 | 只读     | 模型窗口 × 60% |
| `code`     | 全量      | 全量    | 最近 40 条 | 只读     | 模型窗口 × 80% |
| `browser`  | 全量      | 全量    | 最近 15 条 | 只读     | 模型窗口 × 65% |
| `verifier` | 全量      | 全量    | 最近 30 条 | 只读     | 模型窗口 × 70% |
| `fault`    | 全量      | 全量    | 最近 20 条 | 只读     | 模型窗口 × 60% |

**设计原则**：
- 专精 brain 的 History 窗口比 central 更小，因为它们只需要当前子任务的上下文
- `data` 和 `fault` 这类 daemon 型 brain 历史更短，因为它们执行的是重复性固定任务
- 所有专精 brain 对跨脑记忆只读，防止专精脑意外污染中央记忆

### 3.3 按 TaskType 的装配调整

```go
func (e *engine) filterHistory(msgs []llm.Message, req AssembleRequest) []llm.Message {
    switch req.TaskType {
    case TaskTypeDelegation:
        // 委托任务：只保留本次委托的相关上下文
        // 丢弃与本次任务无关的历史轮次
        return filterByRelevance(msgs, req.Instruction, maxRelevantMessages: 10)

    case TaskTypeLongRunning:
        // 长时任务（Data/Quant daemon）：只保留最近 5 条 + 里程碑
        return extractMilestones(msgs, 3) + recentN(msgs, 5)

    case TaskTypeVerification:
        // 验证任务：需要看到被验证内容的完整上下文
        return msgs // 保留全量，依赖 token 预算兜底

    case TaskTypeAnalysis:
        // 分析任务：保留里程碑 + 最近 20 条
        return extractMilestones(msgs, 5) + recentN(msgs, 20)

    default:
        return msgs
    }
}
```

---

## 4. Compress 压缩策略

### 4.1 压缩触发条件

```go
// CompressPolicy 定义压缩行为。
type CompressPolicy struct {
    // TriggerThreshold 是触发压缩的 token 占比（相对于预算）。
    // 例如 0.9 表示 token 数超过预算的 90% 时触发压缩。
    TriggerThreshold float64 // 默认 0.85

    // PreferSummary 为 true 时优先使用 LLM 摘要（质量高但延迟高）。
    // 为 false 时优先使用窗口裁剪（延迟低但信息损失可能更大）。
    PreferSummary bool // 默认 false（速度优先）

    // SummaryMaxTokens 是 LLM 生成的摘要最大 token 数。
    // 摘要应当简洁，过长的摘要失去意义。
    SummaryMaxTokens int // 默认 512

    // WindowSize 是窗口裁剪保留的最近消息数。
    WindowSize int // 默认 20

    // MilestoneCount 是额外保留的里程碑消息数（窗口之外）。
    MilestoneCount int // 默认 5

    // PeriodicEveryN 若 > 0，表示每 N 个 turn 主动执行一次摘要。
    // 用于长时任务的定期压缩，防止被动触发时 token 已过多。
    PeriodicEveryN int // 默认 0（禁用定期压缩）

    // SummaryModel 指定用于生成摘要的 LLM 模型。
    // 空字符串使用当前 brain 的默认模型。
    // 推荐使用较便宜的模型（如 haiku）生成摘要。
    SummaryModel string
}
```

### 4.2 三层压缩策略详解

#### 策略 A：基于窗口的裁剪（无 LLM 调用，P0）

```text
原始消息列表（N 条）：
[m0] [m1] [m2] ... [m_milestone_1] ... [m_milestone_2] ... [m_N-5] [m_N-4] [m_N-3] [m_N-2] [m_N-1]

裁剪后：
[summary_placeholder?] [m_milestone_1] [m_milestone_2] [m_N-5] [m_N-4] [m_N-3] [m_N-2] [m_N-1]
                       ←── 里程碑（固定保留）───────── ←── 最近 WindowSize 条 ──────────────────
```

```go
// windowCompress 执行基于窗口的裁剪压缩（无 LLM 调用）。
func windowCompress(msgs []llm.Message, policy *CompressPolicy) []llm.Message {
    if len(msgs) <= policy.WindowSize+policy.MilestoneCount {
        return msgs // 无需压缩
    }

    // Step 1: 提取里程碑消息
    milestones := extractMilestones(msgs, policy.MilestoneCount)

    // Step 2: 保留最近 WindowSize 条
    recent := msgs
    if len(msgs) > policy.WindowSize {
        recent = msgs[len(msgs)-policy.WindowSize:]
    }

    // Step 3: 合并（去重）
    seen := make(map[int]bool)
    var result []llm.Message

    for _, m := range milestones {
        if !seen[m.index] {
            result = append(result, m.Message)
            seen[m.index] = true
        }
    }
    for _, m := range recent {
        if !seen[m.index] {
            result = append(result, m)
            seen[m.index] = true
        }
    }

    return result
}

// extractMilestones 识别并提取里程碑消息。
// 里程碑判断规则（优先级从高到低）：
//   1. 消息包含 MilestoneMarker 标记（由 LLM 在特殊节点生成）
//   2. 消息是子任务完成的 tool_result（status: completed）
//   3. 消息包含用户明确确认的关键决策
//   4. 消息的 token 密度最高的 N 条（内容量大通常意味着重要）
func extractMilestones(msgs []llm.Message, count int) []indexedMessage {
    var candidates []indexedMessage
    for i, m := range msgs {
        score := milestoneScore(m)
        if score > 0 {
            candidates = append(candidates, indexedMessage{index: i, Message: m, score: score})
        }
    }
    // 按评分降序取前 count 条，按原始顺序排列
    sort.Slice(candidates, func(i, j int) bool {
        return candidates[i].score > candidates[j].score
    })
    if len(candidates) > count {
        candidates = candidates[:count]
    }
    sort.Slice(candidates, func(i, j int) bool {
        return candidates[i].index < candidates[j].index
    })
    return candidates
}
```

#### 策略 B：基于摘要的压缩（LLM 生成，P1）

```go
// summaryCompress 使用 LLM 生成摘要替代中间历史（高质量，有延迟）。
func (e *engine) summaryCompress(
    ctx context.Context,
    msgs []llm.Message,
    policy *CompressPolicy,
) ([]llm.Message, error) {
    // Step 1: 确定摘要范围
    // 保留 head（最早的几条，建立任务背景）和 tail（最近的窗口）
    // 对 middle 生成摘要
    headCount := 3
    tailCount := policy.WindowSize

    if len(msgs) <= headCount+tailCount {
        return msgs, nil // 无需摘要
    }

    head := msgs[:headCount]
    middle := msgs[headCount : len(msgs)-tailCount]
    tail := msgs[len(msgs)-tailCount:]

    // Step 2: 构建摘要 prompt
    summaryPrompt := buildSummaryPrompt(middle)

    // Step 3: 调用 LLM 生成摘要（使用 SummaryModel，通常是 haiku）
    summaryResp, err := e.llmProvider.Complete(ctx, llm.ChatRequest{
        Model:     policy.SummaryModel,
        MaxTokens: policy.SummaryMaxTokens,
        Messages:  summaryPrompt,
    })
    if err != nil {
        // 摘要失败时降级为窗口裁剪
        e.metrics.IncrCounter("context.summary_fallback", 1)
        return windowCompress(msgs, policy), nil
    }

    // Step 4: 构造摘要消息（带特殊标记，便于后续识别）
    summaryMsg := llm.Message{
        Role: "user",
        Content: []llm.ContentBlock{
            {
                Type: "text",
                Text: fmt.Sprintf("[CONTEXT_SUMMARY]\n%s\n[/CONTEXT_SUMMARY]",
                    summaryResp.Text),
            },
        },
    }
    // 注入 private=false 标记：摘要可以跨脑传递
    summaryMsg = markSummaryMessage(summaryMsg)

    // Step 5: 拼接 head + summary + tail
    result := make([]llm.Message, 0, headCount+1+tailCount)
    result = append(result, head...)
    result = append(result, summaryMsg)
    result = append(result, tail...)

    return result, nil
}

// buildSummaryPrompt 构造摘要指令 prompt。
func buildSummaryPrompt(msgs []llm.Message) []llm.Message {
    var sb strings.Builder
    sb.WriteString("请对以下对话历史生成简洁摘要（不超过 3 段），重点保留：\n")
    sb.WriteString("1. 已完成的关键任务和结果\n")
    sb.WriteString("2. 重要的决策和判断\n")
    sb.WriteString("3. 未解决的问题或待续事项\n\n")
    sb.WriteString("对话历史：\n")
    for _, m := range msgs {
        for _, blk := range m.Content {
            if blk.Type == "text" {
                sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, blk.Text))
            }
        }
    }
    return []llm.Message{
        {Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: sb.String()}}},
    }
}
```

#### 策略 C：基于 token 预算约束的贪心裁剪（兜底）

```go
// budgetConstrainedCompress 在 token 预算内用贪心策略保留最有价值的消息。
// 优先级：里程碑 > 最近消息 > 中间消息（按重要性评分）
func budgetConstrainedCompress(
    msgs []llm.Message,
    budget int,
    counter TokenCounter,
    brainKind agent.Kind,
) []llm.Message {
    // Step 1: 给每条消息打分
    type scoredMsg struct {
        msg      llm.Message
        tokens   int
        score    float64
        index    int
    }

    scored := make([]scoredMsg, len(msgs))
    for i, m := range msgs {
        tk := counter.CountMessage(m, brainKind)
        scored[i] = scoredMsg{
            msg:    m,
            tokens: tk,
            score:  messageImportanceScore(m, i, len(msgs)),
            index:  i,
        }
    }

    // Step 2: 按评分降序排列，贪心选择直到 token 用完
    sort.Slice(scored, func(i, j int) bool {
        return scored[i].score > scored[j].score
    })

    var selected []scoredMsg
    used := 0
    for _, sm := range scored {
        if used+sm.tokens <= budget {
            selected = append(selected, sm)
            used += sm.tokens
        }
    }

    // Step 3: 按原始顺序排列返回
    sort.Slice(selected, func(i, j int) bool {
        return selected[i].index < selected[j].index
    })

    result := make([]llm.Message, len(selected))
    for i, sm := range selected {
        result[i] = sm.msg
    }
    return result
}

// messageImportanceScore 计算单条消息的重要性评分（0.0 - 1.0）。
func messageImportanceScore(m llm.Message, index, total int) float64 {
    score := 0.0

    // 最近性权重：越靠近结尾权重越高
    recencyWeight := float64(index) / float64(total)
    score += recencyWeight * 0.4

    // 里程碑权重
    if isMilestone(m) {
        score += 0.4
    }

    // 角色权重：user 消息比 assistant 更重要（包含人类意图）
    if m.Role == "user" {
        score += 0.1
    }

    // 工具结果权重：成功的 tool_result 通常是关键信息
    if hasSuccessfulToolResult(m) {
        score += 0.1
    }

    return score
}
```

### 4.3 压缩策略选择逻辑

```go
// Compress 是 ContextEngine 接口的实现。
// 策略选择顺序：
//   1. token 在预算内 → 不压缩
//   2. 超预算且 PreferSummary=true → 尝试摘要压缩（失败降级为窗口）
//   3. 超预算且 PreferSummary=false → 窗口裁剪
//   4. 窗口裁剪后仍超预算 → 贪心预算约束裁剪（兜底）
func (e *engine) Compress(
    ctx context.Context,
    msgs []llm.Message,
    policy *CompressPolicy,
) ([]llm.Message, error) {
    if policy == nil {
        policy = e.defaultPolicy
    }

    tokenCount := e.tokenCounter.Count(msgs, "")
    budget := e.defaultBudget("")

    // 未超预算
    if float64(tokenCount) < float64(budget)*policy.TriggerThreshold {
        return msgs, nil
    }

    e.metrics.IncrCounter("context.compress_triggered", 1)

    var result []llm.Message

    if policy.PreferSummary {
        // 优先摘要压缩
        var err error
        result, err = e.summaryCompress(ctx, msgs, policy)
        if err != nil {
            e.logger.Warn("summary compress failed, falling back to window", "error", err)
            result = windowCompress(msgs, policy)
        }
    } else {
        // 优先窗口裁剪（低延迟）
        result = windowCompress(msgs, policy)
    }

    // 检查窗口裁剪是否足够
    afterTokenCount := e.tokenCounter.Count(result, "")
    if afterTokenCount > budget {
        // 兜底：贪心预算约束裁剪
        e.metrics.IncrCounter("context.greedy_compress_triggered", 1)
        result = budgetConstrainedCompress(result, budget, e.tokenCounter, "")
    }

    e.metrics.RecordHistogram("context.compress_ratio",
        float64(afterTokenCount)/float64(tokenCount))

    return result, nil
}
```

### 4.4 定期主动压缩（长时任务专用）

```go
// PeriodicCompressor 是长时任务（Data/Quant daemon）的定期压缩器。
// 由 Runner 在每个 Turn 结束时调用。
type PeriodicCompressor struct {
    policy  *CompressPolicy
    engine  ContextEngine
    logger  Logger
}

func (pc *PeriodicCompressor) MaybeCompress(
    ctx context.Context,
    turnIndex int,
    msgs []llm.Message,
) ([]llm.Message, bool, error) {
    if pc.policy.PeriodicEveryN <= 0 {
        return msgs, false, nil
    }
    if turnIndex == 0 || turnIndex%pc.policy.PeriodicEveryN != 0 {
        return msgs, false, nil
    }

    // 强制执行摘要压缩（不等 token 超限）
    compressed, err := pc.engine.Compress(ctx, msgs, &CompressPolicy{
        PreferSummary:    true, // 定期压缩用摘要，质量更高
        SummaryMaxTokens: pc.policy.SummaryMaxTokens,
        WindowSize:       pc.policy.WindowSize,
        MilestoneCount:   pc.policy.MilestoneCount,
    })
    if err != nil {
        return msgs, false, err
    }

    pc.logger.Info("periodic compress done",
        "turn", turnIndex,
        "before", len(msgs),
        "after", len(compressed))

    return compressed, true, nil
}
```

---

## 5. 跨脑共享协议

### 5.1 设计原则

跨脑上下文共享遵循**最小必要原则**：只传递专精 brain 完成当前任务**必须**的信息，不传递与任务无关的上下文（包括其他专精 brain 的私有执行状态、用户凭证信息、policy 元数据）。

### 5.2 Share() 实现

```go
// ShareOption 是 Share() 的可选参数。
type ShareOption func(*shareConfig)

type shareConfig struct {
    // MaxMessages 限制共享的最大消息数。
    MaxMessages int
    // IncludeMemory 是否同时共享相关记忆摘要。
    IncludeMemory bool
    // WaitAck 是否等待对端确认收到（同步模式）。
    WaitAck bool
    // TTL 上下文在对端的有效时间。0 表示随任务结束自动失效。
    TTL time.Duration
}

// WithMaxMessages 设置最大共享消息数。
func WithMaxMessages(n int) ShareOption {
    return func(c *shareConfig) { c.MaxMessages = n }
}

// WithMemoryShare 共享时同时发送记忆摘要。
func WithMemoryShare() ShareOption {
    return func(c *shareConfig) { c.IncludeMemory = true }
}

// Share 实现跨脑上下文传递协议。
func (e *engine) Share(
    ctx context.Context,
    from agent.Kind,
    to agent.Kind,
    msgs []llm.Message,
    opts ...ShareOption,
) error {
    cfg := &shareConfig{
        MaxMessages: 20, // 默认最多共享 20 条
    }
    for _, o := range opts {
        o(cfg)
    }

    // Step 1: 应用隐私过滤（§5.4）
    filtered := filterPrivateBlocks(msgs)
    filtered = applyPrivacyBoundary(filtered, from, to)

    // Step 2: 如需压缩（超出 MaxMessages），执行窗口裁剪
    if cfg.MaxMessages > 0 && len(filtered) > cfg.MaxMessages {
        policy := &CompressPolicy{
            WindowSize:     cfg.MaxMessages - 3,
            MilestoneCount: 3,
        }
        filtered = windowCompress(filtered, policy)
    }

    // Step 3: 封装为 SharedContextEnvelope
    envelope := SharedContextEnvelope{
        FromBrain:   from,
        ToBrain:     to,
        Messages:    filtered,
        CreatedAt:   time.Now(),
        TTL:         cfg.TTL,
        ProtocolVer: "1",
    }

    // Step 4: 若有记忆摘要，附加
    if cfg.IncludeMemory {
        mem, err := e.memory.RecallForShare(ctx, from, to)
        if err == nil {
            envelope.MemorySummary = mem
        }
    }

    // Step 5: 通过 BrainChannel 投递
    return e.brainChannel.Send(ctx, to, envelope, cfg.WaitAck)
}

// SharedContextEnvelope 是跨脑传递的上下文封装。
type SharedContextEnvelope struct {
    // FromBrain 是发送方 brain 类型。
    FromBrain agent.Kind `json:"from_brain"`

    // ToBrain 是接收方 brain 类型。
    ToBrain agent.Kind `json:"to_brain"`

    // Messages 是过滤后的上下文消息列表。
    Messages []llm.Message `json:"messages"`

    // MemorySummary 是发送方 brain 的相关记忆摘要（可选）。
    MemorySummary string `json:"memory_summary,omitempty"`

    // CreatedAt 是封装创建时间。
    CreatedAt time.Time `json:"created_at"`

    // TTL 是上下文的有效期。0 表示随任务生命周期失效。
    TTL time.Duration `json:"ttl,omitempty"`

    // ProtocolVer 是协议版本，当前固定为 "1"。
    ProtocolVer string `json:"protocol_ver"`
}
```

#### 与跨脑通信协议（35-14）的映射

`Share()` 是 Context Engine 的上层语义接口，底层通过跨脑通信协议传输：

| Context Engine 层 | 跨脑通信层（35-14） | 说明 |
|-------------------|-------------------|------|
| `Share(ctx, from, to, msgs, opts)` | `brain.context_share` RPC（§3.2.5） | 语义调用 → RPC 传输 |
| `SharedContextEnvelope` | `跨脑通信.SharedContextEnvelope`（§3.2.5） | 两层信封独立定义，`brainChannel.Send()` 负责转换 |
| `filterPrivateBlocks()` | `PrivacyFilter`（§4.3） | Context Engine 侧过滤，通信层再校验 |
| `shareConfig.WaitAck` | JSON-RPC response | WaitAck=true 时等待 RPC 响应，false 时 fire-and-forget |

> **调用链**：`ContextEngine.Share()` → 隐私过滤 → 压缩 → 封装 `SharedContextEnvelope` →
> `brainChannel.Send()` → 序列化为 `brain.context_share` RPC params → 跨脑通信传输 →
> 接收端 `ContextEngine.Receive()` 反序列化 → 合入本地 `MemoryStore`。

### 5.3 SubtaskRequest 上下文字段扩展

在现有 `SubtaskRequest`（`sdk/kernel/orchestrator.go`）基础上扩展上下文字段：

```go
// SubtaskRequest 扩展版（在现有 Context json.RawMessage 基础上细化）
// 路径: sdk/kernel/orchestrator.go（扩展现有结构）
type SubtaskRequest struct {
    // ── 现有字段（保持不变）──────────────────────────────────
    TaskID     string             `json:"task_id"`
    TargetKind agent.Kind         `json:"target_kind"`
    Instruction string            `json:"instruction"`
    Budget     *SubtaskBudget    `json:"budget,omitempty"`
    Execution  *executionpolicy.ExecutionSpec `json:"execution,omitempty"`

    // ── 新增：结构化上下文（替代 Context json.RawMessage）──────
    // ContextV2 携带结构化的跨脑上下文，比原来的 RawMessage 更精确。
    // 向后兼容：若 ContextV2 为 nil，继续使用原 Context 字段。
    ContextV2 *SubtaskContext `json:"context_v2,omitempty"`

    // Context 保留旧字段（向后兼容）。
    Context json.RawMessage `json:"context,omitempty"`
}

// SubtaskContext 是 central 委托子任务时传递的结构化上下文。
type SubtaskContext struct {
    // ParentRunID 是 central 的 RunID，用于关联追踪。
    ParentRunID string `json:"parent_run_id"`

    // RelevantMessages 是经过隐私过滤的相关历史消息。
    // central 从自己的 L3 History 中选择与当前子任务最相关的消息。
    RelevantMessages []llm.Message `json:"relevant_messages,omitempty"`

    // TaskGoal 是本次子任务的目标描述（结构化，补充 Instruction 的自然语言）。
    TaskGoal TaskGoal `json:"task_goal"`

    // PriorResults 是同一父任务下，其他已完成子任务的结果摘要。
    // 例如：code brain 执行完后，verifier brain 可以看到代码修改的摘要。
    PriorResults []SubtaskResultSummary `json:"prior_results,omitempty"`

    // SharedMemory 是 central 判断对专精脑有用的记忆摘要。
    SharedMemory string `json:"shared_memory,omitempty"`

    // TokenBudget 是专精脑装配自身上下文时的 token 上限。
    TokenBudget int `json:"token_budget,omitempty"`
}

// TaskGoal 是任务目标的结构化描述。
type TaskGoal struct {
    // Summary 是一句话描述。
    Summary string `json:"summary"`
    // ExpectedOutput 是期望输出的格式/内容描述。
    ExpectedOutput string `json:"expected_output,omitempty"`
    // Constraints 是约束条件列表。
    Constraints []string `json:"constraints,omitempty"`
    // Priority 是任务优先级（影响 token 预算分配）。
    Priority string `json:"priority,omitempty"` // "high" / "normal" / "low"
}

// SubtaskResultSummary 是已完成子任务的结果摘要。
type SubtaskResultSummary struct {
    TaskID     string     `json:"task_id"`
    BrainKind  agent.Kind `json:"brain_kind"`
    Summary    string     `json:"summary"`
    CompletedAt time.Time `json:"completed_at"`
    Status     string     `json:"status"` // "completed" / "partial"
}
```

### 5.4 Central → Specialist 传递什么

```text
central 在 delegate 时传递的上下文由三部分组成：

┌─────────────────────────────────────────────────────────┐
│                   SubtaskContext                         │
│                                                         │
│  ① RelevantMessages（相关历史，≤ 10 条）                │
│     ├── 用户原始指令（必传）                              │
│     ├── central 的规划输出（任务分解部分）                │
│     └── 与本子任务直接相关的先前对话（最多 8 条）         │
│                                                         │
│  ② PriorResults（前置子任务结果摘要）                    │
│     ├── 已完成的同级子任务摘要（各不超过 200 token）      │
│     └── 关键中间产物（如：代码 diff 的 path 列表）        │
│                                                         │
│  ③ SharedMemory（记忆摘要，可选）                        │
│     └── central 的 MemoryStore 中与本任务相关的片段      │
└─────────────────────────────────────────────────────────┘

central 不传递：
- 其他专精脑的完整执行历史
- 用户凭证、API key、policy 配置
- 标记为 private 的消息块
- central 的完整 L3 History（只传相关片段）
```

### 5.5 结果回传与上下文 Merge

```go
// MergeSubtaskResult 将专精 brain 的执行结果 merge 回 central 的上下文。
// 由 Orchestrator 在收到 SubtaskResult 后调用。
func (e *engine) MergeSubtaskResult(
    ctx context.Context,
    centralRunID string,
    result SubtaskResult,
    original SubtaskRequest,
) error {
    // Step 1: 将子任务结果封装为标准 tool_result 消息
    resultMsg := buildSubtaskResultMessage(result, original)

    // Step 2: 提取结果摘要（避免将专精脑完整历史塞回 central）
    summary := extractResultSummary(result, maxTokens: 500)

    // Step 3: 构造里程碑消息（标记为 milestone，保留优先级高）
    milestoneMsg := llm.Message{
        Role: "user",
        Content: []llm.ContentBlock{
            {
                Type: "text",
                Text: fmt.Sprintf(
                    "[SUBTASK_COMPLETE brain=%s task=%s]\n%s\n[/SUBTASK_COMPLETE]",
                    result.TaskID[:8], // 缩短 ID
                    original.TargetKind,
                    summary,
                ),
            },
        },
    }
    markAsMilestone(milestoneMsg)

    // Step 4: 追加到 central 的 History
    return e.historyStore.Append(ctx, centralRunID, []llm.Message{
        resultMsg,
        milestoneMsg,
    })
}

// Merge 策略说明：
// - 不将专精脑的完整对话历史 merge 进 central（避免 token 爆炸）
// - 只 merge 结果摘要（≤ 500 token）
// - 结果消息标记为里程碑，在后续压缩中获得保留优先权
// - 若结果包含文件 artifact（如代码修改），只记录路径和 CAS ref，不含内容
```

### 5.6 隐私边界规则

```go
// PrivacyBoundaryRule 定义跨脑传递的隐私约束。
type PrivacyBoundaryRule struct {
    From       agent.Kind
    To         agent.Kind
    Forbidden  []string // 禁止传递的内容模式（正则）
    AllowedOnly []string // 若非空，则只允许传递匹配的内容
}

// applyPrivacyBoundary 应用隐私边界规则，过滤不可传递的消息块。
func applyPrivacyBoundary(msgs []llm.Message, from, to agent.Kind) []llm.Message {
    // 绝对禁止跨脑传递的内容：
    // 1. private 标记的消息（由生成方明确标注）
    // 2. 包含凭证关键词的文本块（api_key, password, secret, token= 等）
    // 3. policy 元数据（PolicyDecision, ApprovalRecord 等结构）
    // 4. 其他专精脑的完整执行轨迹
    // 5. 用户个人偏好档案的原始数据

    return filterMessages(msgs, func(m llm.Message) bool {
        for _, blk := range m.Content {
            if blk.IsPrivate {
                return false // 过滤掉
            }
            if containsCredentialPattern(blk.Text) {
                return false // 过滤掉
            }
            if blk.Type == "policy_record" || blk.Type == "approval_record" {
                return false // 过滤掉
            }
        }
        return true // 保留
    })
}

// 特定脑对之间的额外约束：
var crossBrainPrivacyRules = []PrivacyBoundaryRule{
    {
        // central → quant：不传递 code brain 的代码执行结果
        From: agent.KindCentral, To: agent.KindQuant,
        Forbidden: []string{`\[SUBTASK_COMPLETE brain=code`},
    },
    {
        // central → browser：不传递 quant 的交易数据
        From: agent.KindCentral, To: agent.KindBrowser,
        Forbidden: []string{`order_id`, `position_size`, `pnl`},
    },
    {
        // central → verifier：只传递待验证的内容，不传递其他任何历史
        From: agent.KindCentral, To: agent.KindVerifier,
        AllowedOnly: []string{`instruction`, `task_goal`, `prior_result.*code`},
    },
}
```

---

## 6. 跨 Turn 记忆持久化

### 6.1 MemoryStore 接口

```go
// MemoryStore 是跨 Turn / 跨 Run 的记忆持久化接口。
// 区别于 L3 History（当前 Run 内的对话历史）：
// MemoryStore 的内容跨越多个 Run，是"长期记忆"。
type MemoryStore interface {
    // Recall 检索与当前任务相关的记忆片段。
    // 返回的消息经过格式化，可直接注入到 Assemble 的消息列表中。
    Recall(ctx context.Context, runID string, brainKind agent.Kind, slots []string) ([]llm.Message, error)

    // RecallForShare 检索可以跨脑共享的记忆摘要。
    RecallForShare(ctx context.Context, from, to agent.Kind) (string, error)

    // Store 存储一条记忆。由 Persist() 在 turn 结束时调用。
    Store(ctx context.Context, entry MemoryEntry) error

    // Evict 执行淘汰策略，删除过期或低价值记忆。
    // 由后台 goroutine 定期调用（推荐每 24h 一次）。
    Evict(ctx context.Context, policy EvictionPolicy) (int, error)

    // List 列出指定 brain 的所有记忆条目（调试 / dashboard 用）。
    List(ctx context.Context, brainKind agent.Kind, limit int) ([]MemoryEntry, error)
}
```

### 6.2 MemoryEntry 存储格式

```go
// MemoryEntry 是单条记忆的存储格式。
type MemoryEntry struct {
    // ── 身份 ────────────────────────────────────────────────
    ID        string     `json:"id"`         // UUID
    BrainKind agent.Kind `json:"brain_kind"` // 产生这条记忆的 brain
    RunID     string     `json:"run_id"`     // 来源 Run ID
    TurnIndex int        `json:"turn_index"` // 来源 Turn 索引

    // ── 内容 ────────────────────────────────────────────────
    // Slot 是记忆的语义槽位，便于按类型检索。
    // 预定义槽位：
    //   "task_outcome"   — 任务执行结果摘要
    //   "user_preference" — 用户偏好（L3 用户级学习输入）
    //   "delegation_score" — 脑委托效果评分（L1 协作级学习输入）
    //   "error_pattern"  — 错误模式（供 fault brain 学习）
    //   "milestone"      — 重要里程碑
    Slot string `json:"slot"`

    // Content 是记忆的文本内容（已提炼，不含原始对话）。
    Content string `json:"content"`

    // Tags 是辅助检索的标签。
    Tags []string `json:"tags,omitempty"`

    // ── 权重 ────────────────────────────────────────────────
    // Importance 是重要性评分（0.0 - 1.0），影响淘汰优先级。
    Importance float64 `json:"importance"`

    // AccessCount 是被 Recall 命中的次数（访问越多越重要）。
    AccessCount int `json:"access_count"`

    // LastAccessAt 是最后一次被 Recall 的时间。
    LastAccessAt time.Time `json:"last_access_at"`

    // ── 时间 ────────────────────────────────────────────────
    CreatedAt time.Time  `json:"created_at"`
    ExpiresAt *time.Time `json:"expires_at,omitempty"` // nil 表示永不过期

    // ── 隐私 ────────────────────────────────────────────────
    // SharePolicy 控制这条记忆能否跨脑共享。
    // "private"  — 只有产生它的 brain 可见
    // "central"  — central 和产生它的 brain 可见
    // "shared"   — 所有 brain 可见（默认）
    SharePolicy string `json:"share_policy"`

    // ── 关联 L0 学习 ─────────────────────────────────────────
    // L0Ref 若非空，表示这条记忆已被该 brain 的 L0 BrainLearner 消费，
    // 关联到对应的学习参数更新记录。
    L0Ref string `json:"l0_ref,omitempty"`
}
```

### 6.3 淘汰策略

```go
// EvictionPolicy 控制记忆淘汰行为。
type EvictionPolicy struct {
    // MaxEntries 是单个 brain 的记忆条目上限。
    // 超出时按 LRU + Importance 加权淘汰。
    MaxEntries int // 默认 1000

    // MaxAge 是记忆的最大存活时间。超过此时间的记忆候选淘汰。
    MaxAge time.Duration // 默认 30 天

    // MinImportance 是保留记忆的最低重要性阈值。
    // 低于此值且超过 MinAge 的记忆强制淘汰。
    MinImportance float64 // 默认 0.1

    // MinAge 是保护新记忆的最小年龄。
    // 年龄小于 MinAge 的记忆不参与淘汰（即使评分低）。
    MinAge time.Duration // 默认 24h

    // PreserveSlots 是永远不淘汰的槽位列表。
    // 例如 ["user_preference"] — 用户偏好记忆永远保留。
    PreserveSlots []string
}

// evictionScore 计算单条记忆的淘汰评分（分越高越先淘汰）。
func evictionScore(e MemoryEntry, now time.Time) float64 {
    age := now.Sub(e.LastAccessAt)

    // 基础淘汰分 = 年龄（天） × (1 - Importance) / (1 + AccessCount)
    ageDays := age.Hours() / 24
    score := ageDays * (1.0 - e.Importance) / float64(1+e.AccessCount)

    return score
}

// Evict 淘汰流程（伪代码）：
// 1. 按 BrainKind 分组
// 2. 对每个 BrainKind：
//    a. 过滤掉 PreserveSlots 和年龄 < MinAge 的记忆（保护）
//    b. 过滤掉有明确 ExpiresAt 且已过期的记忆（强制删除）
//    c. 对剩余记忆计算 evictionScore，按分降序
//    d. 超出 MaxEntries 的部分从高分开始删除
//    e. 低于 MinImportance 且年龄 > MaxAge 的强制删除
```

### 6.4 与 L0 学习的关系

```text
MemoryStore 是 Context Engine 和四层自适应学习体系的桥梁：

  Context Engine
       │
       │ Persist() 写入 task_outcome / error_pattern
       ▼
  MemoryStore
       │
       │ BrainLearner.ExportMetrics() 从记忆提炼学习信号
       ▼
  L0 BrainLearner（各专精脑）
       │
       │ Adapt() 更新 brain 内部参数
       ▼
  L1 Central（delegation_score 槽位）
       │
       │ 汇总各脑评分，更新委托策略
       ▼
  L2 策略库（任务组合效果统计）

关键约束：
- MemoryStore 写入是异步的，不阻塞主流程
- L0 Adapt() 由专精脑自己在空闲时调用，不由 Context Engine 触发
- Context Engine 只负责写入和检索，不负责学习逻辑
```

---

## 7. Token 计数和预算管理

### 7.1 TokenCounter 接口

```go
// TokenCounter 是 token 计数的统一接口。
// 不同 LLM 提供商使用不同 tokenizer，必须通过此接口屏蔽差异。
type TokenCounter interface {
    // Count 统计消息列表的总 token 数。
    Count(msgs []llm.Message, brainKind agent.Kind) int

    // CountMessage 统计单条消息的 token 数。
    CountMessage(m llm.Message, brainKind agent.Kind) int

    // CountString 统计任意字符串的 token 数（用于摘要预估）。
    CountString(s string, model string) int

    // ModelContextWindow 返回指定模型的上下文窗口大小。
    ModelContextWindow(model string) int
}
```

### 7.2 Tokenizer 适配策略

```go
// tokenCounterImpl 是 TokenCounter 的实现。
// 根据 brain 使用的模型选择对应 tokenizer。
type tokenCounterImpl struct {
    // brainModels 记录每个 brain 使用的 LLM 模型 ID。
    // 从 OrchestratorConfig.Brains[].Model 读取。
    brainModels map[agent.Kind]string

    // counters 是模型对应的 tokenizer 实现。
    counters map[string]modelTokenizer
}

// modelTokenizer 是单个模型的 tokenizer 接口。
type modelTokenizer interface {
    Count(text string) int
    ContextWindow() int
}

// 支持的 tokenizer 实现：
//
// anthropicTokenizer — 用于 Claude 系列模型
//   使用 claude_tokens 近似算法：
//   token_count ≈ len(text_utf8_bytes) / 3.5（中文约 1.5 字/token，英文约 4 字/token）
//   精确计数需调用 Anthropic Token Count API（有成本，不适合每次调用）
//   → 策略：使用近似计数 + 10% 安全余量
//
// openaiTokenizer — 用于 OpenAI/DeepSeek 系列模型
//   使用 tiktoken 库（cl100k_base encoding）
//   → 精确计数，成本低
//
// fallbackTokenizer — 通用后备实现
//   token_count ≈ len(text) / 4（简单字符近似）
//   → 最低精度，用于未知模型

// 上下文窗口大小（截至 2026-04-16）：
var modelContextWindows = map[string]int{
    "claude-opus-4-5":     200_000,
    "claude-sonnet-4-5":   200_000,
    "claude-haiku-4-5":    200_000,
    "gpt-4o":              128_000,
    "gpt-4o-mini":         128_000,
    "deepseek-chat":       64_000,
    "deepseek-reasoner":   64_000,
    "hunyuan-turbo":       256_000,
}
```

### 7.3 预算分配策略

```go
// BudgetAllocator 在多脑任务中分配 token 预算。
type BudgetAllocator struct {
    // TotalBudget 是整个多脑任务的 token 总预算。
    TotalBudget int

    // Allocations 记录已分配给各子任务的 token 数。
    Allocations map[string]int // taskID → allocated tokens
    mu          sync.Mutex
}

// Allocate 为新的子任务分配 token 预算。
// 分配策略：按任务优先级和历史消耗动态调整。
func (b *BudgetAllocator) Allocate(taskID string, req SubtaskContext) int {
    b.mu.Lock()
    defer b.mu.Unlock()

    remaining := b.TotalBudget
    for _, used := range b.Allocations {
        remaining -= used
    }

    // 基础分配：剩余预算的 40%（为后续子任务保留空间）
    base := remaining * 40 / 100

    // 按优先级调整
    switch req.TaskGoal.Priority {
    case "high":
        base = remaining * 60 / 100
    case "low":
        base = remaining * 20 / 100
    }

    // 安全下限：至少 2000 token（保证最基本的任务能跑）
    if base < 2000 {
        base = 2000
    }

    // 安全上限：不超过单模型上下文窗口的 75%
    maxSafe := 150_000 // 以 claude 200k 为例，75% = 150k
    if base > maxSafe {
        base = maxSafe
    }

    b.Allocations[taskID] = base
    return base
}

// 实时 token 统计集成到 Agent Loop Runner：
// Runner 在每个 Turn 后更新 Budget.UsedTokens，
// ContextEngine 在 Assemble 时读取 Budget.Remaining().TokensRemaining
// 作为 TokenBudget 的上限，两层约束共同生效。
```

### 7.4 预算告警

```go
// TokenBudgetMonitor 监控 token 消耗，在接近上限时触发告警。
type TokenBudgetMonitor struct {
    budget    int
    used      int
    alertAt   float64 // 消耗到 alertAt 比例时告警（默认 0.8）
    alerted   bool
    onAlert   func(used, budget int)
}

func (m *TokenBudgetMonitor) Update(delta int) {
    m.used += delta
    ratio := float64(m.used) / float64(m.budget)

    if !m.alerted && ratio >= m.alertAt {
        m.alerted = true
        if m.onAlert != nil {
            m.onAlert(m.used, m.budget)
        }
        // 触发主动压缩
    }
}
```

---

## 8. ContextEngine 实现骨架

```go
// engine 是 ContextEngine 的主实现。
// 路径: sdk/contextengine/engine_impl.go
type engine struct {
    // 依赖
    historyStore  HistoryStore  // 读写 L3 History
    memory        MemoryStore   // 读写跨 Turn 记忆
    tokenCounter  TokenCounter  // token 计数
    llmProvider   llm.Provider  // 用于摘要生成
    brainChannel  BrainChannel  // 跨脑消息投递
    metrics       Metrics
    logger        Logger

    // 配置
    defaultPolicy     *CompressPolicy
    brainBudgets      map[agent.Kind]int // 各 brain 的默认 token 预算
    brainWindowSizes  map[agent.Kind]int // 各 brain 的默认窗口大小
}

// NewEngine 创建 ContextEngine 实例。
func NewEngine(opts ...EngineOption) ContextEngine {
    e := &engine{
        defaultPolicy: &CompressPolicy{
            TriggerThreshold: 0.85,
            PreferSummary:    false,
            SummaryMaxTokens: 512,
            WindowSize:       20,
            MilestoneCount:   5,
        },
        brainBudgets: map[agent.Kind]int{
            agent.KindCentral:  170_000, // 200k × 85%
            agent.KindQuant:    150_000, // 200k × 75%
            agent.KindData:     120_000, // 200k × 60%
            agent.KindCode:     160_000, // 200k × 80%
            agent.KindBrowser:  130_000, // 200k × 65%
            agent.KindVerifier: 140_000, // 200k × 70%
            agent.KindFault:    120_000, // 200k × 60%
        },
    }
    for _, o := range opts {
        o(e)
    }
    return e
}

// EngineOption 是构造选项（函数式选项模式）。
type EngineOption func(*engine)

func WithHistoryStore(s HistoryStore) EngineOption {
    return func(e *engine) { e.historyStore = s }
}

func WithMemoryStore(s MemoryStore) EngineOption {
    return func(e *engine) { e.memory = s }
}

func WithLLMProvider(p llm.Provider) EngineOption {
    return func(e *engine) { e.llmProvider = p }
}

func WithTokenCounter(c TokenCounter) EngineOption {
    return func(e *engine) { e.tokenCounter = c }
}

func WithBrainChannel(ch BrainChannel) EngineOption {
    return func(e *engine) { e.brainChannel = ch }
}
```

---

## 9. 与现有代码的集成点

### 9.1 与 Agent Loop Runner 的集成

```go
// Runner 在 buildChatRequest() 时调用 ContextEngine.Assemble()
// 替代当前的直接 L3 History 读取。

// 当前（修改前）：
func (r *Runner) buildChatRequest(turn int) llm.ChatRequest {
    return llm.ChatRequest{
        Messages: r.historyMessages, // 直接使用原始历史
        // ...
    }
}

// 修改后：
func (r *Runner) buildChatRequest(ctx context.Context, turn int) (llm.ChatRequest, error) {
    msgs, err := r.contextEngine.Assemble(ctx, contextengine.AssembleRequest{
        RunID:              r.runID,
        BrainKind:          r.brainKind,
        TurnIndex:          turn,
        TaskType:           r.currentTaskType,
        TokenBudget:        r.budget.Remaining().TokensRemaining,
        IncludeMemory:      true,
        MaxHistoryMessages: brainMaxHistoryMessages[r.brainKind],
    })
    if err != nil {
        return llm.ChatRequest{}, err
    }

    return llm.ChatRequest{
        Messages: msgs,
        // 其余字段不变...
    }, nil
}
```

### 9.2 与 Orchestrator.Delegate() 的集成

```go
// Orchestrator 在 delegate 子任务时，通过 ContextEngine 构建上下文。

func (o *Orchestrator) Delegate(ctx context.Context, req SubtaskRequest) (*SubtaskResult, error) {
    // 新增：构建结构化上下文
    if req.ContextV2 == nil && o.contextEngine != nil {
        ctxMsgs, err := o.contextEngine.Assemble(ctx, contextengine.AssembleRequest{
            RunID:          req.TaskID,
            BrainKind:      agent.KindCentral,
            TaskType:       contextengine.TaskTypeDelegation,
            Instruction:    req.Instruction,
            ExcludePrivate: true, // 跨脑必须过滤私有内容
            MaxHistoryMessages: 10,
        })
        if err == nil {
            req.ContextV2 = &SubtaskContext{
                ParentRunID:      o.centralRunID,
                RelevantMessages: ctxMsgs,
                TaskGoal: SubtaskGoal{
                    Summary: req.Instruction,
                },
            }
        }
    }

    // 投递给专精脑（现有逻辑不变）
    return o.sendToSpecialist(ctx, req)
}
```

### 9.3 与 SubtaskResult Merge 的集成

```go
// Orchestrator 收到专精脑结果后，调用 MergeSubtaskResult
func (o *Orchestrator) handleSubtaskResult(result SubtaskResult, original SubtaskRequest) {
    if o.contextEngine != nil {
        if err := o.contextEngine.MergeSubtaskResult(
            context.Background(),
            o.centralRunID,
            result,
            original,
        ); err != nil {
            o.logger.Warn("merge subtask result failed", "error", err)
        }
    }
}
```

---

## 10. 存储路径约定

```text
~/.brain/
├── memory/
│   ├── central/
│   │   ├── entries.jsonl          # MemoryEntry 追加写，JSONL 格式
│   │   └── index.json             # 槽位索引（加速检索）
│   ├── quant/
│   │   └── entries.jsonl
│   ├── data/
│   │   └── entries.jsonl
│   └── ...（其他 brain）
├── history/
│   ├── {runID}/
│   │   └── messages.jsonl         # L3 History 持久化
│   └── ...
└── context-engine.yaml            # Context Engine 全局配置
```

### 10.1 context-engine.yaml 配置示例

```yaml
# Context Engine 全局配置
# 路径: ~/.brain/context-engine.yaml

# 默认压缩策略
compress:
  trigger_threshold: 0.85      # token 超预算 85% 时触发
  prefer_summary: false        # 默认用窗口裁剪（低延迟）
  summary_max_tokens: 512      # 摘要最大长度
  window_size: 20              # 窗口保留最近 N 条
  milestone_count: 5           # 额外保留的里程碑数
  summary_model: ""            # 空表示使用当前 brain 的模型

# 长时任务的定期压缩（适用于 data/quant daemon）
periodic_compress:
  enabled: true
  every_n_turns: 30           # 每 30 个 turn 主动执行一次摘要

# 记忆淘汰策略
memory_eviction:
  max_entries_per_brain: 1000
  max_age_days: 30
  min_importance: 0.1
  min_age_hours: 24
  preserve_slots:
    - user_preference
    - delegation_score

# Token 预算（占模型上下文窗口的比例）
token_budget_ratio:
  central: 0.85
  quant: 0.75
  data: 0.60
  code: 0.80
  browser: 0.65
  verifier: 0.70
  fault: 0.60

# 跨脑共享限制
cross_brain_share:
  max_messages: 20          # 最多共享 20 条消息
  include_memory: false     # 默认不包含记忆摘要（按需开启）
```

---

## 11. 可观测性指标

| 指标名 | 类型 | 说明 |
|--------|------|------|
| `context.assemble_duration_ms` | Histogram | Assemble() 耗时 |
| `context.compress_triggered` | Counter | 压缩触发次数 |
| `context.compress_ratio` | Histogram | 压缩前后 token 比例 |
| `context.summary_fallback` | Counter | 摘要失败降级为窗口裁剪次数 |
| `context.greedy_compress_triggered` | Counter | 贪心兜底压缩触发次数 |
| `context.share_messages` | Histogram | 每次 Share() 传递的消息数 |
| `context.privacy_filtered` | Counter | 隐私过滤移除的消息块数 |
| `context.memory_recall_hit` | Counter | Recall() 命中次数 |
| `context.memory_entries_per_brain` | Gauge | 各 brain 记忆条目数 |
| `context.memory_evicted` | Counter | 淘汰的记忆条目数 |
| `context.token_budget_used_ratio` | Histogram | 实际消耗 / 预算比例（按 brain） |

---

## 12. 实施优先级

| 编号 | 工作项 | 优先级 | 依赖 | 预估工作量 |
|------|--------|--------|------|-----------|
| CE-1 | AssembleRequest 数据模型 + ContextEngine 接口定义 | P0 | 无 | 0.5 天 |
| CE-2 | TokenCounter 实现（近似 + tiktoken 两路） | P0 | CE-1 | 1 天 |
| CE-3 | 窗口裁剪压缩（无 LLM 依赖） | P0 | CE-1, CE-2 | 1 天 |
| CE-4 | HistoryStore 接口 + 文件实现 | P0 | CE-1 | 1 天 |
| CE-5 | Assemble() 主流水线接通 Runner | P0 | CE-1~4 | 1.5 天 |
| CE-6 | SubtaskContext 扩展 + Orchestrator 集成 | P1 | CE-5 | 1 天 |
| CE-7 | MergeSubtaskResult + 里程碑标记 | P1 | CE-6 | 1 天 |
| CE-8 | MemoryStore 接口 + JSONL 实现 | P1 | CE-4 | 1.5 天 |
| CE-9 | LLM 摘要压缩 | P2 | CE-5, CE-8 | 1.5 天 |
| CE-10 | 隐私边界过滤 + 跨脑 Share() | P2 | CE-6, CE-8 | 1 天 |
| CE-11 | PeriodicCompressor + 长时任务集成 | P2 | CE-9 | 0.5 天 |
| CE-12 | 贪心预算约束压缩（兜底） | P3 | CE-3, CE-5 | 0.5 天 |
| CE-13 | BudgetAllocator（多脑任务 token 分配） | P3 | CE-2 | 1 天 |
| CE-14 | EvictionPolicy + 后台淘汰 goroutine | P3 | CE-8 | 1 天 |

**P0（接通主流程，约 5 天）→ P1（跨脑协作，约 3.5 天）→ P2（记忆+摘要，约 4 天）→ P3（高级功能，约 2.5 天）**
