# 43. Browser Brain 必做项

> **定位**:把已建好的基础设施**真正闭环**,或修复已知隐患。这些项不做,前面 21 个任务的投入会有"建好但不生效"的窟窿。
>
> 本文除了列出每项的必要性和交付,还包含**并行实施计划 + Agent Team 编排**(§9-11),压缩总工期从 3-4 周到约 2 周。

## 0. 2026-04-20 状态快照

> 本节是对当前代码状态的核查回写,用于给 [44](./44-Browser-Brain开发计划.md) 的 P1/P3/P4 前置条件提供统一口径。
> 口径说明:
> - `[x]` = 代码主路径已具备基础能力
> - `[~]` = 代码已实现,但仍有主路径接线/生产化边界
> - `[ ]` = 尚未完成

- `[ ]` M1 阶段 0 真人实验
  - `sdk/docs/41-语义理解阶段0实验设计.md` 需要的人工标注 / `report.md` 仍是方法论前置,不是当前仓库代码能自动补齐的项。
- `[x]` M2 confidence 降级
  - `browser.pattern_match / pattern_exec` 已消费 semantic confidence,并会输出 `_degrade_reason` 或拒绝执行。
- `[x]` M3 模式自动停用
  - `PatternLibrary.RecordExecution` 已支持 `FailureCount >= 5 && SuccessRate < 0.3` 自动停用。
- `[x]` M4 UI injection 检测
  - anomaly v2 已有 `ui_injection` 类型与高严重度路由。
- `[x]` M5 on_anomaly 路由实际执行
  - `abort / retry / fallback_pattern / human_intervention` 四分支已进入 pattern 执行链。
- `[x]` M6 学习闭环
  - 工具执行结果已能回灌到 `AdaptiveToolPolicy.RecordOutcome(...)`。
- `[x]` M7 JS error 订阅真实启用
  - browser session 初始化时已挂 `Runtime.consoleAPICalled / Runtime.exceptionThrown` 监听并进入 anomaly history。

**当前结论**:

- P0“必做项代码闭环”除 M1 人工实验外,其余 M2-M7 已基本落地。
- 因 M1 仍未完成,`44` 文档里“P1/P2/P3 正式准入”仍不能视为全部满足。

---

## 1. 优先级与并行分组(总览)

按"不做损失严重程度"排序:

| 等级 | 项 | 损失描述 |
|---|---|---|
| 🔴 S | M4 UI injection 检测 | prompt-injection 同源漏洞,可被恶意页带偏 |
| 🔴 S | M6 学习闭环 | 已建基础设施不生效;1 天 ROI 最高 |
| 🟠 A | M3 模式自动停用 | 坏模式累积,模式库越用越差 |
| 🟠 A | M2 confidence 降级 | 低质量语义污染下游决策 |
| 🟠 A | M5 on_anomaly 执行 | 阶段 2 核心 schema 空壳 |
| 🟡 B | M7 JS error 订阅 | 白屏/崩溃子类异常识别率 0 |
| 🟡 B | M1 阶段 0 真跑 | 数据驱动缺失,拍脑袋推进 |

**依赖与文件冲突分析**(决定哪些能并行):

| 项 | 主改文件 | 依赖 |
|---|---|---|
| M1 | 无代码(数据线) | 独立 |
| M2 | builtin_browser_pattern.go | M3(同文件先入) |
| M3 | ui_pattern.go + builtin_browser_pattern.go | M6(装饰器先稳定) |
| M4 | builtin_browser_anomaly_v2.go | M7(同文件先入) |
| M5 | builtin_browser_pattern.go(pattern_exec) | M2 + M3 + M4(消费三者) |
| M6 | sequence_recorder.go + builtin_browser_anomaly.go | 无 |
| M7 | builtin_browser_anomaly_v2.go + cdp/session.go | 无 |

**并行分组**:

```
批次 P0(立刻)—— 完全独立,3 并发
  M6 学习闭环         ← 1 天  ← dev-pattern 线第一棒
  M7 JS error 订阅    ← 1-2 天 ← dev-anomaly 线第一棒
  M1 阶段 0 真跑      ← 1 周  ← 后台数据线(不占 agent)

批次 P1(P0 之后)—— 2 并发
  M3 模式自动停用     ← 2 天  ← dev-pattern(承接 M6)
  M4 UI injection     ← 3-4 天 ← dev-anomaly(承接 M7)

批次 P2(P1 之后)—— 线性
  M2 confidence 降级  ← 2-3 天 ← dev-pattern(承接 M3)

批次 P3(M2+M3+M4 齐)—— 压轴
  M5 on_anomaly 执行  ← 1 周  ← dev-integrator(新 agent)
```

**压缩后总工期:约 2 周**(串行估算 3-4 周)。

---

## 2. M1. 阶段 0 实验必须真跑一轮

**依据**:文档 40 §3.1 / 文档 41 整篇

**现状**:`sdk/docs/experiments/phase0/` 骨架齐全(scripts/ + samples/ + labels/ 目录),**但**:
- 10 个样本的人工标注未完成
- 1080 次 LLM 调用(10 页 × 3 输入形态 × 3 模型 × 12 元素)未跑
- `report.md` 没有真实数据

**不做的后果**:阶段 1 的 `browser.understand`(已实现)投入了 4-7 周对标成本,**但我们不知道它的真实准确率**。LLM 如果在 action_intent/reversibility/risk_level/flow_role 四维度某些维度上根本不靠谱,当前 understand 是在用不可靠数据喂下游——整条链路成立与否没数据支撑。

**最少交付**:
1. 10 个样本页采集完成(`scripts/capture.py` 已写)
2. 四维度人工标注(`labels/<page_id>.json`)
3. 跑 `run_experiment.py` 拿结果
4. `analyze.py` → `report.md`,给出 Go/No-Go 决策

**预估**:1 周,后台人工

---

## 3. M2. 防御机制 §5.2 — confidence < 0.7 自动降级

**依据**:文档 40 §5.2

**现状**:
- `browser.understand` 返回 `confidence` 和 `semantic_quality` 字段 ✅
- 下游工具**没读这两个字段做分支** ❌
- 阶段 2 `pattern_match` 没有"understand confidence 低时回落结构 snapshot"的逻辑

**不做的后果**:LLM 拿到低质量语义标注会自信地按错误理解做决策——文档 40 §5.2 明确指出这是必须堵的口。

**最少交付**:
1. `pattern_match` / `pattern_exec` 执行前检查关联 understand 条目的 confidence
2. confidence < 0.7 时返回结构化 `low_confidence` 标记,由 Agent 决定是否继续
3. 新增 `_degrade_reason` 字段在工具输出里

**预估**:2-3 天

---

## 4. M3. 防御机制 §5.3 — 模式失败率自动停用

**依据**:文档 40 §5.3

**现状**:
- `PatternStats.FailureCount / SuccessCount` 字段齐全 ✅
- `Stats.SuccessRate()` 方法存在 ✅
- **没有"成功率 < 阈值 且样本 ≥ N 时自动 disable 该模式"的守卫** ❌
- 失败模式会一直被 match_match 选中

**不做的后果**:模式库越用越差——坏模式永远在召回链路里,把 Agent 往错路上带。

**最少交付**:
1. `PatternLibrary` 加 `Enabled bool` 字段
2. 每次 `RecordExecution` 后检查:`FailureCount ≥ 5` 且 `SuccessRate < 0.3` → 自动 `Enabled = false`
3. `List` / `pattern_match` 过滤 `Enabled = false` 的条目
4. 管理端点允许 ops 手动重置

**预估**:2 天

---

## 5. M4. 防御机制 §5.4 — UI injection 检测

**依据**:文档 40 §5.4

**现状**:完全没实现。文档明确列出"红色 + 感叹号 + 倒计时 + 高紧迫 CTA 突然插入"的检测策略,代码里一行都没有。

**不做的后果**:恶意网页(或被劫持的站点)可以在页面上突然注入"立即点击领取"之类的按钮,当前 Agent 很容易被带偏。这是和 prompt injection 同源的漏洞。

**最少交付**:
1. 扩展 `anomaly_v2` 加一个 `AnomalyUIInjection` 类型
2. 检测启发式:MutationObserver 监听到新增的 `fixed/sticky` + `z-index > 1000` + 含 "立即/紧急/倒计时/验证" 文案的按钮
3. 触发时强制标 severity=high,主循环强制注入(已有机制)
4. destructive 类动作在 UI injection 告警期内必须走 `human.request_takeover`

**预估**:3-4 天

---

## 6. M5. on_anomaly 路由实际执行

**依据**:文档 42 §5

**现状**:
- `UIPattern.OnAnomaly map[string]AnomalyHandler` 数据结构存在 ✅
- `AnomalyHandler` 支持 `action: abort / retry / fallback_pattern / human_intervention` ✅
- **执行引擎没真正消费这些分支** ❌ — 异常发生时仍然靠 Agent 自己判断

**不做的后果**:阶段 2 模式库设计里最关键的"模式对异常的反应"退化为纯静态数据,白白做了 schema。

**最少交付**:
1. `pattern_exec` 在每步动作后检查 `_anomalies` 字段
2. 匹配 `OnAnomaly[anomaly.subtype]`,按 action 字段分发
3. `fallback_pattern` → 切换到指定模式继续执行
4. `human_intervention` → 调 `human.request_takeover`(#16 已建)

**预估**:1 周

---

## 7. M6. 低成功率工具自动筛(学习闭环)

**依据**:文档 40 §7.3(反馈闭环)

**现状**:
- AdaptiveToolPolicy 已有 `RecordOutcome(toolName, taskType, success)` 方法 ✅
- `Evaluate` 里有"成功率 ≤ 15% 且调用 ≥ 5 次 → 临时禁用"逻辑 ✅
- **但 browser 工具执行结果没有回调 `RecordOutcome`** ❌

**不做的后果**:#11 / #13 建好的基础设施不闭环——Agent 用得再多,筛选策略永远按静态配置走。

**最少交付**:
1. `anomalyInjectingTool` 装饰器在工具 Execute 后调 `RecordOutcome`
2. 成功判定:`!res.IsError`
3. taskType 从当前 run 的 recorder 拿

**预估**:1 天

---

## 8. M7. Anomaly v2 的 JS error 订阅真实启用

**依据**:文档 42 §2.6 / #9

**现状**:
- `Runtime.consoleAPICalled` 订阅接口已写 ✅
- **初始化时没有默认启用订阅** ❌ — Agent 拿不到 JS 报错信号

**不做的后果**:白屏 / JS 崩溃类异常的识别率为 0,文档 42 §2.6 定义的这个异常类实际不工作。

**最少交付**:
1. `browserSessionHolder.get` 初始化时自动开启 `Runtime.enable` + `Runtime.consoleAPICalled` 订阅
2. 捕获的 error 入 `anomalyHistory.jsErrors`
3. `check_anomaly` 读取并上报

**预估**:1-2 天

---

## 9. Agent Team 编排

**team**:`browser-brain-mandatory`(已 TeamCreate)

**成员**:

| 成员 | 类型 | 任务线 | 执行顺序 |
|---|---|---|---|
| team-lead | 人(本对话) | 总协调、M1 人工推进、M5 前 gate | 全程 |
| dev-pattern | general-purpose | pattern 文件线 | M6 → M3 → M2 |
| dev-anomaly | general-purpose | anomaly 文件线 | M7 → M4 |
| dev-integrator | general-purpose(P3 再起) | 压轴集成 | M5 |

**文件冲突规避**:
- pattern 相关文件(`builtin_browser_pattern.go` / `ui_pattern*.go`)只由 dev-pattern 动
- anomaly 相关文件(`builtin_browser_anomaly*.go` / `cdp/session.go`)只由 dev-anomaly 动
- 装饰器(`anomalyInjectingTool`)归 dev-pattern(M6 先改,后续 wrapper 不再动)
- `sequence_recorder.go` 归 dev-pattern(M6 在这里加 OutcomeSink)

**交接协议**:
1. 每完成一个任务,owner 调 `TaskUpdate status=completed` + SendMessage 汇报
2. 下一个任务的 blockedBy 自动解锁后,该 owner 继续 TaskList 认领
3. M5 的 dev-integrator 在 M2/M3/M4 全部 completed 后由 team-lead spawn
4. 每次提交前跑 `go build ./... && go vet ./... && go test ./... -count=1 -timeout 240s` 全绿

---

## 10. 时间表(压缩)

```
Day 1        M6 ✓ + M7 启动
Day 2        M7 ✓ + M1 标注启动(后台)
Day 3-4      M3 ∥ M4 并行
Day 5-6      M4 ✓ + M3 ✓ + M2 启动
Day 7-8      M2 ✓
Day 9-14     M5(spawn dev-integrator)
(后台)      M1 在 Day 7 前后出 report.md
```

**总计**:约 2 周(10-14 个工作日)。

---

## 11. 总计(压缩前后对比)

| 项 | 独立工作量 | 串行总工期 | 并行压缩后 | 风险消除 |
|---|---|---|---|---|
| M1 阶段 0 真跑 | 1 周 | — | 后台 | 数据驱动决策替代拍脑袋 |
| M2 confidence 降级 | 2-3 天 | — | — | 错误语义不污染决策 |
| M3 模式自动停用 | 2 天 | — | — | 坏模式不累积 |
| M4 UI injection 检测 | 3-4 天 | — | — | prompt-injection 同源漏洞 |
| M5 on_anomaly 执行 | 1 周 | — | — | 阶段 2 核心价值闭环 |
| M6 学习闭环 | 1 天 | — | — | #11/#13 基础设施生效 |
| M7 JS error 订阅 | 1-2 天 | — | — | 白屏/崩溃异常识别 |
| **合计** | — | **3-4 周** | **约 2 周** | — |

M1 作为后台数据线不占关键路径,其余 M2-M7 按三并发收敛。

---

## 12. 钱学森工程控制论审计

必做项完成后,对照 `sdk/docs/钱学森工程控制论-设计原则.md` 7 条原则逐项检查(详见 `44-开发计划.md` §8):

| 原则 | 本轮必做项覆盖 |
|---|---|
| 反馈闭环 | M6 学习闭环回调 ✅ |
| 稳定性折衷 | M2 confidence 降级 + M3 模式自动停用 ✅ |
| 不互相影响 | Agent Team 文件线隔离(§9 编排规则) ✅ |
| 时滞 | 无新时滞引入(M6/M7 都是 O(1) 回调) ✅ |
| 噪声过滤 | M4 UI injection 检测 + M7 JS error 分级 ✅ |
| 适应环境 | M5 on_anomaly 路由(4 分支动态响应) ✅ |
| 误差控制 | M3 失败率阈值守卫 + M5 abort/human 兜底 ✅ |

**结论**:必做项 7 条原则无开倒车项。
