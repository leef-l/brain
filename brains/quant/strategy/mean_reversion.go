package strategy

import (
	"fmt"
	"math"
	"time"
)

type MeanReversion struct{}

func NewMeanReversion() Strategy { return MeanReversion{} }

func (MeanReversion) Name() string { return "MeanReversion" }

func (MeanReversion) Timeframes() []string { return []string{"15m", "1H"} }

func (m MeanReversion) Compute(view MarketView) Signal {
	if view.HasFeatureView() {
		return m.computeFromFeatures(view)
	}
	return m.computeLegacy(view)
}

// computeFromFeatures reads BB position, RSI, ADX, volume from the 192-dim
// feature vector via FeatureView. O(1) per indicator.
func (MeanReversion) computeFromFeatures(view MarketView) Signal {
	f := view.Feature()
	tf := view.Timeframe()

	bbPos := f.BBPosition(tf)     // [0,1]: 0=at lower, 0.5=at mid, 1=at upper
	rsiVal := f.RSI(tf)           // [0,1] normalized
	adxVal := f.ADX(tf)           // [0,1] normalized
	volRatio := f.VolumeRatio(tf) // current vol / SMA vol
	atrRatio := f.ATRRatio(tf)

	// Guard: if core indicators are all zero the feature vector is not yet
	// populated (indicators still warming up). Returning a signal here would
	// be spurious — e.g. bbPos==0 would look like extreme oversold.
	if atrRatio == 0 && bbPos == 0 && rsiVal == 0 {
		return Signal{Strategy: "MeanReversion", Direction: DirectionHold, Reason: "feature vector not ready", Timestamp: time.Now().UTC()}
	}

	// Mean reversion: price near bands + low ADX (ranging market) + no volume spike
	long := (bbPos < 0.15 || (bbPos < 0.35 && rsiVal < 0.35)) && volRatio <= 1.2
	short := (bbPos > 0.85 || (bbPos > 0.65 && rsiVal > 0.65)) && volRatio <= 1.2

	if !long && !short {
		return Signal{
			Strategy:   "MeanReversion",
			Direction:  DirectionHold,
			Confidence: 0.12,
			Reason:     "mean reversion setup absent",
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := 0.40
	reason := ""
	direction := DirectionHold

	if long {
		direction = DirectionLong
		confidence += 0.25
		reason = fmt.Sprintf("bb_pos=%.2f (oversold), rsi=%.2f, adx=%.2f", bbPos, rsiVal, adxVal)
	} else {
		direction = DirectionShort
		confidence += 0.25
		reason = fmt.Sprintf("bb_pos=%.2f (overbought), rsi=%.2f, adx=%.2f", bbPos, rsiVal, adxVal)
	}

	// Low ADX (ranging) boosts confidence for mean reversion
	if adxVal < 0.25 {
		confidence *= 1.15
		reason += "; low adx confirms range"
	}

	// Funding rate contrarian: crowded longs = short opportunity
	if fr := f.FundingRate(); direction == DirectionShort && fr > 0.0005 {
		confidence *= 1.1
		reason += "; high funding supports short"
	} else if direction == DirectionLong && fr < -0.0005 {
		confidence *= 1.1
		reason += "; negative funding supports long"
	}

	confidence = clamp(confidence, 0, 1)
	priceNow := f.CurrentPrice()
	stopDistance := atrRatio * priceNow * 1.5
	if stopDistance <= 0 {
		stopDistance = math.Abs(priceNow) * 0.01
	}

	signal := Signal{
		Strategy:   "MeanReversion",
		Direction:  direction,
		Confidence: confidence,
		Entry:      priceNow,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	// Target = mid band (bbPos ≈ 0.5), estimate distance from ATR
	takeDistance := atrRatio * priceNow * 2
	if long {
		signal.StopLoss = priceNow - stopDistance
		signal.TakeProfit = priceNow + takeDistance
	} else {
		signal.StopLoss = priceNow + stopDistance
		signal.TakeProfit = priceNow - takeDistance
	}
	return signal
}

// computeLegacy is the original candle-based computation for backtest mode.
func (MeanReversion) computeLegacy(view MarketView) Signal {
	candles := view.Candles(view.Timeframe())
	if len(candles) < 25 {
		return Signal{Strategy: "MeanReversion", Direction: DirectionHold, Reason: "insufficient candles", Timestamp: time.Now().UTC()}
	}

	prices := closes(candles)
	vols := volumes(candles)
	mid, upper, lower, width := bollinger(prices, 20, 2.5)
	rsiValue := rsi(prices, 14)
	adxValue, _, _ := adx(candles, 14)
	volMA := sma(vols, 20)
	closePrice := last(prices)
	entry := view.CurrentPrice()
	if entry <= 0 {
		entry = closePrice
	}

	long := (closePrice < lower || (closePrice < mid && rsiValue < 35)) && last(vols) <= volMA*1.2
	short := (closePrice > upper || (closePrice > mid && rsiValue > 65)) && last(vols) <= volMA*1.2
	if !long && !short {
		return Signal{
			Strategy:   "MeanReversion",
			Direction:  DirectionHold,
			Confidence: 0.12,
			Reason:     "mean reversion setup absent",
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := 0.40
	reason := ""
	if long {
		confidence += 0.25
		reason = fmt.Sprintf("price below lower band, rsi=%.2f, adx=%.2f", rsiValue, adxValue)
	} else {
		confidence += 0.25
		reason = fmt.Sprintf("price above upper band, rsi=%.2f, adx=%.2f", rsiValue, adxValue)
	}

	stopDistance := width * 0.5
	if stopDistance <= 0 {
		stopDistance = math.Abs(entry) * 0.01
	}
	signal := Signal{
		Strategy:   "MeanReversion",
		Confidence: clamp(confidence, 0, 1),
		Entry:      entry,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	if long {
		signal.Direction = DirectionLong
		signal.StopLoss = entry - stopDistance
		signal.TakeProfit = mid
	} else {
		signal.Direction = DirectionShort
		signal.StopLoss = entry + stopDistance
		signal.TakeProfit = mid
	}
	return signal
}
