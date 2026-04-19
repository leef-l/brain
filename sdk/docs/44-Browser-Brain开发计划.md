# 44. Browser Brain 开发计划

> **定位**:超出"必做项"(见 [43](./43-Browser-Brain必做项.md))的增量开发。这些项目价值高但时限宽,按批次规划,每批跑完根据结果决定下一批。
>
> 必做项是"修窟窿",本文是"向前走"。

---

## 批次规划速览

| 批次 | 周期 | 关键词 | 对应章节 |
|---|---|---|---|
| P0 | 现在执行 | 见 [43](./43-Browser-Brain必做项.md) | 必做项 |
| P1 | 必做项完成后 2-3 周 | 场景化深耕 | §2 |
| P2 | 数据驱动,2-4 周 | 标准基准 | §3 |
| P3 | 视情况,3-4 周 | 长期研究 | §4 |
| P4 | 商业化节点前 | 免费版/Pro 版 | §5 |

---

## 1. 前置条件

- 必做项 M1 的阶段 0 报告给出 Go 决策
- 必做项 M6 的学习闭环开始产出真实成功率数据
- 至少一周量的 InteractionSequence 数据(由 #13 持久化,#8 聚类消费)

没有以上任一条,不开工后续批次。

---

## 2. 批次 P1 — 场景化深耕(2-3 周)

**依据**:文档 40 §2.1 原则 3("封闭场景先行")+ §7.1("高频封闭场景的熟练工人级")

**价值**:Browser Brain 当前 20 个种子模式(#7)偏通用,在高频场景深耕能让命中率从 40% 冲到 80%+。

### 2.1 封闭场景包:登录 / 注册 / 验证流程
- 手工种子模式 5-8 个(用户名密码 / 邮箱验证 / OAuth / 2FA / CAPTCHA 前置)
- 与 #9 异常 v2 的 session_expired 联动
- 指标:一周内登录类任务成功率 ≥ 90%

### 2.2 封闭场景包:电商购物流程
- 模式 5-8 个(浏览 / 加购 / 去结算 / 填地址 / 下单 / 支付跳转)
- 与 #4 anomaly 的 modal 类别联动(各种优惠弹窗)
- 指标:至少 3 个真实电商 demo 站的端到端跑通率 ≥ 70%

### 2.3 封闭场景包:后台管理(CRUD + 表格 + 筛选)
- 模式 5-8 个(列表翻页 / 筛选 / 批量操作 / 导出 / 详情编辑)
- 重点研究"表格分页模式"的跨站一致性
- 指标:AdminLTE / Ant Design Pro 两个 demo 的覆盖率

### 2.4 模式库 import / export 接口
- 文档 40 §3.3 提过"社区可分享模式"
- 最小交付:JSON 文件导入 + 按站点/类别导出
- 为 P4 Pro 版的"共享模式市场"预留

---

## 3. 批次 P2 — 标准基准(2-4 周)

**依据**:文档 40 §6.2

**价值**:没有公开 benchmark 数据,对外声称"熟练工人级"就是嘴炮。要能拿出数字对标 Claude CUA / Browser Use / OpenAdapt 等同类产品。

### 3.1 WebArena 真实接入
- #12 已经接入框架,任务定义 5 条
- 扩到 30-50 条覆盖 WebArena 主要类别(reddit/shopping/map/gitlab)
- 建立 CI 夜间跑 + 趋势看板(复用 #17 WebUI 面板)

### 3.2 Mind2Web 子集接入
- Mind2Web 侧重"跨站同类任务的泛化能力"
- 跑 100 条覆盖多个站点,看模式库的跨站迁移率

### 3.3 Visual WebArena(视觉依赖场景)
- 验证 #12 的 `browser.visual_inspect` 兜底机制
- 对比"纯 snapshot"vs"snapshot + visual"的准确率差
- 给阶段 3 兜底策略提供性价比数据

### 3.4 端到端跨页任务评测集
- 自建 10 条"登录→搜索→加购→结算→支付"长链路
- 测 context 传递 / 状态持久化 / 异常恢复
- 暴露 token budget 和 turn 爆炸的临界点

---

## 4. 批次 P3 — 长期研究(3-4 周,按需)

### 4.1 异常感知 v3(文档 42 §7.3)

- **异常模板库**:像模式库一样沉淀异常处理模板,匹配即应用
- **跨站异常模式识别**:学"站 A 的风控"vs"站 B 的风控"的差异
- **LLM 辅助自动修复**:异常发生时 LLM 生成修复 action 序列,成功率 ≥ 60% 时自动执行

### 4.2 模式自分裂(文档 42 §5.2)

- 当"登录模式"在某站连续失败且检测到特定异常组合(如 captcha 频现),自动分裂出"登录模式-带 captcha 版"
- 需要 #8 聚类算法扩展
- 目标:模式库在 3 个月内从 20 → 50,其中 > 30 是自学

### 4.3 真人接管期间的 DOM 操作录制 ✅ P3.3

**已交付**(2026-04-19):

- `sdk/tool/cdp/human_events.go` —— CDP 事件源抽象 + 两种实现
  - `CDPEventSource`:页面注入 `addScriptToEvaluateOnNewDocument` hook(click/input/change/submit),通过 `Runtime.addBinding` + `Runtime.bindingCalled` 回传 `HumanEvent`
  - `MemoryEventSource`:测试用内存 channel,单测不起浏览器
- `sdk/tool/human_takeover.go` —— `humanActionRecorder`
  - 进入 takeover 时通过 `HumanEventSourceFactory` 启动,退出时 Stop
  - 噪声过滤:只认 click / input / change / submit(hover/scroll 根本不订阅)
  - 输入聚合:500ms 窗内同元素连续 input 合并为一条 `browser.type`(保留最后 value);非 input 事件中断窗口
  - 每条 RecordedAction 打 `Params["_human"] = true`,下游聚类按此识别
  - 复用 `SequenceRecorder.append` 管道(学习闭环)+ 写 `HumanDemoSequence` 表(ops 审批前不入学习模式)
- `sdk/persistence` —— 由 P3.0 #16 提供的 `HumanDemoSequence` 表 + `SaveHumanDemoSequence` 接口
- 测试:`sdk/tool/cdp/human_events_test.go`(4 条)+ `sdk/tool/human_takeover_test.go` 扩 4 条 DOM 路径测试,全绿

**保护机制**:
- 工厂未注入时降级到老行为(只录标记),向前兼容
- 空 demo 不落盘,避免堆积空记录
- CDP listener 用 `stopped` 标志防写已关 channel
- `Approved = false` 默认,ops 审批后才能进模式库

### 4.4 性能工程化

- **Snapshot 增量更新**:MutationObserver diff 驱动,不再每次全扫
- **模式库匹配索引**:按 URL pattern 预筛,避免 100+ 模式线性扫
- **Sitemap 持久化缓存**:同站复用,节省嗅探成本
- 指标:单次 turn 耗时从 500ms → 150ms(大页场景)

### 4.5 BrowserStage 自动切换

- 当前 `BrowserStage`(new_page/known_flow/destructive/fallback)靠 LLM 自己判断
- 改为:模式库匹配度 > 0.8 自动进入 known_flow,低于阈值降到 new_page,连续 3 turn 无进展降到 fallback
- 减少 LLM token 消耗,稳定性提升

---

## 5. 批次 P4 — 商业化(文档 31 落地)

**依据**:文档 31(免费版与 Pro 版规划)

**价值**:把 Browser Brain 作为可售卖的专精大脑,和 Claude CUA / Browser Use 竞争。

### 5.1 License gate

- sdk/license 已有 sidecar 校验基础
- 在 understand / pattern_match / visual_inspect 三个"重工具"前加 feature gate
- 免费版只给 snapshot + 基础动作,Pro 版开放语义层

### 5.2 Pro 版核心卖点

- **深度语义理解**:understand + pattern 库完整可用
- **私有模式库**:企业内模式不外流,支持加密导入导出(和 P1.4 的 import/export 结合)
- **优先客服级 Marketplace 模式推送**:官方精选的高质量模式包每周更新
- **VLM 兜底额度池**:Pro 版带配额,免费版不开放 visual_inspect

### 5.3 计量与审计

- 复用 #15 DailySummary 扩展 usage 维度
- 输出 per-user 月度账单数据
- 对接 #19 HookRunner,让企业可以挂自己的账单系统

---

## 6. 不在此计划的方向

以下方向**明确不做**,避免 scope creep:

- ❌ **重造 VLM 视觉推理模型**:文档 40 §7.2 明确表示"跨站一致性 / 商业意图对抗 / 长流程全局规划"是 AGI 问题,不是工程能解决的
- ❌ **全场景人类级理解**:文档 40 §2.1 原则 3 明确只做封闭场景
- ❌ **和 Playwright / Puppeteer 同质的完整浏览器自动化 SDK**:我们是 Agent-centric 的工具层,不是通用测试框架
- ❌ **浏览器外的 OS 自动化**:那是 Desktop Brain(#21)的地盘,不混

---

## 7. 每批次的准入/退出标准

### P1 准入
- 必做项 M1-M7 全部完成
- InteractionSequence 数据 ≥ 1000 条

### P1 退出
- 三个场景包各至少 5 个种子模式
- 对应场景成功率指标达成

### P2 准入
- P1 完成,或 P1 退出标准未达也可直接跑 P2.1 拿数据反推方向

### P2 退出
- 至少两个公开基准(WebArena + Mind2Web)有趋势数据
- 对比竞品的差距量化

### P3 准入
- P1 / P2 都完成,且商业化方向已定

### P3 退出
- 按 4.1-4.5 各自指标达成

### P4 准入
- 前三批完成,商业化节点确定
- 文档 31 最终版 sign off

---

## 8. 钱学森工程控制论审计 checklist

> 依据 `sdk/docs/钱学森工程控制论-设计原则.md` 提炼的 7 条核心原则。
> **每批次退出前必须过一遍**——确认本批次交付没有在某条原则上开倒车。

| 原则 | 含义 | 审计问题 | brain-v3 对应设施 |
|---|---|---|---|
| **反馈闭环** | 系统必须有从结果到输入的闭合回路,不能开环盲跑 | 本批次产出的数据/指标有没有回灌到决策链? | M6 学习闭环 → AdaptiveToolPolicy / P3.5 BrowserStage 决策器 / P3.1 RecordFixOutcome → PromoteCandidate |
| **稳定性折衷** | 控制性能提升不是免费的——速度/精度/稳定性三者有矛盾 | 本批次为了"更准/更快"有没有牺牲稳定性?有没有加守卫? | M3 模式自动停用(精度过低→禁用) / M2 confidence 降级(语义低置信→回落) / P3.4 增量 snapshot(加速但 URL 变更强制全量) |
| **不互相影响** | 多通道控制各自独立,一个通道调节不破坏另一个 | 本批次改的模块有没有串到别的模块的状态? | #18 SharedMessages 分桶隔离 / P3 文件线分工(dev-pattern/dev-anomaly/dev-admin 不互改) / extraSeedProviders init() 零侵入 |
| **时滞** | 延迟会吃掉稳定裕度,不能把延迟理想化 | 本批次引入的网络/LLM/CDP 调用有没有加超时和退避? | #14 retry 指数退避(BackoffHint) / P3.4 sitemap 缓存(跳过 10s BFS) / P3.4 snapshot 增量(500ms → 150ms) |
| **噪声过滤** | 感知噪声直接决定控制上限,脏输入再好的控制律也拖垮 | 本批次的感知工具有没有过滤/分级/降级? | M4 UI injection 检测(severity=high 强制注入) / M7 JS error 订阅(error/warning 分级) / P3.3 真人录制(500ms 输入聚合 + 白名单过滤) |
| **适应环境** | 当环境变化时系统能自调整,不依赖外部频繁人工干预 | 本批次有没有"站点/页面/异常变化时自动调整"的机制? | P3.1 跨站异常模式识别(siteHist 按 origin 分桶) / P3.2 模式自分裂(站点+异常特化) / P3.5 Stage 自动切换(匹配度驱动) |
| **误差控制** | 元件会出错,必须有冗余、可靠性和误差传播控制 | 本批次的失败路径有没有结构化分类、有没有冗余/降级? | #14 ErrorResult(brainerrors.CodeXxx) / M5 on_anomaly 四分支(abort/retry/fallback/human) / P3.1 LLM 辅助修复 fallbackRecovery 兜底 |

**审计节奏**:

- P0-P3 每批次退出时,team-lead 对照上表逐行检查
- 有条目"开倒车"(本批次破坏了某原则)→ 立即建修复任务,不带到下一批次
- 有条目"未触及"(本批次没涉及某原则)→ 正常,不是每批都会动所有原则

---

## 9. easymvp-brain 消费接口预留

> 依据 `/www/wwwroot/project/easymvp/docs/钱学森总纲设计/easymvp-brain-职责与边界定义.md` 和 `EasyMVP-中央大脑与四专精大脑IO合同及升级规则.md`。

### 9.1 背景

EasyMVP 定义了一个**领域专精大脑** `easymvp-brain`,它消费 brain-v3 的四个基础专精大脑(code / browser / verifier / fault)的结果。消费方式是:

```
brain-v3 central + (code/browser/verifier/fault)
   ↓ runtime adapter 归一化
EasyMVP 领域层 → easymvp-brain(审核/编译/裁决/返工/验收规则)
```

### 9.2 brain-v3 侧需要预留的接口

Browser Brain 作为被消费者,需要让 runtime adapter 能取到:

1. **RunResult 归一化**:sidecar ExecuteResult 已有 `status / summary / error / turns`,满足 EasyMVP `RunResult` 的映射需求。**无需改动**。

2. **DeliveryResult 结构化**:当前 `ExecuteResult.Summary` 是自然语言文本,EasyMVP 需要结构化的 `DeliveryResult`(产物清单 + 变更摘要)。**需要新增**:`ExecuteResult.Artifacts []ArtifactRef` 字段(可选)。

3. **VerificationResult**:Browser Brain 的 `check_anomaly` / `pattern_exec PostConditions` 结果需要以结构化方式传到 EasyMVP 的 `VerificationResult`。**需要新增**:PostConditions 检查结果作为 `ExecuteResult.Verification` 字段。

4. **FaultSummary**:Browser Brain 失败时的 anomaly 分类 + 异常模板命中 + on_anomaly 路由决策。**需要新增**:`ExecuteResult.FaultSummary *FaultSummary` 字段。

5. **RuntimeEscalation**:当 Browser Brain 调 `human.request_takeover` 时,EasyMVP 需要知道这是一次 escalation。**已有**:EventBus 的 `task.human.requested` 事件包含足够信息。**无需改动**。

### 9.3 实施时机

这些接口预留属于 **P4 批次(商业化)**的前置工作。不在 P3 scope,但在 P4 准入条件里加一项:

> P4 准入:ExecuteResult 扩展 Artifacts / Verification / FaultSummary 三个可选字段,让 EasyMVP runtime adapter 能零转换消费。

---

## 10. 与其他文档的关系

| 文档 | 关系 |
|---|---|
| [`40-Browser-Brain语义理解架构`](./40-Browser-Brain语义理解架构.md) | 主架构,本文的阶段 0-3 分别对应本文 P0-P3 |
| [`41-语义理解阶段0实验设计`](./41-语义理解阶段0实验设计.md) | 必做项 M1 的执行说明 |
| [`42-Browser-Brain异常感知层设计`](./42-Browser-Brain异常感知层设计.md) | §5 模式分裂 + §7.3 异常 v3 → P3.1 / P3.2 |
| [`43-Browser-Brain必做项`](./43-Browser-Brain必做项.md) | 前置必做项,P1 开工前必须全清 |
| [`31-browser-brain-免费版与Pro版规划`](./31-browser-brain-免费版与Pro版规划.md) | P4 的商业化依据 |
