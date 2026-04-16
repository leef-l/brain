# Web UI 交易界面架构方案

> 状态：规划中（未实施）  
> 创建日期：2026-04-16  
> 目标：替代 `brain chat` 的所有功能，提供美观大气的可视化交易操作界面

---

## 一、技术选型

| 层级 | 技术 | 理由 |
|------|------|------|
| 后端 | Go `net/http` + `go:embed` | 零外部依赖，单二进制分发 |
| 前端框架 | Preact + HTM | 3KB，CDN 引入，无需 npm/webpack |
| 样式 | CSS 变量 + 自适应布局 | 原生响应式，兼容手机和 PC |
| 实时通信 | WebSocket | 行情、持仓、PnL 实时推送 |
| 图表 | Lightweight Charts (TradingView) | 专业 K 线图，55KB |
| 静态嵌入 | `go:embed web/dist/*` | 编译进二进制，无需额外部署 |

## 二、系统架构

```
┌─────────────────────────────────────────────────┐
│                   浏览器 (手机/PC)                │
│  ┌───────────┐  ┌──────────┐  ┌──────────────┐  │
│  │ Dashboard  │  │ 交易面板  │  │  策略监控     │  │
│  └─────┬─────┘  └────┬─────┘  └──────┬───────┘  │
│        └──────────────┼───────────────┘          │
│                       │ HTTP + WebSocket          │
└───────────────────────┼──────────────────────────┘
                        │
┌───────────────────────┼──────────────────────────┐
│              brain serve (:7701)                   │
│  ┌────────────────────┴────────────────────────┐ │
│  │              HTTP Router                     │ │
│  │  /api/v1/*  →  REST API                     │ │
│  │  /ws        →  WebSocket Hub                │ │
│  │  /*         →  go:embed 静态文件             │ │
│  └──────────┬──────────────┬───────────────────┘ │
│             │              │                      │
│  ┌──────────▼──┐  ┌───────▼────────┐            │
│  │ Orchestrator │  │ WebSocket Hub  │            │
│  │ (sidecar RPC)│  │ (广播/订阅)    │            │
│  └──────────┬──┘  └───────┬────────┘            │
│             │              │                      │
│  ┌──────────▼──────────────▼───────────────────┐ │
│  │     Quant Sidecar  ←→  Data Sidecar         │ │
│  └─────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────┘
```

## 三、REST API 设计

基于现有 sidecar tools 映射为 HTTP 端点：

### 3.1 账户与持仓

```
GET    /api/v1/portfolio          → global_portfolio (全局持仓概览)
GET    /api/v1/accounts           → account_status (所有账户状态)
GET    /api/v1/accounts/:id       → 单个账户详情
GET    /api/v1/risk               → global_risk_status (全局风控状态)
```

### 3.2 交易操作

```
POST   /api/v1/trading/pause      → pause_trading (暂停全局交易)
POST   /api/v1/trading/resume     → resume_trading (恢复全局交易)
POST   /api/v1/accounts/:id/pause → account_pause
POST   /api/v1/accounts/:id/resume→ account_resume
POST   /api/v1/accounts/:id/close-all → account_close_all
POST   /api/v1/positions/:id/close→ force_close (强制平仓)
```

### 3.3 策略与信号

```
GET    /api/v1/strategies/weights  → strategy_weights
PUT    /api/v1/strategies/weights  → 动态调整权重
GET    /api/v1/signals/latest      → 最近信号列表
```

### 3.4 历史与分析

```
GET    /api/v1/trades              → trade_history (分页)
GET    /api/v1/pnl/daily           → daily_pnl
GET    /api/v1/traces              → trace_query (决策追踪)
GET    /api/v1/backtest            → backtest_start
```

### 3.5 聊天（保留 brain chat 能力）

```
POST   /api/v1/chat                → 发送消息（流式响应用 SSE）
GET    /api/v1/chat/history        → 历史消息
```

## 四、WebSocket 实时推送

连接端点：`ws://host:7701/ws`

### 推送频道

```json
// 订阅
{"type": "subscribe", "channels": ["ticker", "portfolio", "signals", "pnl"]}

// 行情推送 (1s)
{"ch": "ticker", "data": {"symbol": "BTC-USDT", "price": 65432.10, "change_24h": 2.3}}

// 持仓变动 (事件驱动)
{"ch": "portfolio", "data": {"action": "open", "symbol": "ETH-USDT", "direction": "long", ...}}

// 信号推送 (事件驱动)
{"ch": "signals", "data": {"strategy": "TrendFollower", "symbol": "BTC-USDT", "direction": "long", ...}}

// PnL 推送 (5s)
{"ch": "pnl", "data": {"total_equity": 10234.56, "daily_pnl": 34.56, "open_pnl": -12.3}}
```

## 五、前端页面设计

### 5.1 页面结构

| 页面 | 功能 | 优先级 |
|------|------|--------|
| Dashboard | 总览：资产曲线、当日PnL、持仓概要、活跃信号 | P0 |
| 交易面板 | 实时持仓列表、手动平仓、暂停/恢复交易 | P0 |
| 策略监控 | 策略权重可视化、信号历史、策略绩效对比 | P0 |
| 历史记录 | 成交历史表格（分页/筛选）、盈亏分析 | P1 |
| 风控面板 | 全局风控状态、敞口分布、熔断日志 | P1 |
| 决策追踪 | Trace 查询、信号→执行全链路回溯 | P1 |
| K线图 | TradingView Lightweight Charts、标记开平仓点 | P2 |
| 聊天 | 保留 brain chat 的 LLM 对话能力 | P2 |
| 设置 | 策略参数热调、风控阈值、账户管理 | P2 |

### 5.2 设计风格

- **暗色交易主题**：深色背景（#0d1117），绿涨红跌
- **响应式布局**：
  - PC (>1024px)：三栏布局，左侧导航 + 中间主区 + 右侧面板
  - 平板 (768-1024px)：双栏折叠
  - 手机 (<768px)：底部 Tab 导航，全宽卡片
- **卡片组件**：圆角容器（8px），微妙阴影，信息密度适中
- **数据动画**：数字变化闪烁效果，图表平滑过渡
- **字体**：等宽数字（Tabular Nums），中文用系统默认

### 5.3 关键组件

```
components/
  Header.js          — 顶栏：Logo + 连接状态 + 时间
  Sidebar.js         — PC 侧边导航
  BottomNav.js       — 手机底部导航
  EquityCurve.js     — 资产曲线图（Lightweight Charts）
  PositionCard.js    — 单个持仓卡片（方向、盈亏、操作按钮）
  SignalBadge.js     — 信号徽章（策略名 + 方向 + 信心度）
  PnLCounter.js      — 动画数字计数器
  TradeTable.js      — 成交历史表格（虚拟滚动）
  RiskGauge.js       — 风控仪表盘（敞口占比环形图）
  ChatPanel.js       — LLM 对话面板
```

## 六、项目目录结构

```
web/
├── embed.go              // go:embed 声明
├── handler.go            // HTTP handler (REST + 静态文件)
├── ws_hub.go             // WebSocket 广播管理
├── ws_client.go          // 单个 WS 连接处理
├── api_portfolio.go      // /api/v1/portfolio 等
├── api_trading.go        // /api/v1/trading/* 等
├── api_strategy.go       // /api/v1/strategies/* 等
├── api_history.go        // /api/v1/trades 等
├── api_chat.go           // /api/v1/chat (SSE)
└── dist/                 // 前端静态文件（go:embed）
    ├── index.html
    ├── app.js            // Preact + HTM 主应用
    ├── style.css         // 全局样式 + CSS 变量
    ├── pages/
    │   ├── dashboard.js
    │   ├── trading.js
    │   ├── strategies.js
    │   ├── history.js
    │   ├── risk.js
    │   ├── traces.js
    │   ├── charts.js
    │   ├── chat.js
    │   └── settings.js
    ├── components/       // 上述组件
    └── lib/
        ├── preact.module.js
        ├── htm.module.js
        └── lightweight-charts.js
```

## 七、与现有系统集成

### 7.1 入口

在现有 `brain serve` 命令中扩展，不新增二进制：

```go
// cmd/brain/cmd_serve.go 修改
mux.Handle("/api/v1/", web.NewAPIHandler(orchestrator))
mux.Handle("/ws", web.NewWSHub(orchestrator))
mux.Handle("/", web.StaticHandler())  // go:embed fallback
```

### 7.2 Sidecar 预热

`brain serve` 启动时自动预热 quant 和 data sidecar，而非等首次 RPC 请求：

```go
// 启动后立即触发 sidecar 初始化
go orchestrator.PrewarmSidecars(ctx, []string{"quant-brain", "data-brain"})
```

### 7.3 认证（后期）

- Phase 1：无认证（仅监听 127.0.0.1）
- Phase 2：可选 Bearer Token（环境变量 `BRAIN_UI_TOKEN`）
- Phase 3：OAuth2 / SSO（如需公网暴露）

## 八、实施阶段

### Phase 1 — 基础框架（预计 2-3 天）
- [ ] `web/` 目录骨架 + go:embed
- [ ] REST API: portfolio, accounts, risk
- [ ] WebSocket Hub + ticker/portfolio 推送
- [ ] Dashboard 页面（资产概览 + 持仓列表）
- [ ] 响应式布局框架

### Phase 2 — 交易操作（预计 2 天）
- [ ] 交易面板：暂停/恢复、手动平仓
- [ ] 策略监控：权重可视化、信号历史
- [ ] 风控面板：敞口分布、熔断状态

### Phase 3 — 分析与图表（预计 2-3 天）
- [ ] K 线图 + 开平仓标记
- [ ] 成交历史（分页、筛选、导出）
- [ ] 决策追踪（Trace 全链路）
- [ ] 每日 PnL 图表

### Phase 4 — 高级功能（预计 2 天）
- [ ] 聊天面板（SSE 流式）
- [ ] 设置页面（参数热调）
- [ ] Sidecar 预热优化
- [ ] 认证机制

---

## 九、备注

- 前端不使用 npm/webpack，所有依赖通过 CDN 下载后放入 `web/dist/lib/`
- 编译后为单二进制，`brain serve` 即包含完整 Web UI
- 手机端通过浏览器访问，无需原生 App
- 所有 `brain chat` 的功能在 Web UI 中均有对应入口
