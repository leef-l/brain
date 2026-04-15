package tradestore

import (
	"github.com/leef-l/brain/brains/quant/strategy"
)

// Oracle implements strategy.HistoricalOracle by querying the trade store.
// It provides historical win rates for the Aggregator's veto logic
// and statistics for BayesianSizer's sample-based Kelly.
type Oracle struct {
	store Store
}

// NewOracle creates an Oracle backed by the given Store.
func NewOracle(store Store) *Oracle {
	return &Oracle{store: store}
}

// HistoricalWinRate returns the win rate for a symbol+direction combo.
// The featureVector parameter is accepted for interface compatibility
// but not currently used (future: cosine similarity matching).
func (o *Oracle) HistoricalWinRate(symbol string, direction strategy.Direction, featureVector []float64) (float64, bool) {
	stats := o.store.Stats(Filter{
		Symbol:    symbol,
		Direction: direction,
	})

	if stats.TotalTrades < 5 {
		return 0, false // not enough data
	}

	return stats.WinRate, true
}

// StatsForSizer returns the stats needed by BayesianSizer:
// win rate, avg win, avg loss, sample count.
func (o *Oracle) StatsForSizer(symbol string, direction strategy.Direction) (winRate, avgWin, avgLoss float64, samples int) {
	stats := o.store.Stats(Filter{
		Symbol:    symbol,
		Direction: direction,
	})

	return stats.WinRate, stats.AvgWin, stats.AvgLoss, stats.TotalTrades
}
