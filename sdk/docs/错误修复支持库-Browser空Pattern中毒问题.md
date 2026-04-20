# 错误修复支持库：Browser 空 Pattern 中毒导致导航失效

## 问题编号
BUG-2026-0420-BROWSER-POISON-PATTERN

## 症状
- `central.delegate` 委派 browser 专家后，浏览器始终停在 `about:blank`
- browser sidecar 返回 `status=completed`，但页面实际未导航
- 返回的 snapshot 内容为空白页，没有任何有效元素
- LLM 报告"浏览器服务不可用"或"页面始终是空白页"

## 根因分析

### 直接原因
`ui_patterns.db` 中存在一个有毒的 learned pattern `reloaded_variant`：

```json
{
  "id": "reloaded_variant",
  "category": "auth",
  "applies_when": {},
  "action_sequence": null,
  "source": "learned"
}
```

**三重缺陷叠加**：

1. **`applies_when` 为空** → `evaluateMatch()` 在所有条件检查都跳过后，`reasons` 为空，返回 `true, "default"`，导致该 pattern 匹配任何页面（包括 about:blank）
2. **`action_sequence` 为 null** → `pattern_exec` 执行时什么都不做，直接返回当前页面 snapshot
3. **`pattern_exec` 返回 completed** → `executeWithPerception` 认为任务完成，跳过 LLM plan 和 fallback agent loop

### 来源
该 pattern 由测试 `TestBrowserRuntimeReloader_MaybeRefreshReloadsAnomalyTemplates` 创建，但通过共享的 `ui_patterns.db` 泄漏到了生产路径。

### 为什么删除 `--disable-background-networking` 不是修复
`--disable-background-networking` 只影响 Chrome 的后台预加载（自动更新检查、安全浏览列表等），不影响 `Page.navigate` 的正常导航。直接用 Go 程序调用 CDP Navigate 测试确认 chromium 能正常打开百度。

## 修复方案

### 1. 防止保存空 pattern（`sdk/tool/ui_pattern.go`）
```go
func (lib *PatternLibrary) Upsert(ctx context.Context, p *UIPattern) error {
    if len(p.ActionSequence) == 0 && p.AppliesWhen.IsEmpty() {
        return fmt.Errorf("pattern %s has empty applies_when and no action_sequence, refusing to save", p.ID)
    }
    // ...
}
```

新增 `MatchCondition.IsEmpty()` 方法：
```go
func (m *MatchCondition) IsEmpty() bool {
    return m.URLPattern == "" && m.SiteHost == "" &&
        len(m.Has) == 0 && len(m.HasNot) == 0 &&
        len(m.TitleContains) == 0 && len(m.TextContains) == 0
}
```

### 2. 拒绝匹配空条件 pattern（`sdk/tool/ui_pattern_match.go`）
```go
// 修改前：空 reasons 返回 true（匹配一切）
if len(reasons) == 0 {
    return true, "default"  // BUG: 导致空 pattern 匹配所有页面
}

// 修改后：空 reasons 返回 false（没有正面信号就不匹配）
if len(reasons) == 0 {
    return false, ""
}
```

### 3. pattern 执行失败时自动删除 learned pattern（`brains/browser/cmd/main.go`）
```go
// executeWithPerception 中，pattern 执行失败时删除 + 继续降级
if patternID != "" {
    result := h.executePattern(ctx, registry, patternID, req)
    if result.Status == "completed" {
        return result
    }
    h.deleteLearnedPattern(ctx, patternID)  // 失败就删
    // 继续走 LLM plan → fallback agent loop
}
```

### 4. 三层降级策略完整保证
```
Step 1: pattern_match → 成功返回 / 失败删除 learned pattern → 继续
Step 2: LLM plan     → 成功返回 + 保存 learned pattern / 失败 → 继续
Step 3: fallback agent loop → 多轮 LLM 对话兜底，保证不会失败
```

## 涉及文件

| 文件 | 修改内容 |
|------|----------|
| `sdk/tool/ui_pattern.go` | 新增 `MatchCondition.IsEmpty()`；`Upsert` 拒绝空 pattern |
| `sdk/tool/ui_pattern_match.go` | 空 `AppliesWhen` 不再匹配 |
| `brains/browser/cmd/main.go` | pattern 失败删除 + 诊断日志 + `deleteLearnedPattern` |
| `sdk/tool/cdp/session.go` | 移除 `--disable-background-networking`（不是根因但属清理） |
| `sdk/tool/ui_pattern_enabled_test.go` | 测试 pattern 加 AppliesWhen |
| `sdk/tool/ui_pattern_index_test.go` | 测试 pattern 加 AppliesWhen |
| `brains/browser/cmd/main_test.go` | `reloaded_variant` 测试 pattern 加合法字段 |
| `sdk/kernel/orchestrator_process_test.go` | browser delegate 测试 queue LLM plan response |

## 诊断方法

遇到类似"浏览器总是返回空白页"时的排查步骤：

1. **查看 diagnostics.log**：`tail -50 ~/.brain/logs/diagnostics.log`
   - 看 `pattern_match: patternID=` 字段，确认是否有异常 pattern 被匹配
   - 看 `pattern_exec result: status=` 确认 pattern 执行状态

2. **检查 ui_patterns.db**：
   ```bash
   sqlite3 ~/.brain/ui_patterns.db "SELECT id, source, body FROM ui_patterns WHERE source='learned'"
   ```
   - 检查是否有 `applies_when: {}` 或 `action_sequence: null` 的 pattern

3. **直接测试 CDP**：写一个小 Go 程序直接调用 `cdp.NewBrowserSession` + `sess.Navigate` 来排除 chromium 本身的问题

4. **看 browser sidecar 日志**：`cat ~/.brain/logs/browser.log`
   - 如果只有 `ready` 没有工具执行日志，说明 sidecar 进程被重启或 RPC 异常

## 教训

1. **空匹配条件是危险的**：任何"匹配一切"的 pattern 都应被视为无效
2. **learned pattern 必须有完整性校验**：`Upsert` 入口必须拒绝不完整的 pattern
3. **测试不应向共享数据库写入数据**：测试中创建的 pattern 应在隔离的临时 DB 中
4. **三层降级必须贯通**：pattern 失败不应阻断 LLM plan 和 agent loop 路径
