# 阶段 0 实验目录

本目录实现 [`41-语义理解阶段0实验设计.md`](../../41-语义理解阶段0实验设计.md) 定义的 Go/No-Go 实验。

## 目录结构

```
phase0/
├── README.md           本文件
├── samples/            10 个样本页(每页一个子目录)
│   └── <page_id>/
│       ├── url.txt            页面 URL
│       ├── page.html          完整渲染后 HTML
│       ├── screenshot.png     全页截图
│       ├── snapshot.json      browser.snapshot 预期输出结构
│       └── surrounding.html   元素周围 HTML 片段(供 C 组合使用)
├── labels/             人工 ground truth 标注
│   └── <page_id>.json         一页一个标注文件
├── scripts/            采样和实验脚本
│   ├── capture.py             Playwright 样本采集
│   ├── build_snapshot.js      注入脚本生成 snapshot.json
│   ├── run_experiment.py      跑 1080 次 LLM 调用
│   └── analyze.py             算指标生成报告
└── results/            LLM 预测原始输出
    └── <model>_<input>_<page>.json
```

## 10 个样本清单(变体方案)

| # | 类别 | 页面 | URL | 理由 |
|---|---|---|---|---|
| 1 | 登录(简单) | Gitea demo login | https://demo.gitea.com/user/login | 开源、稳定、无风控 |
| 2 | 登录(复杂) | WordPress admin login | demo.wp-api.org/wp-login.php | 字段多、错误路径丰富 |
| 3 | 电商详情 | Saleor demo product | demo.saleor.io/products/... | 开源电商、按钮密集 |
| 4 | 电商详情 | OpenCart demo | demo.opencart.com/... | 加购/收藏/对比三按钮并存 |
| 5 | 后台(结构化) | AdminLTE demo | adminlte.io/themes/v3/ | 标准 CRUD 后台 |
| 6 | 后台(自由发挥) | Ant Design Pro demo | preview.pro.ant.design | 高密度工具栏 |
| 7 | 搜索 | DuckDuckGo | duckduckgo.com/?q=test | 无账号、简洁 |
| 8 | 搜索 | SearxNG demo | searx.be | 另一种实现方式 |
| 9 | 表单(简单) | GOV.UK Design System form example | design-system.service.gov.uk | 政府级表单规范 |
| 10 | 表单(复杂) | react-jsonschema-form playground | rjsf-team.github.io/... | 动态表单、嵌套字段 |

## 4 维度标注定义

见 41 号文档 §3。

## 运行

```bash
# 1. 采样(离线保存 HTML/截图/snapshot)
python3 scripts/capture.py

# 2. 起草标注(LLM 产出初稿,人工审核)
python3 scripts/draft_labels.py

# 3. 人工审核(开发者打开 labels/*.json 修订)

# 4. 跑实验
python3 scripts/run_experiment.py

# 5. 分析
python3 scripts/analyze.py > report.md
```

## Go/No-Go 决策

见 41 号文档 §6。报告产出后写入 `report.md`。
