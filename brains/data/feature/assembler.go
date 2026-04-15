package feature

import "context"

// FeatureOutput is the result of FeatureAssembler.Compute.
type FeatureOutput struct {
	// Vector is the complete 192-dim feature vector:
	//   [0:176]   rule-based features
	//   [176:192] ML-enhanced or rule-fallback features
	Vector [VectorDim]float64

	// MLSource identifies who produced [176:192]:
	//   "null"     — NullMLEngine (always fallback)
	//   "fallback" — ML engine exists but failed/not ready
	//   "onnx_v1"  — an actual ML engine name
	MLSource string

	// MLReady indicates whether the ML engine is online.
	MLReady bool
}

// MarketRegimeLabel returns the dominant market regime from [176:179].
func (o *FeatureOutput) MarketRegimeLabel() string {
	labels := [4]string{"trend", "range", "breakout", "panic"}
	maxIdx := 0
	maxVal := o.Vector[176]
	for i := 1; i < 4; i++ {
		if o.Vector[176+i] > maxVal {
			maxVal = o.Vector[176+i]
			maxIdx = i
		}
	}
	return labels[maxIdx]
}

// AnomalyLevel returns the combined anomaly score from [187].
func (o *FeatureOutput) AnomalyLevel() float64 {
	return o.Vector[187]
}

// VolPercentile returns the volatility percentile from [182].
func (o *FeatureOutput) VolPercentile() float64 {
	return o.Vector[182]
}

// FeatureAssembler merges rule features [0:176] from Engine with
// ML-enhanced features [176:192] from MLEngine (or RuleFallback).
//
// This is the single exit point for the complete 192-dim vector.
// brain.go's featureLoop should use this instead of Engine directly.
type FeatureAssembler struct {
	rule     *Engine
	ml       MLEngine
	fallback *RuleFallback
}

// NewFeatureAssembler creates an assembler. If ml is nil,
// NullMLEngine is used (pure rule fallback mode).
func NewFeatureAssembler(rule *Engine, ml MLEngine, fallback *RuleFallback) *FeatureAssembler {
	if ml == nil {
		ml = NullMLEngine{}
	}
	return &FeatureAssembler{
		rule:     rule,
		ml:       ml,
		fallback: fallback,
	}
}

// Compute produces the complete 192-dim feature vector for an instrument.
func (a *FeatureAssembler) Compute(instID string) FeatureOutput {
	// 1. Rule features [0:176]
	ruleVec := a.rule.Compute(instID)

	// 2. ML-enhanced features [176:192]
	var mlFeatures MLFeatures
	var mlSource string

	if a.ml.Ready() {
		var err error
		mlFeatures, err = a.ml.Predict(context.Background(), ruleVec[:176])
		if err != nil {
			// ML inference failed, fallback
			mlFeatures = a.fallback.Compute(ruleVec, instID)
			mlSource = "fallback"
		} else {
			mlSource = a.ml.Name()
		}
	} else {
		// ML not ready, rule fallback
		mlFeatures = a.fallback.Compute(ruleVec, instID)
		mlSource = "fallback"
	}

	// 3. Assemble
	var vec [VectorDim]float64
	copy(vec[:], ruleVec)
	copy(vec[176:180], mlFeatures.MarketRegime[:])
	copy(vec[180:184], mlFeatures.VolPredict[:])
	copy(vec[184:188], mlFeatures.AnomalyScore[:])
	copy(vec[188:192], mlFeatures.Reserved[:])

	return FeatureOutput{
		Vector:   vec,
		MLSource: mlSource,
		MLReady:  a.ml.Ready(),
	}
}
