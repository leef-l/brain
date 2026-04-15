package strategy

import "math"

// RegimeWeights defines per-regime strategy weight overrides.
// Missing keys fall back to DefaultWeights().
type RegimeWeights struct {
	Trend    map[string]float64
	Range    map[string]float64
	Breakout map[string]float64
	Panic    map[string]float64
}

// DefaultRegimeWeights returns regime-specific weight sets tuned for the
// four market states detected by [176:179].
func DefaultRegimeWeights() RegimeWeights {
	return RegimeWeights{
		Trend: map[string]float64{
			"TrendFollower":    0.40,
			"MeanReversion":    0.15,
			"BreakoutMomentum": 0.25,
			"OrderFlow":        0.20,
		},
		Range: map[string]float64{
			"TrendFollower":    0.15,
			"MeanReversion":    0.40,
			"BreakoutMomentum": 0.20,
			"OrderFlow":        0.25,
		},
		Breakout: map[string]float64{
			"TrendFollower":    0.25,
			"MeanReversion":    0.10,
			"BreakoutMomentum": 0.40,
			"OrderFlow":        0.25,
		},
		Panic: map[string]float64{
			"TrendFollower":    0.20,
			"MeanReversion":    0.30,
			"BreakoutMomentum": 0.15,
			"OrderFlow":        0.35,
		},
	}
}

// RegimeAwareAggregator extends Aggregator by dynamically selecting strategy
// weights based on the dominant market regime from the 192-dim feature vector.
// When FeatureView is not available, it falls back to static DefaultWeights.
type RegimeAwareAggregator struct {
	base          Aggregator
	regimeWeights RegimeWeights
	// PanicDamping scales down confidence in panic regime (default 0.7).
	PanicDamping float64
}

// NewRegimeAwareAggregator creates a regime-aware aggregator with default settings.
func NewRegimeAwareAggregator() *RegimeAwareAggregator {
	return &RegimeAwareAggregator{
		base:          NewAggregator(),
		regimeWeights: DefaultRegimeWeights(),
		PanicDamping:  0.7,
	}
}

// BaseAggregator returns a copy of the underlying Aggregator for inspection/modification.
func (ra *RegimeAwareAggregator) BaseAggregator() Aggregator {
	return ra.base
}

// SetBaseAggregator replaces the underlying Aggregator (for config overrides).
func (ra *RegimeAwareAggregator) SetBaseAggregator(a Aggregator) {
	ra.base = a
}

// SetOracle sets the historical oracle on the underlying aggregator.
func (ra *RegimeAwareAggregator) SetOracle(oracle HistoricalOracle) {
	ra.base.Oracle = oracle
}

// Aggregate dynamically selects weights by market regime, then delegates to
// the base Aggregator logic.
func (ra *RegimeAwareAggregator) Aggregate(view MarketView, signals []Signal, review ReviewContext) AggregatedSignal {
	regime := "unknown"

	if view.HasFeatureView() {
		f := view.Feature()
		mr := f.MarketRegime()
		regime = mr.Dominant()
		ra.base.Weights = ra.weightsForRegime(regime)
	} else {
		ra.base.Weights = DefaultWeights()
	}

	result := ra.base.Aggregate(view, signals, review)

	// In panic regime: dampen confidence and flag for review
	if regime == "panic" {
		result.Confidence *= ra.PanicDamping
		result.Confidence = clamp(result.Confidence, 0, 1)
		if !result.NeedsReview {
			result.NeedsReview = true
			if result.ReviewReason != "" {
				result.ReviewReason += "; "
			}
			result.ReviewReason += "panic regime detected"
		}
	}

	// Anomaly check from feature vector
	if view.HasFeatureView() {
		anomaly := view.Feature().AnomalyScore()
		if anomaly.Combined > 0.8 {
			result.NeedsReview = true
			if result.ReviewReason != "" {
				result.ReviewReason += "; "
			}
			result.ReviewReason += "high anomaly score"
		}
	}

	return result
}

// weightsForRegime returns the weight map for the given regime string.
func (ra *RegimeAwareAggregator) weightsForRegime(regime string) map[string]float64 {
	switch regime {
	case "trend":
		return copyWeights(ra.regimeWeights.Trend)
	case "range":
		return copyWeights(ra.regimeWeights.Range)
	case "breakout":
		return copyWeights(ra.regimeWeights.Breakout)
	case "panic":
		return copyWeights(ra.regimeWeights.Panic)
	default:
		return DefaultWeights()
	}
}

// BlendedWeights computes a probability-weighted blend of all four regime
// weight sets. This is a smoother alternative to picking the dominant regime.
func (ra *RegimeAwareAggregator) BlendedWeights(mr MarketRegimeProb) map[string]float64 {
	strategies := []string{"TrendFollower", "MeanReversion", "BreakoutMomentum", "OrderFlow"}
	blended := make(map[string]float64, len(strategies))

	probs := [4]float64{mr.Trend, mr.Range, mr.Breakout, mr.Panic}
	regimeMaps := [4]map[string]float64{
		ra.regimeWeights.Trend,
		ra.regimeWeights.Range,
		ra.regimeWeights.Breakout,
		ra.regimeWeights.Panic,
	}

	// Normalize probabilities
	sum := 0.0
	for _, p := range probs {
		sum += math.Abs(p)
	}
	if sum == 0 {
		return DefaultWeights()
	}

	for _, s := range strategies {
		w := 0.0
		for i, p := range probs {
			if regimeMaps[i] != nil {
				w += (math.Abs(p) / sum) * regimeMaps[i][s]
			}
		}
		blended[s] = w
	}
	return blended
}

func copyWeights(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
