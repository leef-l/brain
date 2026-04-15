package strategy

import (
	"fmt"
	"time"
)

type BreakoutMomentum struct{}

func NewBreakoutMomentum() Strategy { return BreakoutMomentum{} }

func (BreakoutMomentum) Name() string { return "BreakoutMomentum" }

func (BreakoutMomentum) Timeframes() []string { return []string{"1H", "4H"} }

func (b BreakoutMomentum) Compute(view MarketView) Signal {
	if view.HasFeatureView() {
		return b.computeFromFeatures(view)
	}
	return b.computeLegacy(view)
}

// computeFromFeatures uses FeatureView for momentum/volume signals, but still
// needs candles for breakout level detection (high/low extremes aren't in the
// feature vector). Falls back to legacy if candles unavailable.
func (BreakoutMomentum) computeFromFeatures(view MarketView) Signal {
	f := view.Feature()
	tf := view.Timeframe()

	// Guard: feature vector not yet populated.
	if f.ATRRatio(tf) == 0 && f.VolumeRatio(tf) == 0 {
		return Signal{Strategy: "BreakoutMomentum", Direction: DirectionHold, Reason: "feature vector not ready", Timestamp: time.Now().UTC()}
	}

	// Breakout detection still needs candles for high/low extremes
	candles := view.Candles(tf)
	if len(candles) < 30 {
		return Signal{Strategy: "BreakoutMomentum", Direction: DirectionHold, Reason: "insufficient candles for breakout levels", Timestamp: time.Now().UTC()}
	}

	priceNow := f.CurrentPrice()
	lookback := 20
	baseCandles := candles[:len(candles)-3]
	if len(baseCandles) < lookback {
		lookback = len(baseCandles)
	}
	if lookback < 5 {
		return Signal{Strategy: "BreakoutMomentum", Direction: DirectionHold, Reason: "insufficient breakout history", Timestamp: time.Now().UTC()}
	}
	breakHigh := highestHigh(baseCandles, lookback)
	breakLow := lowestLow(baseCandles, lookback)

	// Volume and momentum from FeatureView (O(1))
	volRatio := f.VolumeRatio(tf)
	obvSl := f.OBVSlope(tf)
	volBreakout := f.VolumeBreakout(tf)
	atrRatio := f.ATRRatio(tf)
	momentum10 := f.Momentum(tf, 10)

	volumeExpansion := volBreakout || volRatio > 1.8

	aboveConfirmations := 0
	belowConfirmations := 0
	for _, c := range candles[len(candles)-3:] {
		if c.Close > breakHigh {
			aboveConfirmations++
		}
		if c.Close < breakLow {
			belowConfirmations++
		}
	}

	long := priceNow > breakHigh && volumeExpansion && obvSl > 0 && aboveConfirmations >= 1
	short := priceNow < breakLow && volumeExpansion && obvSl < 0 && belowConfirmations >= 1

	if !long && !short {
		return Signal{
			Strategy:   "BreakoutMomentum",
			Direction:  DirectionHold,
			Confidence: 0.12,
			Reason:     "breakout conditions not confirmed",
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := 0.45
	reason := ""
	direction := DirectionHold

	if long {
		direction = DirectionLong
		confidence += 0.30
		reason = fmt.Sprintf("breakout above %.4f, vol_ratio=%.2f, obv_slope=%.4f", breakHigh, volRatio, obvSl)
	} else {
		direction = DirectionShort
		confidence += 0.30
		reason = fmt.Sprintf("breakdown below %.4f, vol_ratio=%.2f, obv_slope=%.4f", breakLow, volRatio, obvSl)
	}

	// Strong momentum confirmation
	if (direction == DirectionLong && momentum10 > 0.02) || (direction == DirectionShort && momentum10 < -0.02) {
		confidence *= 1.15
		reason += fmt.Sprintf("; momentum10=%.4f confirms", momentum10)
	}

	// Higher TF confirmation
	htf := "4H"
	htfMom := f.Momentum(htf, 10)
	if (direction == DirectionLong && htfMom > 0) || (direction == DirectionShort && htfMom < 0) {
		confidence *= 1.2
		reason += "; 4H momentum aligned"
	}

	confidence = clamp(confidence, 0, 1)
	atrDist := atrRatio * priceNow
	if atrDist <= 0 {
		atrDist = priceNow * 0.01
	}

	signal := Signal{
		Strategy:   "BreakoutMomentum",
		Direction:  direction,
		Confidence: confidence,
		Entry:      priceNow,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	if direction == DirectionLong {
		signal.StopLoss = breakHigh - atrDist*1.5
		signal.TakeProfit = priceNow + atrDist*3
	} else {
		signal.StopLoss = breakLow + atrDist*1.5
		signal.TakeProfit = priceNow - atrDist*3
	}
	return signal
}

// computeLegacy is the original candle-based computation for backtest mode.
func (BreakoutMomentum) computeLegacy(view MarketView) Signal {
	candles := view.Candles(view.Timeframe())
	if len(candles) < 30 {
		return Signal{Strategy: "BreakoutMomentum", Direction: DirectionHold, Reason: "insufficient candles", Timestamp: time.Now().UTC()}
	}

	prices := closes(candles)
	vols := volumes(candles)
	entry := view.CurrentPrice()
	if entry <= 0 {
		entry = last(prices)
	}
	lookback := 20
	if len(candles) < lookback+3 {
		lookback = len(candles) - 3
	}
	if lookback < 5 {
		return Signal{Strategy: "BreakoutMomentum", Direction: DirectionHold, Reason: "insufficient breakout history", Timestamp: time.Now().UTC()}
	}

	baseCandles := candles[:len(candles)-3]
	breakHigh := highestHigh(baseCandles, lookback)
	breakLow := lowestLow(baseCandles, lookback)
	volMA := 0.0
	if len(vols) > 1 {
		volMA = sma(vols[:len(vols)-1], min(20, len(vols)-1))
	}
	atrNow := atr(candles, 14)
	if atrNow == 0 {
		return Signal{Strategy: "BreakoutMomentum", Direction: DirectionHold, Reason: "atr unavailable", Timestamp: time.Now().UTC()}
	}

	volumeExpansion := volMA > 0 && last(vols) > volMA*1.8
	obvUp := obvSlope(candles) > 0
	obvDown := obvSlope(candles) < 0

	aboveConfirmations := 0
	belowConfirmations := 0
	for _, c := range candles[len(candles)-3:] {
		if c.Close > breakHigh {
			aboveConfirmations++
		}
		if c.Close < breakLow {
			belowConfirmations++
		}
	}

	long := entry > breakHigh && volumeExpansion && obvUp && aboveConfirmations >= 1
	short := entry < breakLow && volumeExpansion && obvDown && belowConfirmations >= 1
	if !long && !short {
		return Signal{
			Strategy:   "BreakoutMomentum",
			Direction:  DirectionHold,
			Confidence: 0.12,
			Reason:     "breakout conditions not confirmed",
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := 0.45
	reason := ""
	if long {
		confidence += 0.30
		reason = fmt.Sprintf("breakout above %.4f, volume and obv confirm", breakHigh)
	}
	if short {
		confidence += 0.30
		reason = fmt.Sprintf("breakdown below %.4f, volume and obv confirm", breakLow)
	}

	signal := Signal{
		Strategy:   "BreakoutMomentum",
		Confidence: clamp(confidence, 0, 1),
		Entry:      entry,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	if long {
		signal.Direction = DirectionLong
		signal.StopLoss = breakHigh - atrNow*1.5
		signal.TakeProfit = entry + atrNow*3
	}
	if short {
		signal.Direction = DirectionShort
		signal.StopLoss = breakLow + atrNow*1.5
		signal.TakeProfit = entry - atrNow*3
	}
	return signal
}
