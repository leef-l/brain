package quant

import (
	"time"

	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
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

	// Tags for grouping
	Tags []string `json:"tags" yaml:"tags"`

	// AccountRouter config
	Route *RouteConfig `json:"route,omitempty" yaml:"route,omitempty"`
}

// UnitConfig describes one trading unit in the config file.
type UnitConfig struct {
	ID          string   `json:"id" yaml:"id"`
	AccountID   string   `json:"account_id" yaml:"account_id"`
	Symbols     []string `json:"symbols" yaml:"symbols"`
	Timeframe   string   `json:"timeframe" yaml:"timeframe"`
	MaxLeverage int      `json:"max_leverage" yaml:"max_leverage"`
	Enabled     bool     `json:"enabled" yaml:"enabled"`
}

// StrategyConfig holds strategy weights and aggregator thresholds.
type StrategyConfig struct {
	Weights         map[string]float64 `json:"weights" yaml:"weights"`
	LongThreshold   float64            `json:"long_threshold" yaml:"long_threshold"`
	ShortThreshold  float64            `json:"short_threshold" yaml:"short_threshold"`
	DominanceFactor float64            `json:"dominance_factor" yaml:"dominance_factor"`
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

// FullConfig is the complete quant brain configuration.
type FullConfig struct {
	Brain    Config          `json:"brain" yaml:"brain"`
	Accounts []AccountConfig `json:"accounts" yaml:"accounts"`
	Units    []UnitConfig    `json:"units" yaml:"units"`
	Strategy StrategyConfig  `json:"strategy" yaml:"strategy"`
	Risk     RiskConfig      `json:"risk" yaml:"risk"`
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
		base.Weights = sc.Weights
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

	// Adaptive threshold: short timeframes produce weaker, less correlated
	// signals across strategies, so the aggregation threshold must be lower.
	switch timeframe {
	case "1m", "5m":
		base.LongThreshold *= 0.65
		base.ShortThreshold *= 0.65
		base.DominanceFactor = max(base.DominanceFactor*0.8, 1.1)
	case "15m":
		base.LongThreshold *= 0.80
		base.ShortThreshold *= 0.80
	}
	// 1H, 4H, 1D keep the configured threshold as-is.

	agg.SetBaseAggregator(base)
	return agg
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
