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

### 1.1 状态口径

为避免把“helper 已存在”误写成“生产路径已通电”,本文统一使用三档状态:

- **已完成**:代码存在,且已进入当前 browser sidecar / serve 主路径
- **部分完成**:核心实现已存在,但仍缺少 host→sidecar 接线、进程间同步或运行时热更新
- **未完成**:需求仍主要停留在文档、测试或局部 helper 层

### 1.2 当前前置条件核查(2026-04-20)

- `[ ]` M1 阶段 0 报告给出 Go 决策
- `[~]` M6 学习闭环已接代码主路,但真实成功率数据是否已稳定产出,取决于线上运行量
- `[ ]` 至少一周量 InteractionSequence 数据

**结论**:

- P1/P2/P3 的方法论前置条件还没有全部满足。
- 下面的 P3/P4 状态回写,表示“代码实现到哪一步”,不代表这些批次已具备正式开工/退出条件。

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

**当前状态**:部分完成

- `sdk/tool/anomaly_template*.go`、`sdk/tool/anomaly_template_route.go` 已实现异常模板库、模板优先路由和 outcome 统计。
- `cmd/brain/cmd_serve.go` 会把持久化 anomaly templates 读进**宿主进程**的共享库。
- `brains/browser/cmd/main.go` 现已在 sidecar 进程启动时直接打开默认持久化库,把 anomaly templates 装载进 child process 自己的运行时模板库。

**当前边界**:

- browser sidecar 启动时已支持两种持久化来源:
  - 默认 `~/.brain/brain.db`
  - 直接通过 `BRAIN_DB_PATH` 指向的自定义 SQLite 库
- 当前 child 侧 anomaly template 刷新不是“文件变化才重载”,而是 `browserRuntimeReloader.MaybeRefresh()` 在 1s 节流后按调用重装一次 `LearningStore` 视图。
- 仍未完成的是“宿主 runtime 配置/DSN 自动投影到 child”这层统一下发协议,以及更显式的 host→child 主动同步协议。

**执行 checklist**:

- `[x]` 模板结构、匹配逻辑、模板优先级已实现
- `[x]` 宿主可从 `LearningStore` 读取模板
- `[x]` browser sidecar 启动时会从默认库或 `BRAIN_DB_PATH` 指向的持久化库装载 anomaly template 运行时视图
- `[x]` 已运行的 browser sidecar 支持按文件变更触发 anomaly template 重载

### 4.2 模式自分裂(文档 42 §5.2)

**当前状态**:部分完成

**已完成部分**:

- `sdk/tool/pattern_split.go` —— 失败样本聚类 + 变种生成
  - `pattern_exec` 失败时把 `(pattern_id, site_origin, anomaly_subtype, failure_step, page_fingerprint)` 落到 `pattern_failure_samples`
  - `ScanForSplit(...)` 按 `(pattern_id, site_origin, anomaly_subtype)` 聚类,样本数 ≥ 5 时生成站点/异常特化变种
  - 只对 `source="learned"` 且 `Enabled=true` 的父模式分裂,避免 seed 模式污染和坏模式继续派生
- `sdk/tool/ui_pattern.go` + `sdk/tool/ui_pattern_match.go`
  - 新增 `MatchCondition.SiteHost`,变种不再把 host 混进 `URLPattern`,而是显式 host 精确约束
  - 修复了早期实现里“site-specific 变种可能跨站误命中”的缺口
- `cmd/brain/cmd_serve.go`
  - `SetPatternFailureStore(runtime.Stores.LearningStore)` 已接线
  - `brain serve` 启动后会后台定期跑 split 扫描,让失败样本真实长出变种,不再停留在库函数层
- `sdk/tool/ui_pattern.go`
  - `BRAIN_UI_PATTERN_DB_PATH` 已可覆盖 child 侧 `PatternLibrary` 默认路径
- 毕业/淘汰规则已接主路:
  - `Pending=true` 变种累计成功 ≥ 3 自动转正
  - 失败 ≥ 5 且成功率 < 0.3 自动停用(M3)

**当前边界**:

- host 进程里的 split 扫描会把新变种写入 SQLite PatternLibrary。
- browser specialist 虽然仍是独立 sidecar 进程,但其 `PatternLibrary` 已不再是“只启动加载一次”的静态缓存:
  - `sdk/tool/ui_pattern.go` 已有 `ReloadIfChanged`
  - `sdk/tool/builtin_browser_pattern.go` 已有 `RefreshSharedPatternLibraryIfChanged`
  - `brains/browser/cmd/main.go` 会在启动、每次请求分发前和 registry 构建时触发刷新
- 因此当前真实状态更准确地说是:
  - “失败样本 → 站点特化变种 → 持久化库可见 → 已运行 browser sidecar 在后续请求中可刷新看到”
- 剩余边界不是“child 完全不热更新”,而是刷新机制当前仍以文件变更检测 + 调用时机触发为主,不是 host 主动推送协议。

**执行 checklist**:

- `[x]` 失败样本记录到 `pattern_failure_samples`
- `[x]` split 聚类与变种生成逻辑已实现
- `[x]` `SiteHost` 约束已进入匹配逻辑
- `[x]` `Pending -> 正式` 与低成功率自动停用规则已持久化
- `[x]` `brain serve` 已启动后台 split 扫描
- `[x]` 已运行 browser sidecar 会在后续请求中按文件变更刷新新变种
- `[x]` host 扫描结果无需重启 child process 即可进入后续匹配/执行主路

### 4.3 真人接管期间的 DOM 操作录制 ✅ P3.3

**当前状态**:部分完成

**已完成部分**:

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

**当前边界**:

- `cmd/brain serve` 确实会在**宿主进程**调用 `SetHumanEventSourceFactory(...)`。
- `brains/browser/cmd/main.go` 现已在 child process 内装配 `HumanEventSourceFactory`,直接绑定该 sidecar 自己的共享 browser session。
- 因此在真实 delegated browser 路径里,只要 session 已存在,`human.request_takeover` 已不再必然退化到 marker-only。

**执行 checklist**:

- `[x]` DOM 事件源抽象和 memory/CDP 两类实现已存在
- `[x]` takeover 期间的动作聚合、录制、落库逻辑已实现
- `[x]` host `serve` 进程已装配 `SetHumanEventSourceFactory`
- `[x]` browser sidecar 子进程启动时已装配 `HumanEventSourceFactory`
- `[~]` delegated browser 执行链在 session 已创建时能真实拿到 CDP DOM 事件;若 takeover 早于 session 初始化,仍会安全退化为 marker-only

### 4.4 性能工程化

- **Snapshot 增量更新**:MutationObserver diff 驱动,不再每次全扫
- **模式库匹配索引**:按 URL pattern 预筛,避免 100+ 模式线性扫
- **Sitemap 持久化缓存**:同站复用,节省嗅探成本
- 指标:单次 turn 耗时从 500ms → 150ms(大页场景)

### 4.5 BrowserStage 自动切换

**已交付**(2026-04-20):

- `sdk/toolpolicy/browser_stage_decider.go`
  - 规则已固化成纯函数:高匹配度 → `known_flow`,低匹配度/无数据 → `new_page`,连续错误窗口 → `fallback`
  - destructive approval class 仍保留硬约束优先级
- `sdk/tool/sequence_recorder.go` + `sdk/tool/builtin_browser_pattern.go`
  - `pattern_match` top score 会写入 recorder
  - `pattern_exec` / 工具失败会写入 recent turn outcome,给 stage 决策器消费
- `sdk/loop/runner.go` + `sdk/sidecar/loop.go`
  - 已从“只改发给 LLM 的 tools schema”升级为“每 turn 同时重建 schema + dispatch registry”
  - 本轮实际执行工具集合和暴露给 LLM 的工具集合保持一致,不再出现 schema 已切换但 dispatch 仍走基础 registry 的偏差

**结果**:

- BrowserStage 已不再依赖 LLM 自己判断
- 该能力已进入 browser sidecar 主路径,属于真实运行时行为,不是实验性 helper

---

## 5. 批次 P4 — 商业化(文档 31 落地)

**依据**:文档 31(免费版与 Pro 版规划)

**价值**:把 Browser Brain 作为可售卖的专精大脑,和 Claude CUA / Browser Use 竞争。

### 5.1 License gate

**当前状态**:部分完成

- `sdk/tool/browser_feature_gate.go` 已对 `browser.understand / browser.pattern_match / browser.visual_inspect` 加了运行时 feature gate。
- `cmd/brain serve` 已支持通过 `BRAIN_BROWSER_FEATURES` 注入 feature 集。
- `sdk/sidecar/RunLicensed(...)` 也能把真实 `brainlicense.Result.Features` 投影到 `tool.SetBrowserFeatureGate(...)`。
- `brains/browser/cmd/main.go` 现已把 `license.CheckSidecar("brain-browser", ...)` 返回的 `*license.Result` 投影到 `tool.ConfigureBrowserFeatureGateFromLicense(...)`。
- 因此“真实 license 结果 → browser runtime gate”在 browser 正式启动入口上已闭环;当前剩余工作是统一更多宿主/runtime 的下发口径,不是补 browser 单点缺口。

**执行 checklist**:

- `[x]` 三个重工具的 gate wrapper 已实现
- `[x]` env → `BrowserFeatureGate` 已打通
- `[x]` `RunLicensed(...)` helper 已支持真实 license result 投影
- `[x]` `brains/browser/cmd/main.go` 已使用真实 `license.Result` 下发 gate
- `[ ]` 更多 browser runtime/host 统一使用同一套 license→feature gate 注入口径

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

## 9. easymvp-brain 消费接口现状

> 依据 `/www/wwwroot/project/easymvp/docs/钱学森总纲设计/easymvp-brain-职责与边界定义.md` 和 `EasyMVP-中央大脑与四专精大脑IO合同及升级规则.md`。

### 9.1 背景

EasyMVP 定义了一个**领域专精大脑** `easymvp-brain`,它消费 brain-v3 的四个基础专精大脑(code / browser / verifier / fault)的结果。消费方式是:

```
brain-v3 central + (code/browser/verifier/fault)
   ↓ runtime adapter 归一化
EasyMVP 领域层 → easymvp-brain(审核/编译/裁决/返工/验收规则)
```

### 9.2 brain-v3 侧已提供/仍需扩展的接口

Browser Brain 作为被消费者,需要让 runtime adapter 能取到:

1. **RunResult 归一化**:sidecar `ExecuteResult` 已有 `status / summary / error / turns`,满足 EasyMVP `RunResult` 的映射需求。**已落地**。

2. **DeliveryResult 结构化**:`ExecuteResult.Artifacts []ArtifactRef` 已实现,sidecar 会从真实 `tool_result` 中提取结构化产物引用,避免 EasyMVP 解析自然语言 `Summary`。当前已覆盖:

   - browser: `screenshot / snapshot / semantic_annotations / download / storage_state / upload_file / frame_snapshot / response_body / anomaly_report / route_patterns / pattern_catalog / network_trace / network_entry / tab_catalog / frame_catalog / form_fill_results / page_ref`
   - verifier proxy: `screenshot / page_ref / tab_catalog / upload_file`(`verifier.browser_action`)
   - verifier/code: `diff / file / file_list / search_matches`

   **已落地**,且已不再局限于纯 browser 截图类产物;后续仍可继续扩到更多“可持久化/可复核”的 artifact 类型。

3. **VerificationResult**:`ExecuteResult.Verification` 已实现,当前会把多个真实验收来源聚合到同一结构:

   - `browser.pattern_exec` → `pattern_id / post_conditions / success`
   - `browser.check_anomaly / browser.check_anomaly_v2` → `page_health / anomalies`
   - `wait.network_idle` → `status / idle_ms / forced`
   - `browser.upload_file` → `status / files_attached`
   - `browser.select` → `status / value / text / index`
   - `verifier.browser_action(wait|upload_file|select)` → 复用 browser 侧真实验收 schema
   - `verifier.check_output` → `match`
   - `verifier.run_tests` → `passed / timed_out / exit_code`

   聚合规则是按实际 `tool_result` 出现顺序收集 checks,整体 `Passed` 取 AND。**已落地**,但仍主要覆盖 browser + verifier 两侧,其他工具的验收信号还没统一并入。

4. **FaultSummary**:`ExecuteResult.FaultSummary *FaultSummary` 已实现,当前会从失败 `tool_result`、`_anomalies` 以及终态 `TurnResult.Error` 归一化提取 `code / message / route / anomalies`。当前已覆盖:

   - `browser.pattern_exec` 的 `abort / retry / fallback / human_intervention`
   - `browser.check_anomaly(_v2)` 的 `page_health / anomalies`
   - `verifier.check_output` 的 `verification_failed`
   - `verifier.run_tests` 与命令类工具的 `non-zero exit / timeout`
   - license / budget / policy / loop 类结构化错误的稳定 `route`

   **已落地**,但仍是 best-effort 归一化,还没有做到所有工具统一 fault schema。

5. **RuntimeEscalation**:当 Browser Brain 调 `human.request_takeover` 时,EasyMVP 需要知道这是一次 escalation。当前 `task.human.requested / resumed / aborted` 已发布稳定 envelope:`schema / schema_version / event_name / escalation`,同时保留旧顶层字段兼容旧消费者。**已落地**。

6. **HumanEventSourceFactory**:工具层 recorder 和 host/child 两侧装配都已存在。`browser sidecar` 启动时会在 child process 内装配基于共享 session 的 `HumanEventSourceFactory`;若 session 尚未初始化则安全退化为 marker-only。**已进入主路径**。

7. **Serve 侧相关 runtime 接线**:`cmd/brain` 已真实注入 `SetSitemapCache / SetHumanDemoSink / SetPatternFailureStore / SetHumanEventSourceFactory`;`brains/browser/cmd/main.go` 也已在 child process 内打开默认 `brain.db` 或 `BRAIN_DB_PATH` 指向的库,并装配对应的 `LearningStore` 适配器,使 `SitemapCache / HumanDemoSink / PatternFailureStore / anomaly templates` 在 browser sidecar 主路径生效。host 侧还会周期发布 `browser-runtime.sync.json` 投影文件,已运行 child 会按 `Version` 刷新 feature gate / anomaly templates / PatternLibrary。**已进入主路径**。

### 9.3 当前状态与后续边界

上述接口里,`Artifacts / Verification / FaultSummary` 和 RuntimeEscalation 稳定 envelope 都已进入当前 sidecar 代码主路径。

当前代码主路径已具备:

- anomaly template / sitemap cache / pattern failure / human demo 可在默认持久化路径或 `BRAIN_DB_PATH` 指向的库下由 browser child process 自行装载或写回
- pattern library 与 anomaly templates 具备 child 进程内刷新路径
- `BRAIN_UI_PATTERN_DB_PATH` 可覆盖默认 `ui_patterns.db`
- host 会把 persistence + feature gate 统一投影到 `browser-runtime.sync.json`,已运行 child 通过 `Version` 进行热刷新

当前仍保留的后续工作主要是**商业化/扩面**,不是主路径缺失:

> 后续扩展:继续补齐更多 artifact 类型、把更多工具的 verification 结果统一收敛、逐步收紧 fault schema 的一致性,让 EasyMVP runtime adapter 的映射更完整。

建议作为下一批明确计划进入文档:

- **P4 接口扩面 1 / Artifacts**:继续只收“产物语义”明确的结果,优先补更多可持久化文件/报告类输出,避免把普通查询结果误判成 artifact。
- **P4 接口扩面 2 / Verification**:把更多非 browser / verifier 的显式 pass-fail 信号统一并入 `ExecuteResult.Verification`,继续保持“只基于真实 schema,不猜字段”。
- **P4 接口扩面 3 / Fault Schema**:继续把 fault code / route / message 的归一化从 best-effort 收紧成更稳定的跨工具契约。
- **P4 商业化扩展 / License gate**:当前 `serve`、`RunLicensed`、browser 正式启动入口、host→child runtime projection 已共用同一套 browser feature gate 投影;后续只剩更多商业版 feature、额度和计量能力的扩展。

### 9.4 当前未完成项与优先级

> 下面只列**截至当前代码状态仍未完成**的事项,不再重复列已经接线完成的历史项。

| 优先级 | 项 | 类型 | 当前状态 | 说明 |
|---|---|---|---|---|
| **P0** | M1 阶段 0 真人实验 | 人工 | 未启动 | `41-语义理解阶段0实验设计.md` 定义的 10 样本人工标注与 Go/No-Go 报告,不是代码任务,但仍是方法论准入项。 |
| **P4** | Artifacts / Verification / FaultSchema 继续扩面 | 代码 | 后续优化 | 当前主路径已落地并进入 sidecar 输出合同;后续只做覆盖面扩展,不再是“缺功能”。 |
| **P4** | 商业化能力落地 | 代码 | 未完成 | License gate 主路径已统一,但 Pro feature、额度池、计量与审计仍属于下一商业化批次。 |

### 9.5 代码一致性 checklist

> 用于后续每次更新本文时快速核对“文档说已完成”的项,是否真的进入了生产路径。

- `[x]` `Artifacts / Verification / FaultSummary` 已进入 `sdk/sidecar/loop.go` 主路径
- `[x]` `RuntimeEscalation` 事件 envelope 已发布
- `[x]` `PatternFailureStore` 与 split 扫描已在 host `serve` 里装配
- `[x]` split 扫描结果能被**已运行** browser sidecar 在后续请求中刷新看到
- `[x]` host 能从 `LearningStore` 读取 anomaly templates
- `[x]` browser sidecar 启动时自动装载同一份 anomaly template 运行时库
- `[x]` host `serve` 已装配 `HumanEventSourceFactory`
- `[x]` delegated browser sidecar 已真实消费 `HumanEventSourceFactory`
- `[x]` env → browser feature gate 已打通
- `[x]` `RunLicensed(...)` helper → browser feature gate 已打通
- `[x]` browser 正式启动入口已消费真实 `license.Result` 并下发 gate

---

## 10. 与其他文档的关系

| 文档 | 关系 |
|---|---|
| [`40-Browser-Brain语义理解架构`](./40-Browser-Brain语义理解架构.md) | 主架构,本文的阶段 0-3 分别对应本文 P0-P3 |
| [`41-语义理解阶段0实验设计`](./41-语义理解阶段0实验设计.md) | 必做项 M1 的执行说明 |
| [`42-Browser-Brain异常感知层设计`](./42-Browser-Brain异常感知层设计.md) | §5 模式分裂 + §7.3 异常 v3 → P3.1 / P3.2 |
| [`43-Browser-Brain必做项`](./43-Browser-Brain必做项.md) | 前置必做项,P1 开工前必须全清 |
| [`31-browser-brain-免费版与Pro版规划`](./31-browser-brain-免费版与Pro版规划.md) | P4 的商业化依据 |
