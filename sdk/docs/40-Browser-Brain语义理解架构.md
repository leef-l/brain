# 40. Browser Brain 语义理解架构

> **这是 Browser Brain 长期架构的核心文档**。
>
> [`39-Browser-Brain感知与嗅探增强设计`](./39-Browser-Brain感知与嗅探增强设计.md) 定义的 `snapshot / network / sitemap` 是**基础设施层**,本文档定义**语义理解层**——让 Agent 达到"人类那种能理解各个按钮意思"的能力。
>
> 实验先行,评估方案见 [`41-语义理解阶段0实验设计`](./41-语义理解阶段0实验设计.md)。

---

## 1. 为什么要写这份文档

### 1.1 触发原因

在用户审阅 39 号文档后,直言:

> **"我要做的感知是更强大的那种,我要它实现人类那种能理解各个按钮等等的意思。"**

深度思考后发现,39 号规划在做**丰富描述**(句法层),但用户要的是**建立理解**(语用层)。这两件事本质不同。

### 1.2 结构感知 ≠ 人类理解

39 号规划的 `browser.snapshot` 返回:
```
[17] button "登录" x=400 y=520
[18] textbox "邮箱" value="" focused
```

LLM 知道"这是登录按钮",但**以下问题它全答不上**:
- 登录失败 3 次会不会锁号?(人类凭经验知道可能会,会放慢节奏)
- "忘记密码"点了会不会发一封邮件导致对方账号告警?
- 当前这个站的错误提示是"用户名或密码错误"还是"密码错误"?(后者暴露用户名存在,是 enumeration 漏洞)

人类看到按钮时,这些判断**在看到的瞬间**就部分发生了。它们不来自 DOM,来自**对"网页这种东西整体怎么运作"的世界模型**。

### 1.3 人类理解的 9 层模型

| 层级 | 含义 | 39 号覆盖度 |
|---|---|---|
| L1 | 视觉呈现(这里有个东西) | ✓ 完全 |
| L2 | 符号语义(它叫 Submit) | ✓ 完全 |
| L3 | 交互语法(它是可点击的 button) | ✓ 完全 |
| L4 | 流程语义(点它是为了提交表单) | ⚠ 靠 LLM 脑补 |
| L5 | 状态语义(点它会产生什么 HTTP 请求) | ⚠ 事后能拼凑,不能预测 |
| L6 | 后果语义(这个动作是否可逆) | ✗ 完全空白 |
| L7 | 风险语义(这个动作是否危险) | ✗ 完全空白 |
| L8 | 意图对齐(当前任务下该不该点) | ✗ 完全空白 |
| L9 | 世界模型(网页/互联网/人机协议常识) | ✗ 完全空白 |

**39 号干净地做完了 L1-L3,触碰 L4,L5 部分覆盖,L6-L9 一片空白**。

**用户问的是 L6-L8 的事情**。

---

## 2. 核心设计哲学

### 2.1 三条底层原则

1. **理解 > 描述**:不追求把 DOM 描述得更细,追求让 Agent 知道"点它会发生什么"、"点它安不安全"、"点它合不合我现在的目标"。

2. **学习 > 硬编码**:不写无穷多的启发式规则判断"这是危险按钮",而是让项目已有的 L1-L3 学习层**从成功/失败的交互中提炼模式**,越用越强。

3. **封闭场景先行**:承认"全场景人类级理解"在 2-3 年内做不到,但**在登录/电商/搜索/后台管理这类高频封闭场景,能达到熟练工人级**。先把这些做到顶,再谈通用。

### 2.2 反面模式(明确不做)

- ❌ 不搞"感知带宽竞赛"——snapshot 返回越细不等于 Agent 越聪明
- ❌ 不靠截图 + 大模型端到端兜底做主路径(成本/延迟/战略被动)
- ❌ 不在每个动作前都插一次 LLM 预测("先思考再点"成本太高)
- ❌ 不追求一份文档规划三年路线——分阶段验证,每阶段回头调整

---

## 3. 四阶段演进路线

### 3.1 阶段 0:实验先行(1 周,不写代码)

**这是整个架构最重要的一步。不做这个实验就写代码是在赌**。

**目标**:测量当前 LLM 水平下,从不同输入能推断 L6-L8 语用的准确率,确定后面方案 1/2/3 的可行性边界。

**方法**:
1. 挑选 10 个代表性网页(登录/电商详情/后台管理/搜索结果/表单填写各 2 个)
2. 对每个可交互元素**手动标注**:
   - `action_intent` — 点了想干嘛
   - `reversibility` — 可逆/半可逆/不可逆
   - `risk_level` — safe/caution/destructive
   - `flow_role` — primary/secondary/escape/navigation
3. 对比 3 种 LLM 输入:
   - (a) 只给 snapshot 结构数据
   - (b) snapshot + screenshot
   - (c) snapshot + HTML 片段
4. 每种输入下让 LLM 推断上面 4 项,计算准确率

**交付**:一份数据报告,决定阶段 1 到底做不做"语义预处理",做到什么深度。**详见 41 号文档**。

### 3.2 阶段 1:被理解过的 snapshot(2-3 周)

**不再叫 snapshot,叫 `browser.understand`**。它为每个交互元素额外产出 L4-L7 的语义标注:

```
[17] button "登录"
     intent:              "submit_credentials_to_login"
     reversibility:       reversible_on_failure / irreversible_on_success
     risk:                safe_caution          # 多次失败可能触发风控
     flow_role:           primary_action
     context:             "in form[action=/api/login] with textbox[email] textbox[password]"
     predicted_network:   POST /api/login (inferred from form.action)
     predicted_navigation: likely redirects on success (no target specified)
```

**实现策略:混合方案**

| 来源 | 技术 | 占比 |
|---|---|---|
| 从 DOM 静态可推断 | 纯 JS(form.action / onclick href / ARIA / 颜色/文案正则) | ~40% |
| 需要理解才能推断 | sitemap 嗅探时 cheap LLM **批量预处理整站**,缓存到 SQLite | ~60% |
| 运行时 | 先查缓存,miss 才调 LLM | - |

**成本估算**:一次性预处理 50 页的站点,约 25 万 tokens(每页 5k),缓存一周复用,摊薄到单次调用约增加 30-50% token,**换语义密度翻倍**。

**关键细节**:
- 缓存 key:`(url_pattern, dom_hash_prefix)`——URL 改到参数层面也能复用
- 失效策略:DOM 变化超过阈值时重新预处理
- 降级:LLM 调用失败时退回到纯结构 snapshot,标注 `semantic_quality: "structural_only"`

### 3.3 阶段 2:UI 模式库(4-6 周)

**这是真正的护城河**。和项目已有的 `sdk/kernel/learning.go` L1-L3 学习层是**同一件事**——从交互中提炼可复用模式。

**流程**:

```
 ┌─────────────────────────────────┐
 │  Browser Brain 完成任务         │
 │  L2 RecordSequence 记录动作序列 │
 └─────────────────────────────────┘
              │
              ▼
 ┌─────────────────────────────────┐
 │  后台聚类任务(复用 scheduler)  │
 │  相似目标 + 相似页面结构        │
 │  → 聚类为一个"模式"             │
 └─────────────────────────────────┘
              │
              ▼
 ┌─────────────────────────────────┐
 │  模式库(SQLite)                │
 │  pattern: "login_username_pwd"  │
 │  element_roles: {...}           │
 │  action_sequence: [...]         │
 │  post_conditions: [...]         │
 └─────────────────────────────────┘
              │
              ▼
 ┌─────────────────────────────────┐
 │  下次遇到疑似登录页             │
 │  1. 先匹配模式                  │
 │  2. 命中 → 按模板执行(零 LLM)  │
 │  3. miss → 回落到阶段 1 推理    │
 └─────────────────────────────────┘
```

**模式定义示例**:

```go
type UIPattern struct {
    ID            string            // "login_username_password"
    Category      string            // "auth" | "checkout" | "search" | ...
    AppliesWhen   MatchCondition    // 页面匹配条件
    ElementRoles  map[string]RoleSpec
    ActionSequence []ActionStep
    PostConditions []Condition
    Stats         PatternStats      // 命中次数、成功率、最近命中时间
}

// 示例:
{
  ID: "login_username_password",
  Category: "auth",
  AppliesWhen: {
    Has: ["textbox:not([type=password])", "textbox[type=password]", "button"],
    UrlPattern: "^/(login|signin|auth)/?$",  // 可选
  },
  ElementRoles: {
    "email_field":    { Tag: "input", Label: /email|user|账号/i },
    "password_field": { Tag: "input", Type: "password" },
    "submit_button":  { Tag: "button", Text: /登录|login|sign in/i },
  },
  ActionSequence: [
    { Tool: "type", Target: "email_field",    Value: "$credentials.email" },
    { Tool: "type", Target: "password_field", Value: "$credentials.password" },
    { Tool: "click", Target: "submit_button" },
    { Tool: "wait.response", UrlPattern: "/login|/auth", Timeout: 5000 },
  ],
  PostConditions: [
    { Type: "url_changed" },
    { Type: "any_of", Conditions: [
      { Type: "dom_contains", Selector: "[data-user-profile]" },
      { Type: "cookie_set", Name: "session_id" },
    ] },
  ],
}
```

**匹配与执行**:

1. 页面打开后,引擎先把**所有模式**和当前页面做轻量匹配(CSS selector + 可见性 + 关键词)
2. 命中的模式按"历史成功率"降序,Top-1 尝试
3. 执行 `ActionSequence`,每步后检查 `PostConditions`
4. 全部成功 → 记成功;任何一步失败 → 回落到阶段 1 的 LLM 推理

**数据源**:
- **种子**:手工编写 20-30 个高频模式(登录/搜索/加购/结算/列表翻页/筛选...)
- **自学**:L2 RecordSequence 的成功交互序列,后台聚类成新模式
- **用户贡献**:预留导入/导出接口,社区可分享模式

**价值**:
- **效率**:命中模式的任务从 5-10 个 LLM call 降到 0-1 个
- **可靠性**:模板化的交互比 LLM 每次现想更稳定
- **迁移性**:一个模式在 100 个站点都能用(只要 UI 模式相似)

**模式库 import / export**(P1.4 已实现):

`PatternLibrary` 暴露三个公开方法,允许在不同 brain 安装、不同团队、未来 Pro 版"私有模式库"之间分享模式:

- `Export(ctx, ExportFilter) ([]byte, error)` —— 按 ID 集合 / category / source 过滤导出;返回 `PatternExport` 封套(JSON),含 `schema_version` / `exported_at` / `origin` 元信息。
- `ExportByCategory(ctx, category)` —— 快捷方式,导出某个分类下**所有启用且非 learned** 的模式。
- `Import(ctx, data, ImportOptions) (*ImportReport, error)` —— 支持三种模式:
  - `merge`(默认):同 ID 冲突时**保留本地**,只追加新模式。
  - `overwrite`:同 ID 覆盖(但 `source="seed"` 的内置模式默认受保护,除非传 `allow_overwrite_builtin=true`)。
  - `dry-run`:完整校验 + 冲突分析,不写入 DB,`Written=0`,其余计数反映"如果真跑会怎样"。

**安全边界**:

- 导出时 **强制剥离** `Stats`(hit/success/failure 计数)和 `CreatedAt/UpdatedAt`,防止私有使用痕迹通过分享渠道外流。
- 导出时默认 **排除** `Source="learned"` 与 `Enabled=false` 的模式,避免把低质量学到的模式或被系统自动停用的模式扩散出去;需要时用 `IncludeLearned` / `IncludeDisabled` 显式打开。
- 导入时 **拒绝覆盖** 内置 seed 模式(如 `login_username_password`),防止恶意 pack 把登录流程重定向到攻击者的 URL。
- 导入时对每条模式做结构校验(ID 非空、无空白、至少有 AppliesWhen/ElementRoles/ActionSequence 之一),非法条目进入 `ImportReport.RejectedIDs`,不拖累整批。
- 导入覆盖已有用户模式时,**保留本地 Stats 和 Enabled 标志**,不让一次重新 import 把用户辛苦积累的成功率清零。

**CLI 入口**:

```bash
# 导出 auth 分类所有启用模式到文件
brain pattern export auth -o auth-patterns.json --origin team-share

# 按 ID 集合导出
brain pattern export --ids login_username_password,search_query -o share.json

# 默认 merge 模式导入
brain pattern import share.json

# dry-run 只看影响
brain pattern import share.json --mode=dry-run

# 覆盖模式(用户模式),保留本地 Stats
brain pattern import share.json --mode=overwrite

# 谨慎:允许覆盖内置 seed
brain pattern import share.json --mode=overwrite --allow-overwrite-builtin
```

**Schema 兼容策略**:封套的 `schema_version` 当前为 `1.0.0`。同 major 版本互通,minor/patch 向前兼容(未识别字段被 `json.Unmarshal` 忽略)。major 升级时在 `schemaCompatible()` 加迁移分支。

### 3.4 阶段 3:多模态兜底(持续)

**前两层都失败时**的最后一道保险。

**触发条件**:
- 阶段 2 无匹配模式
- 阶段 1 的语义标注质量评分 < 阈值
- 或 Agent 连续 3 个 turn 无进展

**做法**:把 `browser.screenshot + browser.understand` 结果一起喂给主 LLM,让它自己视觉推理。

**定位**:**兜底而不是主路径**。
- 不默认启用,按需触发
- 不主动采样整站,仅针对疑难页面
- 成本高但只在必要时付出

---

## 4. 异常感知层(独立关键模块)

**真实网页交互中,大量 turn 花在处理失败而不是成功**。39 号文档完全没覆盖这块。

### 4.1 需要专门感知的异常类型

| 类型 | 识别特征 | 处理策略 |
|---|---|---|
| Modal / Dialog | `role=dialog` / `role=alertdialog` / 全屏遮罩 | 优先感知,告知 LLM 存在阻塞 |
| 错误提示 Banner | `role=alert` / 红色 + 感叹号 / toast 元素 | 提取文本作为失败原因 |
| 登录过期 | 302 到 `/login` / 401 响应 / 页面 URL 含 login | 触发重新登录流程 |
| CAPTCHA | iframe[src*="recaptcha/hcaptcha"] / `challenges.cloudflare` / 特定文案 | 上报给 LLM 决定是否人工介入 |
| 限流 / 风控 | 429 / 特定文案("操作过于频繁") / 突然出现的验证码 | 退避重试,频率降档 |
| 页面白屏 / JS 报错 | document.body 为空 / window.onerror 捕获 | 刷新或放弃 |

### 4.2 设计:`browser.check_anomaly` 工具

被动监听 + 主动查询双模式:

```json
{
  "mode": "passive" | "active",
  "since_action_id": 42  // 可选,只看这之后发生的
}
```

**被动模式**:注册 MutationObserver + Network 事件监听,后台持续跟踪。每次 Agent 动作后,自动把检测到的异常放到动作结果的 `anomalies` 字段。

**主动模式**:Agent 主动查询当前页面异常状态。

**返回**:
```json
{
  "anomalies": [
    {
      "type": "modal_blocking",
      "severity": "high",
      "description": "A modal dialog appeared: 'Your session expired. Please log in again.'",
      "element_id": 45,
      "suggested_action": "click [id=45] to dismiss, or handle session re-auth"
    }
  ]
}
```

### 4.3 集成到主循环

`sdk/sidecar/loop.go` 的 `RunAgentLoopWithContext` 在每个 turn 结束前,自动调用 `check_anomaly` passive 模式,把结果注入下一 turn 的 tool_result 中。Agent 不需要主动问就能感知异常。

---

## 5. 防御机制

### 5.1 LLM 工具选择困难

新增语义工具 + 模式库后,Agent 可用工具从 15 增到 ~30。**工具越多 LLM 越容易选错**。

**对策**:
1. 每个工具 schema 描述里加 "When to use / When NOT to use"
2. AdaptiveToolPolicy 按场景动态筛选(已有能力,接进来)
3. system prompt 明确:
   ```
   流程:
   1. 新页面 → 调 browser.understand 获取语义清单(优先)
   2. 疑似已知流程 → 检查是否有可匹配的模式(pattern_match)
   3. 只有在 understand 无法推断时 → 才用 screenshot
   4. 不要默认截图,截图是兜底
   ```

### 5.2 语义标注错误

LLM 预处理可能错判("这是购买按钮"其实是"取消按钮")。

**对策**:
- 语义标注返回 `confidence: 0-1`
- confidence < 0.7 时降级为纯结构描述
- 用户反馈机制:LLM 交互失败后可触发"重新预处理"

### 5.3 模式库误匹配

模式匹配可能把完全不同的页面认成同一模式(灾难性后果)。

**对策**:
- `PostConditions` 硬校验,任何一项不满足立即回滚
- 统计每个模式的**失败率**,超阈值自动停用
- 首次使用新模式时强制 LLM 复核("这个页面符合 login_username_password 模式吗?")

### 5.4 UI injection 攻击

恶意网页可能伪造"紧急"按钮诱导 LLM 点击。这是和 prompt injection 同源的漏洞。

**对策**:
- 所有 L7 风险语义标注后,destructive 动作**强制人工审批**
- 敏感操作(支付/删除/权限变更)即使有模式匹配也要审批
- 检测到 UI 突然插入高紧迫 CTA(红色+感叹号+倒计时)时提升警觉级别

---

## 6. 成功度量

### 6.1 技术指标

| 指标 | 当前估计 | 阶段 1 目标 | 阶段 2 目标 | 阶段 3 目标 |
|---|---|---|---|---|
| 简单任务(单页填表)成功率 | 60% | 80% | 90% | 95% |
| 复杂任务(跨页流程)成功率 | 25% | 45% | 65% | 80% |
| 平均 turn 数/任务 | 15 | 10 | 6 | 4 |
| 每 turn token 成本 | 5k | 3k(cache 生效) | 2k(模式命中时) | 2k |
| 异常处理覆盖率 | 0% | 50% | 80% | 95% |

### 6.2 评估基准

定期运行标准任务集(参考 WebArena / Visual WebArena / Mind2Web)回归测试。

---

## 7. 现实主义:能做到什么、做不到什么

### 7.1 2-3 年内能做到

- **高频封闭场景(登录/搜索/电商/后台/表单)的熟练工人级**
- **破坏性操作的风险识别**(红色/确认弹窗/delete 关键词 LLM 天然擅长)
- **单页面流程理解**(购物车几个按钮,哪个是主路径)
- **已知模式的秒级执行**

### 7.2 5 年以上仍难

- **跨站一致性判断**("这个站的删除"和"那个站的删除"行为是否相同)
- **商业意图对抗**("这个红色按钮是给我折扣还是套路")
- **长流程全局规划**(元认知)
- **新颖界面的零样本理解**

### 7.3 三个不可绕过的根本差距

1. **具身经验缺失**:人知道"删错过、懊悔过",LLM 没有痛感记忆
2. **世界模型深度**:人懂"这个站的商业动机",LLM 只有表面模式
3. **意图恒定性**:人能抵抗"醒目 CTA"诱导,LLM 容易被带偏

**这不是工程能解决的,是 AGI 问题**。但**封闭场景 + 高频模式 + 反馈闭环**下,阶段 2 的模式库确实能逼近、部分超越人类效率。

---

## 8. 与其他文档的关系

| 文档 | 关系 |
|---|---|
| [`39-Browser-Brain感知与嗅探增强设计`](./39-Browser-Brain感知与嗅探增强设计.md) | 基础设施层。`snapshot/network/sitemap` 作为本架构的数据管道 |
| [`41-语义理解阶段0实验设计`](./41-语义理解阶段0实验设计.md) | 阶段 0 的评估方案详细设计 |
| [`31-browser-brain-免费版与Pro版规划`](./31-browser-brain-免费版与Pro版规划.md) | 商业化。语义理解作为 Pro 版核心差异化卖点 |
| [`35-自适应学习L1-L3算法设计`](./35-自适应学习L1-L3算法设计.md) | 阶段 2 模式库的学习层接入点 |
| [`38-v3后续增强计划`](./38-v3后续增强计划.md) | 本架构是其中 Browser Brain 章节的核心展开 |

---

## 9. 实施检查清单

### 阶段 0 完成标志
- [ ] 10 个代表性网页完成手工语用标注
- [ ] 3 种 LLM 输入下的推断准确率数据
- [ ] 可行性评估报告(Go / No-Go 决策)

### 阶段 1 完成标志
- [ ] `browser.understand` 工具上线
- [ ] sitemap 嗅探触发的批量预处理链路
- [ ] 语义标注 SQLite 缓存 + 失效机制
- [ ] 降级策略(LLM 失败时回退结构 snapshot)

### 阶段 2 完成标志
- [ ] 20-30 个种子模式(手工编写)
- [ ] L2 RecordSequence → 模式聚类的后台任务
- [ ] 模式匹配引擎(含 PostConditions 校验)
- [ ] 模式统计 Dashboard(命中率 / 成功率)

### 阶段 3 完成标志
- [ ] 多模态兜底触发条件
- [ ] 疑难页面的截图 + 语义联合推理
- [ ] 对 token 成本 / 延迟的监控

### 异常感知层完成标志
- [ ] `browser.check_anomaly` 工具
- [ ] 6 类异常(modal/错误/登录过期/CAPTCHA/风控/白屏)的识别
- [ ] 主循环自动注入异常感知

---

## 10. 总结

**用户那句"人类那种能理解按钮意思"戳穿了一个根本问题:光丰富描述不是理解**。

39 号文档做的是"把窄带宽的结构扩成宽带宽的结构",本文档做的是"从结构升到语义、从语义升到理解、从理解升到可复用模式"。这是架构层面的质变。

**不要指望一步到位,也不要指望永远不可能**。
- 短期(阶段 0-1):被理解过的 snapshot,语用标注,风险分级
- 中期(阶段 2):模式库,越用越强
- 长期(阶段 3):多模态兜底 + 持续学习
- 始终:承认和 AGI 的差距,在封闭场景把熟练工人水平做到顶

这才是"人类那种理解"在当前技术下**可交付的形态**。
