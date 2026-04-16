package learning

import (
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/leef-l/brain/brains/quant/tradestore"
)

// SymbolScorer scores symbols based on historical trading performance.
// Higher-scoring symbols are prioritized in the auto-discovery ranking
// (alongside 24h amplitude from DataBrain).
//
// Score = WinRate × 0.4 + ProfitFactorNorm × 0.3 + FrequencyNorm × 0.3
//
// Symbols with fewer than MinTrades are given a neutral score of 0.5
// (neither boosted nor penalized) so new symbols get a fair chance.
type SymbolScorer struct {
	mu sync.RWMutex

	WindowSize int // consider trades within this many days, default 7
	MinTrades  int // minimum trades to produce a score, default 5

	scores      map[string]float64
	lastUpdated time.Time
	logger      *slog.Logger
}

// SymbolScorerConfig configures the scorer.
type SymbolScorerConfig struct {
	WindowDays int
	MinTrades  int
}

// NewSymbolScorer creates a new scorer.
func NewSymbolScorer(cfg SymbolScorerConfig, logger *slog.Logger) *SymbolScorer {
	if cfg.WindowDays <= 0 {
		cfg.WindowDays = 7
	}
	if cfg.MinTrades <= 0 {
		cfg.MinTrades = 5
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SymbolScorer{
		WindowSize: cfg.WindowDays,
		MinTrades:  cfg.MinTrades,
		scores:     make(map[string]float64),
		logger:     logger,
	}
}

// Score returns the current score for a symbol. Returns 0.5 (neutral)
// if the symbol has not been scored.
func (ss *SymbolScorer) Score(symbol string) float64 {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	if s, ok := ss.scores[symbol]; ok {
		return s
	}
	return 0.5
}

// AllScores returns a copy of all symbol scores.
func (ss *SymbolScorer) AllScores() map[string]float64 {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	out := make(map[string]float64, len(ss.scores))
	for k, v := range ss.scores {
		out[k] = v
	}
	return out
}

// Update recomputes scores from the trade store.
func (ss *SymbolScorer) Update(store tradestore.Store) {
	since := time.Now().AddDate(0, 0, -ss.WindowSize)
	trades := store.Query(tradestore.Filter{Since: since})

	// Filter to closed trades with real exits.
	var closed []tradestore.TradeRecord
	for _, t := range trades {
		if !t.ExitTime.IsZero() && t.Reason != "orphan_cleanup" && t.Reason != "manual_close" {
			closed = append(closed, t)
		}
	}

	// Group by symbol.
	type symbolStats struct {
		wins       int
		losses     int
		totalPnL   float64
		totalWin   float64
		totalLoss  float64
	}
	bySymbol := make(map[string]*symbolStats)
	for _, t := range closed {
		s, ok := bySymbol[t.Symbol]
		if !ok {
			s = &symbolStats{}
			bySymbol[t.Symbol] = s
		}
		s.totalPnL += t.PnL
		if t.PnL > 0 {
			s.wins++
			s.totalWin += t.PnL
		} else {
			s.losses++
			s.totalLoss += -t.PnL
		}
	}

	// Compute scores.
	// First pass: find max profit factor for normalization.
	type scored struct {
		winRate      float64
		profitFactor float64
		tradeCount   int
	}
	symbolScored := make(map[string]scored)
	maxPF := 1.0
	maxCount := 1

	for sym, s := range bySymbol {
		total := s.wins + s.losses
		if total < ss.MinTrades {
			continue
		}
		wr := float64(s.wins) / float64(total)
		pf := 0.0
		if s.totalLoss > 0 {
			pf = s.totalWin / s.totalLoss
		} else if s.totalWin > 0 {
			pf = 3.0 // cap when no losses
		}
		symbolScored[sym] = scored{winRate: wr, profitFactor: pf, tradeCount: total}
		if pf > maxPF {
			maxPF = pf
		}
		if total > maxCount {
			maxCount = total
		}
	}

	// Second pass: compute final scores.
	newScores := make(map[string]float64, len(symbolScored))
	for sym, s := range symbolScored {
		pfNorm := math.Min(s.profitFactor/maxPF, 1.0)
		freqNorm := math.Min(float64(s.tradeCount)/float64(maxCount), 1.0)
		score := s.winRate*0.4 + pfNorm*0.3 + freqNorm*0.3
		score = math.Max(0, math.Min(1, score))
		newScores[sym] = score
	}

	ss.mu.Lock()
	old := ss.scores
	ss.scores = newScores
	ss.lastUpdated = time.Now()
	ss.mu.Unlock()

	// Log significant changes.
	for sym, ns := range newScores {
		os := old[sym]
		if math.Abs(ns-os) > 0.05 {
			ss.logger.Info("symbol score updated",
				"symbol", sym,
				"old", round4(os),
				"new", round4(ns))
		}
	}
	if len(newScores) > 0 {
		ss.logger.Debug("symbol scores refreshed", "scored_symbols", len(newScores))
	}
}
