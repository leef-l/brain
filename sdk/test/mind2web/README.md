# Mind2Web 跨站泛化回归基准

轻量子集实现,目标是**验证 Browser Brain 的模式库在"跨站点同类任务"上的复用能力**。和 WebArena 基准互补:WebArena 看"单站能不能做对",Mind2Web 看"同一类任务换站点能不能复用已学到的 UIPattern"。

参见 `sdk/docs/43-Browser-Brain必做项.md` §P2.2。

## 为什么只做子集

完整 Mind2Web 公开数据集包含 2350 条任务、覆盖 137 个网站,完整复现需要对应站点可访问 + 人工 oracle 标注,不适合日常回归。我们从公开子集中**按场景覆盖度挑 100 条**,跟 P1 模式库(auth / ecommerce / admin)对齐,用来验证这些种子模式在陌生站点上还能不能识别并执行。

## 数据来源

- 来自 Mind2Web public subset([OSUNLP/Mind2Web](https://huggingface.co/datasets/osunlp/Mind2Web))的 `test_domain` / `test_website` split
- 挑选标准:每条任务的 oracle 序列能被 `url_contains` / `url_matches` / `dom_has` / `text_contains` 四种检查覆盖
- 原始 annotation_id 保留在每条任务的 `metadata.annotation_id`,用于回溯完整 oracle

## 目录结构

```
mind2web/
├── README.md           本文件
├── tasks/              100 条任务 JSON
├── runner.go           加载 / 跑 / 校验 / PatternReused 聚合
├── runner_test.go      go test ./sdk/test/mind2web/
└── report.go           Summary + Markdown(含跨站矩阵)
```

## 100 条任务分布

| 类别 | 数量 | 用意 |
|---|---:|---|
| auth | 30 | 验证 P1.1 登录/注册/验证场景包在多 Git/CMS/社交站点的迁移命中率 |
| ecommerce | 30 | 验证 P1.2 电商购物流程在 Magento/Shopify/Prestashop/Saleor/OpenCart 等多平台的加购+结算复用 |
| admin | 20 | 验证 P1.3 后台 CRUD 在多 Admin 后台(WordPress/Strapi/Directus/Metabase…)的分页 + 行编辑复用 |
| misc | 20 | 订阅 / 搜索 / 导航类,作对照组,衡量"非重点场景"的基础成功率 |

约束(包内测试强制):
- 每类覆盖 ≥ 4 个不同站点(`TestLoadTasksCrossSiteCoverage`)
- 每条任务必须带 `metadata.annotation_id`(`TestLoadTasksHaveAnnotationID`)

## 任务 JSON 格式

```json
{
  "id": "m2w-auth-01-login-git-hosting",
  "category": "auth",
  "site": "https://git.example.com",
  "goal": "sign in with the configured test account and land on the dashboard",
  "max_turns": 8,
  "success": [
    {"kind": "url_contains", "value": "/dashboard"}
  ],
  "tags": ["mind2web", "login", "cross_site"],
  "metadata": {
    "annotation_id": "xxx-xxxx-xxxx",
    "domain": "Information",
    "subdomain": "Git Hosting"
  }
}
```

字段继承 `sdk/test/webarena/` 的 Task 基底(id / category / site / goal / max_turns / success / tags),**schema 100% 对齐**,好处是:
- Executor 一套适配器可以同时喂 WebArena / VisualWebArena / Mind2Web
- report 结构可以互相 diff

Mind2Web 专有的东西放在 `metadata` 里,不污染公共 schema。

Tags 约定:`mind2web` 来源标记 + 场景标签(login/logout/addcart/checkout/pagination/rowedit/...)+ 可选 `cross_site` / `transfer` 标识是否列入跨站迁移统计。

## 指标

常规指标和 WebArena 一致(成功率 / 平均 turn / 平均耗时 / 按类别分布),**Mind2Web 专有**的两个指标:

| 指标 | 含义 |
|---|---|
| 按站点分布(`BySite`) | 每个站点的成功率,看"哪个站点特别难" |
| 跨站迁移矩阵(`ByCategorySite`) | category × site 二维,每格 `成功/总数`,直观看得到模式在哪几个站点站不住 |
| 模式复用率(`PatternReuseRate`) | 命中过 pattern 的任务中,pattern_id 在本轮被复用过的比例——高 = 模式库被真正复用 |
| 独立 Pattern 数(`PatternCount`) | 本轮 Run 触发了多少个不同的 UIPattern |

`PatternReused` 判定策略:同一个 `PatternID` 在本轮 Run 中第二次及以后出现即算"跨站复用",由 runner 自动聚合;Executor 也可以主动填。

## 运行

```bash
go test ./sdk/test/mind2web/...
```

默认测试用 mockExecutor,不联网,只校验加载/聚合/报告管道。真实跑需要集成方注入 `Executor`(和 WebArena 同签名),在外部 bench 工具里调用 `Run` + `Summarize` + `WriteMarkdown`。

## 门槛建议(随模式库成熟度提高)

| 阶段 | auth 跨站 | ecommerce 跨站 | admin 跨站 | 模式复用率 |
|---|---:|---:|---:|---:|
| P1 种子刚完成 | ≥ 50% | ≥ 40% | ≥ 40% | ≥ 60% |
| P2 评测集齐备 | ≥ 65% | ≥ 55% | ≥ 55% | ≥ 75% |
| P3 模式库稳定后 | ≥ 80% | ≥ 70% | ≥ 70% | ≥ 85% |

门槛随迭代上调,不在当前任务范围内落地。
