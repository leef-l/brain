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

func (MeanReversion) Compute(view MarketView) Signal {
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
