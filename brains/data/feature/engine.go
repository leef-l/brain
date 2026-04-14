package feature

import (
	"sync"

	"github.com/leef-l/brain/brains/data/processor"
)

// Engine computes 192-dimensional feature vectors from market data.
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

// Compute calculates the 192-dimensional feature vector for the given instrument.
func (e *Engine) Compute(instID string) []float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()

	vec := make([]float64, VectorDim)

	// === Price features [0:60] -- 5 timeframes x 12 dims ===
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

	// === Volume features [60:100] ===
	// Reserved slots with simple placeholders
	for i, tf := range timeframes {
		w := e.candles.GetWindow(instID, tf)
		if w == nil || w.Current.Volume == 0 {
			continue
		}
		vec[60+i*2] = 0   // reserved: volume ratio
		vec[60+i*2+1] = 0 // reserved: OBV slope
	}

	// === Microstructure [100:130] ===
	if ob := e.orderbook.Get(instID); ob != nil {
		vec[100] = ob.Imbalance
		vec[101] = ob.Spread
		// MidPrice may need normalization; store raw for now
		vec[102] = ob.MidPrice
	}
	if tf := e.tradeflow.Get(instID); tf != nil {
		vec[110] = tf.Toxicity()
		vec[111] = tf.BigBuyRatio()
		vec[112] = tf.BigSellRatio()
		vec[113] = tf.TradeDensityRatio()
		vec[114] = tf.BuySellRatio()
	}
	// [120-129] funding rate related, reserved

	// === Time-series features [130:160] ===
	// Multi-scale momentum
	for i, tf := range timeframes {
		w := e.candles.GetWindow(instID, tf)
		if w == nil {
			continue
		}
		base := 130 + i*6
		vec[base+0] = w.PriceChangeRate(1)  // 1 bar
		vec[base+1] = w.PriceChangeRate(3)  // 3 bars
		vec[base+2] = w.PriceChangeRate(10) // 10 bars
		vec[base+3] = w.Volatility(5)       // short-term vol
		vec[base+4] = w.Volatility(20)      // mid-term vol
		// [base+5] reserved
	}

	// === Cross-asset features [160:180] ===
	// BTC as benchmark
	if instID != "BTC-USDT-SWAP" {
		btcWindow := e.candles.GetWindow("BTC-USDT-SWAP", "1m")
		selfWindow := e.candles.GetWindow(instID, "1m")
		if btcWindow != nil && selfWindow != nil {
			btcChange := btcWindow.PriceChangeRate(5)
			selfChange := selfWindow.PriceChangeRate(5)
			vec[160] = selfChange - btcChange // excess return vs BTC
			vec[161] = btcChange              // BTC momentum
		}
	}
	// ETH correlation
	ethWindow := e.candles.GetWindow("ETH-USDT-SWAP", "1m")
	if ethWindow != nil {
		vec[165] = ethWindow.PriceChangeRate(5)
	}
	// [166-179] reserved

	// === Reserved extension [180:192] ===
	// Currently zero; future: macro / sentiment / on-chain

	return vec
}

// ComputeArray returns a fixed-size array (for Ring Buffer usage).
func (e *Engine) ComputeArray(instID string) [VectorDim]float64 {
	return ToArray(e.Compute(instID))
}
