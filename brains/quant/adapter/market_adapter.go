// Package adapter bridges real-time market data to strategy.MarketView.
package adapter

import (
	"sync"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// Tick represents a single market data update.
type Tick struct {
	Symbol    string
	Price     float64
	Volume    float64
	Timestamp int64 // milliseconds

	// Optional microstructure fields.
	FundingRate        float64
	OrderBookImbalance float64
	TradeFlowToxicity  float64
	BigBuyRatio        float64
	BigSellRatio       float64
	TradeDensityRatio  float64
}

// candleRingBuffer stores the most recent completed candles in a ring.
type candleRingBuffer struct {
	buf   []strategy.Candle
	size  int
	head  int
	count int
}

func newCandleRingBuffer(size int) *candleRingBuffer {
	return &candleRingBuffer{buf: make([]strategy.Candle, size), size: size}
}

func (rb *candleRingBuffer) push(c strategy.Candle) {
	rb.buf[rb.head] = c
	rb.head = (rb.head + 1) % rb.size
	if rb.count < rb.size {
		rb.count++
	}
}

func (rb *candleRingBuffer) candles() []strategy.Candle {
	if rb.count == 0 {
		return nil
	}
	out := make([]strategy.Candle, rb.count)
	start := (rb.head - rb.count + rb.size) % rb.size
	for i := 0; i < rb.count; i++ {
		out[i] = rb.buf[(start+i)%rb.size]
	}
	return out
}

// candleBuilder aggregates ticks into a single candle for a fixed interval.
type candleBuilder struct {
	current  strategy.Candle
	interval int64 // milliseconds
	openTime int64
	hasOpen  bool
}

// update processes a tick and returns a completed candle when the interval
// boundary is crossed.
func (cb *candleBuilder) update(tick Tick) *strategy.Candle {
	ts := tick.Timestamp
	intervalStart := (ts / cb.interval) * cb.interval

	if !cb.hasOpen {
		cb.openTime = intervalStart
		cb.current = strategy.Candle{
			Timestamp: cb.openTime,
			Open:      tick.Price,
			High:      tick.Price,
			Low:       tick.Price,
			Close:     tick.Price,
			Volume:    tick.Volume,
		}
		cb.hasOpen = true
		return nil
	}

	if intervalStart != cb.openTime {
		completed := cb.current
		cb.openTime = intervalStart
		cb.current = strategy.Candle{
			Timestamp: cb.openTime,
			Open:      tick.Price,
			High:      tick.Price,
			Low:       tick.Price,
			Close:     tick.Price,
			Volume:    tick.Volume,
		}
		return &completed
	}

	if tick.Price > cb.current.High {
		cb.current.High = tick.Price
	}
	if tick.Price < cb.current.Low {
		cb.current.Low = tick.Price
	}
	cb.current.Close = tick.Price
	cb.current.Volume += tick.Volume
	return nil
}

var timeframeIntervals = map[string]int64{
	"1m":  60 * 1000,
	"5m":  5 * 60 * 1000,
	"15m": 15 * 60 * 1000,
	"1H":  60 * 60 * 1000,
	"4H":  4 * 60 * 60 * 1000,
}

// MarketAdapter implements strategy.MarketView by aggregating ticks into
// multi-timeframe candles and exposing an optional 192-dim feature vector.
type MarketAdapter struct {
	symbol    string
	timeframe string

	mu sync.RWMutex

	lastPrice float64
	lastTick  Tick

	// Feature vector
	featureVec [192]float64
	hasFeature bool
	fv         *strategy.LiveFeatureView

	// Microstructure fallback values (used when feature vector unavailable).
	fundingRate        float64
	orderBookImbalance float64
	tradeFlowToxicity  float64
	bigBuyRatio        float64
	bigSellRatio       float64
	tradeDensityRatio  float64
	similarityWinRate  float64

	// Candle storage
	buffers  map[string]*candleRingBuffer
	builders map[string]*candleBuilder
}

// NewMarketAdapter creates a MarketAdapter for the given symbol and primary
// timeframe. Buffers are initialised for 1m/5m/15m/1H/4H with capacity 200.
func NewMarketAdapter(symbol, timeframe string) *MarketAdapter {
	ma := &MarketAdapter{
		symbol:    symbol,
		timeframe: timeframe,
		buffers:   make(map[string]*candleRingBuffer),
		builders:  make(map[string]*candleBuilder),
	}
	for tf, interval := range timeframeIntervals {
		ma.buffers[tf] = newCandleRingBuffer(200)
		ma.builders[tf] = &candleBuilder{interval: interval}
	}
	return ma
}

// SetSimilarityWinRate sets the historical similarity win rate.
func (ma *MarketAdapter) SetSimilarityWinRate(rate float64) {
	ma.mu.Lock()
	defer ma.mu.Unlock()
	ma.similarityWinRate = rate
}

// UpdateTick ingests a tick, updates the last price, and aggregates candles.
func (ma *MarketAdapter) UpdateTick(tick Tick) {
	ma.mu.Lock()
	defer ma.mu.Unlock()

	ma.lastTick = tick
	ma.lastPrice = tick.Price

	if tick.FundingRate != 0 {
		ma.fundingRate = tick.FundingRate
	}
	if tick.OrderBookImbalance != 0 {
		ma.orderBookImbalance = tick.OrderBookImbalance
	}
	if tick.TradeFlowToxicity != 0 {
		ma.tradeFlowToxicity = tick.TradeFlowToxicity
	}
	if tick.BigBuyRatio != 0 {
		ma.bigBuyRatio = tick.BigBuyRatio
	}
	if tick.BigSellRatio != 0 {
		ma.bigSellRatio = tick.BigSellRatio
	}
	if tick.TradeDensityRatio != 0 {
		ma.tradeDensityRatio = tick.TradeDensityRatio
	}

	for tf, builder := range ma.builders {
		if completed := builder.update(tick); completed != nil {
			ma.buffers[tf].push(*completed)
		}
	}
}

// UpdateFeatureVector updates the 192-dim feature vector and enables the
// structured FeatureView.
func (ma *MarketAdapter) UpdateFeatureVector(vec [192]float64) {
	ma.mu.Lock()
	defer ma.mu.Unlock()
	ma.featureVec = vec
	ma.hasFeature = true
	ma.fv = strategy.NewLiveFeatureView(vec, ma.symbol, ma.lastPrice, true)
}

// ── MarketView interface ─────────────────────────────────────────

func (ma *MarketAdapter) Symbol() string {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	return ma.symbol
}

func (ma *MarketAdapter) Timeframe() string {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if ma.timeframe != "" {
		return ma.timeframe
	}
	return "1H"
}

func (ma *MarketAdapter) Candles(timeframe string) []strategy.Candle {
	if timeframe == "" {
		timeframe = ma.Timeframe()
	}
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if buf, ok := ma.buffers[timeframe]; ok {
		return buf.candles()
	}
	return nil
}

func (ma *MarketAdapter) CurrentPrice() float64 {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	return ma.lastPrice
}

func (ma *MarketAdapter) FeatureVector() []float64 {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	out := make([]float64, 192)
	copy(out, ma.featureVec[:])
	return out
}

func (ma *MarketAdapter) FundingRate() float64 {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if ma.hasFeature && ma.fv != nil {
		return ma.fv.FundingRate()
	}
	return ma.fundingRate
}

func (ma *MarketAdapter) OrderBookImbalance() float64 {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if ma.hasFeature && ma.fv != nil {
		return ma.fv.OrderBookImbalance()
	}
	return ma.orderBookImbalance
}

func (ma *MarketAdapter) TradeFlowToxicity() float64 {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if ma.hasFeature && ma.fv != nil {
		return ma.fv.TradeFlowToxicity()
	}
	return ma.tradeFlowToxicity
}

func (ma *MarketAdapter) BigBuyRatio() float64 {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if ma.hasFeature && ma.fv != nil {
		return ma.fv.BigBuyRatio()
	}
	return ma.bigBuyRatio
}

func (ma *MarketAdapter) BigSellRatio() float64 {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if ma.hasFeature && ma.fv != nil {
		return ma.fv.BigSellRatio()
	}
	return ma.bigSellRatio
}

func (ma *MarketAdapter) TradeDensityRatio() float64 {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if ma.hasFeature && ma.fv != nil {
		return ma.fv.TradeDensityRatio()
	}
	return ma.tradeDensityRatio
}

func (ma *MarketAdapter) SimilarityWinRate() float64 {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	return ma.similarityWinRate
}

// v2 structured feature access.

func (ma *MarketAdapter) Feature() strategy.FeatureView {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	return ma.fv
}

func (ma *MarketAdapter) HasFeatureView() bool {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	return ma.hasFeature
}
