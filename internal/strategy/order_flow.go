package strategy

import (
	"fmt"
	"math"
	"time"
)

type OrderFlow struct{}

func NewOrderFlow() Strategy { return OrderFlow{} }

func (OrderFlow) Name() string { return "OrderFlow" }

func (OrderFlow) Timeframes() []string { return []string{"tick"} }

func (OrderFlow) Compute(view MarketView) Signal {
	candles := view.Candles(view.Timeframe())
	if len(candles) < 8 {
		return Signal{Strategy: "OrderFlow", Direction: DirectionHold, Reason: "insufficient microstructure data", Timestamp: time.Now().UTC()}
	}

	entry := view.CurrentPrice()
	if entry <= 0 {
		entry = last(closes(candles))
	}
	atrValue := atr(candles, 7)
	if atrValue == 0 {
		atrValue = math.Abs(entry) * 0.005
	}

	long := view.OrderBookImbalance() > 0.4 &&
		view.TradeFlowToxicity() > 0.65 &&
		view.BigBuyRatio() > 0.7 &&
		view.TradeDensityRatio() > 2

	short := view.OrderBookImbalance() < -0.4 &&
		view.TradeFlowToxicity() > 0.65 &&
		view.BigBuyRatio() < 0.3 &&
		view.TradeDensityRatio() > 2

	if !long && !short {
		return Signal{
			Strategy:   "OrderFlow",
			Direction:  DirectionHold,
			Confidence: 0.15,
			Reason:     "order flow imbalance not strong enough",
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := 0.45
	reason := ""
	if long {
		confidence += 0.25
		reason = fmt.Sprintf("buy imbalance %.2f with toxicity %.2f", view.OrderBookImbalance(), view.TradeFlowToxicity())
	}
	if short {
		confidence += 0.25
		reason = fmt.Sprintf("sell imbalance %.2f with toxicity %.2f", view.OrderBookImbalance(), view.TradeFlowToxicity())
	}

	signal := Signal{
		Strategy:   "OrderFlow",
		Confidence: clamp(confidence, 0, 1),
		Entry:      entry,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	if long {
		signal.Direction = DirectionLong
		signal.StopLoss = entry - atrValue
		signal.TakeProfit = entry + atrValue*1.8
	}
	if short {
		signal.Direction = DirectionShort
		signal.StopLoss = entry + atrValue
		signal.TakeProfit = entry - atrValue*1.8
	}
	return signal
}
