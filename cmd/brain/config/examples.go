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
#
# default_budget:
#   max_turns / max_cost_usd
#
# file_policy:
#   allow_read / allow_create / allow_edit / allow_delete / deny
`
