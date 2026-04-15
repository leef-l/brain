package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

// brainConfig is the on-disk configuration structure.
// We use JSON (not YAML) to stay zero-dependency.
// File: ~/.brain/config.json
type brainConfig struct {
	Mode               string        `json:"mode,omitempty"`
	Endpoint           string        `json:"endpoint,omitempty"`
	DefaultBrain       string        `json:"default_brain,omitempty"`
	DefaultModel       string        `json:"default_model,omitempty"`
	Output             string        `json:"output,omitempty"`
	LogLevel           string        `json:"log_level,omitempty"`
	NoColor            bool          `json:"no_color,omitempty"`
	Timeout            string        `json:"timeout,omitempty"`
	Budget             *budgetConfig `json:"default_budget,omitempty"`
	ChatMode           string        `json:"chat_mode,omitempty"`
	PermissionMode     string        `json:"permission_mode,omitempty"`
	ServeWorkdirPolicy string        `json:"serve_workdir_policy,omitempty"`
	APIKey             string        `json:"api_key,omitempty"`
	BaseURL            string        `json:"base_url,omitempty"`
	Model              string        `json:"model,omitempty"`

	// Multi-provider support.
	// "providers" maps a name to its configuration.
	// "active_provider" selects which provider to use by default.
	Providers      map[string]*providerConfig `json:"providers,omitempty"`
	ActiveProvider string                     `json:"active_provider,omitempty"`

	// Sandbox configuration for OS-level command isolation.
	Sandbox    *sandboxCfg      `json:"sandbox,omitempty"`
	FilePolicy *filePolicyInput `json:"file_policy,omitempty"`

	// Brains registers specialist brains that the Orchestrator can delegate to.
	// When non-empty, only configured brains are available — the built-in
	// kind list is bypassed. Each entry specifies kind, optional binary path,
	// and optional LLM model override.
	Brains []kernel.BrainRegistration `json:"brains,omitempty"`

	// ToolProfiles contains named include/exclude profiles that can be
	// activated per runtime scope via ActiveTools.
	ToolProfiles map[string]*toolProfileConfig `json:"tool_profiles,omitempty"`

	// ActiveTools maps a runtime scope ("chat", "chat.central.default",
	// "run.code", ...) to one or more profile names (comma-separated).
	ActiveTools map[string]string `json:"active_tools,omitempty"`
}

// sandboxCfg mirrors tool.SandboxConfig but lives in the config package
// to avoid circular imports.
type sandboxCfg struct {
	Enabled           bool     `json:"enabled"`
	AllowWrite        []string `json:"allow_write,omitempty"`
	DenyRead          []string `json:"deny_read,omitempty"`
	AllowNet          []string `json:"allow_net,omitempty"`
	FailIfUnavailable bool     `json:"fail_if_unavailable,omitempty"`
}

// providerConfig holds the configuration for a single LLM provider endpoint.
type providerConfig struct {
	BaseURL string            `json:"base_url"`
	APIKey  string            `json:"api_key"`
	Model   string            `json:"model,omitempty"`
	Models  map[string]string `json:"models,omitempty"` // brain kind → model
}

type budgetConfig struct {
	MaxTurns   int     `json:"max_turns,omitempty"`
	MaxCostUSD float64 `json:"max_cost_usd,omitempty"`
}

// resolvedProvider holds the resolved provider settings after merging all sources.
type resolvedProvider struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
}

// resolveProviderConfig resolves provider settings with the following priority:
//
//  1. CLI flags (apiKey, baseURL, model) — highest
//  2. active_provider → providers[name] → models[brainKind] → model
//  3. top-level api_key / base_url / model (backward compat)
//  4. ANTHROPIC_API_KEY env var
//  5. defaults
func resolveProviderConfig(cfg *brainConfig, flagKey, flagURL, flagModel, brainKind string) resolvedProvider {
	var r resolvedProvider

	// Layer 3: top-level fields (backward compat)
	r.BaseURL = cfg.BaseURL
	r.APIKey = cfg.APIKey
	r.Model = cfg.Model

	// Layer 2: active_provider → providers[name]
	if cfg.ActiveProvider != "" && cfg.Providers != nil {
		if p, ok := cfg.Providers[cfg.ActiveProvider]; ok {
			if p.BaseURL != "" {
				r.BaseURL = p.BaseURL
			}
			if p.APIKey != "" {
				r.APIKey = p.APIKey
			}
			if p.Model != "" {
				r.Model = p.Model
			}
			// models[brainKind] overrides model
			if brainKind != "" && p.Models != nil {
				if m, ok := p.Models[brainKind]; ok {
					r.Model = m
				}
			}
		}
	}

	// Layer 4: env var (only if still empty)
	if r.APIKey == "" {
		r.APIKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	// Layer 1: CLI flags (highest priority)
	if flagKey != "" {
		r.APIKey = flagKey
	}
	if flagURL != "" {
		r.BaseURL = flagURL
	}
	if flagModel != "" {
		r.Model = flagModel
	}

	return r
}

// configPath returns the default config file path.
func configPath() string {
	return toolpolicy.ConfigPath()
}

// loadConfig reads the config file. Returns nil if file doesn't exist.
func loadConfig() (*brainConfig, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cfg := &brainConfig{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %v", path, err)
	}
	if _, err := parseServeWorkdirPolicy(cfg.ServeWorkdirPolicy); err != nil {
		return nil, fmt.Errorf("validate %s: %v", path, err)
	}
	if _, err := executionpolicy.NewFilePolicy(".", cfg.FilePolicy); err != nil {
		return nil, fmt.Errorf("validate %s: %v", path, err)
	}
	if err := toolpolicy.ValidateConfig(toolPolicyConfig(cfg)); err != nil {
		return nil, fmt.Errorf("validate %s: %v", path, err)
	}
	return cfg, nil
}

// initConfig creates a default config file for first-time setup.
func initConfig() error {
	cfg := &brainConfig{
		Mode:               "solo",
		DefaultBrain:       "central",
		ChatMode:           "accept-edits",
		PermissionMode:     "accept-edits",
		ServeWorkdirPolicy: string(serveWorkdirPolicyConfined),
		Timeout:            "30m",
		LogLevel:           "info",
		ActiveProvider:     "anthropic",
		Providers: map[string]*providerConfig{
			"anthropic": {
				BaseURL: "https://api.anthropic.com",
				APIKey:  "",
				Model:   "claude-sonnet-4-20250514",
				Models: map[string]string{
					"central":  "claude-sonnet-4-20250514",
					"code":     "claude-sonnet-4-20250514",
					"verifier": "claude-haiku-4-5-20251001",
				},
			},
		},
		Brains: []kernel.BrainRegistration{
			{Kind: "code", Model: "claude-sonnet-4-20250514"},
			{Kind: "verifier", Model: "claude-haiku-4-5-20251001"},
			{Kind: "data"},
			{Kind: "quant"},
		},
		Budget: &budgetConfig{
			MaxTurns:   20,
			MaxCostUSD: 5.0,
		},
		FilePolicy: &filePolicyInput{
			AllowRead:   []string{"**"},
			AllowCreate: []string{"**"},
			AllowEdit:   []string{"**"},
			AllowDelete: []string{},
			Deny:        []string{".git/**", "bin/**", "**/.env", "**/secrets/**"},
		},
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	// Also generate keybindings.json if it doesn't exist.
	kbPath := keybindingsPath()
	if _, err := os.Stat(kbPath); os.IsNotExist(err) {
		kb := defaultKeybindings()
		data, err := json.MarshalIndent(kb, "", "  ")
		if err == nil {
			data = append(data, '\n')
			_ = os.WriteFile(kbPath, data, 0600)
		}
	}

	// Generate specialist brain example configs.
	writeExamples(filepath.Dir(configPath()))

	return nil
}

// writeExamples generates all example config files in the given directory.
func writeExamples(dir string) {
	writeExample(filepath.Join(dir, "data-brain.example.yaml"), dataConfigExample)
	writeExample(filepath.Join(dir, "quant-brain.example.yaml"), quantConfigExample)
	writeExample(filepath.Join(dir, "central-brain.example.yaml"), centralConfigExample)
	writeExample(filepath.Join(dir, "config-reference.example.yaml"), configReferenceExample)
}

func writeExample(path, content string) {
	_ = os.WriteFile(path, []byte(content), 0600)
}

const dataConfigExample = `# ============================================================
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
  enabled: false                 # 启动时是否回填历史数据
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

const quantConfigExample = `# ============================================================
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
  # --- 纸盘账户（测试用） ---
  - id: paper-main
    exchange: paper               # paper | okx
    initial_equity: 10000         # 初始权益（USDT）
    tags: [test, paper]

    # 账户路由配置（可选）
    route:
      weight_factor: 1.0          # 仓位权重因子（1.0=满仓, 0.5=半仓）
      # allowed_strategies:       # 限制可用策略（空=全部允许）
      #   - trend_follower
      # allowed_symbols:          # 限制可交易品种（空=全部允许）
      #   - BTC-USDT-SWAP

  # --- OKX 实盘账户（示例） ---
  # - id: okx-main
  #   exchange: okx
  #   api_key: "your-api-key"
  #   secret_key: "your-secret-key"
  #   passphrase: "your-passphrase"
  #   base_url: "https://www.okx.com"   # 或 https://aws.okx.com
  #   simulated: false                    # true = OKX 模拟盘
  #   tags: [production]
  #   route:
  #     weight_factor: 0.5               # 保守仓位
  #     allowed_strategies:
  #       - trend_follower
  #       - breakout_momentum

  # --- OKX 模拟盘账户（建议先用模拟盘测试） ---
  # 模拟盘使用真实行情但虚拟资金，OKX 赠送初始资金，亏完可重新申请
  # API Key 获取：OKX 官网 → 头像 → API → 切换"模拟盘"标签 → 创建
  # - id: okx-demo                       # 账户唯一标识（自定义，units 引用此 ID）
  #   exchange: okx                       # 交易所类型：okx
  #   api_key: "demo-api-key"             # OKX 自动生成的 API Key
  #   secret_key: "demo-secret-key"       # OKX 自动生成的 Secret Key（只显示一次）
  #   passphrase: "demo-passphrase"       # 创建 API 时你自己设置的口令密码
  #   base_url: "https://www.okx.com"     # API 地址（一般不用改）
  #   simulated: true                     # true=模拟盘（关键开关！false=实盘）
  #   tags: [demo]                        # 标签，用于分组筛选（自定义）
  #   route:
  #     weight_factor: 1.0                # 仓位权重（1.0=满仓, 0.5=半仓）
  #     # allowed_strategies:             # 限制可用策略（不填=全部策略可用）
  #     #   - trend_follower
  #     # allowed_symbols:                # 限制可交易品种（不填=全部品种可交易）
  #     #   - BTC-USDT-SWAP

# ==================== 交易单元 ====================
# 交易单元 = "交易员"，绑定一个账户，负责交易指定品种
# 一个账户可绑多个 unit，一个 unit 可交易多个品种
#
# symbols 行为：
#   配了 symbols  → 只做列出的品种（固定模式）
#   不配 symbols  → 自动发现模式：从 OKX 按 24h 振幅排名，取波动最大的 Top 20
#                    筛选条件：24h 成交量 > $10M（保证流动性）+ 振幅 > 2%（过滤横盘币）
#   混合也行：unit-A 指定 BTC/ETH，unit-B 不配 → unit-B 做波动大的活跃品种
units:
  - id: unit-btc                          # 单元唯一标识（自定义）
    account_id: paper-main                # 绑定的账户 ID（对应 accounts 中的 id）
    symbols:                              # 该单元交易的品种列表（不配=做全部活跃品种）
      - BTC-USDT-SWAP
      - ETH-USDT-SWAP
    # K线周期 — 决定策略评估的时间粒度
    # 可选值：
    #   1m   — 1 分钟线（高频，信号多但噪音大）
    #   5m   — 5 分钟线（短线交易）
    #   15m  — 15 分钟线（适合均值回归策略）
    #   1H   — 1 小时线（推荐，信号质量和频率的平衡点）
    #   4H   — 4 小时线（中线趋势，适合趋势跟踪和突破策略）
    #   1D   — 日线（长线，信号最少但最稳定）
    #
    # 各策略适用的 timeframe：
    #   trend_follower     — 1H, 4H（趋势在大周期更清晰）
    #   mean_reversion     — 15m, 1H（均值回归在小周期效果好）
    #   breakout_momentum  — 1H, 4H（突破需要足够的价格结构）
    #   order_flow         — tick 级（自动使用实时逐笔数据，不受此字段影响）
    timeframe: "1H"
    max_leverage: 10                      # 最大杠杆倍数
    enabled: true                         # true=启用, false=禁用（不参与交易）

  # - id: unit-alt                        # 可创建多个 unit 分别管理不同品种
  #   account_id: paper-main
  #   symbols:
  #     - SOL-USDT-SWAP
  #     - DOGE-USDT-SWAP
  #   timeframe: "15m"                    # 山寨币用 15 分钟线，捕捉短线波动
  #   max_leverage: 5
  #   enabled: true

# ==================== 执行层 (Execution) ====================
# 可执行文件：brain-quant-execution (exchange-executor)
# 用途：OrderIntent → ExecutionResult 的薄 sidecar
#
# --- 命令行参数 ---
# -intent <json>         内联 OrderIntent JSON
# -intent-file <path>    OrderIntent JSON 文件路径
# -backend <name>        执行后端（默认 "paper"）
# -mark-price <float>    纸盘参考标记价格（默认 0）
# -slippage-bps <float>  纸盘滑点（基点，默认 0）
# -fee-bps <float>       纸盘手续费（基点，默认 5）
#
# --- 启动示例 ---
# 纸盘模式（默认）:
#   brain-quant-execution -intent '{"symbol":"BTC-USDT-SWAP","side":"buy","quantity":"0.1","leverage":10}'
#
# 从文件读取:
#   brain-quant-execution -intent-file ./order.json -mark-price 65000 -slippage-bps 2

# ==================== 策略层 (Strategy) =====================
# 策略权重和聚合器配置。这些值会从配置文件读取，不再是硬编码。
#
# 阈值自适应机制（自动，无需配置）：
#   1m/5m  → 阈值 × 0.65，dominance × 0.80（短周期信号弱，降低门槛）
#   15m    → 阈值 × 0.80
#   1H+    → 使用配置原值
#
# weights 的 key 必须与策略名完全匹配（区分大小写）：
#   TrendFollower / MeanReversion / BreakoutMomentum / OrderFlow
strategy:
  weights:
    TrendFollower: 0.30          # 趋势跟踪
    MeanReversion: 0.25          # 均值回归
    BreakoutMomentum: 0.25       # 突破动量
    OrderFlow: 0.20              # 订单流

  long_threshold: 0.45           # 做多信号最低分数
  short_threshold: 0.45          # 做空信号最低分数
  dominance_factor: 1.5          # 主方向必须是反方向的 1.5 倍

# ==================== 风控层 (Risk) =========================

# --- 单账户风控守卫 (Guard) ---
# 所有字段不配置则使用默认值（括号内即默认值）
risk:
  guard:
    max_single_position_pct: 5         # 单仓最大占权益百分比
    max_leverage: 20                   # 最大杠杆
    min_stop_distance_atr: 1           # 止损最小距离（ATR 倍数）
    max_stop_distance_pct: 10          # 止损最大距离（入场价百分比）
    max_concurrent_positions: 5        # 最大同时持仓数
    max_total_exposure_pct: 30         # 总敞口上限（权益百分比）
    max_same_direction_pct: 20         # 同向敞口上限
    stop_new_trades_loss_pct: 3        # 日亏损停止开新仓
    liquidate_all_loss_pct: 5          # 日亏损全平仓

  position_sizer:
    min_fraction: 0.005                # 最小仓位比例（0.5%）
    max_fraction: 0.05                 # 最大仓位比例（5%）
    scale_fraction: 0.25               # Kelly 缩放因子（1/4 Kelly）

# --- 跨账户全局风控 (GlobalRiskGuard) ---
# 不配置则使用默认值
global_risk:
  max_global_exposure_pct: 50          # 所有账号总敞口/总权益上限
  max_global_same_direction: 30        # 同方向总敞口上限
  max_global_daily_loss: 5             # 所有账号累计日亏损上限
  max_symbol_exposure: 15              # 单品种跨账号总敞口上限

# ==================== LLM 复审 (Review) =====================
# 触发条件满足任一即请求中央大脑 LLM 审查
#
# review:
#   enabled: true                      # 是否开启 LLM 复审
#   trigger_concurrent: 3              # 持仓数 >= 3 时触发
#   trigger_position_pct: 5            # 最大单仓占比 > 5% 时触发
#   trigger_daily_loss: 3              # 当日亏损 > 3% 时触发
#   timeout: 10s                       # LLM 响应超时（超时则自动放行）
#   max_tokens: 500                    # LLM 输出 token 上限
#
# LLM 复审需要中央大脑运行并设置 LLM_API_KEY 环境变量
# 支持的 LLM 服务：DeepSeek V3.2、Claude、HunYuan 或任何 OpenAI 兼容 API

# ==================== 崩溃恢复 (Recovery) ===================
# 需要 PostgreSQL 持久化存储才能启用
#
# recovery:
#   warmup_ticks: 10                   # 恢复后跳过 N 个评估周期（等策略状态重建）
#   validate_with_exchange: true       # 恢复时与交易所核对持仓

# ==================== 持久化 (PostgreSQL) ====================
# 通过命令行参数或环境变量传入：
#   quant-brain -config config.yaml -pg "postgres://user:pass@localhost:5432/brain"
#   或
#   export PG_URL="postgres://user:pass@localhost:5432/brain"
#
# 启用后自动提供：
#   - 交易记录持久化（PGStore）
#   - 信号追踪链持久化（SignalTrace）
#   - 崩溃恢复支持（CrashRecovery）
# 不配置则使用内存存储（重启丢失）

# ==================== 完整启动命令 ====================
#
# 纸盘快速启动（无需配置文件）:
#   quant-brain -paper
#   quant-brain -paper -equity 50000
#
# 配置文件启动:
#   quant-brain -config config.yaml
#
# 带 PostgreSQL 持久化:
#   quant-brain -config config.yaml -pg "postgres://brain:secret@db:5432/brain"
#
# 完整生产模式（含持久化 + 中央大脑 LLM 复审）:
#   export PG_URL="postgres://brain:secret@db:5432/brain"
#   export LLM_API_KEY="your-llm-api-key"
#   quant-brain -config config.yaml -pg "$PG_URL"
`

const centralConfigExample = `# ============================================================
#  中央大脑 (Central Brain) 配置示例
#  中央大脑通过环境变量配置，无需配置文件
# ============================================================

# ==================== 环境变量 ====================

# --- LLM API 配置（必填，启用 LLM 复审功能） ---
# LLM_API_KEY:   API 密钥
# LLM_BASE_URL:  API 地址（默认 https://api.deepseek.com/v1，DeepSeek）
# LLM_MODEL:     模型名称（默认 deepseek-chat）
#
# 支持的 LLM 服务：
#   DeepSeek V3.2（默认，推荐）:
#     export LLM_API_KEY="sk-xxx"
#     # LLM_BASE_URL 和 LLM_MODEL 使用默认值即可
#
#   Claude（Anthropic）:
#     export LLM_API_KEY="sk-ant-xxx"
#     export LLM_BASE_URL="https://api.anthropic.com/v1"
#     export LLM_MODEL="claude-sonnet-4-20250514"
#
#   HunYuan（腾讯混元）:
#     export LLM_API_KEY="your-hunyuan-key"
#     export LLM_BASE_URL="https://api.hunyuan.cloud.tencent.com/v1"
#     export LLM_MODEL="hunyuan-pro"
#
#   任何 OpenAI 兼容 API:
#     export LLM_API_KEY="your-key"
#     export LLM_BASE_URL="https://your-api.example.com/v1"
#     export LLM_MODEL="your-model-name"

# ==================== 提供的工具 ====================
# 中央大脑注册以下工具供 Kernel 调用：
#
# central.plan_create    — 创建计划
# central.plan_update    — 更新计划
# central.delegate       — 委派子任务给专家大脑
# central.review_trade   — LLM 交易复审（量化大脑触发）
# central.daily_review   — 日终分析报告
# central.data_alert     — 数据质量告警处理
# central.echo           — 回声测试
# central.reject_task    — 拒绝任务

# ==================== 启动命令 ====================
#
# 最小启动（不启用 LLM，复审请求将自动放行）:
#   brain-central
#
# 启用 LLM 复审（DeepSeek V3.2）:
#   export LLM_API_KEY="sk-xxx"
#   brain-central
#
# 启用 LLM 复审（自定义 API）:
#   LLM_API_KEY="sk-xxx" LLM_BASE_URL="https://api.example.com/v1" LLM_MODEL="model-name" brain-central

# ==================== 工具调用说明 ====================
#
# --- central.review_trade（交易复审） ---
# 输入:
#   signal:      信号信息（direction, confidence）
#   portfolio:   组合状态（total_equity, daily_pnl_pct, open_positions, largest_pos_pct）
#   market:      市场环境（symbol, price, vol_percentile, market_regime, funding_rate）
#   reason:      触发原因
#
# 输出:
#   approved:    是否批准（true/false）
#   size_factor: 仓位系数（0.0-1.0）
#   reason:      LLM 判断理由
#
# --- central.daily_review（日终分析） ---
# 输入:
#   date:           日期
#   accounts:       账户统计
#   strategy_stats: 策略表现
#   total_trades:   总交易数
#   total_pnl:      总盈亏
#
# 输出:
#   assessment:     整体评价
#   strategy_notes: 策略建议
#   risk_notes:     风险提醒
#   actions:        建议操作列表
#
# --- central.data_alert（数据告警） ---
# 输入:
#   level:      告警级别（warning | critical）
#   alert_type: 告警类型（price_spike | gap | stale）
#   symbol:     品种
#   detail:     详细信息
#
# 输出:
#   received:    已接收
#   action:      响应动作（logged | risk_pause）
#   description: 描述
`

const configReferenceExample = `# ============================================================
#  Brain 主配置文件 (config.json) 字段参考
#  路径: ~/.brain/config.json（Linux/Mac）或 %USERPROFILE%\.brain\config.json（Windows）
#  生成: brain config init
# ============================================================

# ==================== 基础配置 ====================
#
# mode             运行模式："solo"（单机）
# default_brain    默认大脑："central"（中央大脑，即 chat/run 本身）
# log_level        日志级别："debug" | "info" | "warn" | "error"
# timeout          每轮对话超时："30m"（30分钟），"0" 禁用超时

# ==================== 权限模式 ====================
#
# chat_mode        chat/run 命令使用的权限模式（优先级高于 permission_mode）
# permission_mode  serve 命令使用的权限模式（chat_mode 未设置时也作为 chat 的兜底）
#
# 可选值：
#   plan              — 只能读文件和搜索，不能修改（规划模式）
#   default           — 每次工具调用都要用户确认
#   accept-edits      — 文件读写自动放行，shell 命令需确认（推荐）
#   auto              — 所有操作自动放行（高风险）
#   restricted        — 严格受限，需配合 file_policy 使用
#   bypass-permissions — 跳过所有权限检查（仅开发调试用）
#
# serve_workdir_policy    serve 模式的工作目录策略："confined"（限制在工作目录内）

# ==================== LLM 提供商配置 ====================
#
# active_provider   当前激活的提供商名称（对应 providers 中的 key）
#
# providers:
#   <名称>:
#     base_url       API 基础地址（代码会追加 /v1/messages）
#     api_key        API 密钥
#     model          默认模型名称
#     models:        按大脑类型指定不同模型（可选）
#       central      中央大脑使用的模型
#       code         代码大脑使用的模型
#       verifier     验证大脑使用的模型
#       data         数据大脑使用的模型
#       quant        量化大脑使用的模型
#
# --- 常见配置 ---
#
# Anthropic（官方）:
#   base_url: "https://api.anthropic.com"
#   model: "claude-sonnet-4-20250514"
#
# 腾讯 Coding Plan（Anthropic 协议）:
#   base_url: "https://api.lkeap.cloud.tencent.com/coding/anthropic"
#   model: "glm-5"  或  "claude-sonnet-4-20250514"
#
# 注意：base_url 不要以 /v1 或 /v3 结尾，代码会自动追加 /v1/messages

# ==================== 专精大脑注册 ====================
#
# brains:          配置哪些专精大脑可用（空数组 [] = 自动探测所有内置大脑）
#   - kind         大脑类型："code" | "verifier" | "data" | "quant" | "browser" | "fault"
#     binary       可选，sidecar 二进制路径（不填则从 PATH 自动查找）
#     model        可选，该大脑使用的 LLM 模型（通过 LLM Proxy 代理）
#     auto_start   可选，是否在启动时自动启动（默认 false，按需懒启动）

# ==================== 执行预算 ====================
#
# default_budget:
#   max_turns      每次对话最大轮次：20（默认）
#   max_cost_usd   每次对话最大花费：5.0 美元（默认）

# ==================== 文件权限策略 ====================
#
# file_policy:     控制 LLM 工具可以访问哪些文件
#   allow_read     允许读取的路径 glob 列表，如 ["**"]（所有文件）
#   allow_create   允许创建的路径 glob 列表
#   allow_edit     允许编辑的路径 glob 列表
#   allow_delete   允许删除的路径 glob 列表（建议为空）
#   deny           拒绝列表（优先级最高），如 [".git/**", "**/.env"]
#
# 提示：restricted 模式必须配置 file_policy 才能使用
`

// printConfigSetupGuide prints instructions for first-time configuration.
func printConfigSetupGuide() {
	path := configPath()
	fmt.Fprintln(os.Stderr, "\033[1;33m! 未找到配置文件\033[0m")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "请先完成配置，运行以下命令：")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "  \033[1mbrain config init\033[0m              # 生成默认配置文件 (%s)\n", path)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "然后设置 API Key 和模型：")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  \033[1mbrain config set active_provider anthropic\033[0m")
	fmt.Fprintln(os.Stderr, "  \033[1mbrain config set providers.anthropic.api_key sk-ant-xxxxx\033[0m")
	fmt.Fprintln(os.Stderr, "  \033[1mbrain config set providers.anthropic.model claude-sonnet-4-20250514\033[0m")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "可选配置：")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  brain config set permission_mode <mode> # run/serve 默认权限模式")
	fmt.Fprintln(os.Stderr, "  brain config set chat_mode <mode>     # plan, default, accept-edits, auto, restricted, bypass-permissions")
	fmt.Fprintln(os.Stderr, "  brain config set permission_mode restricted")
	fmt.Fprintln(os.Stderr, "  brain config set serve_workdir_policy confined")
	fmt.Fprintln(os.Stderr, "  brain config set timeout 30m")
	fmt.Fprintln(os.Stderr, "  brain config set providers.<name>.models.central <model>")
	fmt.Fprintln(os.Stderr, "  brain config set providers.<name>.models.code <model>")
	fmt.Fprintln(os.Stderr, "  brain config set default_budget.max_turns 20")
	fmt.Fprintln(os.Stderr, "  # 或直接在 config.json 里设置 file_policy")
	fmt.Fprintln(os.Stderr, "  brain config set tool_profiles.safe.include code.read_file,code.search")
	fmt.Fprintln(os.Stderr, "  brain config set active_tools.chat.central.default safe")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "或直接编辑配置文件: \033[2m%s\033[0m\n", path)
	fmt.Fprintln(os.Stderr, "")
}

// saveConfig writes the config to disk, creating the directory if needed.
func saveConfig(cfg *brainConfig) error {
	path := configPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0600)
}

// configToMap converts config struct to flat key-value map for display.
func configToMap(cfg *brainConfig) map[string]string {
	m := make(map[string]string)
	if cfg.Mode != "" {
		m["mode"] = cfg.Mode
	}
	if cfg.Endpoint != "" {
		m["endpoint"] = cfg.Endpoint
	}
	if cfg.DefaultBrain != "" {
		m["default_brain"] = cfg.DefaultBrain
	}
	if cfg.DefaultModel != "" {
		m["default_model"] = cfg.DefaultModel
	}
	if cfg.Output != "" {
		m["output"] = cfg.Output
	}
	if cfg.LogLevel != "" {
		m["log_level"] = cfg.LogLevel
	}
	if cfg.ChatMode != "" {
		m["chat_mode"] = cfg.ChatMode
	}
	if cfg.PermissionMode != "" {
		m["permission_mode"] = cfg.PermissionMode
	}
	if cfg.ServeWorkdirPolicy != "" {
		m["serve_workdir_policy"] = cfg.ServeWorkdirPolicy
	}
	if cfg.NoColor {
		m["no_color"] = "true"
	}
	if cfg.Timeout != "" {
		m["timeout"] = cfg.Timeout
	}
	if cfg.FilePolicy != nil {
		if raw, err := json.Marshal(cfg.FilePolicy); err == nil {
			m["file_policy"] = string(raw)
		}
	}
	if cfg.APIKey != "" {
		m["api_key"] = cfg.APIKey
	}
	if cfg.BaseURL != "" {
		m["base_url"] = cfg.BaseURL
	}
	if cfg.Model != "" {
		m["model"] = cfg.Model
	}
	if cfg.ActiveProvider != "" {
		m["active_provider"] = cfg.ActiveProvider
	}
	if cfg.Providers != nil {
		for name, p := range cfg.Providers {
			prefix := "providers." + name + "."
			if p.BaseURL != "" {
				m[prefix+"base_url"] = p.BaseURL
			}
			if p.APIKey != "" {
				m[prefix+"api_key"] = p.APIKey
			}
			if p.Model != "" {
				m[prefix+"model"] = p.Model
			}
			for brain, model := range p.Models {
				m[prefix+"models."+brain] = model
			}
		}
	}
	if cfg.Budget != nil {
		if cfg.Budget.MaxTurns > 0 {
			m["default_budget.max_turns"] = strconv.Itoa(cfg.Budget.MaxTurns)
		}
		if cfg.Budget.MaxCostUSD > 0 {
			m["default_budget.max_cost_usd"] = strconv.FormatFloat(cfg.Budget.MaxCostUSD, 'f', -1, 64)
		}
	}
	if cfg.ToolProfiles != nil {
		for name, profile := range cfg.ToolProfiles {
			if profile == nil {
				continue
			}
			prefix := "tool_profiles." + name + "."
			if len(profile.Include) > 0 {
				m[prefix+"include"] = strings.Join(profile.Include, ",")
			}
			if len(profile.Exclude) > 0 {
				m[prefix+"exclude"] = strings.Join(profile.Exclude, ",")
			}
		}
	}
	if cfg.ActiveTools != nil {
		for scope, profileNames := range cfg.ActiveTools {
			if strings.TrimSpace(profileNames) == "" {
				continue
			}
			m["active_tools."+scope] = profileNames
		}
	}
	return m
}

// configGet reads a single key from the config.
func configGet(cfg *brainConfig, key string) (string, bool) {
	m := configToMap(cfg)
	v, ok := m[key]
	return v, ok
}

// configSet sets a single key. Returns error for invalid values.
func configSet(cfg *brainConfig, key, value string) error {
	switch key {
	case "mode":
		if value != "solo" && value != "cluster" {
			return fmt.Errorf("invalid mode %q (must be solo or cluster)", value)
		}
		cfg.Mode = value
	case "endpoint":
		cfg.Endpoint = value
	case "default_brain":
		cfg.DefaultBrain = value
	case "default_model":
		cfg.DefaultModel = value
	case "chat_mode":
		if _, err := parseChatMode(value); err != nil {
			return err
		}
		cfg.ChatMode = value
	case "permission_mode":
		if _, err := parsePermissionMode(value); err != nil {
			return err
		}
		cfg.PermissionMode = value
	case "serve_workdir_policy":
		policy, err := parseServeWorkdirPolicy(value)
		if err != nil {
			return err
		}
		cfg.ServeWorkdirPolicy = string(policy)
	case "output":
		if value != "human" && value != "json" {
			return fmt.Errorf("invalid output %q (must be human or json)", value)
		}
		cfg.Output = value
	case "log_level":
		valid := map[string]bool{"trace": true, "debug": true, "info": true, "warn": true, "error": true}
		if !valid[value] {
			return fmt.Errorf("invalid log_level %q (must be trace/debug/info/warn/error)", value)
		}
		cfg.LogLevel = value
	case "no_color":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid no_color %q (must be true or false)", value)
		}
		cfg.NoColor = b
	case "timeout":
		cfg.Timeout = value
	case "file_policy":
		policy, err := parseFilePolicyJSON(value)
		if err != nil {
			return err
		}
		cfg.FilePolicy = policy
	case "api_key":
		cfg.APIKey = value
	case "base_url":
		cfg.BaseURL = value
	case "model":
		cfg.Model = value
	case "active_provider":
		cfg.ActiveProvider = value
	case "default_budget.max_turns":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid max_turns %q (must be positive integer)", value)
		}
		if cfg.Budget == nil {
			cfg.Budget = &budgetConfig{}
		}
		cfg.Budget.MaxTurns = n
	case "default_budget.max_cost_usd":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 {
			return fmt.Errorf("invalid max_cost_usd %q (must be non-negative number)", value)
		}
		if cfg.Budget == nil {
			cfg.Budget = &budgetConfig{}
		}
		cfg.Budget.MaxCostUSD = f
	default:
		// Handle providers.<name>.<field> and providers.<name>.models.<brain>
		if strings.HasPrefix(key, "providers.") {
			return setProviderKey(cfg, key, value)
		}
		if strings.HasPrefix(key, "tool_profiles.") {
			return setToolProfileKey(cfg, key, value)
		}
		if strings.HasPrefix(key, "active_tools.") {
			return setActiveToolsKey(cfg, key, value)
		}
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

// setProviderKey handles "providers.<name>.base_url|api_key|model|models.<brain>"
func setProviderKey(cfg *brainConfig, key, value string) error {
	parts := strings.SplitN(key, ".", 4) // providers, name, field[, subfield]
	if len(parts) < 3 {
		return fmt.Errorf("invalid provider key %q (use providers.<name>.<field>)", key)
	}
	name := parts[1]
	field := parts[2]

	if cfg.Providers == nil {
		cfg.Providers = make(map[string]*providerConfig)
	}
	p, ok := cfg.Providers[name]
	if !ok {
		p = &providerConfig{}
		cfg.Providers[name] = p
	}

	switch field {
	case "base_url":
		p.BaseURL = value
	case "api_key":
		p.APIKey = value
	case "model":
		p.Model = value
	case "models":
		if len(parts) < 4 {
			return fmt.Errorf("invalid key %q (use providers.%s.models.<brain>)", key, name)
		}
		brain := parts[3]
		if p.Models == nil {
			p.Models = make(map[string]string)
		}
		p.Models[brain] = value
	default:
		return fmt.Errorf("unknown provider field %q (use base_url, api_key, model, or models.<brain>)", field)
	}
	return nil
}

// setToolProfileKey handles "tool_profiles.<name>.include|exclude".
func setToolProfileKey(cfg *brainConfig, key, value string) error {
	parts := strings.SplitN(key, ".", 3)
	if len(parts) != 3 {
		return fmt.Errorf("invalid tool profile key %q (use tool_profiles.<name>.<include|exclude>)", key)
	}

	name := strings.TrimSpace(parts[1])
	field := parts[2]
	if name == "" {
		return fmt.Errorf("invalid tool profile key %q (profile name is required)", key)
	}

	patterns := toolpolicy.SplitCSV(value)
	if err := toolpolicy.ValidateProfileName(name); err != nil {
		return err
	}
	if len(patterns) == 0 {
		return fmt.Errorf("invalid %s %q (must be a comma-separated list of tool patterns)", field, value)
	}
	if err := toolpolicy.ValidatePatterns(patterns); err != nil {
		return err
	}

	if cfg.ToolProfiles == nil {
		cfg.ToolProfiles = make(map[string]*toolProfileConfig)
	}
	profile, ok := cfg.ToolProfiles[name]
	if !ok || profile == nil {
		profile = &toolProfileConfig{}
		cfg.ToolProfiles[name] = profile
	}

	switch field {
	case "include":
		profile.Include = patterns
	case "exclude":
		profile.Exclude = patterns
	default:
		return fmt.Errorf("unknown tool profile field %q (use include or exclude)", field)
	}
	return nil
}

// setActiveToolsKey handles "active_tools.<scope>".
func setActiveToolsKey(cfg *brainConfig, key, value string) error {
	scope := strings.TrimSpace(strings.TrimPrefix(key, "active_tools."))
	if scope == "" || scope == key {
		return fmt.Errorf("invalid active_tools key %q (use active_tools.<scope>)", key)
	}

	profiles := toolpolicy.SplitCSV(value)
	raw := strings.Join(profiles, ",")
	if err := toolpolicy.ValidateActiveToolsValue(toolPolicyConfig(cfg), scope, raw); err != nil {
		return err
	}

	if cfg.ActiveTools == nil {
		cfg.ActiveTools = make(map[string]string)
	}
	cfg.ActiveTools[scope] = raw
	return nil
}

// configUnset removes a key from the config.
func configUnset(cfg *brainConfig, key string) {
	switch key {
	case "mode":
		cfg.Mode = ""
	case "endpoint":
		cfg.Endpoint = ""
	case "default_brain":
		cfg.DefaultBrain = ""
	case "default_model":
		cfg.DefaultModel = ""
	case "chat_mode":
		cfg.ChatMode = ""
	case "permission_mode":
		cfg.PermissionMode = ""
	case "serve_workdir_policy":
		cfg.ServeWorkdirPolicy = ""
	case "output":
		cfg.Output = ""
	case "log_level":
		cfg.LogLevel = ""
	case "no_color":
		cfg.NoColor = false
	case "timeout":
		cfg.Timeout = ""
	case "file_policy":
		cfg.FilePolicy = nil
	case "api_key":
		cfg.APIKey = ""
	case "base_url":
		cfg.BaseURL = ""
	case "model":
		cfg.Model = ""
	case "active_provider":
		cfg.ActiveProvider = ""
	case "default_budget.max_turns":
		if cfg.Budget != nil {
			cfg.Budget.MaxTurns = 0
		}
	case "default_budget.max_cost_usd":
		if cfg.Budget != nil {
			cfg.Budget.MaxCostUSD = 0
		}
	default:
		if strings.HasPrefix(key, "providers.") {
			unsetProviderKey(cfg, key)
		}
		if strings.HasPrefix(key, "tool_profiles.") {
			unsetToolProfileKey(cfg, key)
		}
		if strings.HasPrefix(key, "active_tools.") {
			unsetActiveToolsKey(cfg, key)
		}
	}
}

// unsetProviderKey removes a provider key or an entire provider.
func unsetProviderKey(cfg *brainConfig, key string) {
	if cfg.Providers == nil {
		return
	}
	parts := strings.SplitN(key, ".", 4)
	if len(parts) < 2 {
		return
	}
	name := parts[1]
	p, ok := cfg.Providers[name]
	if !ok {
		return
	}
	// "providers.<name>" — remove entire provider
	if len(parts) == 2 {
		delete(cfg.Providers, name)
		if len(cfg.Providers) == 0 {
			cfg.Providers = nil
		}
		return
	}
	field := parts[2]
	switch field {
	case "base_url":
		p.BaseURL = ""
	case "api_key":
		p.APIKey = ""
	case "model":
		p.Model = ""
	case "models":
		if len(parts) == 4 {
			delete(p.Models, parts[3])
			if len(p.Models) == 0 {
				p.Models = nil
			}
		} else {
			p.Models = nil
		}
	}
}

func unsetToolProfileKey(cfg *brainConfig, key string) {
	if cfg.ToolProfiles == nil {
		return
	}

	parts := strings.SplitN(key, ".", 3)
	if len(parts) < 2 {
		return
	}
	name := parts[1]
	profile, ok := cfg.ToolProfiles[name]
	if !ok {
		return
	}

	if len(parts) == 2 {
		delete(cfg.ToolProfiles, name)
		if len(cfg.ToolProfiles) == 0 {
			cfg.ToolProfiles = nil
		}
		toolpolicy.PruneMissingProfiles(toolPolicyConfig(cfg))
		return
	}

	switch parts[2] {
	case "include":
		profile.Include = nil
	case "exclude":
		profile.Exclude = nil
	}

	if len(profile.Include) == 0 && len(profile.Exclude) == 0 {
		delete(cfg.ToolProfiles, name)
		if len(cfg.ToolProfiles) == 0 {
			cfg.ToolProfiles = nil
		}
	}
	toolpolicy.PruneMissingProfiles(toolPolicyConfig(cfg))
}

func unsetActiveToolsKey(cfg *brainConfig, key string) {
	if cfg.ActiveTools == nil {
		return
	}
	scope := strings.TrimSpace(strings.TrimPrefix(key, "active_tools."))
	if scope == "" || scope == key {
		return
	}
	delete(cfg.ActiveTools, scope)
	if len(cfg.ActiveTools) == 0 {
		cfg.ActiveTools = nil
	}
}

// runConfig implements `brain config` with subcommands.
// See 27-CLI命令契约.md §14.
func runConfig(args []string) int {
	if len(args) == 0 {
		printConfigUsage()
		return cli.ExitUsage
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "init":
		return runConfigInit(rest)
	case "list":
		return runConfigList(rest)
	case "get":
		return runConfigGet(rest)
	case "set":
		return runConfigSet(rest)
	case "unset":
		return runConfigUnset(rest)
	case "path":
		return runConfigPath(rest)
	case "-h", "--help", "help":
		printConfigUsage()
		return cli.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "brain config: unknown subcommand %q\n", sub)
		printConfigUsage()
		return cli.ExitUsage
	}
}

func runConfigInit(_ []string) int {
	path := configPath()
	dir := filepath.Dir(path)
	// Ensure config directory exists for example files.
	_ = os.MkdirAll(dir, 0700)

	configExists := false
	if _, err := os.Stat(path); err == nil {
		configExists = true
	}

	if !configExists {
		if err := initConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "brain config init: %v\n", err)
			return cli.ExitSoftware
		}
	}

	// Always regenerate example files (they contain no user data).
	writeExamples(dir)

	if configExists {
		fmt.Fprintf(os.Stdout, "配置文件已存在: %s（未修改）\n", path)
		fmt.Fprintf(os.Stdout, "已更新示例文件:\n")
	} else {
		fmt.Fprintf(os.Stdout, "已生成配置文件:\n")
		fmt.Fprintf(os.Stdout, "  %s\n", path)
		fmt.Fprintf(os.Stdout, "  %s\n", keybindingsPath())
	}
	fmt.Fprintf(os.Stdout, "  %s\n", filepath.Join(dir, "data-brain.example.yaml"))
	fmt.Fprintf(os.Stdout, "  %s\n", filepath.Join(dir, "quant-brain.example.yaml"))
	fmt.Fprintf(os.Stdout, "  %s\n", filepath.Join(dir, "central-brain.example.yaml"))
	fmt.Fprintf(os.Stdout, "  %s\n", filepath.Join(dir, "config-reference.example.yaml"))
	fmt.Fprintln(os.Stdout, "")

	if !configExists {
		fmt.Fprintln(os.Stdout, "下一步，设置你的 API Key：")
		fmt.Fprintln(os.Stdout, "  brain config set providers.anthropic.api_key <your-key>")
		fmt.Fprintln(os.Stdout, "")
	}
	fmt.Fprintln(os.Stdout, "配置专精大脑（可选）：")
	fmt.Fprintln(os.Stdout, "  cp ~/.brain/quant-brain.example.yaml ~/.brain/quant-brain.yaml")
	fmt.Fprintln(os.Stdout, "  cp ~/.brain/data-brain.example.yaml  ~/.brain/data-brain.yaml")
	fmt.Fprintln(os.Stdout, "  # 编辑 yaml 后，在 config.json 的 brains 字段中指定路径")
	return cli.ExitOK
}

func printConfigUsage() {
	fmt.Fprintln(os.Stderr, "Usage: brain config <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  init     Generate default config file")
	fmt.Fprintln(os.Stderr, "  list     List all configuration values")
	fmt.Fprintln(os.Stderr, "  get      Get a configuration value")
	fmt.Fprintln(os.Stderr, "  set      Set a configuration value")
	fmt.Fprintln(os.Stderr, "  unset    Remove a configuration value")
	fmt.Fprintln(os.Stderr, "  path     Print the config file path")
}

// loadConfigOrEmpty loads config, returns empty config if file doesn't exist.
func loadConfigOrEmpty() (*brainConfig, error) {
	cfg, err := loadConfig()
	if cfg == nil && err == nil {
		return &brainConfig{}, nil
	}
	return cfg, err
}

func runConfigList(args []string) int {
	fs := flag.NewFlagSet("config list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain config: %v\n", err)
		return cli.ExitSoftware
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(cfg)
	} else {
		m := configToMap(cfg)
		if len(m) == 0 {
			fmt.Fprintln(os.Stdout, "(no configuration set)")
			fmt.Fprintf(os.Stdout, "Config file: %s\n", configPath())
			return cli.ExitOK
		}
		// Sort keys for stable output
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		maxLen := 0
		for _, k := range keys {
			if len(k) > maxLen {
				maxLen = len(k)
			}
		}
		for _, k := range keys {
			fmt.Fprintf(os.Stdout, "%-*s  %s\n", maxLen, k, m[k])
		}
	}
	return cli.ExitOK
}

func runConfigGet(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain config get <key>")
		return cli.ExitUsage
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain config: %v\n", err)
		return cli.ExitSoftware
	}

	val, ok := configGet(cfg, args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "brain config get: key %q not set\n", args[0])
		return cli.ExitNotFound
	}
	fmt.Fprintln(os.Stdout, val)
	return cli.ExitOK
}

func runConfigSet(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: brain config set <key> <value>")
		return cli.ExitUsage
	}

	key := args[0]
	value := strings.Join(args[1:], " ")

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain config: %v\n", err)
		return cli.ExitSoftware
	}

	if err := configSet(cfg, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "brain config set: %v\n", err)
		return cli.ExitDataErr
	}

	if err := saveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "brain config set: write: %v\n", err)
		if os.IsPermission(err) {
			return cli.ExitNoPerm
		}
		return cli.ExitSoftware
	}
	fmt.Fprintf(os.Stdout, "Updated: %s = %s\n", key, value)
	return cli.ExitOK
}

func runConfigUnset(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain config unset <key>")
		return cli.ExitUsage
	}

	cfg, err := loadConfigOrEmpty()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain config: %v\n", err)
		return cli.ExitSoftware
	}

	configUnset(cfg, args[0])

	if err := saveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "brain config unset: write: %v\n", err)
		return cli.ExitSoftware
	}
	fmt.Fprintf(os.Stdout, "Removed: %s\n", args[0])
	return cli.ExitOK
}

func runConfigPath(_ []string) int {
	fmt.Fprintln(os.Stdout, configPath())
	return cli.ExitOK
}
