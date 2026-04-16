# Brain 量化交易系统 Web UI 设计方案（C+ 级别）

> 状态：设计完成，待实现
> 日期：2026-04-16

## 一、架构设计

### 1.1 部署形态：嵌入 sidecar 的 HTTP 模块

在 quant sidecar 进程内新增 HTTP/WebSocket server goroutine，而非独立进程。

理由：
- 服务器仅 1.3GB 可用 RAM，单独进程增加内存开销
- sidecar 已持有 `*quant.QuantBrain`、`map[string]*quant.Account`、PG 连接池等全部核心对象，零额外 IPC 开销
- `gorilla/websocket` 已在 go.mod 中，无需新增依赖

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

### 1.2 数据获取路径

| 数据类别 | 获取方式 | 延迟 |
|---------|---------|------|
| 账户权益/持仓 | `Account.Exchange.QueryBalance/QueryPositions` | 亚毫秒 |
| 持仓健康度 | `QuantBrain.healthTracker.Health(key)` | 亚毫秒 |
| 交易历史 | `TradeStore.Query/Stats` | ~1ms |
| 权益曲线 | PG 查 `account_snapshots` | ~5ms |
| K 线数据 | `SnapshotSource` / Data sidecar | ~10ms |
| 信号追踪 | `TraceStore.Query` | ~5ms |
| 风控状态 | `QuantBrain.globalGuard` + 遍历账户 | 亚毫秒 |

### 1.3 WebSocket 推送

采用 WebSocket（需要双向通信），推送频率：

| 频道 | 频率 | 内容 |
|------|------|------|
| `portfolio_tick` | 每 2 秒 | 权益、持仓、健康度汇总 |
| `trade_event` | 事件驱动 | 新成交通知 |
| `signal_update` | 每 5 秒 | 策略信号评估结果 |
| `ping` | 每 30 秒 | 心跳，客户端 60 秒超时重连 |

消息格式：
```json
{"type": "portfolio_tick", "data": {...}, "ts": 1713264000000}
```

### 1.4 前端技术栈（全 CDN，无需 npm）

| 库 | 大小 | 用途 |
|---|------|------|
| Alpine.js 3.x | ~15KB | 响应式数据绑定 |
| Lightweight Charts 4.x | ~45KB | K 线图、权益曲线 |
| PicoCSS 2.x | ~10KB | 极简 CSS 框架 |

总计 ~70KB gzip，秒级加载。

## 二、API 设计

### 2.1 REST API（前缀 `/api/v1/`）

**P0 接口：**

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/portfolio` | 投资组合概览 |
| GET | `/api/v1/positions` | 持仓列表 + 健康度 |
| POST | `/api/v1/positions/close` | 一键平仓 |
| GET | `/api/v1/trades?since=&limit=` | 交易历史 |
| GET | `/api/v1/equity-curve?days=7` | 权益曲线 |

**P1 接口：**

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/candles?symbol=&tf=&limit=` | K 线数据 |
| GET | `/api/v1/signals?symbol=&limit=` | 信号追踪 |
| POST | `/api/v1/orders` | 手动下单 |
| PUT | `/api/v1/positions/sltp` | 修改止损止盈 |
| POST | `/api/v1/trading/pause` | 暂停策略 |
| POST | `/api/v1/trading/resume` | 恢复策略 |
| GET | `/api/v1/risk` | 风控面板 |

**P2 接口：**

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/PUT | `/api/v1/config` | 配置读写 |
| GET | `/api/v1/strategy/status` | 策略实时输出 |
| GET | `/api/v1/accounts` | 账户列表 |

### 2.2 WebSocket

端点：`ws://host:8380/ws`

客户端订阅：
```json
{"action": "subscribe", "channels": ["portfolio", "trades", "signals"]}
```

## 三、前端页面结构

### 3.1 路由（Hash Router）

| 路由 | 页面 | 优先级 |
|------|------|--------|
| `#/` | 仪表盘 | P0 |
| `#/positions` | 持仓管理 | P0 |
| `#/trades` | 交易历史 | P0 |
| `#/chart` | K 线图表 | P1 |
| `#/risk` | 风控面板 | P1 |
| `#/config` | 配置管理 | P2 |
| `#/signals` | 信号监控 | P2 |
| `#/logs` | 日志流 | P2 |

### 3.2 仪表盘布局

```
┌──────────────┬──────────────┬──────────────┬──────────────┐
│  总权益       │  今日盈亏     │  总敞口       │  胜率         │
│  $10,234.56  │  +$123.45    │  45.2%       │  68.5%       │
└──────────────┴──────────────┴──────────────┴──────────────┘
┌───────────────────────────────────────────────────────────┐
│  权益曲线（Equity Curve）    [1D] [7D] [30D] [ALL]        │
└───────────────────────────────────────────────────────────┘
┌───────────────────────────────────────────────────────────┐
│  持仓列表                                                  │
│  Symbol | Side | Qty | Entry | Mark | PnL | Health | 操作 │
│  BTC    | long | 0.1 | 65000 | 65500| +50 | ████░  | [平] │
└───────────────────────────────────────────────────────────┘
```

## 四、文件结构

```
brains/quant/
├── webui/                          # Web UI 模块
│   ├── server.go                   # HTTP server 启动/路由
│   ├── handlers.go                 # REST API 实现
│   ├── ws.go                       # WebSocket hub
│   ├── ws_push.go                  # 定时推送
│   ├── middleware.go               # CORS + auth
│   ├── embed.go                    # //go:embed
│   └── static/                     # 前端（go:embed 打包）
│       ├── index.html              # SPA 入口
│       ├── app.js                  # 路由 + WebSocket + 状态
│       ├── style.css               # 自定义样式
│       └── pages/                  # 页面模板
│           ├── dashboard.html
│           ├── positions.html
│           ├── trades.html
│           ├── chart.html
│           ├── risk.html
│           ├── config.html
│           ├── signals.html
│           └── logs.html
```

## 五、分批实现计划

### P0（第一批）— 核心仪表盘

功能：
- 账户概览 4 个 KPI 卡片，2 秒自动刷新
- 持仓列表 + 健康度进度条
- 一键平仓按钮
- 今日交易历史表格
- 权益曲线图

关键文件：
- `webui/server.go` — HTTP 骨架
- `webui/handlers.go` — 5 个 P0 接口
- `webui/ws.go` + `ws_push.go` — WebSocket 实时推送
- `static/index.html` + `app.js` — 前端 SPA
- `static/pages/dashboard.html` + `positions.html` + `trades.html`
- `sidecar/main.go`（修改）— 启动 webui goroutine
- `config.go`（修改）— 加 WebUIAddr 配置

### P1（第二批）— K 线 + 交易操作

功能：
- K 线图 + 策略信号标注（开仓/平仓点）
- 手动开仓表单
- 修改止损/止盈
- 暂停/恢复策略
- 风控面板

### P2（第三批）— 完整功能

功能：
- 配置热修改面板
- 策略信号实时监控
- 日志实时流
- 多账户切换

## 六、技术要点

### 内存预算
- WebSocket 最大 5 个连接
- 前端 embed 约 200KB
- PG 复用现有连接池
- equity curve 降采样到 500 点

### 线程安全
- QuantBrain 已有 sync.RWMutex
- PaperExchange 内部有锁
- WebSocket hub 标准 goroutine-safe 模式

### 开发调试
- `-webui-dev` 参数可从文件系统读静态文件，支持热重载
- 生产模式用 `embed.FS` 打包

### 与 sidecar 共存
- JSON-RPC 走 stdin/stdout
- HTTP 走 TCP :8380
- 共享同一个 QuantBrain 实例，无竞态
