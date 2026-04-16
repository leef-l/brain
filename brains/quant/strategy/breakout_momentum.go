package strategy

import (
	"fmt"
	"time"
)

// BreakoutMomentumParams holds tunable parameters for BreakoutMomentum.
type BreakoutMomentumParams struct {
	VolumeRatioThreshold float64 `json:"volume_ratio_threshold" yaml:"volume_ratio_threshold"` // 量能扩张阈值 (default 1.3)
	MomentumThreshold    float64 `json:"momentum_threshold" yaml:"momentum_threshold"`         // 动量阈值 (default 0.008)
	StrongMomentum       float64 `json:"strong_momentum" yaml:"strong_momentum"`               // 强动量独立触发 (default 0.02)
	BaseConfidence       float64 `json:"base_confidence" yaml:"base_confidence"`               // 基础置信度 (default 0.45)
	SignalBoost          float64 `json:"signal_boost" yaml:"signal_boost"`                     // 方向确认加分 (default 0.30)
	MomentumConfirmBoost float64 `json:"momentum_confirm_boost" yaml:"momentum_confirm_boost"` // 强动量确认乘数 (default 1.15)
	HTFBoost             float64 `json:"htf_boost" yaml:"htf_boost"`                          // 高TF动量确认乘数 (default 1.15)
	SLFallbackPct        float64 `json:"sl_fallback_pct" yaml:"sl_fallback_pct"`               // ATR无效时回退 (default 0.003)
}

func DefaultBreakoutMomentumParams() BreakoutMomentumParams {
	return BreakoutMomentumParams{
		VolumeRatioThreshold: 1.2,
		MomentumThreshold:    0.006,
		StrongMomentum:       0.015,
		BaseConfidence:       0.50,
		SignalBoost:          0.30,
		MomentumConfirmBoost: 1.15,
		HTFBoost:             1.15,
		SLFallbackPct:        0.003,
	}
}

type BreakoutMomentum struct {
	Params BreakoutMomentumParams
}

func NewBreakoutMomentum() Strategy { return BreakoutMomentum{Params: DefaultBreakoutMomentumParams()} }

func NewBreakoutMomentumWithParams(p BreakoutMomentumParams) Strategy {
	d := DefaultBreakoutMomentumParams()
	if p.VolumeRatioThreshold <= 0 { p.VolumeRatioThreshold = d.VolumeRatioThreshold }
	if p.MomentumThreshold <= 0 { p.MomentumThreshold = d.MomentumThreshold }
	if p.StrongMomentum <= 0 { p.StrongMomentum = d.StrongMomentum }
	if p.BaseConfidence <= 0 { p.BaseConfidence = d.BaseConfidence }
	if p.SignalBoost <= 0 { p.SignalBoost = d.SignalBoost }
	if p.MomentumConfirmBoost <= 0 { p.MomentumConfirmBoost = d.MomentumConfirmBoost }
	if p.HTFBoost <= 0 { p.HTFBoost = d.HTFBoost }
	if p.SLFallbackPct <= 0 { p.SLFallbackPct = d.SLFallbackPct }
	return BreakoutMomentum{Params: p}
}

func (BreakoutMomentum) Name() string { return "BreakoutMomentum" }

func (BreakoutMomentum) Timeframes() []string { return []string{"1m", "5m", "15m", "1H", "4H"} }

func (b BreakoutMomentum) Compute(view MarketView) Signal {
	if view.HasFeatureView() {
		return b.computeFromFeatures(view)
	}
	return b.computeLegacy(view)
}

// computeFromFeatures uses FeatureView for momentum/volume signals, but still
// needs candles for breakout level detection (high/low extremes aren't in the
// feature vector). Falls back to legacy if candles unavailable.
func (b BreakoutMomentum) computeFromFeatures(view MarketView) Signal {
	f := view.Feature()
	tf := view.Timeframe()

	// Guard: only bail when there is genuinely no price data.
	if f.CurrentPrice() <= 0 {
		return Signal{Strategy: "BreakoutMomentum", Direction: DirectionHold, Reason: "feature vector not ready", Timestamp: time.Now().UTC()}
	}

	priceNow := f.CurrentPrice()

	// Volume and momentum from FeatureView (O(1))
	volRatio := f.VolumeRatio(tf)
	obvSl := f.OBVSlope(tf)
	volBreakout := f.VolumeBreakout(tf)
	momentum10 := f.Momentum(tf, 10)

	volumeExpansion := volBreakout || volRatio > b.Params.VolumeRatioThreshold

	// Breakout detection: prefers candle-based high/low extremes, but
	// degrades to pure momentum+volume when candles are unavailable.
	var long, short bool
	var breakHigh, breakLow float64
	candles := view.Candles(tf)
	if len(candles) >= 30 {
		lookback := 20
		baseCandles := candles[:len(candles)-3]
		if len(baseCandles) < lookback {
			lookback = len(baseCandles)
		}
		if lookback >= 5 {
			breakHigh = highestHigh(baseCandles, lookback)
			breakLow = lowestLow(baseCandles, lookback)

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

			long = priceNow > breakHigh && volumeExpansion && obvSl > 0 && aboveConfirmations >= 1
			short = priceNow < breakLow && volumeExpansion && obvSl < 0 && belowConfirmations >= 1
		}
	}

	// Fallback: when candles are insufficient, use pure momentum + volume
	// expansion as a degraded breakout signal.
	momTh := b.Params.MomentumThreshold
	if !long && !short && volumeExpansion {
		long = momentum10 > momTh && obvSl > 0
		short = momentum10 < -momTh && obvSl < 0
	}

	// Second fallback: strong momentum alone (no volume required)
	strongMom := b.Params.StrongMomentum
	if !long && !short {
		long = momentum10 > strongMom && obvSl > 0
		short = momentum10 < -strongMom && obvSl < 0
	}

	if !long && !short {
		return Signal{
			Strategy:   "BreakoutMomentum",
			Direction:  DirectionHold,
			Confidence: 0.12,
			Reason:     "breakout conditions not confirmed",
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := b.Params.BaseConfidence
	reason := ""
	direction := DirectionHold

	if long {
		direction = DirectionLong
		confidence += b.Params.SignalBoost
		if breakHigh > 0 {
			reason = fmt.Sprintf("breakout above %.4f, vol_ratio=%.2f, obv_slope=%.4f", breakHigh, volRatio, obvSl)
		} else {
			reason = fmt.Sprintf("momentum breakout, mom10=%.4f, vol_ratio=%.2f, obv=%.4f", momentum10, volRatio, obvSl)
		}
	} else {
		direction = DirectionShort
		confidence += b.Params.SignalBoost
		if breakLow > 0 {
			reason = fmt.Sprintf("breakdown below %.4f, vol_ratio=%.2f, obv_slope=%.4f", breakLow, volRatio, obvSl)
		} else {
			reason = fmt.Sprintf("momentum breakdown, mom10=%.4f, vol_ratio=%.2f, obv=%.4f", momentum10, volRatio, obvSl)
		}
	}

	// Strong momentum confirmation
	if (direction == DirectionLong && momentum10 > b.Params.StrongMomentum) || (direction == DirectionShort && momentum10 < -b.Params.StrongMomentum) {
		confidence *= b.Params.MomentumConfirmBoost
		reason += fmt.Sprintf("; momentum10=%.4f confirms", momentum10)
	}

	// Higher TF confirmation (dynamic)
	htf := higherTF(tf)
	if htf != tf {
		htfMom := f.Momentum(htf, 10)
		if (direction == DirectionLong && htfMom > 0) || (direction == DirectionShort && htfMom < 0) {
			confidence *= b.Params.HTFBoost
			reason += "; " + htf + " momentum aligned"
		}
	}

	confidence = clamp(confidence, 0, 1)
	slATR := bestATRRatio(f, tf)
	slMult, tpMult := SLTPMultipliers(tf)
	atrDist := slATR * priceNow
	if atrDist <= 0 {
		atrDist = priceNow * b.Params.SLFallbackPct
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
		sl := breakHigh - atrDist*slMult
		if breakHigh <= 0 {
			sl = priceNow - atrDist*slMult
		}
		signal.StopLoss = sl
		signal.TakeProfit = priceNow + atrDist*tpMult
	} else {
		sl := breakLow + atrDist*slMult
		if breakLow <= 0 {
			sl = priceNow + atrDist*slMult
		}
		signal.StopLoss = sl
		signal.TakeProfit = priceNow - atrDist*tpMult
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
		signal.TakeProfit = entry + atrNow*2.5
	}
	if short {
		signal.Direction = DirectionShort
		signal.StopLoss = breakLow + atrNow*1.5
		signal.TakeProfit = entry - atrNow*2.5
	}
	return signal
}
