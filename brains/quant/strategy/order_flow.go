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

func (o OrderFlow) Compute(view MarketView) Signal {
	if view.HasFeatureView() {
		return o.computeFromFeatures(view)
	}
	return o.computeLegacy(view)
}

// computeFromFeatures reads microstructure indicators from the 192-dim
// feature vector via FeatureView. O(1) per indicator.
func (OrderFlow) computeFromFeatures(view MarketView) Signal {
	f := view.Feature()

	imbalance := f.OrderBookImbalance()
	toxicity := f.TradeFlowToxicity()
	bigBuy := f.BigBuyRatio()
	bigSell := f.BigSellRatio()
	density := f.TradeDensityRatio()
	buySell := f.BuySellRatio()
	spread := f.Spread()

	priceNow := f.CurrentPrice()
	atrRatio := f.ATRRatio("1m") // use shortest TF for order flow
	atrDist := atrRatio * priceNow
	if atrDist <= 0 {
		atrDist = math.Abs(priceNow) * 0.005
	}

	long := imbalance > 0.4 &&
		toxicity > 0.65 &&
		bigBuy > 0.7 &&
		density > 2

	short := imbalance < -0.4 &&
		toxicity > 0.65 &&
		bigSell > 0.7 &&
		density > 2

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
	direction := DirectionHold

	if long {
		direction = DirectionLong
		confidence += 0.25
		reason = fmt.Sprintf("buy imbalance %.2f, toxicity %.2f, bigBuy %.2f", imbalance, toxicity, bigBuy)
	} else {
		direction = DirectionShort
		confidence += 0.25
		reason = fmt.Sprintf("sell imbalance %.2f, toxicity %.2f, bigSell %.2f", imbalance, toxicity, bigSell)
	}

	// Buy/sell ratio confirmation
	if (direction == DirectionLong && buySell > 1.5) || (direction == DirectionShort && buySell < 0.67) {
		confidence *= 1.15
		reason += fmt.Sprintf("; buySellRatio=%.2f confirms", buySell)
	}

	// Tight spread = more reliable signal
	if spread < 0.001 {
		confidence *= 1.1
		reason += "; tight spread"
	}

	confidence = clamp(confidence, 0, 1)

	signal := Signal{
		Strategy:   "OrderFlow",
		Direction:  direction,
		Confidence: confidence,
		Entry:      priceNow,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	if direction == DirectionLong {
		signal.StopLoss = priceNow - atrDist
		signal.TakeProfit = priceNow + atrDist*1.8
	} else {
		signal.StopLoss = priceNow + atrDist
		signal.TakeProfit = priceNow - atrDist*1.8
	}
	return signal
}

// computeLegacy is the original MarketView-method-based computation for backtest mode.
func (OrderFlow) computeLegacy(view MarketView) Signal {
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
