# E2E 长链路评测集

Browser Brain 的 **跨页长链路** 压测,对齐 `sdk/docs/40-Browser-Brain语义理解架构.md` §6.1 阶段 2 指标。

和 `sdk/test/webarena` 的分工:
- `webarena/` —— 每条任务单目标(登录 / 搜索 / 单表单),低耦合回归门。
- `e2e_longchain/` —— 每条任务是 **5-10 步跨页链路**,压测 context 传递 + 模式库跨 URL 迁移 + 异常恢复。

## 目录结构

```
e2e_longchain/
├── README.md           本文件
├── tasks/              10 条长链路任务 JSON
├── runner.go           Task/Step 结构 + LoadTasks + Run
├── report.go           Summary + report.md 输出
├── runner_test.go      CI 门:结构校验 + mock executor 跑通
├── gen_report_test.go  -tags e2e_longchain_report 刷新 baseline report.md
└── report.md           基线报告(mock executor 跑出来的,版本化提交)
```

## 10 条任务设计(对齐 TaskList #10 description)

| # | ID | 压测目标 |
|---|---|---|
| 01 | gitea_search_star | auth → search → star(跨 P1 模式) |
| 02 | opencart_cart_checkout | P1.2 commerce 全链路 |
| 03 | wordpress_post_publish | login + admin CRUD |
| 04 | saleor_register_buy | auth → ecommerce 接力(跨 URL 迁移) |
| 05 | adminlte_bulk_delete | auth + admin bulk action |
| 06 | session_expired_relog | **on_anomaly 路由到 fallback_pattern** |
| 07 | long_multipage_form | 10 页分步表单(context / token budget 临界) |
| 08 | wishlist_similar_search | fallback_pattern(out-of-stock) |
| 09 | admin_filter_export_csv | `browser.downloads` 感知 |
| 10 | captcha_human_handoff | `human_intervention` → #16 coordinator 放行 |

## 核心指标(对齐文档 40 §6.1)

- 长链路任务成功率 **≥ 65%**
- 平均 turn 数 **≤ 10**
- Token budget 临界 **~ 100k**(`long_multipage_form` 设 90000 试探)
- 异常恢复率:对任一带 `expect_anomaly` 的步骤,`on_anomaly` 必须按 `expect_recovery` 路由到对应 handler

## 任务 JSON 字段

```json
{
  "id": "01_gitea_search_star",
  "description": "...",
  "site": "https://demo.gitea.com",
  "max_turns": 10,
  "token_budget": 40000,
  "steps": [
    {
      "name": "login",
      "url_hint": "/user/login",
      "category": "auth",
      "pattern_id": "login_username_password",
      "goal": "Log in",
      "expect_anomaly": "session_expired",
      "expect_recovery": "fallback_pattern"
    }
  ],
  "success": [
    { "kind": "url_contains", "value": "dashboard" }
  ],
  "tags": ["longchain", "phase2"]
}
```

- `steps[*].pattern_id` —— 这一步期望命中的 UIPattern ID。Executor 用真实 `PatternLibrary` 匹配,把命中的 ID 回填到 `StepResult.MatchedID`。期望 vs 命中对照就是"模式库跨 URL 迁移"的度量。
- `expect_anomaly` / `expect_recovery` —— 在该步故意注入异常(fixture 层 / pattern fault injection 层 / mock coordinator),验证 `sdk/tool/builtin_browser_anomaly_v2.go` 的 `RouteAnomalyForPattern` 是否按 `on_anomaly` 路由到预期 handler。
- `success` —— 全链路终态检查,沿用 `webarena` 的 `kind/value` 风格以便共享校验器。

## 失败分类铁律

所有失败必须归到 `sdk/errors.ErrorClass` 六值之一:
- `transient` / `permanent` / `user_fault` / `quota_exceeded` / `safety_refused` / `internal_bug`

`Executor` 在返回 `ChainResult` 时若 `Success=false`,**必须**填 `FailClass`;mock 兜底给 `ClassInternalBug`(见 `runner.go:classifyOrDefault`)。

## 跑法

**CI(默认,mock executor,不起浏览器)**:

```bash
go test ./sdk/test/e2e_longchain/ -count=1
```

**刷新 baseline report.md**(仍是 mock,但会覆盖仓库里的 report.md):

```bash
go test -tags e2e_longchain_report ./sdk/test/e2e_longchain/ -run TestGenerateBaselineReport -count=1
```

**接真 Browser Brain 跑**(集成方负责):

```go
import "github.com/leef-l/brain/sdk/test/e2e_longchain"

tasks, _ := e2e_longchain.LoadTasks("sdk/test/e2e_longchain/tasks")
results := e2e_longchain.Run(ctx, tasks, myBrainExecutor{})  // 实现 Executor
s := e2e_longchain.Summarize(results)
e2e_longchain.WriteMarkdown("report.md", results, s)
```

## Executor 实现提示

- **Session 复用**:每条 Task 用同一个 BrowserContext 跑所有 Step,验证 cookie / localStorage / cart_id 在 turn 之间保持。
- **Pattern 匹配**:Step 到达时调 `PatternLibrary.List` 对当前 URL 匹配,若命中的 ID ≠ `step.PatternID` 不直接算失败(自学到的新 pattern 可能接力),而是回填 `MatchedID`,由报告对比。
- **异常注入**:`expect_anomaly=session_expired` 的 step 可在 fixture 里主动清掉 session cookie 再执行;`captcha` 可在 fixture 里塞一个假 captcha 元素。
- **Token 预算**:每 turn 统计 prompt+completion token,累加到 `TokensUsed`;超过 `TokenBudget` 应触发 `ClassQuotaExceeded` 失败,而不是 `internal_bug`。
