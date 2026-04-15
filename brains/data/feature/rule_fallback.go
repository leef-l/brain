package feature

import (
	"math"

	"github.com/leef-l/brain/brains/data/processor"
)

// RuleFallback computes rule-based approximations for the ML-enhanced
// feature dimensions [176:192]. These are used when MLEngine.Ready()
// returns false (the default state), ensuring all 192 dimensions always
// have meaningful values.
//
// The quant brain does not need to know whether a dimension was computed
// by ML or by RuleFallback — the vector is the vector.
type RuleFallback struct {
	candles   *processor.CandleAggregator
	orderbook *processor.OrderBookTracker
	tradeflow *processor.TradeFlowTracker
}

// NewRuleFallback creates a RuleFallback from the three processor
// components.
func NewRuleFallback(
	candles *processor.CandleAggregator,
	orderbook *processor.OrderBookTracker,
	tradeflow *processor.TradeFlowTracker,
) *RuleFallback {
	return &RuleFallback{
		candles:   candles,
		orderbook: orderbook,
		tradeflow: tradeflow,
	}
}

// Compute produces rule-based ML feature dimensions for the given
// instrument. ruleVec is the [0:176] rule feature vector (used for
// volume ratio at index 60).
func (f *RuleFallback) Compute(ruleVec []float64, instID string) MLFeatures {
	var ml MLFeatures

	f.computeMarketRegime(&ml, ruleVec, instID)
	f.computeVolPredict(&ml, ruleVec, instID)
	f.computeAnomalyScore(&ml, ruleVec, instID)

	return ml
}

// computeMarketRegime fills [176:180] with trend/range/breakout/panic
// probabilities using ADX, EMA alignment, BB width, and ATR.
func (f *RuleFallback) computeMarketRegime(ml *MLFeatures, ruleVec []float64, instID string) {
	w := f.candles.GetWindow(instID, "1H")
	if w == nil || !w.ADX14.Ready() || !w.EMA9.Ready() || !w.EMA21.Ready() || !w.EMA55.Ready() || !w.BB20.Ready() || !w.ATR14.Ready() {
		// Indicators not ready — uniform distribution
		ml.MarketRegime = [4]float64{0.25, 0.25, 0.25, 0.25}
		return
	}

	adx := w.ADX14.Value()
	emaAligned := (w.EMA9.Value() > w.EMA21.Value() && w.EMA21.Value() > w.EMA55.Value()) ||
		(w.EMA9.Value() < w.EMA21.Value() && w.EMA21.Value() < w.EMA55.Value())
	atr := w.ATR14.Value()
	price := w.Current.Close

	// Trend probability
	if adx > 25 && emaAligned {
		ml.MarketRegime[0] = 0.7
	} else if adx > 20 {
		ml.MarketRegime[0] = 0.4
	} else {
		ml.MarketRegime[0] = 0.15
	}

	// Range probability
	bbWidth := 0.0
	if w.BB20.Ready() {
		upper := w.BB20.Upper()
		lower := w.BB20.Lower()
		mid := w.BB20.Middle()
		if mid > 0 {
			bbWidth = (upper - lower) / mid
		}
	}
	atrRatio := 0.0
	if price > 0 {
		atrRatio = atr / price
	}
	if adx < 20 && bbWidth < atrRatio*1.5 {
		ml.MarketRegime[1] = 0.7
	} else if adx < 25 {
		ml.MarketRegime[1] = 0.4
	} else {
		ml.MarketRegime[1] = 0.1
	}

	// Breakout probability
	bbPos := w.BBPosition()
	volRatio := 1.0
	if len(ruleVec) > 60 && ruleVec[60] > 0 {
		volRatio = ruleVec[60]
	}
	if (bbPos > 0.95 || bbPos < 0.05) && volRatio > 2.0 {
		ml.MarketRegime[2] = 0.7
	} else if bbPos > 0.85 || bbPos < 0.15 {
		ml.MarketRegime[2] = 0.3
	} else {
		ml.MarketRegime[2] = 0.05
	}

	// Panic probability
	priceMove := 0.0
	if atr > 0 && price > 0 {
		priceMove = math.Abs(w.PriceChangeRate(1)) * price / atr
	}
	if priceMove > 3 && volRatio > 3 {
		ml.MarketRegime[2] = math.Max(ml.MarketRegime[2], 0.3) // breakout is also elevated
		ml.MarketRegime[3] = 0.7
	} else if priceMove > 2 {
		ml.MarketRegime[3] = 0.2
	} else {
		ml.MarketRegime[3] = 0.05
	}

	// Normalize to probability distribution (sum = 1.0)
	normalizeProb(ml.MarketRegime[:])
}

// computeVolPredict fills [180:184] with volatility predictions using
// current ATR as a proxy.
func (f *RuleFallback) computeVolPredict(ml *MLFeatures, _ []float64, instID string) {
	// 1H volatility prediction ≈ current ATR ratio
	if w := f.candles.GetWindow(instID, "1H"); w != nil && w.ATR14.Ready() && w.Current.Close > 0 {
		ml.VolPredict[0] = w.ATR14.Value() / w.Current.Close
	}

	// 4H volatility prediction
	if w := f.candles.GetWindow(instID, "4H"); w != nil && w.ATR14.Ready() && w.Current.Close > 0 {
		ml.VolPredict[1] = w.ATR14.Value() / w.Current.Close
	}

	// Volatility percentile: vol5/vol20 as rough proxy
	if w := f.candles.GetWindow(instID, "1H"); w != nil {
		vol5 := w.Volatility(5)
		vol20 := w.Volatility(20)
		if vol20 > 0 {
			ml.VolPredict[2] = math.Min(vol5/vol20, 3.0) / 3.0
		} else {
			ml.VolPredict[2] = 0.5
		}
		// Volatility direction: accelerating or decelerating
		ml.VolPredict[3] = vol5/math.Max(vol20, 1e-10) - 1
	}
}

// computeAnomalyScore fills [184:188] with anomaly scores based on
// simple threshold rules.
func (f *RuleFallback) computeAnomalyScore(ml *MLFeatures, ruleVec []float64, instID string) {
	// Price anomaly: |change| > 3*ATR → 1.0
	if w := f.candles.GetWindow(instID, "1H"); w != nil && w.ATR14.Ready() && w.Current.Close > 0 {
		atr := w.ATR14.Value()
		if atr > 0 {
			priceAnomaly := math.Abs(w.PriceChangeRate(1)) * w.Current.Close / atr
			ml.AnomalyScore[0] = math.Min(priceAnomaly/3.0, 1.0)
		}
	}

	// Volume anomaly: vol > 5x avg → 1.0
	if len(ruleVec) > 60 && ruleVec[60] > 0 {
		ml.AnomalyScore[1] = math.Min(ruleVec[60]/5.0, 1.0)
	}

	// OrderBook anomaly: extreme imbalance + wide spread
	if ob := f.orderbook.Get(instID); ob != nil {
		obAnomaly := math.Abs(ob.Imbalance)
		if obAnomaly > 0.8 {
			ml.AnomalyScore[2] = obAnomaly
		}
	}

	// Combined: max of individual scores
	ml.AnomalyScore[3] = math.Max(ml.AnomalyScore[0],
		math.Max(ml.AnomalyScore[1], ml.AnomalyScore[2]))
}

// normalizeProb normalizes a slice to sum to 1.0.
func normalizeProb(p []float64) {
	sum := 0.0
	for _, v := range p {
		sum += v
	}
	if sum > 0 {
		for i := range p {
			p[i] /= sum
		}
	}
}
