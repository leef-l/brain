package feature

import (
	"context"
	"fmt"
)

// MLFeatures holds the ML-enhanced feature dimensions [176:192].
// When MLEngine is not available, RuleFallback computes these from
// rule-based heuristics.
type MLFeatures struct {
	MarketRegime [4]float64 // [176:180] P(trend), P(range), P(breakout), P(panic)
	VolPredict   [4]float64 // [180:184] vol1H, vol4H, volPercentile, volDirection
	AnomalyScore [4]float64 // [184:188] price, volume, orderbook, combined
	Reserved     [4]float64 // [188:192] future use
}

// MLEngine is the ML inference interface for the data brain.
// It produces enhanced feature dimensions [176:192] from the
// rule-based features [0:176].
//
// MLEngine is a perception component (not a decision maker):
// it classifies market regime, predicts volatility, and detects anomalies.
// The quant brain consumes these dimensions transparently.
type MLEngine interface {
	// Predict takes the rule features [0:176] and returns ML-enhanced
	// dimensions [176:192].
	Predict(ctx context.Context, ruleFeatures []float64) (MLFeatures, error)

	// Ready reports whether the model is loaded and operational.
	// When false, FeatureAssembler uses RuleFallback automatically.
	Ready() bool

	// Name returns the engine identifier (for monitoring and logging).
	Name() string
}

// NullMLEngine is the default MLEngine implementation.
// It always returns Ready()=false, causing FeatureAssembler to use
// rule-based fallback for [176:192]. This ensures the system is fully
// operational without any ML models.
type NullMLEngine struct{}

// Predict always returns an error since NullMLEngine has no model.
func (NullMLEngine) Predict(_ context.Context, _ []float64) (MLFeatures, error) {
	return MLFeatures{}, fmt.Errorf("NullMLEngine: no model loaded")
}

// Ready always returns false.
func (NullMLEngine) Ready() bool { return false }

// Name returns "null".
func (NullMLEngine) Name() string { return "null" }
