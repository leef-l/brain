package quant

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
	"gopkg.in/yaml.v3"
)

// AccountConfig describes one trading account in the config file.
type AccountConfig struct {
	ID         string `json:"id" yaml:"id"`
	Exchange   string `json:"exchange" yaml:"exchange"`     // "okx", "paper"
	APIKey     string `json:"api_key" yaml:"api_key"`
	SecretKey  string `json:"secret_key" yaml:"secret_key"`
	Passphrase string `json:"passphrase" yaml:"passphrase"`
	BaseURL    string `json:"base_url" yaml:"base_url"`
	Simulated  bool   `json:"simulated" yaml:"simulated"` // OKX demo mode

	// Paper exchange config
	InitialEquity float64 `json:"initial_equity" yaml:"initial_equity"`
	SlippageBps   float64 `json:"slippage_bps" yaml:"slippage_bps"` // paper滑点(基点), default 5 = 0.05%
	FeeBps        float64 `json:"fee_bps" yaml:"fee_bps"`           // paper手续费(基点), default 4 = 0.04%

	// Tags for grouping
	Tags []string `json:"tags" yaml:"tags"`

	// AccountRouter config
	Route *RouteConfig `json:"route,omitempty" yaml:"route,omitempty"`
}

// UnitConfig describes one trading unit in the config file.
// Each unit can override the global Strategy/Risk config. When Strategy or Risk
// is nil/zero, the global config is used. This allows one account to run
// multiple units with different strategy parameters and timeframes.
type UnitConfig struct {
	ID          string   `json:"id" yaml:"id"`
	AccountID   string   `json:"account_id" yaml:"account_id"`
	Symbols     []string `json:"symbols" yaml:"symbols"`
	Timeframe   string   `json:"timeframe" yaml:"timeframe"`
	MaxLeverage int      `json:"max_leverage" yaml:"max_leverage"`
	Enabled     bool     `json:"enabled" yaml:"enabled"`

	// Per-unit strategy override. When non-nil, this unit uses its own strategy
	// parameters instead of the global StrategyConfig. This enables running
	// different strategies on the same account (e.g. 1m scalping + 1H trend).
	Strategy *StrategyConfig `json:"strategy,omitempty" yaml:"strategy,omitempty"`

	// Per-unit risk override. When non-nil, this unit uses its own risk params.
	Risk *RiskConfig `json:"risk,omitempty" yaml:"risk,omitempty"`
}

// StrategyConfig holds strategy weights, aggregator thresholds, and per-strategy tunable params.
type StrategyConfig struct {
	Weights         map[string]float64              `json:"weights" yaml:"weights"`
	LongThreshold   float64                         `json:"long_threshold" yaml:"long_threshold"`
	ShortThreshold  float64                         `json:"short_threshold" yaml:"short_threshold"`
	DominanceFactor float64                         `json:"dominance_factor" yaml:"dominance_factor"`
	// MinActiveStrategies requires at least N strategies to produce directional
	// signals before the aggregator will output a trade. 0 = no minimum.
	MinActiveStrategies int                          `json:"min_active_strategies" yaml:"min_active_strategies"`
	// HighConfidenceBypass: 当单策略 confidence 超过此值时，绕过 MinActiveStrategies 限制。
	// 0 = 不启用。推荐 0.85-0.95，防止错过强势行情。
	HighConfidenceBypass float64                     `json:"high_confidence_bypass" yaml:"high_confidence_bypass"`
	TrendFollower   strategy.TrendFollowerParams     `json:"trend_follower" yaml:"trend_follower"`
	MeanReversion   strategy.MeanReversionParams     `json:"mean_reversion" yaml:"mean_reversion"`
	BreakoutMomentum strategy.BreakoutMomentumParams `json:"breakout_momentum" yaml:"breakout_momentum"`
	OrderFlow       strategy.OrderFlowParams         `json:"order_flow" yaml:"order_flow"`
}

// GuardConfig mirrors risk.Guard fields for config-file overrides.
type GuardConfig struct {
	MaxSinglePositionPct   float64 `json:"max_single_position_pct" yaml:"max_single_position_pct"`
	MaxLeverage            int     `json:"max_leverage" yaml:"max_leverage"`
	MinStopDistanceATR     float64 `json:"min_stop_distance_atr" yaml:"min_stop_distance_atr"`
	MaxStopDistancePct     float64 `json:"max_stop_distance_pct" yaml:"max_stop_distance_pct"`
	MaxConcurrentPositions int     `json:"max_concurrent_positions" yaml:"max_concurrent_positions"`
	MaxTotalExposurePct    float64 `json:"max_total_exposure_pct" yaml:"max_total_exposure_pct"`
	MaxSameDirectionPct    float64 `json:"max_same_direction_pct" yaml:"max_same_direction_pct"`
	StopNewTradesLossPct   float64 `json:"stop_new_trades_loss_pct" yaml:"stop_new_trades_loss_pct"`
	LiquidateAllLossPct    float64 `json:"liquidate_all_loss_pct" yaml:"liquidate_all_loss_pct"`
}

// SizerConfig mirrors risk.PositionSizer fields.
type SizerConfig struct {
	MinFraction   float64 `json:"min_fraction" yaml:"min_fraction"`
	MaxFraction   float64 `json:"max_fraction" yaml:"max_fraction"`
	ScaleFraction float64 `json:"scale_fraction" yaml:"scale_fraction"`
}

// RiskConfig groups guard + sizer configs.
type RiskConfig struct {
	Guard  GuardConfig `json:"guard" yaml:"guard"`
	Sizer  SizerConfig `json:"position_sizer" yaml:"position_sizer"`
}

// SignalExitConfig controls signal-reversal-based position closing.
type SignalExitConfig struct {
	// Enabled turns on signal reversal exit. Default: false.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// MinConfidence is the minimum aggregated confidence required for the
	// reversal signal to trigger a close. Default: 0.5.
	MinConfidence float64 `json:"min_confidence" yaml:"min_confidence"`

	// RequireMultiStrategy requires at least N strategies to agree on the
	// reversal direction before closing. Default: 2.
	RequireMultiStrategy int `json:"require_multi_strategy" yaml:"require_multi_strategy"`

	// MinHoldDuration is the minimum time a position must be held before
	// signal_exit can close it. Prevents the open→close→open churn loop
	// that occurs when signals flicker on short timeframes.
	// Default: 60s. Set to 0 to disable.
	MinHoldDuration time.Duration `json:"min_hold_duration" yaml:"min_hold_duration"`

	// CooldownAfterExit is the cooldown period after a signal_exit close
	// before the same symbol can be re-opened. Prevents immediately
	// re-entering a position that was just closed by signal reversal.
	// Default: 120s. Set to 0 to disable.
	CooldownAfterExit time.Duration `json:"cooldown_after_exit" yaml:"cooldown_after_exit"`

	// PositionHealth configures the EWMA-based health tracker for smooth exits.
	// When enabled, replaces the binary reversal check with a continuous health
	// score that decays gradually as signals turn against the position.
	PositionHealth PositionHealthConfig `json:"position_health" yaml:"position_health"`
}

// DefaultSignalExitConfig returns conservative defaults for signal-based exits.
func DefaultSignalExitConfig() SignalExitConfig {
	return SignalExitConfig{
		Enabled:              false,
		MinConfidence:        0.5,
		RequireMultiStrategy: 2,
		MinHoldDuration:      30 * time.Second,
		CooldownAfterExit:    60 * time.Second,
		PositionHealth:       DefaultPositionHealthConfig(),
	}
}

// TrailingStopConfig controls trailing stop-loss behavior.
// When price moves favorably past the activation threshold, SL follows
// the peak price at a configurable callback distance, locking in profits.
type TrailingStopConfig struct {
	// Enabled turns on trailing stop. Default: false.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// ActivationPct: activate trailing stop when unrealized profit reaches
	// this percentage of the TP distance. E.g. 0.5 = 50% of the way to TP.
	// Default: 0.5.
	ActivationPct float64 `json:"activation_pct" yaml:"activation_pct"`

	// CallbackPct: once activated, SL trails the peak price at this percentage
	// distance. E.g. 0.003 = 0.3% below peak (for longs).
	// Default: 0.003.
	CallbackPct float64 `json:"callback_pct" yaml:"callback_pct"`

	// StepPct: minimum improvement required to update SL (avoids flooding
	// the exchange with tiny updates). E.g. 0.001 = only move SL when the
	// new value is at least 0.1% better than current.
	// Default: 0.001.
	StepPct float64 `json:"step_pct" yaml:"step_pct"`

	// MaxLossWithoutTrailing: 当仓位未被移动止损激活（即价格从未向有利方向
	// 移动到激活线）且浮动亏损超过此金额（USDT）时，强制平仓止损。
	// 防止没有移动止损保护的仓位持续亏损。支持小数，如 0.5 = 0.5 USDT。
	// 0 = 不启用。Default: 0.
	MaxLossWithoutTrailing float64 `json:"max_loss_without_trailing" yaml:"max_loss_without_trailing"`
}

// DefaultTrailingStopConfig returns conservative defaults.
func DefaultTrailingStopConfig() TrailingStopConfig {
	return TrailingStopConfig{
		Enabled:       false,
		ActivationPct: 0.5,
		CallbackPct:   0.003,
		StepPct:       0.001,
	}
}

// WebUIConfig configures the embedded Web dashboard.
type WebUIConfig struct {
	// Enabled turns on the HTTP/WebSocket dashboard. Default: false.
	Enabled bool   `json:"enabled" yaml:"enabled"`
	// Addr is the listen address. Default: ":8380".
	Addr    string `json:"addr" yaml:"addr"`
}

// FullConfig is the complete quant brain configuration.
type FullConfig struct {
	Brain      Config            `json:"brain" yaml:"brain"`
	Accounts   []AccountConfig   `json:"accounts" yaml:"accounts"`
	Units      []UnitConfig      `json:"units" yaml:"units"`
	Strategy   StrategyConfig    `json:"strategy" yaml:"strategy"`
	Risk       RiskConfig        `json:"risk" yaml:"risk"`
	AutoRisk   AutoRiskConfig    `json:"auto_risk" yaml:"auto_risk"`
	GlobalRisk risk.GlobalRiskConfig `json:"global_risk" yaml:"global_risk"`
	SignalExit    SignalExitConfig    `json:"signal_exit" yaml:"signal_exit"`
	TrailingStop TrailingStopConfig `json:"trailing_stop" yaml:"trailing_stop"`
	WebUI        WebUIConfig        `json:"webui" yaml:"webui"`

	// ConfigPath is the file path from which this config was loaded.
	// Not serialized — set at load time for SaveConfig to know where to write.
	ConfigPath string `json:"-" yaml:"-"`
}

// SaveConfig writes the config back to the file it was loaded from.
// Format is determined by file extension (.json or .yaml).
func (fc FullConfig) SaveConfig() error {
	if fc.ConfigPath == "" {
		return fmt.Errorf("no config path set (using defaults, cannot save)")
	}
	ext := strings.ToLower(filepath.Ext(fc.ConfigPath))
	var data []byte
	var err error
	switch ext {
	case ".json":
		data, err = json.MarshalIndent(fc, "", "  ")
	default:
		data, err = yaml.Marshal(fc)
	}
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(fc.ConfigPath, data, 0644); err != nil {
		return fmt.Errorf("write config %s: %w", fc.ConfigPath, err)
	}
	return nil
}

// DefaultFullConfig returns a minimal working configuration with a paper account.
func DefaultFullConfig() FullConfig {
	return FullConfig{
		Brain: Config{
			CycleInterval:    5 * time.Second,
			DefaultTimeframe: "1H",
		},
		Accounts: []AccountConfig{
			{
				ID:            "paper-default",
				Exchange:      "paper",
				InitialEquity: 10000,
				Tags:          []string{"test"},
			},
		},
		Units: []UnitConfig{
			{
				ID:          "default-unit",
				AccountID:   "paper-default",
				Timeframe:   "1H",
				MaxLeverage: 10,
				Enabled:     true,
			},
		},
	}
}

// BuildAggregator creates a RegimeAwareAggregator from StrategyConfig.
// If config fields are zero, defaults are used. The timeframe parameter
// enables adaptive thresholds: shorter TFs get lower thresholds because
// individual signals are weaker and less synchronized.
func (sc StrategyConfig) BuildAggregator(timeframe string) *strategy.RegimeAwareAggregator {
	agg := strategy.NewRegimeAwareAggregator()
	base := agg.BaseAggregator()

	if len(sc.Weights) > 0 {
		// Deep copy: each unit gets its own map so regime-aware aggregator
		// can swap weights per-regime without cross-unit pollution.
		wCopy := make(map[string]float64, len(sc.Weights))
		for k, v := range sc.Weights {
			wCopy[k] = v
		}
		base.Weights = wCopy
	}
	if sc.LongThreshold > 0 {
		base.LongThreshold = sc.LongThreshold
	}
	if sc.ShortThreshold > 0 {
		base.ShortThreshold = sc.ShortThreshold
	}
	if sc.DominanceFactor > 0 {
		base.DominanceFactor = sc.DominanceFactor
	}
	if sc.MinActiveStrategies > 0 {
		base.MinActiveStrategies = sc.MinActiveStrategies
	}
	if sc.HighConfidenceBypass > 0 {
		base.HighConfidenceBypass = sc.HighConfidenceBypass
	}

	// Adaptive threshold: short timeframes produce weaker, less correlated
	// signals across strategies. We scale thresholds down slightly but not
	// aggressively — too-low thresholds cause noisy single-strategy trades.
	//
	// MinActiveStrategies is also reduced for short TFs because 1m/5m
	// signals are noisier and rarely align across 2+ strategies. Requiring
	// multi-strategy consensus at 1m effectively blocks all trades.
	// Short timeframes: lower thresholds + reduce MinActiveStrategies for
	// high-frequency trading. 1m signals are weak individually so we need
	// lower barriers. Risk is managed by tight trailing stops, not by
	// blocking entries.
	switch timeframe {
	case "1m":
		base.LongThreshold *= 0.70   // 1m HFT: low barrier, rely on trailing stop
		base.ShortThreshold *= 0.70
		base.MinActiveStrategies = 1  // single strategy OK for 1m
		if base.HighConfidenceBypass > 0 {
			base.HighConfidenceBypass *= 0.80
		}
	case "5m":
		base.LongThreshold *= 0.80
		base.ShortThreshold *= 0.80
		base.MinActiveStrategies = 1
		if base.HighConfidenceBypass > 0 {
			base.HighConfidenceBypass *= 0.85
		}
	case "15m":
		base.LongThreshold *= 0.90
		base.ShortThreshold *= 0.90
	}
	// 1H, 4H, 1D keep the configured threshold as-is.

	agg.SetBaseAggregator(base)
	return agg
}

// BuildPool creates a strategy Pool with per-strategy params from config.
// Zero-value params fall back to strategy defaults.
func (sc StrategyConfig) BuildPool() *strategy.Pool {
	return strategy.NewPool(
		strategy.NewTrendFollowerWithParams(sc.TrendFollower),
		strategy.NewMeanReversionWithParams(sc.MeanReversion),
		strategy.NewBreakoutMomentumWithParams(sc.BreakoutMomentum),
		strategy.NewOrderFlowWithParams(sc.OrderFlow),
	)
}

// BuildGuard creates an AdaptiveGuard from RiskConfig.
// Zero fields use DefaultGuard values.
func (rc RiskConfig) BuildGuard() *risk.AdaptiveGuard {
	ag := risk.DefaultAdaptiveGuard()
	g := &ag.Base

	if rc.Guard.MaxSinglePositionPct > 0 {
		g.MaxSinglePositionPct = rc.Guard.MaxSinglePositionPct
	}
	if rc.Guard.MaxLeverage > 0 {
		g.MaxLeverage = rc.Guard.MaxLeverage
	}
	if rc.Guard.MinStopDistanceATR > 0 {
		g.MinStopDistanceATR = rc.Guard.MinStopDistanceATR
	}
	if rc.Guard.MaxStopDistancePct > 0 {
		g.MaxStopDistancePct = rc.Guard.MaxStopDistancePct
	}
	if rc.Guard.MaxConcurrentPositions > 0 {
		g.MaxConcurrentPositions = rc.Guard.MaxConcurrentPositions
	}
	if rc.Guard.MaxTotalExposurePct > 0 {
		g.MaxTotalExposurePct = rc.Guard.MaxTotalExposurePct
	}
	if rc.Guard.MaxSameDirectionPct > 0 {
		g.MaxSameDirectionPct = rc.Guard.MaxSameDirectionPct
	}
	if rc.Guard.StopNewTradesLossPct > 0 {
		g.StopNewTradesLossPct = rc.Guard.StopNewTradesLossPct
	}
	if rc.Guard.LiquidateAllLossPct > 0 {
		g.LiquidateAllLossPct = rc.Guard.LiquidateAllLossPct
	}
	return ag
}

// BuildSizer creates a BayesianSizer from SizerConfig.
func (rc RiskConfig) BuildSizer() *risk.BayesianSizer {
	s := risk.DefaultBayesianSizer()
	if rc.Sizer.MinFraction > 0 {
		s.Base.MinFraction = rc.Sizer.MinFraction
	}
	if rc.Sizer.MaxFraction > 0 {
		s.Base.MaxFraction = rc.Sizer.MaxFraction
	}
	if rc.Sizer.ScaleFraction > 0 {
		s.Base.ScaleFraction = rc.Sizer.ScaleFraction
	}
	return s
}
