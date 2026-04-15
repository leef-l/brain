package risk

import (
	"github.com/leef-l/brain/brains/quant/strategy"
)

// VolContext holds volatility predictions from the 192-dim feature vector
// [180:183]. These are used by AdaptiveGuard to tighten limits dynamically.
type VolContext struct {
	Vol1H         float64 // predicted 1H volatility
	Vol4H         float64 // predicted 4H volatility
	VolPercentile float64 // current vol percentile [0,1]
	VolDirection  float64 // vol trend direction
}

// AdaptiveGuard wraps Guard and dynamically adjusts risk limits based on
// the volatility prediction from the data brain's feature vector.
//
// When FeatureView is not available, it uses the base Guard unchanged.
type AdaptiveGuard struct {
	Base Guard

	// HighVolPercentile is the threshold above which limits tighten.
	HighVolPercentile float64 // default: 0.75
	// ExtremeVolPercentile triggers extreme tightening.
	ExtremeVolPercentile float64 // default: 0.90

	// Multipliers for high vol regime
	HighVolMaxPositionMult   float64 // default: 0.6 (reduce max position)
	HighVolMaxExposureMult   float64 // default: 0.7
	HighVolMaxConcurrentMult float64 // default: 0.6

	// Multipliers for extreme vol regime
	ExtremeVolMaxPositionMult   float64 // default: 0.4
	ExtremeVolMaxExposureMult   float64 // default: 0.5
	ExtremeVolMaxConcurrentMult float64 // default: 0.4
}

// DefaultAdaptiveGuard returns an AdaptiveGuard with sensible defaults.
func DefaultAdaptiveGuard() *AdaptiveGuard {
	return &AdaptiveGuard{
		Base:                        DefaultGuard(),
		HighVolPercentile:           0.75,
		ExtremeVolPercentile:        0.90,
		HighVolMaxPositionMult:      0.6,
		HighVolMaxExposureMult:      0.7,
		HighVolMaxConcurrentMult:    0.6,
		ExtremeVolMaxPositionMult:   0.4,
		ExtremeVolMaxExposureMult:   0.5,
		ExtremeVolMaxConcurrentMult: 0.4,
	}
}

// Evaluate runs the full 3-layer risk check with volatility-adaptive limits.
// If view has a FeatureView, limits are tightened based on VolPrediction.
func (ag *AdaptiveGuard) Evaluate(req OrderRequest, portfolio PortfolioSnapshot, circuit CircuitSnapshot, view strategy.MarketView) Decision {
	guard := ag.adaptedGuard(view)
	return guard.Evaluate(req, portfolio, circuit)
}

// adaptedGuard returns a copy of Base with limits adjusted for current vol.
func (ag *AdaptiveGuard) adaptedGuard(view strategy.MarketView) Guard {
	if view == nil || !view.HasFeatureView() {
		return ag.Base
	}

	vp := view.Feature().VolPrediction()
	vol := VolContext{
		Vol1H:         vp.Vol1H,
		Vol4H:         vp.Vol4H,
		VolPercentile: vp.VolPercentile,
		VolDirection:  vp.VolDirection,
	}

	return ag.adjustLimits(vol)
}

// adjustLimits creates a tightened Guard based on volatility context.
func (ag *AdaptiveGuard) adjustLimits(vol VolContext) Guard {
	g := ag.Base

	if vol.VolPercentile >= ag.ExtremeVolPercentile {
		g.MaxSinglePositionPct *= ag.ExtremeVolMaxPositionMult
		g.MaxTotalExposurePct *= ag.ExtremeVolMaxExposureMult
		g.MaxConcurrentPositions = int(float64(g.MaxConcurrentPositions) * ag.ExtremeVolMaxConcurrentMult)
		if g.MaxConcurrentPositions < 1 {
			g.MaxConcurrentPositions = 1
		}
	} else if vol.VolPercentile >= ag.HighVolPercentile {
		g.MaxSinglePositionPct *= ag.HighVolMaxPositionMult
		g.MaxTotalExposurePct *= ag.HighVolMaxExposureMult
		g.MaxConcurrentPositions = int(float64(g.MaxConcurrentPositions) * ag.HighVolMaxConcurrentMult)
		if g.MaxConcurrentPositions < 1 {
			g.MaxConcurrentPositions = 1
		}
	}

	// If volatility is trending up, further reduce same-direction exposure
	if vol.VolDirection > 0.5 {
		g.MaxSameDirectionPct *= 0.8
	}

	return g
}
