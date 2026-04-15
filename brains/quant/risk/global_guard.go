package risk

import (
	"fmt"
	"sync"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// GlobalRiskConfig defines cross-account aggregate risk limits.
type GlobalRiskConfig struct {
	// MaxGlobalExposurePct is the maximum total exposure across all accounts
	// as a percentage of total equity. Default: 50%.
	MaxGlobalExposurePct float64 `json:"max_global_exposure_pct" yaml:"max_global_exposure_pct"`

	// MaxGlobalSameDirection is the maximum exposure in one direction
	// across all accounts, as a percentage of total equity. Default: 30%.
	MaxGlobalSameDirection float64 `json:"max_global_same_direction" yaml:"max_global_same_direction"`

	// MaxGlobalDailyLoss is the maximum cumulative daily loss across all
	// accounts, as a percentage of total equity. Default: 5%.
	MaxGlobalDailyLoss float64 `json:"max_global_daily_loss" yaml:"max_global_daily_loss"`

	// MaxSymbolExposure is the maximum exposure for a single symbol
	// across all accounts, as a percentage of total equity. Default: 15%.
	MaxSymbolExposure float64 `json:"max_symbol_exposure" yaml:"max_symbol_exposure"`
}

// DefaultGlobalRiskConfig returns the default global risk thresholds.
func DefaultGlobalRiskConfig() GlobalRiskConfig {
	return GlobalRiskConfig{
		MaxGlobalExposurePct:   50,
		MaxGlobalSameDirection: 30,
		MaxGlobalDailyLoss:     5,
		MaxSymbolExposure:      15,
	}
}

// GlobalSnapshot is a point-in-time view of all accounts' combined state.
type GlobalSnapshot struct {
	TotalEquity float64
	Positions   []Position            // flattened from all accounts
	DailyPnL    map[string]float64    // accountID → today's PnL
}

// GlobalRiskGuard enforces cross-account risk limits.
// It prevents the scenario where two accounts individually pass their
// per-unit risk checks but collectively exceed safe aggregate limits.
type GlobalRiskGuard struct {
	config GlobalRiskConfig
	mu     sync.RWMutex
}

// NewGlobalRiskGuard creates a GlobalRiskGuard with the given config.
func NewGlobalRiskGuard(cfg GlobalRiskConfig) *GlobalRiskGuard {
	return &GlobalRiskGuard{config: cfg}
}

// Evaluate checks whether the proposed order is safe within the global
// cross-account risk limits. It returns a Decision; if Allowed=false,
// the order must be rejected across ALL accounts.
func (g *GlobalRiskGuard) Evaluate(req OrderRequest, snap GlobalSnapshot) Decision {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if snap.TotalEquity <= 0 {
		return deny("global", "total equity must be positive", false)
	}

	// Aggregate existing exposures
	totalExposure := 0.0
	longExposure := 0.0
	shortExposure := 0.0
	symbolExposure := make(map[string]float64)

	for _, p := range snap.Positions {
		totalExposure += p.Notional
		symbolExposure[p.Symbol] += p.Notional
		switch p.Direction {
		case strategy.DirectionLong:
			longExposure += p.Notional
		case strategy.DirectionShort:
			shortExposure += p.Notional
		}
	}

	// Add the proposed order
	totalExposure += req.Notional
	symbolExposure[req.Symbol] += req.Notional
	switch req.Direction {
	case strategy.DirectionLong:
		longExposure += req.Notional
	case strategy.DirectionShort:
		shortExposure += req.Notional
	}

	equity := snap.TotalEquity

	// Check 1: Global total exposure
	if totalExposure/equity*100 > g.config.MaxGlobalExposurePct {
		return deny("global", fmt.Sprintf(
			"global exposure %.1f%% exceeds limit %.0f%%",
			totalExposure/equity*100, g.config.MaxGlobalExposurePct), false)
	}

	// Check 2: Single symbol cross-account exposure
	if symbolExposure[req.Symbol]/equity*100 > g.config.MaxSymbolExposure {
		return deny("global", fmt.Sprintf(
			"symbol %s cross-account exposure %.1f%% exceeds limit %.0f%%",
			req.Symbol, symbolExposure[req.Symbol]/equity*100, g.config.MaxSymbolExposure), false)
	}

	// Check 3: Same direction cross-account exposure
	maxDir := longExposure
	if shortExposure > maxDir {
		maxDir = shortExposure
	}
	if maxDir/equity*100 > g.config.MaxGlobalSameDirection {
		return deny("global", fmt.Sprintf(
			"same direction exposure %.1f%% exceeds limit %.0f%%",
			maxDir/equity*100, g.config.MaxGlobalSameDirection), false)
	}

	// Check 4: Global daily loss
	totalDailyLoss := 0.0
	for _, pnl := range snap.DailyPnL {
		if pnl < 0 {
			totalDailyLoss += -pnl
		}
	}
	if equity > 0 && totalDailyLoss/equity*100 > g.config.MaxGlobalDailyLoss {
		return deny("global", fmt.Sprintf(
			"global daily loss %.1f%% exceeds limit %.0f%%",
			totalDailyLoss/equity*100, g.config.MaxGlobalDailyLoss), false)
	}

	return allow("global")
}

// Config returns the current config (for health/debug).
func (g *GlobalRiskGuard) Config() GlobalRiskConfig {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.config
}
