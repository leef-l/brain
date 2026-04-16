package quant

import (
	"math"

	"github.com/leef-l/brain/brains/quant/risk"
)

// AutoRiskConfig enables automatic risk parameter calculation based on
// account equity and a risk appetite level. When enabled, the system
// derives guard limits and position sizer fractions from the actual
// account balance — no manual parameter tuning needed.
type AutoRiskConfig struct {
	// Enabled turns on auto-scaling. When true, risk parameters are
	// computed from account equity and Level. Manual values in RiskConfig
	// serve as ceilings — auto-computed values never exceed them.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Level controls risk appetite:
	//   "conservative" — real money, small drawdown tolerance
	//   "moderate"     — balanced (default)
	//   "aggressive"   — paper trading / data accumulation
	Level string `json:"level" yaml:"level"`

	// MinOrderUSDT is the exchange's minimum order notional.
	// Used to derive min_fraction so every order is executable.
	// Default: 5.0 (most crypto exchanges accept ≥ 5 USDT).
	MinOrderUSDT float64 `json:"min_order_usdt" yaml:"min_order_usdt"`
}

// riskProfile holds the derived parameters for a given level.
type riskProfile struct {
	MaxSinglePct      float64
	MaxTotalExposure  float64
	MaxSameDirection  float64
	MaxConcurrent     int
	StopNewTradesLoss float64
	LiquidateAllLoss  float64
	KellyScale        float64
	MaxFraction       float64
	// Global risk mirrors
	GlobalExposure    float64
	GlobalSameDir     float64
	GlobalDailyLoss   float64
	GlobalSymbol      float64
}

var profiles = map[string]riskProfile{
	"conservative": {
		MaxSinglePct:      2,
		MaxTotalExposure:  20,
		MaxSameDirection:  12,
		MaxConcurrent:     3,
		StopNewTradesLoss: 2,
		LiquidateAllLoss:  3,
		KellyScale:        0.15,
		MaxFraction:       0.03,
		GlobalExposure:    25,
		GlobalSameDir:     15,
		GlobalDailyLoss:   3,
		GlobalSymbol:      8,
	},
	"moderate": {
		MaxSinglePct:      5,
		MaxTotalExposure:  40,
		MaxSameDirection:  25,
		MaxConcurrent:     8,
		StopNewTradesLoss: 5,
		LiquidateAllLoss:  8,
		KellyScale:        0.25,
		MaxFraction:       0.05,
		GlobalExposure:    50,
		GlobalSameDir:     30,
		GlobalDailyLoss:   5,
		GlobalSymbol:      15,
	},
	"aggressive": {
		MaxSinglePct:      8,
		MaxTotalExposure:  80,
		MaxSameDirection:  50,
		MaxConcurrent:     15,
		StopNewTradesLoss: 10,
		LiquidateAllLoss:  15,
		KellyScale:        0.35,
		MaxFraction:       0.08,
		GlobalExposure:    80,
		GlobalSameDir:     50,
		GlobalDailyLoss:   15,
		GlobalSymbol:      20,
	},
}

// AutoScale computes RiskConfig from account equity and risk level.
// Manual config values act as ceilings — auto values never exceed them.
// Zero manual values mean no ceiling (auto value used as-is).
func (ac AutoRiskConfig) AutoScale(equity float64, manual RiskConfig) RiskConfig {
	if !ac.Enabled || equity <= 0 {
		return manual
	}

	level := ac.Level
	if level == "" {
		level = "moderate"
	}
	p, ok := profiles[level]
	if !ok {
		p = profiles["moderate"]
	}

	minOrderUSDT := ac.MinOrderUSDT
	if minOrderUSDT <= 0 {
		minOrderUSDT = 5.0
	}

	// Derive min_fraction: ensure every order meets exchange minimum.
	// Floor at 0.2% to avoid absurdly small fractions on large accounts.
	minFraction := math.Max(minOrderUSDT/equity, 0.002)

	// Adjust max_concurrent for account size.
	maxConcurrent := p.MaxConcurrent
	switch {
	case equity < 100:
		// Micro accounts: very few positions to keep each meaningful.
		maxConcurrent = clampInt(maxConcurrent, 1, 2)
	case equity < 1000:
		// Small accounts: limit concurrency.
		maxConcurrent = clampInt(maxConcurrent, 1, 3)
	case equity > 50000:
		// Large accounts: allow more diversification.
		maxConcurrent = clampInt(int(float64(maxConcurrent)*1.5), 1, 20)
	}

	// For small accounts, if minFraction × maxConcurrent > maxTotalExposure,
	// reduce concurrency so positions don't exceed exposure limit.
	if minFraction*float64(maxConcurrent)*100 > p.MaxTotalExposure {
		maxConcurrent = clampInt(int(p.MaxTotalExposure/100/minFraction), 1, maxConcurrent)
	}

	// Max fraction must be ≥ min fraction.
	maxFraction := math.Max(p.MaxFraction, minFraction*1.5)

	result := RiskConfig{
		Guard: GuardConfig{
			MaxSinglePositionPct:   p.MaxSinglePct,
			MaxConcurrentPositions: maxConcurrent,
			MaxTotalExposurePct:    p.MaxTotalExposure,
			MaxSameDirectionPct:    p.MaxSameDirection,
			StopNewTradesLossPct:   p.StopNewTradesLoss,
			LiquidateAllLossPct:    p.LiquidateAllLoss,
		},
		Sizer: SizerConfig{
			MinFraction:   minFraction,
			MaxFraction:   maxFraction,
			ScaleFraction: p.KellyScale,
		},
	}

	// Apply manual ceilings: if user set a non-zero value, auto can't exceed it.
	applyGuardCeiling(&result.Guard, &manual.Guard)
	applySizerCeiling(&result.Sizer, &manual.Sizer)

	return result
}

// AutoScaleGlobalRisk computes GlobalRiskConfig from risk level.
func (ac AutoRiskConfig) AutoScaleGlobalRisk(manual risk.GlobalRiskConfig) risk.GlobalRiskConfig {
	if !ac.Enabled {
		return manual
	}

	level := ac.Level
	if level == "" {
		level = "moderate"
	}
	p, ok := profiles[level]
	if !ok {
		p = profiles["moderate"]
	}

	result := risk.GlobalRiskConfig{
		MaxGlobalExposurePct:   p.GlobalExposure,
		MaxGlobalSameDirection: p.GlobalSameDir,
		MaxGlobalDailyLoss:     p.GlobalDailyLoss,
		MaxSymbolExposure:      p.GlobalSymbol,
	}

	// Apply ceilings.
	if manual.MaxGlobalExposurePct > 0 && result.MaxGlobalExposurePct > manual.MaxGlobalExposurePct {
		result.MaxGlobalExposurePct = manual.MaxGlobalExposurePct
	}
	if manual.MaxGlobalSameDirection > 0 && result.MaxGlobalSameDirection > manual.MaxGlobalSameDirection {
		result.MaxGlobalSameDirection = manual.MaxGlobalSameDirection
	}
	if manual.MaxGlobalDailyLoss > 0 && result.MaxGlobalDailyLoss > manual.MaxGlobalDailyLoss {
		result.MaxGlobalDailyLoss = manual.MaxGlobalDailyLoss
	}
	if manual.MaxSymbolExposure > 0 && result.MaxSymbolExposure > manual.MaxSymbolExposure {
		result.MaxSymbolExposure = manual.MaxSymbolExposure
	}

	return result
}

// applyGuardCeiling ensures auto values don't exceed manual ceilings.
func applyGuardCeiling(auto, manual *GuardConfig) {
	if manual.MaxSinglePositionPct > 0 && auto.MaxSinglePositionPct > manual.MaxSinglePositionPct {
		auto.MaxSinglePositionPct = manual.MaxSinglePositionPct
	}
	if manual.MaxConcurrentPositions > 0 && auto.MaxConcurrentPositions > manual.MaxConcurrentPositions {
		auto.MaxConcurrentPositions = manual.MaxConcurrentPositions
	}
	if manual.MaxTotalExposurePct > 0 && auto.MaxTotalExposurePct > manual.MaxTotalExposurePct {
		auto.MaxTotalExposurePct = manual.MaxTotalExposurePct
	}
	if manual.MaxSameDirectionPct > 0 && auto.MaxSameDirectionPct > manual.MaxSameDirectionPct {
		auto.MaxSameDirectionPct = manual.MaxSameDirectionPct
	}
	if manual.StopNewTradesLossPct > 0 && auto.StopNewTradesLossPct > manual.StopNewTradesLossPct {
		auto.StopNewTradesLossPct = manual.StopNewTradesLossPct
	}
	if manual.LiquidateAllLossPct > 0 && auto.LiquidateAllLossPct > manual.LiquidateAllLossPct {
		auto.LiquidateAllLossPct = manual.LiquidateAllLossPct
	}
	// MaxLeverage and stop distances are not auto-scaled (too domain-specific).
	// They come from manual config or defaults.
	if manual.MaxLeverage > 0 {
		auto.MaxLeverage = manual.MaxLeverage
	}
	if manual.MinStopDistanceATR > 0 {
		auto.MinStopDistanceATR = manual.MinStopDistanceATR
	}
	if manual.MaxStopDistancePct > 0 {
		auto.MaxStopDistancePct = manual.MaxStopDistancePct
	}
}

// applySizerCeiling ensures auto sizer values don't exceed manual ceilings.
func applySizerCeiling(auto, manual *SizerConfig) {
	if manual.MaxFraction > 0 && auto.MaxFraction > manual.MaxFraction {
		auto.MaxFraction = manual.MaxFraction
	}
	// MinFraction: manual value is a floor override (user says "at least this much").
	if manual.MinFraction > 0 && auto.MinFraction < manual.MinFraction {
		auto.MinFraction = manual.MinFraction
	}
	// ScaleFraction: manual ceiling.
	if manual.ScaleFraction > 0 && auto.ScaleFraction > manual.ScaleFraction {
		auto.ScaleFraction = manual.ScaleFraction
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
