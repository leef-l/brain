package learning

import (
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/leef-l/brain/brains/quant/tradestore"
)

// SLTPRecommendation holds the recommended ATR multipliers for stop-loss
// and take-profit, derived from historical MAE/MFE distribution analysis.
type SLTPRecommendation struct {
	StopLossATR   float64 // recommended SL ATR multiplier
	TakeProfitATR float64 // recommended TP ATR multiplier
	UseTrailing   bool    // recommend trailing stop if MFE >> TP
	Confidence    float64 // [0,1] higher = more samples, more reliable
}

// SLTPOptimizerConfig configures the optimizer.
type SLTPOptimizerConfig struct {
	MinSamples  int // minimum closed trades with MAE/MFE data, default 30
	WindowDays  int // look back this many days, default 30
	ATRLookback int // ATR period for normalizing, default 14
}

// SLTPOptimizer analyzes historical MAE/MFE distributions to recommend
// optimal stop-loss and take-profit ATR multipliers.
//
// Core insight:
//   - If many trades have MAE < current SL distance → SL is too tight (gets swept then reverses)
//   - If many trades have MFE >> current TP distance → TP is too tight (leaving money on table)
//
// The optimizer recommends ATR multipliers at specific percentiles of the
// MAE/MFE distributions:
//   - SL = P75 of MAE distribution (covers 75% of adverse excursions)
//   - TP = P50 of MFE distribution (captures median favorable excursion)
type SLTPOptimizer struct {
	mu     sync.RWMutex
	config SLTPOptimizerConfig

	// Per-symbol recommendations (updated periodically).
	recommendations map[string]SLTPRecommendation

	// Global recommendation (across all symbols).
	global SLTPRecommendation

	lastUpdated time.Time
	logger      *slog.Logger
}

// NewSLTPOptimizer creates an optimizer with the given config.
func NewSLTPOptimizer(cfg SLTPOptimizerConfig, logger *slog.Logger) *SLTPOptimizer {
	if cfg.MinSamples <= 0 {
		cfg.MinSamples = 30
	}
	if cfg.WindowDays <= 0 {
		cfg.WindowDays = 30
	}
	if cfg.ATRLookback <= 0 {
		cfg.ATRLookback = 14
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SLTPOptimizer{
		config:          cfg,
		recommendations: make(map[string]SLTPRecommendation),
		logger:          logger,
	}
}

// Global returns the global (cross-symbol) recommendation.
func (o *SLTPOptimizer) Global() SLTPRecommendation {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.global
}

// ForSymbol returns the recommendation for a specific symbol.
// Falls back to Global() if the symbol has insufficient data.
func (o *SLTPOptimizer) ForSymbol(symbol string) SLTPRecommendation {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if r, ok := o.recommendations[symbol]; ok {
		return r
	}
	return o.global
}

// AllRecommendations returns a copy of per-symbol recommendations.
func (o *SLTPOptimizer) AllRecommendations() map[string]SLTPRecommendation {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make(map[string]SLTPRecommendation, len(o.recommendations))
	for k, v := range o.recommendations {
		out[k] = v
	}
	return out
}

// tradeExcursion holds normalized MAE/MFE for one trade.
type tradeExcursion struct {
	mae    float64 // as ratio of entry price
	mfe    float64
	symbol string
}

// Update recomputes recommendations from trade history.
func (o *SLTPOptimizer) Update(store tradestore.Store) {
	since := time.Now().AddDate(0, 0, -o.config.WindowDays)
	trades := store.Query(tradestore.Filter{Since: since})

	var all []tradeExcursion
	bySymbol := make(map[string][]tradeExcursion)

	for _, t := range trades {
		if t.ExitTime.IsZero() || t.EntryPrice <= 0 {
			continue
		}
		if t.MAE <= 0 && t.MFE <= 0 {
			continue // no MAE/MFE data (old trade)
		}
		if t.Reason == "orphan_cleanup" || t.Reason == "manual_close" {
			continue
		}
		m := tradeExcursion{
			mae:    t.MAE / t.EntryPrice, // normalize to percentage of entry
			mfe:    t.MFE / t.EntryPrice,
			symbol: t.Symbol,
		}
		all = append(all, m)
		bySymbol[t.Symbol] = append(bySymbol[t.Symbol], m)
	}

	// Compute global recommendation.
	globalRec := o.computeRecommendation(all)

	// Compute per-symbol recommendations.
	newRecs := make(map[string]SLTPRecommendation)
	for sym, data := range bySymbol {
		if len(data) >= o.config.MinSamples/2 { // lower bar for per-symbol (half of global)
			rec := o.computeRecommendation(data)
			if rec.Confidence > 0.3 { // only store if reasonably confident
				newRecs[sym] = rec
			}
		}
	}

	o.mu.Lock()
	oldGlobal := o.global
	o.global = globalRec
	o.recommendations = newRecs
	o.lastUpdated = time.Now()
	o.mu.Unlock()

	// Log changes.
	if math.Abs(globalRec.StopLossATR-oldGlobal.StopLossATR) > 0.1 ||
		math.Abs(globalRec.TakeProfitATR-oldGlobal.TakeProfitATR) > 0.1 {
		o.logger.Info("SL/TP recommendation updated",
			"sl_atr", round4(globalRec.StopLossATR),
			"tp_atr", round4(globalRec.TakeProfitATR),
			"trailing", globalRec.UseTrailing,
			"confidence", round4(globalRec.Confidence),
			"samples", len(all),
			"symbols", len(newRecs))
	}
}

// computeRecommendation derives SL/TP ATR multipliers from MAE/MFE distributions.
func (o *SLTPOptimizer) computeRecommendation(data []tradeExcursion) SLTPRecommendation {
	if len(data) < o.config.MinSamples {
		return SLTPRecommendation{
			StopLossATR:   0, // 0 means "use strategy default"
			TakeProfitATR: 0,
			Confidence:    0,
		}
	}

	// Extract MAE and MFE arrays.
	maes := make([]float64, len(data))
	mfes := make([]float64, len(data))
	for i, d := range data {
		maes[i] = d.mae
		mfes[i] = d.mfe
	}
	sort.Float64s(maes)
	sort.Float64s(mfes)

	// SL recommendation: P75 of MAE distribution.
	// This means the SL would cover 75% of adverse excursions (25% would still be hit).
	// We convert from price-ratio back to approximate ATR multiplier using the
	// empirical relationship: 1 ATR ≈ 1-2% of price for crypto.
	// A more precise conversion would use actual ATR values, but this ratio-based
	// approach works well for relative comparison.
	maeP75 := percentile(maes, 0.75)
	mfeP50 := percentile(mfes, 0.50)
	mfeP75 := percentile(mfes, 0.75)

	// Convert percentage to approximate ATR multipliers.
	// Typical crypto ATR(14) on 1m ≈ 0.3-0.8% of price.
	// We use 0.5% as the baseline ATR ratio.
	const atrRatio = 0.005
	slATR := maeP75 / atrRatio
	tpATR := mfeP50 / atrRatio

	// Clamp to reasonable ranges.
	slATR = math.Max(1.0, math.Min(slATR, 5.0))
	tpATR = math.Max(1.5, math.Min(tpATR, 8.0))

	// Ensure TP > SL (minimum 1.2x risk/reward).
	if tpATR < slATR*1.2 {
		tpATR = slATR * 1.2
	}

	// Trailing stop recommendation: if P75 MFE is much larger than P50 MFE,
	// the distribution has a long right tail → trailing stop captures more.
	useTrailing := mfeP75 > mfeP50*1.8 && mfeP50 > 0

	// Confidence based on sample size (asymptotic to 1.0).
	confidence := 1.0 - 1.0/math.Sqrt(float64(len(data))/float64(o.config.MinSamples))
	confidence = math.Max(0, math.Min(1, confidence))

	return SLTPRecommendation{
		StopLossATR:   math.Round(slATR*100) / 100,
		TakeProfitATR: math.Round(tpATR*100) / 100,
		UseTrailing:   useTrailing,
		Confidence:    confidence,
	}
}

// percentile returns the p-th percentile of a sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := p * float64(len(sorted)-1)
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper || upper >= len(sorted) {
		return sorted[lower]
	}
	frac := idx - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}
