package strategy

import (
	"fmt"
	"math"
	"time"
)

// OrderFlowParams holds tunable parameters for OrderFlow.
type OrderFlowParams struct {
	ImbalanceThreshold float64 `json:"imbalance_threshold" yaml:"imbalance_threshold"` // min |imbalance| to score (default 0.15)
	ToxicityThreshold  float64 `json:"toxicity_threshold" yaml:"toxicity_threshold"`   // min toxicity to score (default 0.45)
	FlowScoreThreshold float64 `json:"flow_score_threshold" yaml:"flow_score_threshold"` // min score to trigger (default 0.6)
}

func DefaultOrderFlowParams() OrderFlowParams {
	return OrderFlowParams{
		ImbalanceThreshold: 0.15,
		ToxicityThreshold:  0.45,
		FlowScoreThreshold: 0.6,
	}
}

type OrderFlow struct {
	Params OrderFlowParams
}

func NewOrderFlow() Strategy { return OrderFlow{Params: DefaultOrderFlowParams()} }

func NewOrderFlowWithParams(p OrderFlowParams) Strategy {
	d := DefaultOrderFlowParams()
	if p.ImbalanceThreshold <= 0 {
		p.ImbalanceThreshold = d.ImbalanceThreshold
	}
	if p.ToxicityThreshold <= 0 {
		p.ToxicityThreshold = d.ToxicityThreshold
	}
	if p.FlowScoreThreshold <= 0 {
		p.FlowScoreThreshold = d.FlowScoreThreshold
	}
	return OrderFlow{Params: p}
}

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
func (o OrderFlow) computeFromFeatures(view MarketView) Signal {
	f := view.Feature()

	imbalance := f.OrderBookImbalance()
	toxicity := f.TradeFlowToxicity()
	bigBuy := f.BigBuyRatio()
	bigSell := f.BigSellRatio()
	density := f.TradeDensityRatio()
	buySell := f.BuySellRatio()
	spread := f.Spread()

	// Guard: microstructure data not yet populated.
	if spread == 0 && imbalance == 0 && toxicity == 0 && density == 0 {
		return Signal{Strategy: "OrderFlow", Direction: DirectionHold, Reason: "microstructure data not ready", Timestamp: time.Now().UTC()}
	}

	priceNow := f.CurrentPrice()
	atrRatio := f.ATRRatio("1m") // use shortest TF for order flow
	atrDist := atrRatio * priceNow
	if atrDist <= 0 {
		atrDist = math.Abs(priceNow) * 0.005
	}

	// Scoring approach: each indicator contributes to a directional score.
	// This replaces the hard AND gate that rarely triggers in swap markets.
	var buyScore, sellScore float64

	// Order book imbalance (strongest single signal)
	imbTh := o.Params.ImbalanceThreshold
	if imbalance > imbTh {
		buyScore += imbalance
	} else if imbalance < -imbTh {
		sellScore += -imbalance
	}

	// Trade flow toxicity (directional conviction)
	toxTh := o.Params.ToxicityThreshold
	if toxicity > toxTh {
		if imbalance > 0 {
			buyScore += (toxicity - toxTh) * 2
		} else if imbalance < 0 {
			sellScore += (toxicity - toxTh) * 2
		}
	}

	// Big order flow
	if bigBuy > 0.2 {
		buyScore += bigBuy * 0.5
	}
	if bigSell > 0.2 {
		sellScore += bigSell * 0.5
	}

	// Trade density (market activity)
	if density > 1.0 {
		densityBonus := math.Min((density-1.0)*0.3, 0.3)
		buyScore += densityBonus
		sellScore += densityBonus
	}

	// Buy/sell ratio
	if buySell > 1.2 {
		buyScore += (buySell - 1.0) * 0.3
	} else if buySell < 0.8 && buySell > 0 {
		sellScore += (1.0 - buySell) * 0.3
	}

	scoreTh := o.Params.FlowScoreThreshold
	long := buyScore >= scoreTh && buyScore > sellScore*1.3
	short := sellScore >= scoreTh && sellScore > buyScore*1.3

	if !long && !short {
		return Signal{
			Strategy:   "OrderFlow",
			Direction:  DirectionHold,
			Confidence: 0.15,
			Reason:     fmt.Sprintf("order flow imbalance not strong enough (buy=%.2f sell=%.2f)", buyScore, sellScore),
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := 0.40
	reason := ""
	direction := DirectionHold

	if long {
		direction = DirectionLong
		confidence += math.Min(buyScore*0.3, 0.35)
		reason = fmt.Sprintf("buy flow score=%.2f, imb=%.2f, tox=%.2f, bigBuy=%.2f", buyScore, imbalance, toxicity, bigBuy)
	} else {
		direction = DirectionShort
		confidence += math.Min(sellScore*0.3, 0.35)
		reason = fmt.Sprintf("sell flow score=%.2f, imb=%.2f, tox=%.2f, bigSell=%.2f", sellScore, imbalance, toxicity, bigSell)
	}

	// Tight spread = more reliable signal
	if spread > 0 && spread < 0.001 {
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
	// SL = 1.2× ATR, TP = 1.8× ATR → 1:1.5 盈亏比.
	// 1m 短线快进快出，10x 杠杆下留够呼吸空间.
	if direction == DirectionLong {
		signal.StopLoss = priceNow - atrDist*1.2
		signal.TakeProfit = priceNow + atrDist*1.8
	} else {
		signal.StopLoss = priceNow + atrDist*1.2
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

	long := view.OrderBookImbalance() > 0.25 &&
		view.TradeFlowToxicity() > 0.50 &&
		view.BigBuyRatio() > 0.3 &&
		view.TradeDensityRatio() > 1.2

	short := view.OrderBookImbalance() < -0.25 &&
		view.TradeFlowToxicity() > 0.50 &&
		view.BigSellRatio() > 0.3 &&
		view.TradeDensityRatio() > 1.2

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
		signal.StopLoss = entry - atrValue*1.2
		signal.TakeProfit = entry + atrValue*1.8
	}
	if short {
		signal.Direction = DirectionShort
		signal.StopLoss = entry + atrValue*1.2
		signal.TakeProfit = entry - atrValue*1.8
	}
	return signal
}
