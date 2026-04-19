# Visual WebArena 视觉依赖基准

给 `browser.visual_inspect`(#12 多模态兜底工具)做 A/B 对比,
量化"纯 snapshot"vs"snapshot + visual_inspect"的成功率差,
为阶段 3 兜底策略提供 ROI 数据。

见 `sdk/docs/40-Browser-Brain语义理解架构.md` §5.1 + §3.4。

## 为什么需要它

`visual_inspect` 成本显著高于 `snapshot + understand`(约 5x token),
需要证据回答:**什么时候值得掏钱调它?**

P2.1 的 `webarena` 基准覆盖的是"DOM 可见的"语义任务,
visual 工具无用武之地。本包专门挑 **DOM 不够用** 的任务:

- canvas 图表 / 自绘 UI(Chart.js、D3、Flutter Web)
- 图像搜索 / 以图搜图(商品相似、反向图搜)
- 地图点选 / 拖拽定位(Leaflet、Google Maps、热力图)
- 富文本所见即所得编辑器里的非文本元素(图片、嵌入媒体)
- 视觉状态判定(颜色主题切换、loading spinner、进度条位置)

## 目录结构

```
visualwebarena/
├── README.md          本文件
├── tasks/             30 条任务 JSON
├── runner.go          A/B 双轮 runner
├── report.go          对比报告生成
├── runner_test.go     CI smoke(mock)
└── report.md          ← 跑后生成
```

## 任务 JSON 格式(和 webarena 同基底,加 visual 维度)

```json
{
  "id": "canvas-chart-inspect",
  "category": "canvas",
  "site": "https://observablehq.com/@d3/bar-chart",
  "goal": "Read the bar chart and report the country with the highest population",
  "max_turns": 10,
  "visual_required": "hard",
  "modality": ["canvas"],
  "success": [
    { "kind": "text_contains", "value": "China" }
  ],
  "tags": ["canvas", "chart"]
}
```

`visual_required` 取值:
- `hard`   — DOM 里完全不存在答案(canvas / 图像像素级)
- `soft`   — DOM 里有线索但语义不完整(隐藏的 aria-label、颜色态)
- `aux`    — DOM 够用但 visual 能加速(视觉上一眼能认的布局)

`modality` 取值:`canvas` / `image_search` / `map` / `richtext` / `visual_state`。

## A/B 对比

每条任务跑两轮:
- **Run A — DOM-only**:`BrowserStage = "known_flow"`,工具集不含 `visual_inspect`
- **Run B — with visual**:`BrowserStage = "fallback"`,`visual_inspect` 进工具集

两者复用同一套 AdaptiveToolPolicy(`sdk/toolpolicy/adaptive.go`),
不新造 feature flag。走 `ToolScopesForBrowserStage` 内建分级。

### 关键指标

| 指标 | 定义 |
|---|---|
| 成功率差 (Δ) | success_rate_B − success_rate_A |
| Turn 膨胀 | avg_turns_B / avg_turns_A |
| 视觉触发率 | B 轮里真正调用 visual_inspect 的任务比例 |
| Token 膨胀 | avg_tokens_B / avg_tokens_A (mock 模式占位) |

### 结论触发阈值

由 `DecideVisualROI(summary)` 给出建议:

- Δ < 10% 且 token_ratio ≥ 2.0 → **收紧**:建议限制 visual_inspect 只在 pattern_match 连续 N 次失败后开放
- Δ ≥ 30%                   → **放宽**:按当前 fallback stage 自动开放即可
- 10% ≤ Δ < 30%             → **保持**:维持现状,继续观察

## 运行

```bash
# CI 默认只跑 mock smoke,避免联网和巨额 token:
go test ./sdk/test/visualwebarena/...

# 真实跑(接入 Browser Brain):
go test -tags visualwebarena ./sdk/test/visualwebarena/...
```

真实接入需要调用方注入 `Executor`,runner 不直接起浏览器。

## 复用铁律

- 基础 `Task / TaskResult / Summarize / WriteMarkdown` 沿用 `sdk/test/webarena` 的数据类型,不重造
- A/B 开关走 `BrowserStage` + `AdaptiveToolPolicy`,不新造 feature flag
- 任务 JSON 字段超集,webarena runner 也能读(未知字段忽略)
