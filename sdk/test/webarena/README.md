# WebArena 回归基准

轻量子集实现,目标是**定期跑、看趋势**,不是复现完整 WebArena(812 任务、需要托管数据集、VM)。

见 `sdk/docs/40-Browser-Brain语义理解架构.md` §6.2。

## 为什么只做子集

完整 WebArena 需要本地起 Gitea/Magento/Wikipedia 等 docker 镜像、镜像数十 GB,不适合 CI。我们挑 5-10 条**公开稳定 demo 站上可复现的任务**,覆盖登录 / 搜索 / 表单 / 电商几个高频场景,作为 Browser Brain 语义理解的回归门槛。

## 目录结构

```
webarena/
├── README.md           本文件
├── tasks/              任务定义,每条一个 JSON
├── runner.go           基准运行器
├── runner_test.go      入口:go test -tags webarena ./sdk/test/webarena/
└── report.go           指标聚合 + Markdown 输出
```

## 任务 JSON 格式

```json
{
  "id": "gitea-login-happy",
  "category": "auth",
  "site": "https://demo.gitea.com",
  "goal": "log in with user 'tester' password 'tester123' and land on /dashboard",
  "max_turns": 8,
  "success": [
    {"kind": "url_contains", "value": "/dashboard"}
  ],
  "tags": ["login", "happy_path"]
}
```

`success` 列表全部满足即任务成功。支持的检查 kind:
- `url_contains` — 当前页 URL 包含 value
- `url_matches`  — 正则匹配
- `dom_has`      — selector 元素存在
- `text_contains`— 页面文本出现

## 指标

每次运行 runner 输出:

| 指标 | 含义 |
|---|---|
| 成功率 | 成功任务 / 总任务 |
| 平均 turn 数 | 所有任务 turn 的平均 |
| 平均 token 成本 | 预留位,接入成本追踪后填充 |
| 类别分布 | auth/search/form/... 各自成功率 |

## 运行

```bash
# 默认 skip(CI 默认不跑,避免联网)
go test ./sdk/test/webarena/...

# 显式打开
go test -tags webarena ./sdk/test/webarena/...
```

成功后在 `./sdk/test/webarena/report.md` 生成报告。

## 门槛建议

- 阶段 1 完成后:简单任务(单页) ≥ 70%
- 阶段 2 完成后:整体 ≥ 80%,auth 类 ≥ 90%
- 阶段 3 完成后:复杂任务(跨页) ≥ 65%
