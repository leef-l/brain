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

func (TrendFollower) Compute(view MarketView) Signal {
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
