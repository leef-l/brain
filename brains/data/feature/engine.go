package feature

import (
	"math"
	"sync"

	"github.com/leef-l/brain/brains/data/processor"
	"github.com/leef-l/brain/brains/data/provider"
)

// Engine computes the rule-based feature dimensions [0:176] of the
// 192-dimensional feature vector. The remaining [176:192] are produced
// by MLEngine (or RuleFallback) and merged by FeatureAssembler.
type Engine struct {
	candles   *processor.CandleAggregator
	orderbook *processor.OrderBookTracker
	tradeflow *processor.TradeFlowTracker
	mu        sync.RWMutex
}

// NewEngine creates a feature engine from the three data trackers.
func NewEngine(
	candles *processor.CandleAggregator,
	orderbook *processor.OrderBookTracker,
	tradeflow *processor.TradeFlowTracker,
) *Engine {
	return &Engine{
		candles:   candles,
		orderbook: orderbook,
		tradeflow: tradeflow,
	}
}

// timeframes used for multi-timeframe feature extraction.
var timeframes = []string{"1m", "5m", "15m", "1H", "4H"}

// Compute calculates the rule-based feature dimensions [0:176].
// The full 192-dim vector is assembled by FeatureAssembler which
// appends ML/fallback dimensions [176:192].
//
// For backward compatibility, this still returns a 192-dim slice
// (with [176:192] zeroed). Use FeatureAssembler for the complete vector.
func (e *Engine) Compute(instID string) []float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()

	vec := make([]float64, VectorDim)

	e.computePrice(vec, instID)
	e.computeVolume(vec, instID)
	e.computeMicrostructure(vec, instID)
	e.computeMomentum(vec, instID)
	e.computeCrossAsset(vec, instID)

	return vec
}

// ComputeArray returns a fixed-size array (for Ring Buffer usage).
func (e *Engine) ComputeArray(instID string) [VectorDim]float64 {
	return ToArray(e.Compute(instID))
}

// ── Price Features [0:60] — 5 timeframes × 12 dims ──────────────

func (e *Engine) computePrice(vec []float64, instID string) {
	for i, tf := range timeframes {
		w := e.candles.GetWindow(instID, tf)
		if w == nil || w.Current.Close == 0 {
			continue
		}
		base := i * 12
		price := w.Current.Close

		// EMA deviations
		if w.EMA9.Ready() {
			vec[base+0] = (price - w.EMA9.Value()) / price
		}
		if w.EMA21.Ready() {
			vec[base+1] = (price - w.EMA21.Value()) / price
		}
		if w.EMA55.Ready() {
			vec[base+2] = (price - w.EMA55.Value()) / price
		}
		// EMA cross
		if w.EMA9.Ready() && w.EMA21.Ready() {
			vec[base+3] = (w.EMA9.Value() - w.EMA21.Value()) / price
		}
		// RSI normalized
		if w.RSI14.Ready() {
			vec[base+4] = w.RSI14.Value() / 100.0
		}
		// MACD histogram
		if w.MACD.Ready() {
			vec[base+5] = w.MACD.Histogram() / price
		}
		// BB position
		if w.BB20.Ready() {
			vec[base+6] = w.BBPosition()
		}
		// ATR ratio
		if w.ATR14.Ready() {
			vec[base+7] = w.ATR14.Value() / price
		}
		// Price change rates
		vec[base+8] = w.PriceChangeRate(5)
		vec[base+9] = w.PriceChangeRate(20)
		// Volatility
		vec[base+10] = w.Volatility(20)
		// ADX
		if w.ADX14.Ready() {
			vec[base+11] = w.ADX14.Value() / 100.0
		}
	}
}

// ── Volume Features [60:100] — 5 timeframes × 8 dims ────────────

func (e *Engine) computeVolume(vec []float64, instID string) {
	for i, tf := range timeframes {
		w := e.candles.GetWindow(instID, tf)
		if w == nil || len(w.HistoryCandles) == 0 {
			continue
		}
		base := 60 + i*8
		candles := w.HistoryCandles
		curVol := w.Current.Volume

		// +0: Volume ratio = current volume / SMA(volume, 20)
		avgVol := volumeSMA(candles, 20)
		if avgVol > 0 && curVol > 0 {
			vec[base+0] = math.Min(curVol/avgVol, 10.0) // clip at 10
		}

		// +1: OBV slope (5-bar linear regression slope / price)
		if len(candles) >= 5 {
			slope := obvSlope(candles, 5)
			if w.Current.Close > 0 {
				vec[base+1] = clipFloat(slope/w.Current.Close, -0.1, 0.1)
			}
		}

		// +2: Volume-price correlation (20-bar)
		if len(candles) >= 20 {
			vec[base+2] = volumePriceCorrelation(candles, 20)
		}

		// +3: Volume breakout indicator
		// vol > 2×avg AND |price_change| > ATR → 1, else 0
		if avgVol > 0 && curVol > 2*avgVol && w.ATR14.Ready() {
			priceChange := math.Abs(w.PriceChangeRate(1)) * w.Current.Close
			if priceChange > w.ATR14.Value() {
				vec[base+3] = 1.0
			}
		}

		// +4: VWAP deviation (simplified: use recent candles)
		if len(candles) >= 20 && w.Current.Close > 0 {
			vwap := computeVWAP(candles, 20)
			if vwap > 0 {
				vec[base+4] = clipFloat((w.Current.Close-vwap)/w.Current.Close, -0.05, 0.05)
			}
		}

		// +5: Buy volume ratio (from tradeflow, TF-agnostic)
		if i == 0 { // only compute once for 1m (tradeflow is tick-level)
			if flow := e.tradeflow.Get(instID); flow != nil {
				vec[base+5] = flow.BuySellRatio()
			}
		}

		// +6: Big order volume ratio (from tradeflow)
		if i == 0 {
			if flow := e.tradeflow.Get(instID); flow != nil {
				vec[base+6] = flow.BigBuyRatio() + flow.BigSellRatio()
			}
		}

		// +7: reserved
	}
}

// ── Microstructure Features [100:130] ────────────────────────────

func (e *Engine) computeMicrostructure(vec []float64, instID string) {
	if ob := e.orderbook.Get(instID); ob != nil {
		vec[100] = ob.Imbalance
		vec[101] = ob.Spread

		// MidPrice deviation from EMA (not raw MidPrice)
		// Use 1m EMA21 as reference price
		if w := e.candles.GetWindow(instID, "1m"); w != nil && w.EMA21.Ready() && w.EMA21.Value() > 0 {
			vec[102] = (ob.MidPrice - w.EMA21.Value()) / w.EMA21.Value()
		}

		// Bid depth concentration: top1_bid / total_bid
		totalBid := ob.BidDepth
		if totalBid > 0 {
			vec[103] = ob.Bids[0].Size / totalBid
		}

		// Ask depth concentration: top1_ask / total_ask
		totalAsk := ob.AskDepth
		if totalAsk > 0 {
			vec[104] = ob.Asks[0].Size / totalAsk
		}

		// Depth ratio: bid / (bid + ask)
		total := totalBid + totalAsk
		if total > 0 {
			vec[105] = totalBid / total
		}

		// Buy-sell pressure: (bidDepth × imbalance) normalized
		if total > 0 {
			vec[108] = ob.BidDepth * ob.Imbalance / total
		}
	}

	if flow := e.tradeflow.Get(instID); flow != nil {
		vec[110] = flow.Toxicity()
		vec[111] = flow.BigBuyRatio()
		vec[112] = flow.BigSellRatio()
		vec[113] = math.Min(flow.TradeDensityRatio(), 10.0) // clip
		vec[114] = flow.BuySellRatio()

		// Net big flow: (bigBuy - bigSell) / total
		bigNet := flow.BigBuyRatio() - flow.BigSellRatio()
		vec[115] = clipFloat(bigNet, -1, 1)
	}

	// Funding rate features [120:123]
	// Note: funding rate is stored on MarketSnapshot directly;
	// we leave [120:123] as 0 here — FeatureAssembler or brain.go
	// can fill them from the provider if available.
}

// ── Momentum Features [130:160] — 5 timeframes × 6 dims ─────────

func (e *Engine) computeMomentum(vec []float64, instID string) {
	for i, tf := range timeframes {
		w := e.candles.GetWindow(instID, tf)
		if w == nil {
			continue
		}
		base := 130 + i*6
		vec[base+0] = w.PriceChangeRate(1)
		vec[base+1] = w.PriceChangeRate(3)
		vec[base+2] = w.PriceChangeRate(10)
		vec[base+3] = w.Volatility(5)
		vec[base+4] = w.Volatility(20)

		// +5: Volatility ratio (vol5/vol20 - trend acceleration)
		vol20 := w.Volatility(20)
		if vol20 > 0 {
			vec[base+5] = w.Volatility(5)/vol20 - 1
		}
	}
}

// ── Cross-asset Features [160:176] ───────────────────────────────

func (e *Engine) computeCrossAsset(vec []float64, instID string) {
	selfWindow := e.candles.GetWindow(instID, "1m")

	// BTC as benchmark
	if instID != "BTC-USDT-SWAP" {
		btcWindow := e.candles.GetWindow("BTC-USDT-SWAP", "1m")
		if btcWindow != nil && selfWindow != nil {
			btcChange5 := btcWindow.PriceChangeRate(5)
			selfChange5 := selfWindow.PriceChangeRate(5)
			vec[160] = selfChange5 - btcChange5 // excess return vs BTC
			vec[161] = btcChange5               // BTC 5-bar momentum
			vec[162] = btcWindow.PriceChangeRate(20)
			vec[163] = btcWindow.Volatility(20)
		}
	}

	// ETH
	if instID != "ETH-USDT-SWAP" {
		ethWindow := e.candles.GetWindow("ETH-USDT-SWAP", "1m")
		if ethWindow != nil && selfWindow != nil {
			ethChange5 := ethWindow.PriceChangeRate(5)
			selfChange5 := selfWindow.PriceChangeRate(5)
			vec[164] = selfChange5 - ethChange5 // excess return vs ETH
			vec[165] = ethChange5               // ETH 5-bar momentum
		}
	}

	// BTC-ETH spread change
	btcW := e.candles.GetWindow("BTC-USDT-SWAP", "1m")
	ethW := e.candles.GetWindow("ETH-USDT-SWAP", "1m")
	if btcW != nil && ethW != nil {
		vec[166] = btcW.PriceChangeRate(5) - ethW.PriceChangeRate(5)
	}

	// Correlation with BTC (using rolling 60-bar price changes)
	if instID != "BTC-USDT-SWAP" && btcW != nil && selfWindow != nil {
		vec[167] = rollingCorrelation(selfWindow, btcW, 60)
	}

	// Correlation with ETH
	if instID != "ETH-USDT-SWAP" && ethW != nil && selfWindow != nil {
		vec[168] = rollingCorrelation(selfWindow, ethW, 60)
	}
}

// ── Helper functions ─────────────────────────────────────────────

// volumeSMA computes the simple moving average of volume over the
// last n candles.
func volumeSMA(candles []provider.Candle, n int) float64 {
	if len(candles) == 0 || n <= 0 {
		return 0
	}
	start := len(candles) - n
	if start < 0 {
		start = 0
	}
	sum := 0.0
	count := 0
	for i := start; i < len(candles); i++ {
		sum += candles[i].Volume
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// obvSlope computes the OBV slope over the last n candles using
// simple linear regression.
func obvSlope(candles []provider.Candle, n int) float64 {
	if len(candles) < n || n < 2 {
		return 0
	}
	start := len(candles) - n

	// Build OBV series
	obv := make([]float64, n)
	obv[0] = 0
	for i := 1; i < n; i++ {
		idx := start + i
		if candles[idx].Close > candles[idx-1].Close {
			obv[i] = obv[i-1] + candles[idx].Volume
		} else if candles[idx].Close < candles[idx-1].Close {
			obv[i] = obv[i-1] - candles[idx].Volume
		} else {
			obv[i] = obv[i-1]
		}
	}

	// Linear regression slope
	return linearRegressionSlope(obv)
}

// linearRegressionSlope computes the slope of a simple linear regression.
func linearRegressionSlope(y []float64) float64 {
	n := float64(len(y))
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumX2 float64
	for i, v := range y {
		x := float64(i)
		sumX += x
		sumY += v
		sumXY += x * v
		sumX2 += x * x
	}
	denom := n*sumX2 - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denom
}

// volumePriceCorrelation computes Pearson correlation between
// price changes and volume changes over the last n candles.
func volumePriceCorrelation(candles []provider.Candle, n int) float64 {
	if len(candles) < n+1 || n < 2 {
		return 0
	}
	start := len(candles) - n

	priceChanges := make([]float64, n)
	volChanges := make([]float64, n)
	for i := 0; i < n; i++ {
		idx := start + i
		if idx > 0 && candles[idx-1].Close > 0 {
			priceChanges[i] = (candles[idx].Close - candles[idx-1].Close) / candles[idx-1].Close
		}
		if idx > 0 && candles[idx-1].Volume > 0 {
			volChanges[i] = (candles[idx].Volume - candles[idx-1].Volume) / candles[idx-1].Volume
		}
	}

	return pearsonCorrelation(priceChanges, volChanges)
}

// computeVWAP computes volume-weighted average price over last n candles.
func computeVWAP(candles []provider.Candle, n int) float64 {
	if len(candles) == 0 || n <= 0 {
		return 0
	}
	start := len(candles) - n
	if start < 0 {
		start = 0
	}
	var sumPV, sumV float64
	for i := start; i < len(candles); i++ {
		typical := (candles[i].High + candles[i].Low + candles[i].Close) / 3
		sumPV += typical * candles[i].Volume
		sumV += candles[i].Volume
	}
	if sumV == 0 {
		return 0
	}
	return sumPV / sumV
}

// rollingCorrelation computes Pearson correlation of price change rates
// between two CandleWindows over the last n bars.
func rollingCorrelation(a, b *processor.CandleWindow, n int) float64 {
	aLen := a.History.Len()
	bLen := b.History.Len()
	if aLen < n+1 || bLen < n+1 || n < 2 {
		return 0
	}

	aChanges := make([]float64, n)
	bChanges := make([]float64, n)
	for i := 0; i < n; i++ {
		aIdx := aLen - n + i
		bIdx := bLen - n + i
		aPrev := a.History.Get(aIdx - 1)
		bPrev := b.History.Get(bIdx - 1)
		if aPrev > 0 {
			aChanges[i] = (a.History.Get(aIdx) - aPrev) / aPrev
		}
		if bPrev > 0 {
			bChanges[i] = (b.History.Get(bIdx) - bPrev) / bPrev
		}
	}

	return pearsonCorrelation(aChanges, bChanges)
}

// pearsonCorrelation computes Pearson correlation between two slices.
func pearsonCorrelation(x, y []float64) float64 {
	n := len(x)
	if n != len(y) || n < 2 {
		return 0
	}

	var sumX, sumY float64
	for i := range x {
		sumX += x[i]
		sumY += y[i]
	}
	meanX := sumX / float64(n)
	meanY := sumY / float64(n)

	var cov, varX, varY float64
	for i := range x {
		dx := x[i] - meanX
		dy := y[i] - meanY
		cov += dx * dy
		varX += dx * dx
		varY += dy * dy
	}

	denom := math.Sqrt(varX) * math.Sqrt(varY)
	if denom == 0 {
		return 0
	}
	return cov / denom
}

// clipFloat clips v to [lo, hi].
func clipFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
