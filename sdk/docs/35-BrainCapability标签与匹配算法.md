# 35. Brain Capability 标签体系与匹配算法

> **版本**: v2.0(全量对齐 capability.go + orchestrator.go:1591 路由公式)
> **更新日期**: 2026-05-02
> **实现位置**:
> - `sdk/kernel/capability.go` — 标签解析 + Index + Matcher(461 行)
> - `sdk/kernel/orchestrator.go:1591` — `resolveTargetKind` 路由决策(MACCS 5.1 + Wave 7)
> - `sdk/kernel/model_router.go` — 多模型差异化路由(MACCS 2.6)
> - `sdk/kernel/learning.go` — RankBrains L1 排名

---

## 0. 设计目标

Brain Capability 系统解决三个问题:

1. **标识**:每个 brain 用结构化标签声明它"能做什么"——便于自动匹配
2. **匹配**:任务声明 Required + Preferred 标签 → 选出能做该任务的 brain
3. **路由**:多个能做的 brain 中,综合考虑能力匹配、历史成绩、因果证据,选最优
4. **模型差异化**(MACCS 2.6):不同 brain 用最适合自己领域的 LLM 模型

---

## 1. CapabilityTag 标签结构(`capability.go:26`)

```go
type CapabilityTag struct {
    Raw      string // "function.write_file.v2"
    Category string // "function" | "domain" | "resource" | "mode"
    Primary  string // "write_file"
    Sub      string // ""
    Version  string // "v2"(可选)
}
```

### 1.1 四类标签前缀

| Category | 用途 | 示例 |
|----------|-----|------|
| `function` | brain 能做什么操作(默认无前缀) | `trading.execute`、`web.crawl` |
| `domain` | 覆盖什么领域 | `domain.web`、`domain.trading` |
| `resource` | 能访问什么资源 | `resource.filesystem`、`resource.browser` |
| `mode` | 支持什么运行模式 | `mode.stream`、`mode.batch` |

### 1.2 ParseCapabilityTag 算法(`line 49`)

```
parts = raw.split(".")
last = parts[-1]
if isVersionString(last):  # 形如 "v2", "v1.3"
    Version = last
    parts = parts[:-1]

first = parts[0]
if first in {domain, resource, mode}:
    Category = first
    Primary = parts[1]
    Sub = ".".join(parts[2:])
else:
    Category = "function"
    Primary = parts[0]
    Sub = parts[1] if len(parts)>1 else ""
```

### 1.3 isVersionString(`line 84`)

`v?[0-9]+(\.[0-9]+)*` — 第一字符 `v`/`V`,其余仅数字和点。

---

## 2. CapabilityIndex(`capability.go:116`)

```go
type CapabilityIndex struct {
    brains   map[Kind]*BrainCapabilitySet  // brain → 该 brain 全部标签
    tagIndex map[string]map[Kind]struct{}  // 完整标签 → 拥有该标签的 brain 集
    mu       sync.RWMutex
}
```

### 2.1 公开方法

| 方法 | 用途 |
|------|------|
| `AddBrain(kind, capabilities)` | 注册 brain 与全部能力 |
| `RemoveBrain(kind)` | 移除该 brain 的全部索引 |
| `FindByTag(tag)` | 精确匹配,返回拥有该标签的 brain 列表 |
| `FindByPrefix(prefix)` | 前缀匹配,返回拥有任一匹配标签的 brain |
| `FindByCategory(category)` | 按类别(function/domain/resource/mode)查 |
| `AllBrains()` | 已注册全部 brain |
| `BrainCapabilities(kind)` | 返回该 brain 全部能力字符串 |

---

## 3. CapabilityMatcher 三阶段匹配(`capability.go:291`)

```go
type MatchRequest struct {
    Required  []string  // 硬匹配:候选必须全部具备
    Preferred []string  // 软匹配:有更好,没有也行
}

type MatchResult struct {
    BrainKind     Kind
    HardScore     float64  // 0 或 1
    SoftScore     float64  // [0, 1] = 命中 Preferred 的比例
    CombinedScore float64  // HardScore*0.6 + SoftScore*0.4
}
```

### 3.1 Match 算法(`line 315`)

```
for kind, capSet in index.brains:
    tagSet = {tag.Raw for tag in capSet}

    # 阶段 1:硬匹配
    if any req in Required not satisfied (用 hasCapability):
        skip
    HardScore = 1.0

    # 阶段 2:软匹配
    if Preferred:
        SoftScore = (Preferred 命中数) / len(Preferred)
    else:
        SoftScore = 0

    # 阶段 3:综合
    Combined = HardScore*0.6 + SoftScore*0.4

    results.append({kind, HardScore, SoftScore, Combined})

按 Combined 降序排序,同分按 Kind 字母序稳定排序
```

### 3.2 hasCapability 版本语义(`line 381`)

```
精确匹配:tagSet 含 req → true
请求无版本:匹配同 (Category, Primary, Sub) 任意版本 → true
请求有版本(reqVer):
    匹配同 (Category, Primary, Sub):
        - brain 无版本声明 → 默认最新,匹配
        - brain 版本 ≥ reqVer → 匹配(向前兼容)
    其他不匹配
```

> **版本兼容**:`function.write_file.v2` 请求会匹配声明 `v2/v3/v4` 的 brain,但不匹配 `v1`。

---

## 4. 路由决策 — Orchestrator.resolveTargetKind(`orchestrator.go:1591`)

这是 brain v3 选择执行 brain 的**核心决策点**——超越纯 capability 匹配,加入历史学习与因果证据。

### 4.1 完整流程

```
1. capMatcher.Match(req.RequiredCaps, req.PreferredCaps) → candidates 列表
2. if len(candidates) == 0: 返回 ""(无匹配)
3. if len(candidates) == 1 || learner == nil: 直接取 candidates[0].BrainKind
4. 主动学习探索(epsilon=5%):
     如果命中 → 返回 ActiveLearner pending 中的 brain(优先采样高不确定 brain)
5. 三因子加权:
     learnRanking = learner.RankBrains(taskType)
     for c in candidates:
       capScore    = c.CombinedScore        # 来自第 3 节
       learnScore  = learnRanking[c.BrainKind]
       causalScore = brainCausalScore(causal, c.BrainKind)  # MACCS 5.1
       combined    = capScore * 0.4
                  + learnScore * 0.25
                  + causalScore * 0.35
     选 combined 最高的 brain
```

### 4.2 加权公式演进史

| 版本 | capScore | learnScore | causalScore | 合计 |
|------|---------|-----------|------------|-----|
| v1(初始) | 1.0 | 0 | 0 | 1.0 |
| v2.0(2026-04-26) | 0.5 | 0.3 | 0.2 | 1.0 |
| v2.4(2026-05-02 0139b5e) | **0.4** | **0.25** | **0.35** | 1.0 |

**0139b5e 调整理由**:原 0.2 因果权重太弱被表观相关性淹没。0.35 让因果信号在评分接近时主导路由,**剔除"高成功率是因为该 brain 历史接的任务都简单"这类混杂相关**。

### 4.3 brainCausalScore(`orchestrator.go:1709`)

```
rels = causal.QueryEffect("brain", kind)  # 返回 effect="success" 的关系
if rels 空: 0

raw = max(strength × confidence) for r in rels where r.Effect=="success"
      direction=="negative" → 视为负向证据,raw *= -1
raw 截断到 [-1, 1]

线性映射 [-1, 1] → [0, 1]
```

### 4.4 主动学习探索 exploreCandidate(`orchestrator.go:1660`)

```
const activeExplorationEpsilon = 0.05  # 5%

# 用 randRead 拿 8 字节随机 → 对 1000 取模 < 50 触发
if rand_sample < epsilon:
    pending = active.GetPendingRequests()
    for req in pending:
        if req.BrainKind in candidates:
            return req.BrainKind  # 探索:派给高不确定 brain

return ""  # 不探索,走正常加权
```

> **为什么 5%**:刻意低值,不影响主线任务质量。100 次决策中只有 5 次走探索,既能采集高不确定数据,又不破坏稳定性。

---

## 5. ModelRouter 多模型差异化(MACCS 2.6,`model_router.go`)

### 5.1 设计目标

不同 brain 适合不同模型:

| BrainKind | 推荐模型 | 理由 |
|-----------|---------|------|
| `central` | 超长上下文模型 | 项目级记忆 + 全局编排,需要 200K+ 窗口 |
| `code` | 代码强化模型(Sonnet) | 代码理解与生成 |
| `data` | 快速模型(Haiku) | 数据处理量大,延迟敏感 |
| `verifier` | 推理强模型 | 审核需要严密推理 |
| `browser` | 视觉模型 | 截图理解 + UI 操作 |

### 5.2 接口

```go
type ModelRouter interface {
    Resolve(brainKind, taskType) string  // 返回模型 ID
    SyncToLLMProxy(proxy *LLMProxy)
    SetMapping(brainKind, model)
}

NewModelRouter(strategy)              // strategy: Static / Dynamic
StrategyStatic                         // 按配置静态映射
StrategyDynamic                        // 按学习数据动态调整
```

### 5.3 接入路径

```go
// PlanOrchestrator 构造时:
router := NewModelRouter(StrategyStatic)
router.SyncToLLMProxy(proxy)  // 把映射推到 LLMProxy

// ExecuteProject 内:
router.Resolve(kind, taskType) → 写 diaglog
```

> **静态默认 + 学习增强**:配置先注入静态映射,运行期学习数据足够时按 RankBrains 动态调整。

---

## 6. brain.json Manifest 中的 capability 声明

每个 brain 的 `brain.json` 顶级 `capabilities` 字段:

```json
{
  "kind": "code",
  "capabilities": [
    "function.read_file",
    "function.write_file.v2",
    "function.shell_exec",
    "domain.code",
    "domain.test",
    "resource.filesystem",
    "mode.stream"
  ]
}
```

CapabilityIndex.AddBrain 启动时从 `brain.json` 读入。

---

## 7. 全链路示例

```
请求: DelegateRequest{
  RequiredCaps: ["function.write_file"],
  PreferredCaps: ["domain.code", "mode.stream"]
}

[阶段 1: capMatcher.Match]
  candidates 检查 8 个 brain 的 tag set:
    code      ✓ 必须有 ✓ 软命中 2/2 → SoftScore=1.0, Combined=1.0
    browser   ✗ 没有 write_file → 跳过
    data      ✓ 必须有 ✓ 软命中 0/2 → SoftScore=0.0, Combined=0.6
  → candidates = [code(1.0), data(0.6)]

[阶段 2: resolveTargetKind 路由]
  exploreCandidate (5% 概率走) → 此次未触发
  RankBrains(taskType="write") → {code: 0.85, data: 0.42}
  brainCausalScore(causal, "code") = 0.7  # 因果证据强
  brainCausalScore(causal, "data") = 0.3

  combined for code = 1.0*0.4 + 0.85*0.25 + 0.7*0.35 = 0.857
  combined for data = 0.6*0.4 + 0.42*0.25 + 0.3*0.35 = 0.450

  → 派给 code

[阶段 3: ModelRouter.Resolve]
  router.Resolve("code", "write") = "claude-sonnet-4-6"
  → llmProxy 用此模型调 LLM
```

---

## 8. 关键事实速查

- **三阶段匹配**:Hard(0.6) + Soft(0.4) → CombinedScore
- **路由公式(0139b5e)**:`combined = cap*0.4 + learn*0.25 + causal*0.35`
- **active 探索率**:5%(`activeExplorationEpsilon = 0.05`)
- **版本兼容**:同 (Cat,Pri,Sub) 下,brain 版本 ≥ 请求版本即匹配
- **8 个内置 brain**:browser, code, data, desktop, easymvp, fault, quant, verifier

---

## 9. 引用代码位置速查

```
sdk/kernel/capability.go             # 461 行,标签 + Index + Matcher
sdk/kernel/orchestrator.go:1591      # resolveTargetKind 路由
sdk/kernel/orchestrator.go:1660      # exploreCandidate 5% 探索
sdk/kernel/orchestrator.go:1709      # brainCausalScore 因果分计算
sdk/kernel/model_router.go           # MACCS 2.6 模型路由
sdk/kernel/learning.go               # L1 RankBrains
brains/<kind>/brain.json             # capabilities 声明
```

## 10. 相关文档

- 上位:`32-v3-Brain架构.md` §3.6 能力标签
- 平级:`33-Brain-Manifest规格.md`(brain.json 字段)
- 平级:`35-BrainPool实现设计.md`(brain 进程管理)
- 平级:`35-自适应学习L1-L3算法设计.md`(L1 RankBrains + MACCS 5.1 因果)
- 平级:`35-Dispatch-Policy-冲突图与Batch分组算法.md`(冲突解决)
- 平级:`29-第三方专精大脑开发.md`(第三方 brain 接入)
- 上层:`docs/MACCS-架构总纲-v2.md` §3 中央大脑战略编排
