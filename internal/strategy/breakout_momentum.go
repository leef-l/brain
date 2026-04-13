package strategy

import (
	"fmt"
	"time"
)

type BreakoutMomentum struct{}

func NewBreakoutMomentum() Strategy { return BreakoutMomentum{} }

func (BreakoutMomentum) Name() string { return "BreakoutMomentum" }

func (BreakoutMomentum) Timeframes() []string { return []string{"1H", "4H"} }

func (BreakoutMomentum) Compute(view MarketView) Signal {
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
