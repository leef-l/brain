# 31. Browser Brain 免费版与 Pro 版规划

> **⚠️ 路径迁移说明（2026-04-24）：** 代码路径已从 `/www/wwwroot/project/brain/cmd/brain-browser/` 迁移到 `brain-v3/brains/browser/cmd/`，工具代码从 `brain/tool/` 迁移到 `brain-v3/sdk/tool/`。

> 目标：明确 `brain-browser` 当前免费版边界，以及未来 `brain-browser-pro`
> 的收费能力范围，避免产品线混乱。

---

## 1. 结论

现在的 `brain-browser` 已经不是 demo 版，而是**通用可用版**。

按 v3 术语来讲，当前免费版浏览器大脑可以理解为：

- 一个 `Browser Brain`
- 当前主实现是 `native runtime`
- 后续最可能演进到 `hybrid runtime`
- 正式交付时建议采用 `Brain Package`

当前已实现的免费能力覆盖：

- 导航
- 点击 / 双击 / 右键
- 输入 / 按键
- 滚动 / hover / drag
- 下拉选择 / 文件上传
- 截图
- JS 执行
- 显式等待
- 多标签基础操作

代码入口：

- [`cmd/brain-browser/main.go`](/www/wwwroot/project/brain/cmd/brain-browser/main.go:1)
- [`tool/builtin_browser.go`](/www/wwwroot/project/brain/tool/builtin_browser.go:56)

这意味着：

- 免费版已经足够做大量网页自动化任务
- Pro 版不应该只是“把现有能力再包装一遍”
- Pro 版应该卖的是**更深的浏览器工程能力、团队能力、证据能力、企业能力**

更进一步说：

- 免费版的长期路线适合保持 `native` 或轻量 `hybrid`
- Pro 版更适合明确走 `hybrid runtime + paid package`

---

## 2. 当前免费版能力边界

当前 `brain-browser` 免费版建议稳定为下面这 15 个核心工具：

1. `browser.open`
2. `browser.navigate`
3. `browser.click`
4. `browser.double_click`
5. `browser.right_click`
6. `browser.type`
7. `browser.press_key`
8. `browser.scroll`
9. `browser.hover`
10. `browser.drag`
11. `browser.select`
12. `browser.upload_file`
13. `browser.screenshot`
14. `browser.eval`
15. `browser.wait`

现有实现位置：

- [`tool/builtin_browser.go`](/www/wwwroot/project/brain/tool/builtin_browser.go:60)

### 2.1 免费版定位

免费版应定位为：

- 通用网页操作
- 通用 UI 自动化
- 中低复杂度页面任务
- 单 run 内的基础浏览器执行

### 2.2 免费版不必继续无限堆功能

免费版后续可以继续补一些“基础完整性”功能，但不建议把所有高级能力都塞进去。

适合继续留在免费版的，通常是：

- 稳定性增强
- 兼容性增强
- 基础体验增强

例如：

- selector 稳定性更好
- 更可靠的 wait 条件
- 更好的 screenshot 输出
- 更稳的 tab 管理

---

## 3. Pro 版应该卖什么

Pro 版建议卖的是下面 **6 类能力**(**2026-04-20 更新**:新增"语义理解"作为核心差异化)。

### 3.0 语义理解层(核心差异化,2026-04-20 新增)

> 这是 Pro 版**最重要的商业化差异**。
> 免费版是"**能操作浏览器**",Pro 版是"**能理解浏览器在做什么**"。
>
> 详细技术架构见:
> - [`40-Browser-Brain语义理解架构`](./40-Browser-Brain语义理解架构.md)
> - [`41-语义理解阶段0实验设计`](./41-语义理解阶段0实验设计.md)
> - [`42-Browser-Brain异常感知层设计`](./42-Browser-Brain异常感知层设计.md)

语义理解层包含三大工具,适合整包销售:

**(a) 元素语义理解 `browser_pro.understand`**
- 为每个可交互元素推断 `intent / reversibility / risk / flow_role / predicted_network`
- 基于站点预处理缓存,Pro 版附带云端模式库
- 价值:让 Agent 从"按结构操作"升级为"按意图操作",任务成功率提升 30-50%

**(b) UI 模式库 `browser_pro.patterns`**
- 跨会话积累可复用模式(登录/结算/搜索/加购...)
- 熟悉页面秒级执行,不熟悉页面回落到 LLM 推理
- 价值:重复任务的 turn 数从 10+ 降到 1-2,成本下降 80%+
- **差异化护城河**:使用越多,模式库越强,客户越离不开

**(c) 异常感知层 `browser_pro.anomaly`**
- 自动识别 modal、错误、登录过期、captcha、风控、白屏六类异常
- 分级处理,不可自动时老实上报
- 价值:真实网页交互 30-40% 的 turn 花在异常处理上,这层直接决定 Agent 生产可用性

**商业化组合**:
```json
{
  "features": {
    "browser-pro.semantic_understanding": true,    // a
    "browser-pro.pattern_library": true,           // b
    "browser-pro.anomaly_awareness": true          // c
  }
}
```

三个可独立售卖,也可打包"智能包"。**建议第一期主推**,因为它的价值 LLM 用户能立刻感受到(成功率、效率、成本)。

---

### 3.1 证据与调试能力

这类能力非常适合企业付费，因为它们直接服务于：

- 自动化回溯
- 故障定位
- 审计留档
- CI 报告

建议工具：

- `browser.trace.start`
- `browser.trace.stop`
- `browser.trace.export`
- `browser.console.logs`
- `browser.network.capture`
- `browser.network.har_export`
- `browser.page.snapshot`
- `browser.dom.dump`
- `browser.video.start`
- `browser.video.stop`

### 3.2 测试与断言能力

这是最容易形成商业价值的一层。

建议工具：

- `browser.assert.visible`
- `browser.assert.hidden`
- `browser.assert.text`
- `browser.assert.url`
- `browser.assert.count`
- `browser.assert.network`
- `browser.assert.js`
- `browser.expect.download`

免费版能“操作浏览器”，Pro 版则能“可靠验收浏览器行为”。

### 3.3 会话与环境控制能力

企业用户往往需要更可控的浏览器上下文。

建议工具：

- `browser.session.save`
- `browser.session.load`
- `browser.cookies.get`
- `browser.cookies.set`
- `browser.storage.get`
- `browser.storage.set`
- `browser.context.new`
- `browser.context.close`
- `browser.emulate.device`
- `browser.emulate.viewport`
- `browser.emulate.geolocation`
- `browser.emulate.timezone`

### 3.4 更强的网络与页面控制

这部分是高级自动化和测试平台最需要的。

建议工具：

- `browser.route.block`
- `browser.route.mock`
- `browser.request.replay`
- `browser.download.wait`
- `browser.pdf.print`
- `browser.file.downloads`
- `browser.tab.pin`
- `browser.tab.group`
- `browser.multi_tab.plan`

### 3.5 工作流与录制能力

这是最容易从“工具”进化到“产品”的部分。

建议工具：

- `browser.record.start`
- `browser.record.stop`
- `browser.record.export`
- `browser.workflow.replay`
- `browser.workflow.assert`
- `browser.workflow.parameterize`

---

## 4. 免费版 / Pro 版推荐边界

### 4.1 免费版保留

免费版保留“浏览器执行基础面”：

- 页面打开与导航
- 鼠标键盘交互
- 表单操作
- 截图
- JS 执行
- 等待
- 基础 tab 操作

### 4.2 Pro 版新增

Pro 版新增“工程化能力”：

- trace / console / network / video / snapshot
- assert / expect
- session / context / device / geo / timezone
- route mock / request replay / download / pdf
- recording / replay / workflow

### 4.3 不建议的拆法

不建议这样拆：

- 免费版只有 `open/click/type`
- 其他都收费

这种拆法会让免费版太弱，最终反而不利于生态扩张。

也不建议这样拆：

- 免费版和 Pro 版都有一套几乎一样的工具
- 只是 Pro 版多一点性能优化

这种拆法边界不清，销售和技术都容易混乱。

---

## 5. 推荐产品形态

建议最终分成两个 package：

### 5.1 免费版

- package：`leef-l/browser`
- runtime：`brain-browser`

定位：

- 开源
- 通用网页自动化
- 基础浏览器交互执行
- Manifest 里 `license.required = false`

### 5.2 付费版

- package：`leef-l/browser-pro`
- runtime：`brain-browser-pro`

定位：

- 商业版 brain package
- 企业自动化 / 测试 / 审计 / 证据采集
- 更强控制面和可观测性
- Manifest 里 `license.required = true`
- runtime 建议走 `hybrid`

---

## 6. 推荐工具命名前缀

为了边界清楚，建议不要把 Pro 功能继续混在裸 `browser.*` 下无限扩张。

有两种可行方案。

### 方案 A：继续共用 `browser.*`

优点：

- 对模型更自然
- 用户心智简单

缺点：

- 免费版和 Pro 版边界容易模糊

适合：

- 你希望 Pro 版看起来像“浏览器大脑增强包”

### 方案 B：Pro 明确走 `browser_pro.*`

例如：

- `browser_pro.trace_export`
- `browser_pro.assert_text`
- `browser_pro.session_save`

优点：

- 边界清晰
- 授权控制更直观
- 文档和销售更好讲

缺点：

- 名称稍长

**建议**：第一版商业化优先用 `browser_pro.*`，更稳。

---

## 7. 推荐的 Pro 首发功能包

**2026-04-20 更新**:先做 **4 组**最值钱的,**智能包放在最前**(核心差异化)。

### 7.1 智能包(最优先,核心差异化)

- `browser_pro.understand`       — 元素语义理解
- `browser_pro.patterns_match`   — UI 模式匹配
- `browser_pro.patterns_learn`   — 模式学习(从成功交互沉淀)
- `browser_pro.anomaly_check`    — 异常感知

**为什么放第一**:这是**用户立刻能感受到差异**的包——Pro 版 Agent 完成同样任务 turn 数减半、成功率提升、能处理免费版卡住的异常页面。其他三个包是"企业深度用户"才关心的,这个包是"所有用户都关心"的。

### 7.2 证据包

- `browser_pro.console_logs`
- `browser_pro.network_capture`
- `browser_pro.har_export`
- `browser_pro.page_snapshot`

### 7.3 断言包

- `browser_pro.assert_visible`
- `browser_pro.assert_text`
- `browser_pro.assert_url`
- `browser_pro.assert_network`

### 7.4 会话包

- `browser_pro.session_save`
- `browser_pro.session_load`
- `browser_pro.cookies_get`
- `browser_pro.cookies_set`

这是最适合第一阶段商业化的 **16** 个能力(智能 4 + 证据 4 + 断言 4 + 会话 4)。

---

## 8. 与授权模型怎么结合

这份规划和 [`30-付费专精大脑授权方案`](./30-付费专精大脑授权方案.md) 可以直接配合：

```json
{
  "allowed_brains": ["browser-pro"],
  "features": {
    "browser-pro.intelligence": true,
    "browser-pro.evidence": true,
    "browser-pro.assertions": true,
    "browser-pro.sessions": true
  }
}
```

然后在 `brain-browser-pro` runtime 启动时决定注册哪些工具。

例如：

- `browser-pro.intelligence = true` → 注册 understand/patterns/anomaly(**智能包,核心差异化**)
- `browser-pro.evidence = true` → 注册 trace/network/snapshot
- `browser-pro.assertions = true` → 注册 assert 系列
- `browser-pro.sessions = true` → 注册 session/cookies/storage 系列

---

## 9. 一句话边界

`brain-browser` 免费版负责"**把浏览器用起来**"——结构感知 + 动作执行。
`brain-browser-pro` 负责"**把浏览器用对**"——语义理解 + 模式复用 + 异常感知 + 企业级证据/断言/会话。

更精准地说(**2026-04-20 补充**):
- 免费版做 **L1-L3**(视觉/符号/语法)
- Pro 版做 **L4-L8**(流程/状态/后果/风险/意图对齐)
- **L9(世界模型)是 AGI 问题,两版都做不到**,但 Pro 版通过模式库和云端知识库**逼近**它

如果按 v3 长期架构表达：

- `browser` 是免费 Brain Package
- `browser-pro` 是付费 Brain Package
- 免费版偏 `native`
- Pro 版优先 `hybrid`
