package strategy

import (
	"fmt"
	"math"
	"time"
)

// TrendFollowerParams holds tunable parameters for TrendFollower.
type TrendFollowerParams struct {
	ADXThreshold    float64 `json:"adx_threshold" yaml:"adx_threshold"`       // 最低 ADX 确认趋势 (default 0.15)
	BaseConfidence  float64 `json:"base_confidence" yaml:"base_confidence"`   // 基础置信度 (default 0.35)
	SignalBoost     float64 `json:"signal_boost" yaml:"signal_boost"`         // 方向确认加分 (default 0.30)
	EMA55Boost      float64 `json:"ema55_boost" yaml:"ema55_boost"`           // EMA55 同向加分 (default 0.10)
	HTFBoost        float64 `json:"htf_boost" yaml:"htf_boost"`              // 高级别TF确认乘数 (default 1.20)
	FundingBoost    float64 `json:"funding_boost" yaml:"funding_boost"`       // 资金费率对齐乘数 (default 1.15)
	SLFallbackPct   float64 `json:"sl_fallback_pct" yaml:"sl_fallback_pct"`   // ATR 无效时止损回退比例 (default 0.003)
}

func DefaultTrendFollowerParams() TrendFollowerParams {
	return TrendFollowerParams{
		ADXThreshold:   0.10,
		BaseConfidence: 0.40,
		SignalBoost:    0.35,
		EMA55Boost:     0.10,
		HTFBoost:       1.20,
		FundingBoost:   1.15,
		SLFallbackPct:  0.003,
	}
}

type TrendFollower struct {
	Params TrendFollowerParams
}

func NewTrendFollower() Strategy { return TrendFollower{Params: DefaultTrendFollowerParams()} }

func NewTrendFollowerWithParams(p TrendFollowerParams) Strategy {
	d := DefaultTrendFollowerParams()
	if p.ADXThreshold <= 0 {
		p.ADXThreshold = d.ADXThreshold
	}
	if p.BaseConfidence <= 0 {
		p.BaseConfidence = d.BaseConfidence
	}
	if p.SignalBoost <= 0 {
		p.SignalBoost = d.SignalBoost
	}
	if p.EMA55Boost <= 0 {
		p.EMA55Boost = d.EMA55Boost
	}
	if p.HTFBoost <= 0 {
		p.HTFBoost = d.HTFBoost
	}
	if p.FundingBoost <= 0 {
		p.FundingBoost = d.FundingBoost
	}
	if p.SLFallbackPct <= 0 {
		p.SLFallbackPct = d.SLFallbackPct
	}
	return TrendFollower{Params: p}
}

func (TrendFollower) Name() string { return "TrendFollower" }

func (TrendFollower) Timeframes() []string { return []string{"1m", "5m", "15m", "1H", "4H"} }

func (t TrendFollower) Compute(view MarketView) Signal {
	if view.HasFeatureView() {
		return t.computeFromFeatures(view)
	}
	return t.computeLegacy(view)
}

// computeFromFeatures reads pre-computed indicators from the 192-dim
// feature vector via FeatureView. O(1) per indicator, no recomputation.
func (t TrendFollower) computeFromFeatures(view MarketView) Signal {
	f := view.Feature()
	tf := view.Timeframe()

	ema9dev := f.EMADeviation(tf, 9)
	ema21dev := f.EMADeviation(tf, 21)
	ema55dev := f.EMADeviation(tf, 55)
	macdHist := f.MACDHistogram(tf)
	adxVal := f.ADX(tf)

	// Guard: EMA deviations are essential for trend detection. If both
	// EMA9 and EMA21 are zero the indicators haven't warmed up yet.
	if ema9dev == 0 && ema21dev == 0 {
		return Signal{Strategy: "TrendFollower", Direction: DirectionHold, Reason: "feature vector not ready", Timestamp: time.Now().UTC()}
	}

	// EMA alignment: core requirement is EMA9+EMA21 agreement.
	// EMA55 alignment and ADX provide additional confidence but are not required.
	adxTh := t.Params.ADXThreshold
	bullish := ema9dev > 0 && ema21dev > 0 && ema9dev > ema21dev &&
		(adxVal > adxTh || macdHist > 0)

	bearish := ema9dev < 0 && ema21dev < 0 && ema9dev < ema21dev &&
		(adxVal > adxTh || macdHist < 0)

	if !bullish && !bearish {
		return Signal{
			Strategy:   "TrendFollower",
			Direction:  DirectionHold,
			Confidence: 0.1,
			Reason:     "trend conditions not aligned",
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := t.Params.BaseConfidence
	direction := DirectionHold
	reason := ""

	if bullish {
		direction = DirectionLong
		confidence += t.Params.SignalBoost
		reason = fmt.Sprintf("ema alignment bullish, adx=%.2f, macd=%.4f", adxVal, macdHist)
		if ema55dev > 0 {
			confidence += t.Params.EMA55Boost
			reason += "; ema55 confirms"
		}
	} else {
		direction = DirectionShort
		confidence += t.Params.SignalBoost
		reason = fmt.Sprintf("ema alignment bearish, adx=%.2f, macd=%.4f", adxVal, macdHist)
		if ema55dev < 0 {
			confidence += t.Params.EMA55Boost
			reason += "; ema55 confirms"
		}
	}

	// Higher TF confirmation (dynamic: one level up from current TF)
	htf := higherTF(tf)
	if htf != tf {
		htfEma9 := f.EMADeviation(htf, 9)
		htfEma21 := f.EMADeviation(htf, 21)
		if direction == DirectionLong && htfEma9 > 0 && htfEma21 > 0 {
			confidence *= t.Params.HTFBoost
			reason += "; " + htf + " confirms trend"
		}
		if direction == DirectionShort && htfEma9 < 0 && htfEma21 < 0 {
			confidence *= t.Params.HTFBoost
			reason += "; " + htf + " confirms trend"
		}
	}

	// Funding rate
	if fr := f.FundingRate(); direction == DirectionLong && fr > 0 || direction == DirectionShort && fr < 0 {
		confidence *= t.Params.FundingBoost
		reason += "; funding aligned"
	}

	// Historical similarity (via legacy interface, kept for compatibility)
	if winRate := view.SimilarityWinRate(); winRate > 0 {
		if direction == DirectionLong && winRate >= 0.55 {
			confidence *= 1.2
			reason += "; similar history supportive"
		}
		if direction == DirectionShort && winRate <= 0.45 {
			confidence *= 1.2
			reason += "; similar history supportive"
		}
	}

	confidence = clamp(confidence, 0, 1)
	priceNow := f.CurrentPrice()
	slATR := bestATRRatio(f, tf)
	slMult, tpMult := SLTPMultipliers(tf)
	stopDistance := slATR * priceNow * slMult
	takeDistance := slATR * priceNow * tpMult
	if stopDistance <= 0 {
		stopDistance = math.Abs(priceNow) * t.Params.SLFallbackPct
		takeDistance = stopDistance * (tpMult / slMult)
	}

	signal := Signal{
		Strategy:   "TrendFollower",
		Direction:  direction,
		Confidence: confidence,
		Entry:      priceNow,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	if direction == DirectionLong {
		signal.StopLoss = priceNow - stopDistance
		signal.TakeProfit = priceNow + takeDistance
	} else {
		signal.StopLoss = priceNow + stopDistance
		signal.TakeProfit = priceNow - takeDistance
	}
	return signal
}

// computeLegacy is the original candle-based computation, used when
// FeatureView is not available (backtest mode).
func (t TrendFollower) computeLegacy(view MarketView) Signal {
	candles := view.Candles(view.Timeframe())
	if len(candles) < 60 {
		return Signal{Strategy: "TrendFollower", Direction: DirectionHold, Reason: "insufficient candles", Timestamp: time.Now().UTC()}
	}

	prices := closes(candles)
	ema9 := ema(prices, 9)
	ema21 := ema(prices, 21)
	ema55 := ema(prices, 55)
	macdHist := macdHistogram(prices)
	adxValue, diPlus, diMinus := adx(candles, 14)
	atrValue := atr(candles, 14)
	closePrice := last(prices)
	priceNow := view.CurrentPrice()
	if priceNow <= 0 {
		priceNow = closePrice
	}

	bullish := ema9 > ema21 && ema21 > ema55 &&
		closePrice > ema55 &&
		((adxValue > 20 && diPlus > diMinus) || macdHist > 0)

	bearish := ema9 < ema21 && ema21 < ema55 &&
		closePrice < ema55 &&
		((adxValue > 20 && diMinus > diPlus) || macdHist < 0)

	confidence := t.Params.BaseConfidence
	reason := ""
	direction := DirectionHold

	switch {
	case bullish:
		direction = DirectionLong
		reason = fmt.Sprintf("ema alignment bullish, adx=%.2f, macd=%.4f", adxValue, macdHist)
		confidence += t.Params.SignalBoost
	case bearish:
		direction = DirectionShort
		reason = fmt.Sprintf("ema alignment bearish, adx=%.2f, macd=%.4f", adxValue, macdHist)
		confidence += t.Params.SignalBoost
	default:
		return Signal{
			Strategy:   "TrendFollower",
			Direction:  DirectionHold,
			Confidence: 0.1,
			Reason:     "trend conditions not aligned",
			Timestamp:  time.Now().UTC(),
		}
	}

	htfLegacy := higherTF(view.Timeframe())
	if higher := view.Candles(htfLegacy); len(higher) >= 21 {
		higherPrices := closes(higher)
		higherEma9 := ema(higherPrices, 9)
		higherEma21 := ema(higherPrices, 21)
		if direction == DirectionLong && higherEma9 > higherEma21 {
			confidence *= t.Params.HTFBoost
			reason += "; " + htfLegacy + " confirms trend"
		}
		if direction == DirectionShort && higherEma9 < higherEma21 {
			confidence *= t.Params.HTFBoost
			reason += "; " + htfLegacy + " confirms trend"
		}
	}

	if fr := view.FundingRate(); direction == DirectionLong && fr > 0 || direction == DirectionShort && fr < 0 {
		confidence *= t.Params.FundingBoost
		reason += "; funding aligned"
	}

	if winRate := view.SimilarityWinRate(); winRate > 0 {
		if direction == DirectionLong && winRate >= 0.55 {
			confidence *= 1.2
			reason += "; similar history supportive"
		}
		if direction == DirectionShort && winRate <= 0.45 {
			confidence *= 1.2
			reason += "; similar history supportive"
		}
	}

	confidence = clamp(confidence, 0, 1)
	stopDistance := atrValue * 1.5
	takeDistance := atrValue * 2.0
	if stopDistance <= 0 {
		stopDistance = math.Abs(priceNow) * t.Params.SLFallbackPct
		takeDistance = stopDistance * 1.3
	}
	signal := Signal{
		Strategy:   "TrendFollower",
		Direction:  direction,
		Confidence: confidence,
		Entry:      priceNow,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	if direction == DirectionLong {
		signal.StopLoss = priceNow - stopDistance
		signal.TakeProfit = priceNow + takeDistance
	} else {
		signal.StopLoss = priceNow + stopDistance
		signal.TakeProfit = priceNow - takeDistance
	}
	return signal
}
