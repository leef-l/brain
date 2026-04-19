# 42. Browser Brain 异常感知层设计

> **问题**:真实网页交互中,**大量 turn 花在处理失败而不是成功**。
> 弹窗、错误、登录过期、captcha、风控限流、白屏——这些是 Agent 任务失败的主要原因,
> 但 [`39-Browser-Brain感知与嗅探增强设计`](./39-Browser-Brain感知与嗅探增强设计.md) 和主流方案都**几乎只谈成功路径**。
>
> 本文档定义 **Browser Brain 的异常感知层**——独立、主动、低开销的一套机制,
> 让 Agent 不再被异常"突袭"。
>
> 它是 [`40-Browser-Brain语义理解架构`](./40-Browser-Brain语义理解架构.md) §4 的展开和实现细节。

---

## 1. 为什么单独拎出来

### 1.1 统计事实

观察真实 Agent 交互会发现:
- **约 30-40% 的 turn** 花在处理 modal、错误提示、captcha 等"计划外"情况
- **约 20%** 的任务失败原因是"agent 没看到异常或错判异常"(点了不该点的"取消"、漏了关键"确认")
- 异常处理的**碎片化成本极高**——每个异常要单独写一套识别+处理逻辑,代码膨胀

### 1.2 当前规划的空白

| 规划 | 覆盖异常 |
|---|---|
| 39 号 snapshot | 能"看到"异常元素但不"识别" |
| 39 号 network | 能看到 4xx/5xx 响应但不关联 UI |
| 40 号 understand | 关注成功路径的元素语义 |
| 40 号 patterns | 模式是"成功流程"的抽象 |

**异常感知是一个横切关注点,必须单独设计**。

### 1.3 异常 vs 错误

重要区分:
- **异常**(anomaly):**Agent 需要意识到并采取特殊动作**的状态。不是程序错误。
- **错误**(error):Agent 动作本身失败(click 的坐标不存在)。这由 39 号工具错误结构化处理。

本文只管前者。

---

## 2. 六类异常及识别特征

### 2.1 Modal / Dialog 阻塞

**识别**:
- `[role=dialog]` 或 `[role=alertdialog]`
- 全屏遮罩层(`z-index > 1000 + position:fixed + 覆盖 viewport 50%+`)
- 浏览器原生 `alert / confirm / prompt`(CDP: `Page.javascriptDialogOpening` 事件)
- 特定类名(`.modal`, `.dialog`, `.popup`, `.overlay`)

**子类**:
| 子类 | 示例 | Agent 策略 |
|---|---|---|
| 信息弹窗 | "您有新消息" | 关闭,继续任务 |
| 确认弹窗 | "确定删除?" | 根据任务意图决策 |
| 阻塞弹窗 | Cookie consent、GDPR | 通常点"同意"或"拒绝非必要" |
| 广告弹窗 | 订阅推荐 | 关闭 |
| 会话过期弹窗 | "您的登录已过期" | 触发重新登录 |

**CDP 实现**:
```
Page.javascriptDialogOpening 事件订阅 → 把 native alert/confirm 上报
MutationObserver 监听 role=dialog 元素插入
```

### 2.2 错误提示 Banner / Toast

**识别**:
- `[role=alert]`
- 红色 + 感叹号图标(视觉启发式)
- 特定类名(`.error`, `.alert-danger`, `.notification-error`, `.toast-error`)
- 常见关键词(error, failed, invalid, 失败, 错误, 无效)

**关键信息提取**:
- 错误文本(通常 1-3 句)
- 出现位置(关联到上一个动作的哪个元素)
- 持续时间(toast 几秒就消失,banner 持久)

**价值**:Agent 点了"登录"后看到"密码错误",应该停止重试避免锁号。

### 2.3 登录过期 / 会话失效

**识别**(多信号协同):
- 301/302 重定向到 `/login`, `/signin`, `/auth`
- 401/403 响应
- 页面 URL 从 `/dashboard` 变为 `/login`
- DOM 里出现登录表单但之前没有
- 特定文案("会话已过期", "请重新登录", "session expired")

**Agent 策略**:
- 触发"重新登录"子流程(可能调 `browser.storage.import` 恢复上次 session)
- 如果没有 stored credentials,上报用户

### 2.4 CAPTCHA / 挑战

**识别**:
- iframe[src*="recaptcha"], iframe[src*="hcaptcha"]
- iframe[src*="challenges.cloudflare"]
- 特定文案("I'm not a robot", "请拖动滑块", "验证你是真人")
- Cloudflare 的 "Checking your browser..." 页面(title 含 "Just a moment")
- 图形验证码(img 后跟 input[name="captcha"])

**子类**:
| 子类 | 可自动解决? |
|---|---|
| reCAPTCHA v2 (勾选框) | 部分(Chrome 新机可能自动过) |
| reCAPTCHA v3 (invisible) | 通常自动过(如果 stealth 做得好) |
| hCaptcha | 基本不行 |
| Cloudflare Turnstile | 部分 |
| Cloudflare JS Challenge | 等几秒自动过 |
| 图形验证码 | 不能 |
| 滑块验证 | 不能 |
| 短信验证 | 需要人工介入 |

**Agent 策略**:
- 可自动的:等或点击
- 不可自动的:**上报给用户**,不要傻试(会触发更高级别风控)

### 2.5 限流 / 风控

**识别**:
- 429 Too Many Requests
- 特定文案("操作过于频繁", "请稍后再试", "Rate limit exceeded")
- 突然出现的 captcha(强化风控信号)
- 多次 4xx 在短时间内

**Agent 策略**:
- 立即暂停当前批次操作
- 触发指数退避(wait 10s, 30s, 60s...)
- 降低并发(多 tab 操作时关掉一些)
- 超过 3 次 → 上报用户

### 2.6 页面白屏 / JS 报错 / 加载失败

**识别**:
- `document.body.innerHTML.trim() === ""`
- `document.readyState !== 'complete'` 超过 30s
- `window.onerror` 捕获到的 JS 报错
- Console 里的 error(CDP `Runtime.consoleAPICalled` 订阅)
- Network 里关键资源(`main.js`, `app.css`)失败

**Agent 策略**:
- 刷新一次
- 仍白屏 → 放弃,上报用户

---

## 3. 核心工具:`browser.check_anomaly`

### 3.1 双模式设计

**被动模式(推荐默认)**:
- 后台持续监听,检测到异常立即缓存
- 每次 Agent 动作后,自动把新异常附加到动作结果的 `anomalies` 字段
- **不增加显式工具调用次数**

**主动模式**:
- Agent 主动查询当前页面的所有异常状态
- 用于疑难场景,Agent 感觉不对时主动 check

### 3.2 输入 schema

```json
{
  "mode": "passive" | "active",         // 默认 passive
  "since_action_id": 42,                 // 可选,只看这之后的
  "filter_types": ["modal", "error"],   // 可选,只关心某类
  "min_severity": "medium"              // 可选
}
```

### 3.3 输出 schema

```json
{
  "anomalies": [
    {
      "type": "modal_blocking",
      "severity": "high",
      "subtype": "session_expired",
      "description": "A modal dialog appeared: 'Your session expired. Please log in again.'",
      "evidence": {
        "element_id": 45,
        "bbox": {"x": 400, "y": 300, "w": 500, "h": 200},
        "text": "Your session expired. Please log in again.",
        "detected_at": "2026-04-20T12:34:56Z"
      },
      "suggested_actions": [
        {"tool": "click", "target_id": 46, "description": "dismiss modal"},
        {"tool": "browser.storage.import", "description": "restore session from backup"}
      ],
      "auto_resolvable": false
    },
    {
      "type": "captcha",
      "severity": "blocker",
      "subtype": "cloudflare_turnstile",
      "description": "Cloudflare challenge detected.",
      "suggested_actions": [
        {"action": "wait_for_auto_pass", "max_wait_ms": 5000},
        {"action": "request_human", "description": "may require human intervention"}
      ],
      "auto_resolvable": "maybe"
    }
  ],
  "page_health": "healthy" | "degraded" | "blocked",
  "next_check_hint_ms": 1000
}
```

### 3.4 严重级别

| severity | 含义 | Agent 行为 |
|---|---|---|
| `info` | 纯提示,不影响流程 | 记录即可 |
| `low` | 轻微影响,可自动处理 | 尝试自动处理 |
| `medium` | 显著影响,建议处理 | 优先处理 |
| `high` | 阻塞性,必须处理 | 停下来先处理 |
| `blocker` | 无法自动,需人工 | 上报用户 |

---

## 4. 与主循环集成

### 4.1 修改 `sdk/sidecar/loop.go`

在 `RunAgentLoopWithContext` 的每个 turn **结束前**自动:

```go
// 伪代码,在 dispatchTools 之后
anomalies, err := callBrowserCheckAnomalyPassive(ctx)
if err == nil && len(anomalies.Anomalies) > 0 {
    // 把异常注入到下一 turn 的 tool_result 中
    injectAnomaliesToContext(messages, anomalies)
}
```

**关键**:异常信息以 `system` 级 message 或 `tool_result` 形式注入,**让 LLM 意识到**"页面发生了计划外的事,你要先处理它"。

### 4.2 严重级别自动升级

- `high` / `blocker`:**强制**插入到 LLM context,Agent 必须响应
- `medium`:附加但不强制
- `low` / `info`:只记录,不注入(省 token)

### 4.3 性能开销控制

被动监听的实现必须轻量:
- MutationObserver 只监听 `role=alert, role=dialog, [class*=error]` 等特定 selector,不扫全树
- Network 事件已由 39 号 §3.2 订阅,复用
- 异常检测函数每 500ms 运行一次(可配置),不同步阻塞

预期开销:**每 turn 增加 50-100ms + 100-300 tokens**。

---

## 5. 异常-模式库联动(阶段 2 对接)

40 号文档阶段 2 的模式库和异常感知协同:

### 5.1 模式的 `on_anomaly` 字段

```go
type UIPattern struct {
    // ...
    OnAnomaly map[string]AnomalyHandler  // 遇到特定异常的处理策略
}

type AnomalyHandler struct {
    Action      string  // "abort" | "retry" | "fallback_pattern" | "human_intervention"
    FallbackID  string  // 如果 action=fallback_pattern
    MaxRetries  int
    BackoffMS   int
}
```

示例:登录模式的 `OnAnomaly`:

```json
{
  "on_anomaly": {
    "error_alert_with_wrong_password": {
      "action": "abort",
      "reason": "don't retry to avoid lockout"
    },
    "captcha": {
      "action": "human_intervention"
    },
    "session_cookie_already_set": {
      "action": "fallback_pattern",
      "fallback_id": "skip_login_already_authed"
    }
  }
}
```

### 5.2 从异常学习新模式

当模式执行失败 + 检测到特定异常组合时,学习层可以:
1. 标记该模式在此页面的**成功率下降**
2. 累积一定样本后,**分裂模式**(如"登录模式"分裂出"登录模式-带 captcha 版")
3. 逐步让模式库适应真实世界的复杂度

---

## 6. 测试策略

### 6.1 异常注入测试

搭建一组**故意有异常的测试页面**:
- `test_modal.html` — 2 秒后弹 modal
- `test_error.html` — 表单提交返回 400 + 错误 banner
- `test_session_expire.html` — 5 秒后强制 redirect 到 /login
- `test_captcha_stub.html` — 嵌入假 captcha iframe
- `test_rate_limit.html` — 第 10 次请求返回 429
- `test_blank_page.html` — body 为空

### 6.2 覆盖率目标

每类异常:
- 识别率 >= 95%
- 误报率(healthy 页面误报异常)<= 2%
- 平均识别延迟 < 1s

### 6.3 回归集成

异常感知纳入 Browser Brain 的 compliance 测试,每次 CI 跑一遍上述 6 个场景。

---

## 7. 实施路线

### 7.1 最小可用版本(1 周)

- Modal 识别(MutationObserver + role=dialog)
- 错误 Banner 识别(关键词 + role=alert)
- 登录过期识别(URL 变化 + 401/403)
- `check_anomaly` 被动模式集成到主循环
- 6 个测试页面

### 7.2 完整版(2-3 周)

- CAPTCHA 识别(各子类)
- 限流识别 + 退避逻辑
- 白屏 + JS 报错识别
- 严重级别分级 + 自动升级
- 异常-模式库联动接口

### 7.3 长期

- 异常模板库(像模式库一样沉淀)
- 跨站异常模式识别(这个站的风控 vs 那个站的)
- 自动修复建议(LLM 辅助生成)

**实现状态 (P3.1,2026-04):**

| 子项 | 状态 | 实现要点 |
|---|---|---|
| 异常模板库 | ✅ 已落地 | `sdk/tool/anomaly_template.go` 定义 `AnomalyTemplate` + `AnomalyTemplateLibrary`(线程安全、带 M3 式自动停用阈值 5 次失败+成功率<0.3)。持久化走 `anomaly_templates` 表,入口 `kernel.LearningEngine.SaveAnomalyTemplate` / `GetAnomalyTemplate` / `ListAnomalyTemplates` / `DeleteAnomalyTemplate` |
| 跨站识别 | ✅ 已落地 | `anomalyHistory` 新增 `siteHist` 分桶(`anomaly_site_profile.go`),按 `site_origin` 聚合 Frequency / AvgDuration / RecoverSuccessRate。`CheckAnomaliesV2` 生成异常后顺手录一份到 siteHist,由 kernel `UpsertSiteAnomalyProfile` 落盘到 `site_anomaly_profile` 表 |
| LLM 辅助修复 | ✅ 已落地 | 新工具 `browser.request_anomaly_fix`(`builtin_browser_anomaly_fix.go`):输入当前 anomaly + 最近 3 步 + 同 host 画像,LLM 返回 recovery JSON。不自动触发,由 Agent 显式调;`RecordFixOutcome` 累计成功 ≥ 3 且成功率 ≥ 0.6 时通过 `PromoteCandidate` 固化为 `AnomalyTemplate` 入库;LLM 失败 / 解析错自动降级到 `fallbackRecovery`(captcha / session 类直接 human_intervention,其余 retry + human 兜底) |
| M5 on_anomaly 集成桥 | ✅ 已落地 | `anomaly_template_route.go#ResolveAnomalyHandler`:模板命中 → `recoveryToHandler` 翻译成既有 `AnomalyHandler`(复用 abort/retry/fallback_pattern/human_intervention 主路由),miss 回退 pattern 静态 `OnAnomaly`。真正嵌入 `runActionSequence` 的调用点由 P3.2(pattern 线)接入 |

---

## 8. 设计原则回顾

1. **独立维度**:异常感知不是"附加在感知工具上的标志",是**平行于感知的独立层**
2. **被动为主,主动为辅**:默认让 Agent 感知到而不用主动问
3. **分级处理**:不所有异常都要停下来,低级别异常让 LLM 知道就行
4. **和模式库协同**:异常是模式执行的"例外处理",在模式定义里有位置
5. **承认边界**:CAPTCHA 等不可自动解决的异常**老实上报**,别硬试

---

## 9. 与其他文档关系

| 文档 | 关系 |
|---|---|
| [`40-Browser-Brain语义理解架构`](./40-Browser-Brain语义理解架构.md) | 主架构。本文是其 §4 异常感知层的实现细节 |
| [`39-Browser-Brain感知与嗅探增强设计`](./39-Browser-Brain感知与嗅探增强设计.md) | 基础设施。本文复用其 MutationObserver 和 Network 事件订阅 |
| [`41-语义理解阶段0实验设计`](./41-语义理解阶段0实验设计.md) | 实验设计。阶段 0 可顺便评估 LLM 识别异常的准确率 |
| [`23-安全模型`](./23-安全模型.md) | 安全边界。captcha 检测涉及和风控系统交互,遵守其中外部网络策略 |

---

## 10. 总结

**异常处理不是事后打补丁,是感知层的一等公民**。

现有规划几乎把全部精力放在"成功路径如何做得更准更快",但真实网页交互里,**成功路径可能只占 60% 的 turn 时间**。剩下 40% 花在处理计划外情况——而这 40% 才是 Agent 能不能真正落地的关键。

本文定义的异常感知层:
- 让 Agent **意识到**异常(而不是"被异常打懵")
- 让 Agent **分级处理**(不浪费 token 在低优先级异常上)
- 让 Agent **承认能力边界**(不能处理的老实上报)

这是把 Browser Brain 从 demo 级推向生产级的必要一步。
