package strategy

import (
	"fmt"
	"math"
	"time"
)

type TrendFollower struct{}

func NewTrendFollower() Strategy { return TrendFollower{} }

func (TrendFollower) Name() string { return "TrendFollower" }

func (TrendFollower) Timeframes() []string { return []string{"1H", "4H"} }

func (t TrendFollower) Compute(view MarketView) Signal {
	if view.HasFeatureView() {
		return t.computeFromFeatures(view)
	}
	return t.computeLegacy(view)
}

// computeFromFeatures reads pre-computed indicators from the 192-dim
// feature vector via FeatureView. O(1) per indicator, no recomputation.
func (TrendFollower) computeFromFeatures(view MarketView) Signal {
	f := view.Feature()
	tf := view.Timeframe()

	ema9dev := f.EMADeviation(tf, 9)
	ema21dev := f.EMADeviation(tf, 21)
	ema55dev := f.EMADeviation(tf, 55)
	macdHist := f.MACDHistogram(tf)
	adxVal := f.ADX(tf)
	atrRatio := f.ATRRatio(tf)

	// EMA alignment: bullish = all positive & 9 > 21
	bullish := ema9dev > 0 && ema21dev > 0 && ema55dev > 0 &&
		ema9dev > ema21dev &&
		(adxVal > 0.20 || macdHist > 0)

	bearish := ema9dev < 0 && ema21dev < 0 && ema55dev < 0 &&
		ema9dev < ema21dev &&
		(adxVal > 0.20 || macdHist < 0)

	if !bullish && !bearish {
		return Signal{
			Strategy:   "TrendFollower",
			Direction:  DirectionHold,
			Confidence: 0.1,
			Reason:     "trend conditions not aligned",
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := 0.35
	direction := DirectionHold
	reason := ""

	if bullish {
		direction = DirectionLong
		confidence += 0.35
		reason = fmt.Sprintf("ema alignment bullish, adx=%.2f, macd=%.4f", adxVal, macdHist)
	} else {
		direction = DirectionShort
		confidence += 0.35
		reason = fmt.Sprintf("ema alignment bearish, adx=%.2f, macd=%.4f", adxVal, macdHist)
	}

	// Higher TF confirmation
	htf := "4H"
	htfEma9 := f.EMADeviation(htf, 9)
	htfEma21 := f.EMADeviation(htf, 21)
	htfEma55 := f.EMADeviation(htf, 55)
	if direction == DirectionLong && htfEma9 > 0 && htfEma21 > 0 && htfEma55 > 0 {
		confidence *= 1.3
		reason += "; 4H confirms trend"
	}
	if direction == DirectionShort && htfEma9 < 0 && htfEma21 < 0 && htfEma55 < 0 {
		confidence *= 1.3
		reason += "; 4H confirms trend"
	}

	// Funding rate
	if fr := f.FundingRate(); direction == DirectionLong && fr > 0 || direction == DirectionShort && fr < 0 {
		confidence *= 1.15
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
	stopDistance := atrRatio * priceNow * 2
	takeDistance := atrRatio * priceNow * 4
	if stopDistance <= 0 {
		stopDistance = math.Abs(priceNow) * 0.01
		takeDistance = stopDistance * 2
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
func (TrendFollower) computeLegacy(view MarketView) Signal {
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

	confidence := 0.35
	reason := ""
	direction := DirectionHold

	switch {
	case bullish:
		direction = DirectionLong
		reason = fmt.Sprintf("ema alignment bullish, adx=%.2f, macd=%.4f", adxValue, macdHist)
		confidence += 0.35
	case bearish:
		direction = DirectionShort
		reason = fmt.Sprintf("ema alignment bearish, adx=%.2f, macd=%.4f", adxValue, macdHist)
		confidence += 0.35
	default:
		return Signal{
			Strategy:   "TrendFollower",
			Direction:  DirectionHold,
			Confidence: 0.1,
			Reason:     "trend conditions not aligned",
			Timestamp:  time.Now().UTC(),
		}
	}

	if higher := view.Candles("4H"); len(higher) >= 55 {
		higherPrices := closes(higher)
		higherEma9 := ema(higherPrices, 9)
		higherEma21 := ema(higherPrices, 21)
		higherEma55 := ema(higherPrices, 55)
		if direction == DirectionLong && higherEma9 > higherEma21 && higherEma21 > higherEma55 && last(higherPrices) > higherEma55 {
			confidence *= 1.3
			reason += "; 4H confirms trend"
		}
		if direction == DirectionShort && higherEma9 < higherEma21 && higherEma21 < higherEma55 && last(higherPrices) < higherEma55 {
			confidence *= 1.3
			reason += "; 4H confirms trend"
		}
	}

	if fr := view.FundingRate(); direction == DirectionLong && fr > 0 || direction == DirectionShort && fr < 0 {
		confidence *= 1.15
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
	stopDistance := atrValue * 2
	takeDistance := atrValue * 4
	if stopDistance <= 0 {
		stopDistance = math.Abs(priceNow) * 0.01
		takeDistance = stopDistance * 2
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
