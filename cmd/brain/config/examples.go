package config

import (
	"os"
	"path/filepath"
)

// WriteExamples generates all example config files in the given directory.
func WriteExamples(dir string) {
	writeExample(filepath.Join(dir, "data-brain.example.yaml"), DataConfigExample)
	writeExample(filepath.Join(dir, "quant-brain.example.yaml"), QuantConfigExample)
	writeExample(filepath.Join(dir, "central-brain.example.yaml"), CentralConfigExample)
	writeExample(filepath.Join(dir, "config-reference.example.yaml"), ConfigReferenceExample)
}

func writeExample(path, content string) {
	_ = os.WriteFile(path, []byte(content), 0600)
}

const DataConfigExample = `# ============================================================
#  数据大脑 (Data Brain) 配置示例
#  复制本文件为 config.yaml 并按需修改
# ============================================================

# --- 活跃合约筛选 ---
active_list:
  min_volume_24h: 10000000       # 24h 最低成交量（USDT），低于此值的合约不跟踪
  max_instruments: 100           # 最多同时跟踪的合约数
  update_interval: 168h          # 活跃列表刷新周期（7天）
  always_include:                # 无论成交量如何，始终跟踪的合约
    - BTC-USDT-SWAP
    - ETH-USDT-SWAP
    - SOL-USDT-SWAP
  rank_by_volatility: false      # true = 按 24h 振幅排名（而非成交量），适合量化场景
  min_amplitude_pct: 0           # 最低 24h 振幅百分比，如 2.0 = 过滤振幅 < 2% 的品种

# --- 数据源 ---
providers:
  - name: okx-swap               # 实例名称（自定义）
    type: okx-swap                # 数据源类型
    params:
      ws_url: "wss://ws.okx.com:8443/ws/v5/public"   # WebSocket 地址
      rest_url: "https://www.okx.com"                  # REST API 地址
      ping_interval: 25s                               # WebSocket 心跳间隔
      reconnect_delay:                                 # 断线重连退避（依次尝试）
        - 1s
        - 2s
        - 5s
        - 10s
        - 30s

# --- 历史回填 ---
backfill:
  enabled: true                  # 启动时是否回填历史数据（有 PG 时自动执行）
  max_days: 90                   # 回填天数
  batch_size: 100                # 每次请求的 K 线数量
  rate_limit: 200ms              # REST 请求最小间隔（避免触发限频）
  concurrent: 3                  # 并行回填 worker 数

# --- 数据校验 ---
validation:
  max_price_jump: 0.10           # 单根 K 线最大价格跳变比例（0.10 = 10%）
  max_gap_duration: 5m           # K 线序列最大允许缺口
  stale_timeout: 60s             # 数据源静默超过此时间标记为 stale

# --- 内存环形缓冲区 ---
ring_buffer:
  candle_depth: 1000             # 每个（合约, 周期）保留的 K 线数
  trade_depth: 5000              # 每个合约保留的最近成交数
  order_book_depth: 100          # 每个合约保留的订单簿快照数

# --- 实时特征计算 ---
feature:
  enabled: true                  # 是否开启 192 维特征向量计算
  windows:                       # 滚动窗口大小（K 线根数）
    - 5
    - 10
    - 20
    - 60
  interval: 1s                   # 特征重算周期

# --- 数据库（通过命令行或环境变量传入） ---
# PostgreSQL 连接串，以下二选一：
#   命令行:   brain-data -pg "postgres://user:pass@localhost:5432/brain"
#   环境变量: export PG_URL="postgres://user:pass@localhost:5432/brain"
#
# 不配置则以无持久化模式运行（仅内存，适合调试）

# --- 完整启动命令示例 ---
# 最小启动（无持久化，自动发现合约）:
#   brain-data
#
# 指定合约 + 回填:
#   brain-data -instruments BTC-USDT-SWAP,ETH-USDT-SWAP -backfill -backfill-days 30
#
# 完整生产模式:
#   brain-data -pg "postgres://brain:secret@db:5432/brain" -backfill -backfill-days 90
`

const QuantConfigExample = `# ============================================================
#  量化大脑 (Quant Brain) 配置示例
#  文件格式：YAML（代码同时支持 .yaml 和 .json）
#  启动命令：quant-brain -config config.yaml
# ============================================================

# ==================== 大脑核心参数 ====================
brain:
  cycle_interval: 5s             # 评估周期（每 5 秒扫描一次所有品种）
  default_timeframe: "1H"        # 默认 K 线周期（unit 未指定时使用，可选: 1m/5m/15m/1H/4H/1D）

# ==================== 账户配置 ====================
accounts:
  - id: paper-main
    exchange: paper
    initial_equity: 10000
    tags: [test, paper]
    route:
      weight_factor: 1.0

# ==================== 交易单元 ====================
units:
  - id: unit-btc
    account_id: paper-main
    symbols:
      - BTC-USDT-SWAP
      - ETH-USDT-SWAP
    timeframe: "1H"
    max_leverage: 10
    enabled: true

# ==================== 策略层 ====================
strategy:
  weights:
    TrendFollower: 0.30
    MeanReversion: 0.25
    BreakoutMomentum: 0.25
    OrderFlow: 0.20
  long_threshold: 0.45
  short_threshold: 0.45
  dominance_factor: 1.5
  trend_follower:
    adx_threshold: 0.15
  mean_reversion:
    bb_oversold: 0.15
    bb_overbought: 0.85
    max_volume_ratio: 1.2
  breakout_momentum:
    volume_ratio_threshold: 1.3
    momentum_threshold: 0.008
    strong_momentum: 0.02
  order_flow:
    imbalance_threshold: 0.15
    toxicity_threshold: 0.45
    flow_score_threshold: 0.6

# ==================== 风控层 ====================
risk:
  guard:
    max_single_position_pct: 5
    max_leverage: 20
    min_stop_distance_atr: 1
    max_stop_distance_pct: 10
    max_concurrent_positions: 5
    max_total_exposure_pct: 30
    max_same_direction_pct: 20
    stop_new_trades_loss_pct: 3
    liquidate_all_loss_pct: 5
  position_sizer:
    min_fraction: 0.005
    max_fraction: 0.05
    scale_fraction: 0.25
global_risk:
  max_global_exposure_pct: 50
  max_global_same_direction: 30
  max_global_daily_loss: 5
  max_symbol_exposure: 15
`

const CentralConfigExample = `# ============================================================
#  中央大脑 (Central Brain) 配置示例
#  使用方式：
#    export CENTRAL_CONFIG=/path/to/central-config.yaml
#    brain-central
# ============================================================

llm:
  api_key: ""
  base_url: "https://api.deepseek.com/v1"
  model: "deepseek-chat"
  max_tokens: 500
  temperature: 0.3
  timeout: "15s"

review:
  enabled: true
  trigger_concurrent: 3
  trigger_position_pct: 5
  trigger_daily_loss: 3
  timeout: "10s"
  max_tokens: 500
`

const ConfigReferenceExample = `# ============================================================
#  Brain 主配置文件 (config.json) 字段参考
#  路径: ~/.brain/config.json
#  生成: brain config init
# ============================================================
#
# mode             运行模式："solo"（单机）
# default_brain    默认大脑："central"
# log_level        日志级别："debug" | "info" | "warn" | "error"
# diagnostics      诊断日志开关（默认关闭）
# diagnostics.enabled      true/false
# diagnostics.categories   ["process","delegate","llm","tool"] 或 ["all"]
# diagnostics.file         诊断日志文件路径；默认 ~/.brain/logs/diagnostics.log
# diagnostics.stderr       是否同时输出到 stderr
# diagnostics.level        debug/info/warn/error
# diagnostics.format       text/json
# diagnostics.debug        细粒度调试开关(默认全 false,生产关闭,出问题再开)
#   .runner          每轮 LLM 输出时打 stop_reason / tool_use_count / tools 等
#                    用于定位"嘴上承诺但工具调用没发出"
#   .llm_request     每次 ChatRequest 摘要 (model / messages / max_tokens / tools)
#   .llm_response    每次 ChatResponse 摘要 (stop_reason / blocks / lengths)
#   .tool_dispatch   每次工具调用 (name / args 摘要 / 耗时 / IsError)
#   .context_engine  Assemble 流程 (项目记忆 / 历史加载 / Compress)
# timeout          每轮对话超时："30m"，"0" 禁用超时
#
# chat_mode        chat/run 权限模式
# permission_mode  serve 权限模式
#   可选值：plan, default, accept-edits, auto, restricted, bypass-permissions
#
# active_provider  激活的 LLM 提供商名称
# providers:
#   <名称>:
#     base_url / api_key / model / models.<brain>
#
# brains:          专精大脑注册列表
#   - kind / binary / model / auto_start
#   - max_instances 该 brain 的硬实例上限（0 = 不限，仅受机器资源约束）
#   - min_instances 最小预热实例数（默认 1）
#
# 多实例并发（MACCS 设计）：
#   默认 BrainPool 用机器 50% 的 CPU/内存做总预算（环境变量 BRAIN_RESOURCE_PERCENT 覆盖）。
#   单 kind 同时跑多个 task 时自动扩容到资源上限；到顶后多 task 共享负载最低的实例。
#   set max_instances=N 强制单 brain 不超过 N 个实例。
#
# default_budget:
#   max_turns / max_cost_usd
#
# file_policy:
#   allow_read / allow_create / allow_edit / allow_delete / deny
#
# ============================================================
#  MACCS（多大脑协同系统）配置 — v2.2 新增
#  整个 maccs 块可省略，未列出的字段都使用默认值。
#  所有组件默认启用，对生产环境零开销（关闭仅是为了排障 / 极致性能）。
# ============================================================
#
# maccs:
#
#   # 6.1 组件健康监控 → GET /v1/health
#   health:
#     enabled: true                  # 默认 true，false 时 /v1/health 返回 404
#
#   # 6.3 性能指标采样 → GET /v1/metrics/perf
#   perf:
#     enabled: true                  # brain_delegate 等指标的 P50/P95/P99
#
#   # 6.4 调用链 Span → GET /v1/observability
#   observability:
#     enabled: true                  # delegateOnce 包 TraceSpan，按 trace_id 查
#
#   # 6.5 入参注入审计（POST /v1/projects 的 goal/project_name）
#   security:
#     enabled: true
#     reject_severity: "high"        # critical/high/medium/low
#                                     #   "high"   → critical+high 拒绝（推荐）
#                                     #   "medium" → 加上 medium
#                                     #   "low"    → 任何发现都拒绝（最严）
#                                     #   "critical" → 仅 critical 拒绝（最宽）
#
#   # 6.6 项目级配额 → POST /v1/projects 超额返回 429
#   multi_project:
#     enabled: true
#     max_concurrent: 3              # 同时活跃项目数；超过进队列
#     queue_size: 16                 # 队列长度；满了返回 429
#
#   # 5.5 自适应 Prompt（A/B 变体）→ 注入 LLMProxy.PromptManager
#   adaptive_prompt:
#     enabled: true                  # 需用户后续 RegisterVariant 才有实际效果
#
#   # 4.2 + 4.5 冲突感知重排（ExecutionScheduler 路径）
#   conflict:
#     enabled: true
#     dry_run: true                  # 默认 true：仅日志记录冲突重排建议
#                                     # 生产观察一周确认无误报后切到 false 启用强制重排
#
#   # 4.3 + 4.4 死锁检测 + 仲裁（Wave 7，依赖 conflict.enabled=true 才有数据）
#   deadlock:
#     enabled: true
#     dry_run: true                  # 默认 true：仅日志记录死锁仲裁结果，不真中止 victim
#                                     # 生产观察一周后切到 false 启用强制中止
#
#   # 5.4 项目模式抽取（PlanOrchestrator.ExecuteProject 完成后异步）
#   pattern_extractor:
#     enabled: true                  # 关闭后不会写 ProjectMemory 的 pattern entries
`
