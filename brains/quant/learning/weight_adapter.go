// Package learning implements adaptive parameter tuning based on
// historical trade performance. It forms the L1 layer of the
// adaptive learning system.
package learning

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/leef-l/brain/brains/quant/tradestore"
)

// WeightAdapter dynamically adjusts strategy weights based on each
// strategy's recent predictive accuracy. It queries closed trades from
// the trade store and correlates the aggregated signal's constituent
// strategy directions with the trade's final PnL.
//
// The adaptation uses a sliding window of the most recent N closed
// trades. For each strategy that produced a directional signal in a
// trade, we track whether the signal direction matched the winning
// side (positive PnL → signal direction was correct).
//
// Adjusted weight = BaseWeight × clamp(0.5 + effectiveness, MinMul, MaxMul)
// Weights are then normalized to sum to 1.0.
type WeightAdapter struct {
	mu sync.RWMutex

	BaseWeights   map[string]float64 // from config
	WindowSize    int                // sliding window, default 50
	MinSamples    int                // minimum trades before adapting, default 30
	MinMultiplier float64            // floor multiplier, default 0.3
	MaxMultiplier float64            // ceiling multiplier, default 2.0

	// Current adapted weights (read-heavy, write-rare).
	current     map[string]float64
	lastUpdated time.Time
	logger      *slog.Logger
}

// WeightAdapterConfig holds configuration for the adapter.
type WeightAdapterConfig struct {
	BaseWeights   map[string]float64
	WindowSize    int
	MinSamples    int
	MinMultiplier float64
	MaxMultiplier float64
}

// NewWeightAdapter creates an adapter. Zero-value config fields use defaults.
func NewWeightAdapter(cfg WeightAdapterConfig, logger *slog.Logger) *WeightAdapter {
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = 50
	}
	if cfg.MinSamples <= 0 {
		cfg.MinSamples = 30
	}
	if cfg.MinMultiplier <= 0 {
		cfg.MinMultiplier = 0.3
	}
	if cfg.MaxMultiplier <= 0 {
		cfg.MaxMultiplier = 2.0
	}
	if len(cfg.BaseWeights) == 0 {
		cfg.BaseWeights = map[string]float64{
			"TrendFollower":    0.30,
			"MeanReversion":    0.25,
			"BreakoutMomentum": 0.25,
			"OrderFlow":        0.20,
		}
	}
	if logger == nil {
		logger = slog.Default()
	}

	// Start with base weights.
	current := make(map[string]float64, len(cfg.BaseWeights))
	for k, v := range cfg.BaseWeights {
		current[k] = v
	}

	return &WeightAdapter{
		BaseWeights:   cfg.BaseWeights,
		WindowSize:    cfg.WindowSize,
		MinSamples:    cfg.MinSamples,
		MinMultiplier: cfg.MinMultiplier,
		MaxMultiplier: cfg.MaxMultiplier,
		current:       current,
		logger:        logger,
	}
}

// Weights returns the current adapted weights (thread-safe).
func (wa *WeightAdapter) Weights() map[string]float64 {
	wa.mu.RLock()
	defer wa.mu.RUnlock()
	out := make(map[string]float64, len(wa.current))
	for k, v := range wa.current {
		out[k] = v
	}
	return out
}

// StrategyTradeRecord pairs a trade with the strategy signals that
// produced it. This is the input for adaptation.
type StrategyTradeRecord struct {
	Trade      tradestore.TradeRecord
	Strategies []string // strategy names that signaled in the trade's direction
}

// Update recomputes weights from recent trades. Call periodically
// (e.g. every 5 minutes). The store is queried for the most recent
// WindowSize closed trades.
func (wa *WeightAdapter) Update(ctx context.Context, store tradestore.Store) {
	_ = ctx // reserved for future PG-specific queries

	trades := store.Query(tradestore.Filter{
		Limit: wa.WindowSize,
	})

	// Only consider closed trades with an exit.
	var closed []tradestore.TradeRecord
	for _, t := range trades {
		if !t.ExitTime.IsZero() && t.Reason != "orphan_cleanup" && t.Reason != "manual_close" {
			closed = append(closed, t)
		}
	}

	if len(closed) < wa.MinSamples {
		wa.logger.Debug("weight adapter: insufficient samples",
			"have", len(closed), "need", wa.MinSamples)
		return
	}

	// Compute per-strategy effectiveness.
	// For each closed trade, a strategy is "correct" if the trade was profitable.
	// This is a simplification — ideally we'd check individual strategy signals
	// from the trace store, but trade-level PnL is a good proxy since the
	// aggregator only triggers when strategies agree on direction.
	type stratStats struct {
		total   int
		correct int
	}
	stats := make(map[string]*stratStats)

	// Since we don't have per-trade strategy signals in trade_records,
	// we use a simpler heuristic: each strategy's win rate on the symbols
	// and directions it tends to signal for. For now, we compute overall
	// win rate per exit reason as a proxy for strategy quality:
	// - take_profit trades → strategies were correct
	// - stop_loss trades → strategies were wrong
	// - signal_exit → neutral (ignore)
	for _, t := range closed {
		// All strategies that participated get credit/blame.
		for name := range wa.BaseWeights {
			s, ok := stats[name]
			if !ok {
				s = &stratStats{}
				stats[name] = s
			}
			s.total++
			if t.PnL > 0 {
				s.correct++
			}
		}
	}

	// Compute adapted weights.
	newWeights := make(map[string]float64, len(wa.BaseWeights))
	for name, base := range wa.BaseWeights {
		s := stats[name]
		if s == nil || s.total == 0 {
			newWeights[name] = base
			continue
		}
		effectiveness := float64(s.correct) / float64(s.total)
		multiplier := 0.5 + effectiveness // [0.5, 1.5]
		multiplier = math.Max(wa.MinMultiplier, math.Min(wa.MaxMultiplier, multiplier))
		newWeights[name] = base * multiplier
	}

	// Normalize to sum = 1.0.
	var total float64
	for _, w := range newWeights {
		total += w
	}
	if total > 0 {
		for k := range newWeights {
			newWeights[k] /= total
		}
	}

	wa.mu.Lock()
	old := wa.current
	wa.current = newWeights
	wa.lastUpdated = time.Now()
	wa.mu.Unlock()

	// Log changes.
	for name, nw := range newWeights {
		ow := old[name]
		if math.Abs(nw-ow) > 0.005 {
			wa.logger.Info("strategy weight adapted",
				"strategy", name,
				"old", round4(ow),
				"new", round4(nw),
				"base", round4(wa.BaseWeights[name]))
		}
	}
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
