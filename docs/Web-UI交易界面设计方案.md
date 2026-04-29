# Web UI 交易界面设计方案

> **状态**：方案 A 已决策，Dashboard 基础框架已就位（/v1/dashboard/* + WebSocket Hub），交易专用 API 端点待 Phase 2 实施
> **创建日期**：2026-04-16
> **最后更新**：2026-04-26
> **目标**：提供可视化交易操作界面，替代/补充 `brain chat` 的量化交易功能

---

## 方案对比总览

本文档包含**两种竞争部署方案**，长期并存会导致实现歧义，必须在实施前决策：

| 维度 | 方案 A：全局 WebUI（推荐） | 方案 B：嵌入式 Sidecar |
|------|-------------------------|----------------------|
| **部署位置** | `brain serve` 进程 (:7701) | `quant sidecar` 进程 (:8380) |
| **适用范围** | 全脑 Dashboard + 交易面板 | 仅量化交易专用面板 |
| **前端技术** | Preact + HTM (~3KB) | Alpine.js + PicoCSS (~70KB) |
| **后端依赖** | `net/http` + `go:embed`（零外部依赖） | gorilla/websocket（已在 go.mod） |
| **内存开销** | 共享 serve 进程 | 额外 ~200KB 嵌入 + 5 个 WS 连接 |
| **数据获取** | 通过 Orchestrator 调用各 sidecar | 直接访问 QuantBrain 内存对象 |
| **延迟** | 多一次 RPC 中转 | 亚毫秒级直接访问 |
| **优势** | 统一入口、全脑可见、架构一致 | 低延迟、零 IPC、复用 PG 连接池 |
| **劣势** | 多一次 RPC 开销 | 仅限 quant，其他脑无法接入 |

**决策结论（已确定）**：
- ✅ **方案 A：全局 WebUI** 已选定为实施路线。
- Dashboard 基础框架已在 `brain serve` 中就位：
  - `/v1/dashboard/overview` — 系统总览
  - `/v1/dashboard/brains` / `/v1/dashboard/brains/{kind}` — Brain 列表与详情
  - `/v1/dashboard/events` — 事件流
  - `/v1/dashboard/leases` — 租约状态
  - `/ws` — WebSocket Hub（实时推送）
  - `/dashboard/` — 嵌入式静态文件服务（go:embed）
- 交易专用 REST API（portfolio / accounts / trading / strategies 等）作为 Phase 2 扩展项，
  当前通过 Dashboard 细粒度端点 + sidecar tools 间接覆盖。
- 方案 B（嵌入式 Sidecar）保留为未来量化专用低延迟面板的备选，暂不实施。

---

## 方案 A：全局 WebUI（brain serve 扩展）

### A.1 系统架构

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
│  └────────┬──────────────┬───────────────────┘ │
│           │              │                      │
│  ┌────────▼──┐  ┌───────▼────────┐            │
│  │ Orchestrator│  │ WebSocket Hub  │            │
│  │ (sidecar RPC)│  │ (广播/订阅)    │            │
│  └────────┬──┘  └───────┬────────┘            │
│           │              │                      │
│  ┌────────▼──────────────▼───────────────────┐ │
│  │     Quant Sidecar  ←→  Data Sidecar         │ │
│  └─────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────┘
```

### A.2 技术选型

| 层级 | 技术 | 理由 |
|------|------|------|
| 后端 | Go `net/http` + `go:embed` | 零外部依赖，单二进制分发 |
| 前端框架 | Preact + HTM | 3KB，CDN 引入，无需 npm/webpack |
| 样式 | CSS 变量 + 自适应布局 | 原生响应式，兼容手机和 PC |
| 实时通信 | WebSocket | 行情、持仓、PnL 实时推送 |
| 图表 | Lightweight Charts (TradingView) | 专业 K 线图，55KB |
| 静态嵌入 | `go:embed web/dist/*` | 编译进二进制，无需额外部署 |

### A.3 REST API 设计

基于现有 sidecar tools 映射为 HTTP 端点：

**账户与持仓**
```
GET    /api/v1/portfolio          → global_portfolio
GET    /api/v1/accounts           → account_status
GET    /api/v1/accounts/:id       → 单个账户详情
GET    /api/v1/risk               → global_risk_status
```

**交易操作**
```
POST   /api/v1/trading/pause      → pause_trading
POST   /api/v1/trading/resume     → resume_trading
POST   /api/v1/accounts/:id/pause → account_pause
POST   /api/v1/accounts/:id/resume→ account_resume
POST   /api/v1/accounts/:id/close-all → account_close_all
POST   /api/v1/positions/:id/close→ force_close
```

**策略与信号**
```
GET    /api/v1/strategies/weights  → strategy_weights
PUT    /api/v1/strategies/weights  → 动态调整权重
GET    /api/v1/signals/latest      → 最近信号列表
```

**历史与分析**
```
GET    /api/v1/trades              → trade_history
GET    /api/v1/pnl/daily           → daily_pnl
GET    /api/v1/traces              → trace_query
GET    /api/v1/backtest            → backtest_start
```

**聊天（保留 brain chat 能力）**
```
POST   /api/v1/chat                → 发送消息（SSE 流式响应）
GET    /api/v1/chat/history        → 历史消息
```

### A.4 WebSocket 实时推送

连接端点：`ws://host:7701/ws`

```json
// 订阅
{"type": "subscribe", "channels": ["ticker", "portfolio", "signals", "pnl"]}

// 行情推送 (1s)
{"ch": "ticker", "data": {"symbol": "BTC-USDT", "price": 65432.10, "change_24h": 2.3}}

// 持仓变动 (事件驱动)
{"ch": "portfolio", "data": {"action": "open", "symbol": "ETH-USDT", "direction": "long"}}

// PnL 推送 (5s)
{"ch": "pnl", "data": {"total_equity": 10234.56, "daily_pnl": 34.56}}
```

### A.5 前端页面结构

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

**设计风格**：暗色交易主题（#0d1117），绿涨红跌，响应式三栏/双栏/单栏布局。

### A.6 项目目录

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
    ├── style.css
    ├── pages/
    │   ├── dashboard.js
    │   ├── trading.js
    │   ├── strategies.js
    │   └── ...
    ├── components/
    └── lib/
        ├── preact.module.js
        ├── htm.module.js
        └── lightweight-charts.js
```

### A.7 与现有系统集成

```go
// cmd/brain/cmd_serve.go 扩展
mux.Handle("/api/v1/", web.NewAPIHandler(orchestrator))
mux.Handle("/ws", web.NewWSHub(orchestrator))
mux.Handle("/", web.StaticHandler())

// 启动时预热 sidecar
go orchestrator.PrewarmSidecars(ctx, []string{"quant", "data"})
```

---

## 方案 B：嵌入式 Sidecar（quant 专用面板）

### B.1 部署形态

在 `quant sidecar` 进程内新增 HTTP/WebSocket server goroutine：

```
┌─────────────────────────────────────────────────┐
│              brain-quant-sidecar                 │
│                                                  │
│  ┌──────────────┐   ┌──────────────────────────┐│
│  │ JSON-RPC     │   │  HTTP/WebSocket Server   ││
│  │ (stdin/out)  │   │  :8380 (可配置)          ││
│  │ ← kernel     │   │  ← 浏览器               ││
│  └──────┬───────┘   └──────────┬───────────────┘│
│         │                      │                 │
│         └──────┬───────────────┘                 │
│                ▼                                  │
│  ┌──────────────────────────────────┐            │
│  │  QuantBrain + Accounts + PGPool  │            │
│  └──────────────────────────────────┘            │
└─────────────────────────────────────────────────┘
```

**适用场景**：服务器仅 1.3GB RAM、需要亚毫秒数据延迟、高频盯盘。

### B.2 数据获取路径（亚毫秒级）

| 数据类别 | 获取方式 | 延迟 |
|---------|---------|------|
| 账户权益/持仓 | `Account.Exchange.QueryBalance/QueryPositions` | 亚毫秒 |
| 持仓健康度 | `QuantBrain.healthTracker.Health(key)` | 亚毫秒 |
| 交易历史 | `TradeStore.Query/Stats` | ~1ms |
| 权益曲线 | PG 查 `account_snapshots` | ~5ms |
| K 线数据 | `SnapshotSource` / Data sidecar | ~10ms |
| 信号追踪 | `TraceStore.Query` | ~5ms |
| 风控状态 | `QuantBrain.globalGuard` + 遍历账户 | 亚毫秒 |

### B.3 前端技术栈

| 库 | 大小 | 用途 |
|---|------|------|
| Alpine.js 3.x | ~15KB | 响应式数据绑定 |
| Lightweight Charts 4.x | ~45KB | K 线图、权益曲线 |
| PicoCSS 2.x | ~10KB | 极简 CSS 框架 |

总计 ~70KB gzip。

### B.4 API 与页面

**REST API（前缀 `/api/v1/`）**

P0 接口：`/portfolio`、`/positions`、`/positions/close`、`/trades`、`/equity-curve`

P1 接口：`/candles`、`/signals`、`/orders`、`/positions/sltp`、`/trading/pause`、`/trading/resume`、`/risk`

P2 接口：`/config`、`/strategy/status`、`/accounts`

**WebSocket 端点**：`ws://host:8380/ws`

推送频道：`portfolio_tick`（2s）、`trade_event`（事件驱动）、`signal_update`（5s）、`ping`（30s 心跳）

**前端路由（Hash Router）**

| 路由 | 页面 |
|------|------|
| `#/` | 仪表盘（权益、盈亏、敞口、胜率 + 权益曲线 + 持仓列表） |
| `#/positions` | 持仓管理 |
| `#/trades` | 交易历史 |
| `#/chart` | K 线图表 |
| `#/risk` | 风控面板 |
| `#/config` | 配置管理 |
| `#/signals` | 信号监控 |
| `#/logs` | 日志流 |

### B.5 项目目录

```
brains/quant/webui/
├── server.go        # HTTP server 启动/路由
├── handlers.go      # REST API 实现
├── ws.go            # WebSocket hub
├── ws_push.go       # 定时推送
├── middleware.go    # CORS + auth
├── embed.go         //go:embed
└── static/          # 前端（go:embed 打包）
    ├── index.html
    ├── app.js
    ├── style.css
    └── pages/
        ├── dashboard.html
        ├── positions.html
        └── ...
```

### B.6 工程约束

- **内存预算**：WS 最大 5 个连接，前端 embed 约 200KB，PG 复用现有连接池，equity curve 降采样到 500 点
- **线程安全**：QuantBrain 已有 `sync.RWMutex`，PaperExchange 内部有锁
- **开发调试**：`-webui-dev` 参数可从文件系统读静态文件，支持热重载
- **与 sidecar 共存**：JSON-RPC 走 stdin/stdout，HTTP 走 TCP :8380，共享同一个 QuantBrain 实例

---

## 实施阶段（两种方案共用）

### Phase 1 — 基础框架（已完成）
- [x] 目录骨架 + go:embed（`cmd/brain/dashboard/` + `go:embed static/*`）
- [x] Dashboard REST API: `/v1/dashboard/overview`, `/v1/dashboard/brains`, `/v1/dashboard/events`, `/v1/dashboard/leases`
- [x] WebSocket Hub + 事件广播（`dashboard/ws_hub.go`，`/ws` 端点）
- [x] Dashboard 静态页面（`static/index.html` 等，暗色主题响应式布局）
- [x] Brain 细粒度端点 `/v1/dashboard/brains/{kind}`（含 AutoStart 状态）

### Phase 2 — 交易操作（待实施）
- [ ] 交易专用 REST API: `/api/v1/portfolio`, `/api/v1/accounts`, `/api/v1/trading/*`
- [ ] 交易面板：暂停/恢复、手动平仓
- [ ] 策略监控：权重可视化、信号历史
- [ ] 风控面板：敞口分布、熔断状态

### Phase 3 — 分析与图表（待实施）
- [ ] K 线图 + 开平仓标记
- [ ] 成交历史（分页、筛选、导出）
- [ ] 决策追踪（Trace 全链路）
- [ ] 每日 PnL 图表

### Phase 4 — 高级功能（待实施）
- [ ] 聊天面板（SSE 流式）
- [ ] 设置页面（参数热调）
- [ ] Sidecar 预热优化
- [ ] 认证机制（Phase 1 无认证仅 127.0.0.1，Phase 2 Bearer Token，Phase 3 OAuth2）
