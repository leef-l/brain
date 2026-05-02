# 35. Context Engine 详细设计

> **版本**: v2.0(全量对齐代码)
> **更新日期**: 2026-05-02
> **实现位置**:
> - `sdk/kernel/context_engine.go` — 默认 ContextEngine + Compress 三层策略 + Share 桶
> - `sdk/kernel/context_engine_memory.go` — 项目记忆增强包装(MACCS 2.5)
> - `sdk/kernel/memory_retrieval.go` — 4 维加权检索(MACCS 2.2)
> - `sdk/persistence/` — SharedMessageStore + ProjectStore 持久化

---

## 0. 设计目标

Context Engine 在 LLM 调用前装配上下文,负责四件事:

1. **Assemble**:根据 brain 类型 / 任务类型 / token 预算装配消息列表
2. **Compress**:超预算时分层压缩(窗口裁剪 → 旧消息截断 → LLM 摘要 → 硬截断兜底)
3. **Share**:跨脑上下文传递(隐私过滤 + 数量/token 限制 + 按 (from,to) 分桶)
4. **Memory 注入**(MACCS 2.5 增强):自动加载项目历史记忆,前置注入

---

## 1. 接口(`context_engine.go:48-62`)

```go
type ContextEngine interface {
    Assemble(ctx, req AssembleRequest) ([]llm.Message, error)
    Compress(ctx, messages, budget) ([]llm.Message, error)
    Share(ctx, from, to, messages) error
}
```

### 1.1 AssembleRequest(`line 30-45`)

```go
type AssembleRequest struct {
    RunID       string
    BrainKind   agent.Kind
    TaskType    string         // "analysis" / "execution" 等
    Messages    []llm.Message  // L3 History
    TokenBudget int            // 0 = 不限制
    ProjectID   string         // 非空 → ContextEngine 自动加载项目历史(MACCS 2.5)
}
```

---

## 2. DefaultContextEngine(`context_engine.go:69`)

```go
type DefaultContextEngine struct {
    Summarizer       llm.Provider     // 可选,Compress 阶段 2.5 LLM 摘要
    SummaryModel     string           // 默认 "claude-haiku-4-5-20251001"
    SharedStore      persistence.SharedMessageStore  // 可选,Share 异步持久化
    ProjectStore     persistence.ProjectStore        // 可选,Assemble 自动加载历史
    MaxShareMessages int              // 默认 30
    ShareTokenBudget int              // 默认 8000
    sharedBuckets    map[sharedKey][]llm.Message  // 按 (from,to) 分桶
    SharedMessages   []llm.Message    // Deprecated:仅最近一次 Share 的消息(legacy 测试)
}
```

### 2.1 构造函数

| 函数 | 用途 |
|------|------|
| `NewDefaultContextEngine()` | 不带 LLM 摘要的纯本地实现 |
| `NewContextEngineWithLLM(provider, model)` | 注入 Summarizer 启用 Compress 2.5 |

### 2.2 Token 估算(`line 128`)

```go
estimateTokens(messages) = Σ(len(role)/4 + len(text)/4 + len(toolName)/4 + len(input)/4 + len(output)/4)
                          ≥ len(messages)
```

> **粗略法**:4 字符 ≈ 1 token,不依赖外部 tokenizer,不需调 LLM API,所有 brain 内部估算用同一个公式保证一致性。

---

## 3. Compress 三层(实际四层)压缩策略(`line 207`)

```
budget = TokenBudget - estimateTokens(systemMessages)

策略 1:窗口裁剪 windowTrim
  从尾部往前累加,保留最新一批 ≤ budget 的消息
  combined = systemMsgs + result
  if combined ≤ budget: 返回

策略 2:截断旧消息内容 truncateOldMessages
  对最老的非 system 消息做内容截断:
    text/thinking 块 > 200 字符 → 前 200 字符 + "[...已截断]"
  逐条截断直到 total ≤ budget

策略 2.5:LLM 摘要 summarizeMessages(仅当 Summarizer 非 nil)
  保留最新 2 条原样,前面所有消息文本拼接
  prompt = "请将以下对话历史压缩为一段简洁的摘要,保留关键信息、决策和结论。
            用中文输出,不超过 500 字。\n\n" + 拼接文本
  调 e.Summarizer.Complete(SummaryModel, MaxTokens=1024)
  返回 [system + summary + recent2]

策略 3:硬截断 hardTruncate(兜底)
  从尾部往前,每条消息硬截到 100 字符
  累加直到 ≤ budget
```

### 3.1 system 消息保护

`isSystemMessage(m) = m.Role == "system"`。**所有 system 消息永远不被压缩**,作为"装配契约"前置在最前。预算不够 system 时直接只返回 system,这是认了。

---

## 4. Share — 跨脑上下文传递(`line 437`)

### 4.1 sharedKey 桶设计

```go
type sharedKey struct {
    from agent.Kind
    to   agent.Kind
}

sharedBuckets map[sharedKey][]llm.Message
```

> **历史 Bug**(Task #18):原本只有一个 `SharedMessages []` 全局字段,多 delegate 并发时被后写覆盖,产生跨脑上下文串。修复:按 (from, to) 分桶,`SharedMessages` 保留为 Deprecated legacy 字段。

### 4.2 Share 流程

```
1. 隐私过滤:
   credentialPattern = /(api_key|password|secret|token\s*=|private_key|credential)/i
   命中 Text/Input/Output 任一字段就丢弃整条消息
2. 数量限制:filtered[max(0, len-MaxShareMessages):]
3. token 预算:estimateTokens > ShareTokenBudget → windowTrim
4. 写入桶:sharedBuckets[(from, to)] = filtered
5. 异步持久化(可选):SharedStore.Save(JSON 序列化的 messages)
```

### 4.3 SharedFor / ClearShared

| 方法 | 用途 |
|------|------|
| `SharedFor(from, to)` | 取桶消息拷贝(线程安全) |
| `ClearShared(from, to)` | 切断桶(Delegate 结束 SHOULD 调) |
| `ClearShared("", "")` | 清空全部桶(engine 重置时使用) |

> **必须遵守**:Orchestrator.Delegate 在 subtask 完成后必须调 `ClearShared(from, to)`,**否则下一次 delegate 会继承前一次的 shared 消息**(Task #18 描述的串扰风险)。

---

## 5. ContextEngineWithMemory — MACCS 2.5 项目级记忆增强(`context_engine_memory.go`)

```go
type ContextEngineWithMemory struct {
    engine    *DefaultContextEngine
    memory    ProjectMemory
    retriever *MemoryRetriever
}

const memoryTokenCap   = 2000
const memoryTokenRatio = 0.15
```

### 5.1 Assemble 流程(`line 49`)

```
if req.ProjectID != "" && memory != nil:
    maxTokens = min(TokenBudget * 0.15, 2000)
    summary, err := memory.Summarize(ctx, ProjectID, maxTokens)
    if summary != "":
        memoryMsg := system{ Text: "[项目记忆] " + summary }
        req.Messages = [memoryMsg] + req.Messages
return engine.Assemble(ctx, req)
```

> **Token 预算**:记忆摘要至多占 TokenBudget 的 15%,上限 2000 token,避免吃掉过多窗口。

### 5.2 接入路径

`PlanOrchestrator` 启动时会**包装** Orchestrator 的 ContextEngine:

```go
ce := NewContextEngineWithMemory(defaultCE, projectMemory)
orch.SetContextEngine(ce)
```

之后所有 `Orchestrator.delegateOnce` 间接调 `ce.Assemble`,自动注入记忆。

### 5.3 持久化版 ProjectMemory(MACCS Wave 7+)

默认 `ProjectMemory` 是 `MemProjectMemory`(内存,重启丢)。多项目持久化场景下用 `kernel.NewPersistentProjectMemory(store)`,把 `persistence.ProjectMemoryStore` 包装为 `kernel.ProjectMemory`:

```go
// chat 装配示例(repl.go):
if state.ProjectMemoryStore != nil {
    persistentMem := kernel.NewPersistentProjectMemory(state.ProjectMemoryStore)
    ce := kernel.NewContextEngineWithMemory(defaultCE, persistentMem)
    orch.SetContextEngine(ce)
}
```

这样 lessons / decisions / patterns 等记忆条目跨会话保留,Wave 5.4 PatternExtractor 写入的 pattern 下次启动同项目仍能被 Retriever 检索到。详见 `35-项目级记忆与多项目管理.md`。

---

## 6. MemoryRetriever — 4 维加权检索(`memory_retrieval.go`)

### 6.1 默认权重(`line 33`)

| 维度 | 权重 | 计算方式 |
|------|-----|---------|
| `KeywordWeight` | **0.4** | query 词在 Content+Summary 中命中比例 |
| `TagWeight` | **0.2** | query tags 与 entry tags 的 Jaccard 相似度 |
| `RecencyWeight` | **0.2** | exp(-ln2 × elapsed / halfLife) 时间指数衰减 |
| `ImportanceWeight` | **0.2** | entry.Importance(已归一化 0-1) |
| `DecayHalfLife` | 7 days | 时间衰减半衰期 |

### 6.2 Retrieve 算法(`line 54`)

```
keywords = query.split(" ").lower().filter(non-empty)

for each entry in entries:
  kw  = keywordScore(entry.Content + " " + entry.Summary, keywords)
  tg  = tagJaccardScore(entry.Tags, queryTags)
  rc  = recencyScore(entry.CreatedAt, halfLife)
  imp = entry.Importance
  score = kw*0.4 + tg*0.2 + rc*0.2 + imp*0.2
  matchType = max(kw, tg, rc) 中贡献最高的维度

按 score 降序排序,取 top-N
```

### 6.3 接入路径

`PlanOrchestrator.ExecuteProject` 在 reflection 后调 `MemoryRetriever.Retrieve`,top-N 摘要追加到 `reflection.Recommendations`,形成跨 plan 的反馈闭环。

---

## 7. 持久化(`sdk/persistence/`)

| 接口 | 用途 |
|------|------|
| `SharedMessageStore.Save(*SharedMessage)` | Share 异步持久化(SQLite shared_messages 表) |
| `ProjectStore.LoadMessages(projectID, limit)` | Assemble 加载项目历史 |
| `ProjectStore.AppendMessage(...)` | 项目过程中追加消息 |

> SharedMessage 失败 silent(stderr 打印),不阻塞主流程。

---

## 8. 与 MACCS 各 Wave 的关联

| 任务 | 触点 |
|------|------|
| **2.2 记忆检索** | `MemoryRetriever` + `PlanOrchestrator.Recommendations` |
| **2.5 Context Engine 增强** | `ContextEngineWithMemory` 包装注入 |
| **5.4 PatternExtractor** | 通过 `ProjectMemory.Store` 写入 pattern entries,被本 Retriever 读到 |
| 0139b5e Lessons 反馈 | `PlanOrchestrator` 把 reflection lesson 写入 ProjectMemory(阈值 0.3),下轮被 Retriever 读到 |

---

## 9. 关键事实速查

- **Token 估算**:4 字符 ≈ 1 token(`estimateTokens`),不依赖外部 tokenizer
- **Compress 默认模型**:`claude-haiku-4-5-20251001`(快速 + 低成本)
- **Share 默认上限**:30 条 / 8000 token
- **记忆 token 占比**:TokenBudget 的 15%,硬上限 2000
- **桶设计**:按 (from, to) 分,防止多 delegate 串扰

---

## 10. 引用代码位置速查

```
sdk/kernel/context_engine.go           # 561 行,主体
sdk/kernel/context_engine_memory.go    # 115 行,MACCS 2.5
sdk/kernel/memory_retrieval.go         # ~240 行,4 维加权检索
sdk/kernel/project_memory.go           # ProjectMemory 接口与默认实现
sdk/kernel/orchestrator.go:192         # ContextEngine getter
sdk/kernel/orchestrator.go:199         # SetContextEngine setter
sdk/persistence/shared_message_store.go # SQLite shared_messages 表
sdk/persistence/project_store.go       # SQLite project_messages 表
```

## 11. 相关文档

- 上位:`32-v3-Brain架构.md` §6 Context Engine
- 平级:`35-自适应学习L1-L3算法设计.md`(MemoryRetriever 是学习层与 Engine 的接口)
- 上层:`docs/MACCS-架构总纲-v2.md` §3 中央大脑长上下文记忆
- 上层:`docs/MACCS-中央大脑智能化编排规范.md`
