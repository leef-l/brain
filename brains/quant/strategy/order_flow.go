package strategy

import (
	"fmt"
	"math"
	"time"
)

// OrderFlowParams holds tunable parameters for OrderFlow.
type OrderFlowParams struct {
	ImbalanceThreshold float64 `json:"imbalance_threshold" yaml:"imbalance_threshold"` // 订单簿失衡阈值 (default 0.15)
	ToxicityThreshold  float64 `json:"toxicity_threshold" yaml:"toxicity_threshold"`   // 成交流毒性阈值 (default 0.45)
	FlowScoreThreshold float64 `json:"flow_score_threshold" yaml:"flow_score_threshold"` // 触发最低分数 (default 0.6)
	BaseConfidence     float64 `json:"base_confidence" yaml:"base_confidence"`         // 基础置信度 (default 0.40)
	BigOrderThreshold  float64 `json:"big_order_threshold" yaml:"big_order_threshold"` // 大单比例阈值 (default 0.20)
	DensityThreshold   float64 `json:"density_threshold" yaml:"density_threshold"`     // 交易密度阈值 (default 1.0)
	BuySellBullish     float64 `json:"buy_sell_bullish" yaml:"buy_sell_bullish"`       // 买卖比看多阈值 (default 1.2)
	BuySellBearish     float64 `json:"buy_sell_bearish" yaml:"buy_sell_bearish"`       // 买卖比看空阈值 (default 0.8)
	DirectionEdge      float64 `json:"direction_edge" yaml:"direction_edge"`           // 方向优势比 (default 1.3)
	SpreadBoost        float64 `json:"spread_boost" yaml:"spread_boost"`               // 窄点差加成 (default 1.10)
	SLFallbackPct      float64 `json:"sl_fallback_pct" yaml:"sl_fallback_pct"`         // ATR无效时回退 (default 0.005)
}

func DefaultOrderFlowParams() OrderFlowParams {
	return OrderFlowParams{
		ImbalanceThreshold: 0.10,
		ToxicityThreshold:  0.35,
		FlowScoreThreshold: 0.45,
		BaseConfidence:     0.45,
		BigOrderThreshold:  0.20,
		DensityThreshold:   1.0,
		BuySellBullish:     1.2,
		BuySellBearish:     0.8,
		DirectionEdge:      1.3,
		SpreadBoost:        1.10,
		SLFallbackPct:      0.005,
	}
}

type OrderFlow struct {
	Params OrderFlowParams
}

func NewOrderFlow() Strategy { return OrderFlow{Params: DefaultOrderFlowParams()} }

func NewOrderFlowWithParams(p OrderFlowParams) Strategy {
	d := DefaultOrderFlowParams()
	if p.ImbalanceThreshold <= 0 { p.ImbalanceThreshold = d.ImbalanceThreshold }
	if p.ToxicityThreshold <= 0 { p.ToxicityThreshold = d.ToxicityThreshold }
	if p.FlowScoreThreshold <= 0 { p.FlowScoreThreshold = d.FlowScoreThreshold }
	if p.BaseConfidence <= 0 { p.BaseConfidence = d.BaseConfidence }
	if p.BigOrderThreshold <= 0 { p.BigOrderThreshold = d.BigOrderThreshold }
	if p.DensityThreshold <= 0 { p.DensityThreshold = d.DensityThreshold }
	if p.BuySellBullish <= 0 { p.BuySellBullish = d.BuySellBullish }
	if p.BuySellBearish <= 0 { p.BuySellBearish = d.BuySellBearish }
	if p.DirectionEdge <= 0 { p.DirectionEdge = d.DirectionEdge }
	if p.SpreadBoost <= 0 { p.SpreadBoost = d.SpreadBoost }
	if p.SLFallbackPct <= 0 { p.SLFallbackPct = d.SLFallbackPct }
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
		atrDist = math.Abs(priceNow) * o.Params.SLFallbackPct
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
	if bigBuy > o.Params.BigOrderThreshold {
		buyScore += bigBuy * 0.5
	}
	if bigSell > o.Params.BigOrderThreshold {
		sellScore += bigSell * 0.5
	}

	// Trade density (market activity)
	if density > o.Params.DensityThreshold {
		densityBonus := math.Min((density-o.Params.DensityThreshold)*0.3, 0.3)
		buyScore += densityBonus
		sellScore += densityBonus
	}

	// Buy/sell ratio
	if buySell > o.Params.BuySellBullish {
		buyScore += (buySell - 1.0) * 0.3
	} else if buySell < o.Params.BuySellBearish && buySell > 0 {
		sellScore += (1.0 - buySell) * 0.3
	}

	scoreTh := o.Params.FlowScoreThreshold
	long := buyScore >= scoreTh && buyScore > sellScore*o.Params.DirectionEdge
	short := sellScore >= scoreTh && sellScore > buyScore*o.Params.DirectionEdge

	if !long && !short {
		return Signal{
			Strategy:   "OrderFlow",
			Direction:  DirectionHold,
			Confidence: 0.15,
			Reason:     fmt.Sprintf("order flow imbalance not strong enough (buy=%.2f sell=%.2f)", buyScore, sellScore),
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := o.Params.BaseConfidence
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
		confidence *= o.Params.SpreadBoost
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
	// OrderFlow uses 1m-level SL/TP multipliers (tick ≈ 1m).
	slMult, tpMult := SLTPMultipliers("1m")
	if direction == DirectionLong {
		signal.StopLoss = priceNow - atrDist*slMult
		signal.TakeProfit = priceNow + atrDist*tpMult
	} else {
		signal.StopLoss = priceNow + atrDist*slMult
		signal.TakeProfit = priceNow - atrDist*tpMult
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
