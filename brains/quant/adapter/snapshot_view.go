// Package adapter bridges the data brain's output (MarketSnapshot) to the
// quant brain's input (strategy.MarketView).
package adapter

import (
	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/brains/quant/strategy"
)

// SnapshotView implements strategy.MarketView by reading from a
// ringbuf.MarketSnapshot. Live trading uses this adapter; backtesting
// uses strategy.Snapshot directly.
type SnapshotView struct {
	snap      ringbuf.MarketSnapshot
	fv        *strategy.LiveFeatureView
	timeframe string
	candles   map[string][]strategy.Candle // optional, populated from history
	winRate   float64
}

// NewSnapshotView creates a MarketView from a data brain snapshot.
// timeframe is the primary timeframe this view represents (e.g. "1H").
func NewSnapshotView(snap ringbuf.MarketSnapshot, timeframe string) *SnapshotView {
	fv := strategy.NewLiveFeatureView(
		snap.FeatureVector,
		snap.InstID,
		snap.CurrentPrice,
		snap.MLReady,
	)
	return &SnapshotView{
		snap:      snap,
		fv:        fv,
		timeframe: timeframe,
		candles:   make(map[string][]strategy.Candle),
	}
}

// SetCandles attaches candle history for a specific timeframe.
// This is needed by strategies that still require candle data
// (e.g. BreakoutMomentum for high/low extremes).
func (v *SnapshotView) SetCandles(tf string, candles []strategy.Candle) {
	v.candles[tf] = candles
}

// SetSimilarityWinRate sets the historical similarity win rate.
func (v *SnapshotView) SetSimilarityWinRate(rate float64) {
	v.winRate = rate
}

// ── MarketView interface ────────────────────────────────────────

func (v *SnapshotView) Symbol() string    { return v.snap.InstID }
func (v *SnapshotView) Timeframe() string { return v.timeframe }

func (v *SnapshotView) Candles(tf string) []strategy.Candle {
	if c, ok := v.candles[tf]; ok {
		return append([]strategy.Candle(nil), c...)
	}
	return nil
}

func (v *SnapshotView) CurrentPrice() float64       { return v.snap.CurrentPrice }
func (v *SnapshotView) FeatureVector() []float64     { return v.fv.RawVector() }
func (v *SnapshotView) FundingRate() float64         { return v.snap.FundingRate }
func (v *SnapshotView) OrderBookImbalance() float64  { return v.snap.OrderBookImbalance }
func (v *SnapshotView) TradeFlowToxicity() float64   { return v.snap.TradeFlowToxicity }
func (v *SnapshotView) BigBuyRatio() float64         { return v.snap.BigBuyRatio }
func (v *SnapshotView) TradeDensityRatio() float64   { return v.snap.TradeDensityRatio }
func (v *SnapshotView) SimilarityWinRate() float64   { return v.winRate }
func (v *SnapshotView) Feature() strategy.FeatureView { return v.fv }
func (v *SnapshotView) HasFeatureView() bool          { return true }
