package strategy

import (
	"fmt"
	"math"
)

// HistoricalOracle optionally supplies historical win rates for feature-vector
// similarity checks.
type HistoricalOracle interface {
	HistoricalWinRate(symbol string, direction Direction, featureVector []float64) (float64, bool)
}

// ReviewContext captures the high-risk conditions that should trigger a
// follow-up LLM review in the main thread.
type ReviewContext struct {
	OpenPositions      int
	LargestPositionPct float64
	DailyLossPct       float64
	AnomalyDetected    bool
}

// Aggregator combines strategy outputs into one final signal.
type Aggregator struct {
	Weights         map[string]float64
	LongThreshold   float64
	ShortThreshold  float64
	DominanceFactor float64
	Oracle          HistoricalOracle
	// MinActiveStrategies is the minimum number of strategies that must produce
	// a directional (non-hold) signal for the aggregator to output a trade.
	// Default 0 = no minimum. Set to 2 to require multi-strategy agreement.
	MinActiveStrategies int
}

// AggregatedSignal is the final decision produced by the aggregator.
type AggregatedSignal struct {
	Symbol          string
	Direction       Direction
	Confidence      float64
	LongScore       float64
	ShortScore      float64
	Signals         []Signal
	NeedsReview     bool
	ReviewReason    string
	RejectionReason string
}

func DefaultWeights() map[string]float64 {
	return map[string]float64{
		"TrendFollower":    0.30,
		"MeanReversion":    0.25,
		"BreakoutMomentum": 0.25,
		"OrderFlow":        0.20,
	}
}

func NewAggregator() Aggregator {
	return Aggregator{
		Weights:         DefaultWeights(),
		LongThreshold:   0.45,
		ShortThreshold:  0.45,
		DominanceFactor: 1.5,
	}
}

func (a Aggregator) Aggregate(view MarketView, signals []Signal, review ReviewContext) AggregatedSignal {
	result := AggregatedSignal{Symbol: view.Symbol()}
	if len(signals) == 0 {
		result.Direction = DirectionHold
		result.RejectionReason = "no strategy signals"
		return result
	}

	for _, signal := range signals {
		if signal.Direction != DirectionLong && signal.Direction != DirectionShort {
			continue
		}
		weight := a.weightFor(signal.Strategy)
		score := clamp(signal.Confidence, 0, 1) * weight
		if signal.Direction == DirectionLong {
			result.LongScore += score
		} else {
			result.ShortScore += score
		}
		result.Signals = append(result.Signals, signal)
	}

	// Count how many distinct strategies produced a directional signal.
	if a.MinActiveStrategies > 0 {
		activeCount := 0
		for _, s := range result.Signals {
			if s.Direction == DirectionLong || s.Direction == DirectionShort {
				activeCount++
			}
		}
		if activeCount < a.MinActiveStrategies {
			result.Direction = DirectionHold
			result.RejectionReason = fmt.Sprintf("only %d active strategies, need %d", activeCount, a.MinActiveStrategies)
			return result
		}
	}

	switch {
	case result.LongScore > a.LongThreshold && result.LongScore > result.ShortScore*a.DominanceFactor:
		result.Direction = DirectionLong
	case result.ShortScore > a.ShortThreshold && result.ShortScore > result.LongScore*a.DominanceFactor:
		result.Direction = DirectionShort
	default:
		result.Direction = DirectionHold
		result.RejectionReason = "signals did not cross directional threshold"
	}

	if result.Direction != DirectionHold && a.Oracle != nil {
		if winRate, ok := a.Oracle.HistoricalWinRate(view.Symbol(), result.Direction, view.FeatureVector()); ok {
			switch result.Direction {
			case DirectionLong:
				if winRate < 0.35 {
					result.Direction = DirectionHold
					result.RejectionReason = "historical long win rate below 0.35"
				}
			case DirectionShort:
				if winRate > 0.65 {
					result.Direction = DirectionHold
					result.RejectionReason = "historical short win rate above 0.65"
				}
			}
		}
	}

	result.Confidence = clamp(math.Max(result.LongScore, result.ShortScore), 0, 1)
	if result.Direction == DirectionLong {
		result.Confidence = clamp(result.LongScore, 0, 1)
	}
	if result.Direction == DirectionShort {
		result.Confidence = clamp(result.ShortScore, 0, 1)
	}

	if review.OpenPositions >= 3 {
		result.NeedsReview = true
		result.ReviewReason = "open positions >= 3"
	}
	if review.LargestPositionPct > 5 {
		result.NeedsReview = true
		if result.ReviewReason != "" {
			result.ReviewReason += "; "
		}
		result.ReviewReason += "largest position pct > 5"
	}
	if review.DailyLossPct > 3 {
		result.NeedsReview = true
		if result.ReviewReason != "" {
			result.ReviewReason += "; "
		}
		result.ReviewReason += "daily loss pct > 3"
	}
	if review.AnomalyDetected {
		result.NeedsReview = true
		if result.ReviewReason != "" {
			result.ReviewReason += "; "
		}
		result.ReviewReason += "anomaly detected"
	}

	if result.Direction == DirectionHold && result.RejectionReason == "" {
		result.RejectionReason = "no usable direction"
	}
	return result
}

func (a Aggregator) weightFor(strategyName string) float64 {
	if a.Weights == nil {
		return DefaultWeights()[strategyName]
	}
	if w, ok := a.Weights[strategyName]; ok && w > 0 {
		return w
	}
	return DefaultWeights()[strategyName]
}
