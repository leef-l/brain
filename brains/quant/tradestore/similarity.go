package tradestore

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// SimilarityQuery describes the current trade context for finding
// historically similar trades.
type SimilarityQuery struct {
	Symbol     string
	Direction  strategy.Direction
	ATR        float64 // current ATR, for matching similar volatility environments
	Confidence float64 // current signal confidence
	Strategy   string  // dominant strategy name
	TopK       int     // return top-K most similar trades (default 5)
}

// SimilarTrade pairs a historical trade record with its similarity score.
type SimilarTrade struct {
	Record     TradeRecord
	Similarity float64 // 0-1, higher means more similar
}

// SimilaritySearch queries the store for closed trades and ranks them by
// similarity to the given query. Only closed trades (ExitTime non-zero) are
// considered. Results are sorted by similarity descending, capped at TopK.
func SimilaritySearch(store Store, query SimilarityQuery) []SimilarTrade {
	topK := query.TopK
	if topK <= 0 {
		topK = 5
	}

	// Fetch all closed trades (no specific filter — we score everything).
	all := store.Query(Filter{})

	var scored []SimilarTrade
	for _, r := range all {
		// Skip trades that are still open.
		if r.ExitTime.IsZero() {
			continue
		}

		sim := computeSimilarity(query, r)
		scored = append(scored, SimilarTrade{Record: r, Similarity: sim})
	}

	// Sort descending by similarity.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Similarity > scored[j].Similarity
	})

	if len(scored) > topK {
		scored = scored[:topK]
	}
	return scored
}

// computeSimilarity calculates a 0-1 similarity score between a query and
// a historical trade record using weighted feature matching.
func computeSimilarity(q SimilarityQuery, r TradeRecord) float64 {
	var score float64

	// Same symbol: +0.3
	if q.Symbol != "" && q.Symbol == r.Symbol {
		score += 0.3
	}

	// Same direction: +0.2
	if q.Direction != "" && q.Direction == r.Direction {
		score += 0.2
	}

	// Same strategy: +0.2
	if q.Strategy != "" && q.Strategy == r.Strategy {
		score += 0.2
	}

	// ATR similarity: +0.15 × (1 - |qATR - rATR| / max(qATR, rATR, 1e-10))
	maxATR := math.Max(math.Max(q.ATR, r.ATR), 1e-10)
	atrSim := 1.0 - math.Abs(q.ATR-r.ATR)/maxATR
	score += 0.15 * atrSim

	// Confidence similarity: +0.15 × (1 - |qConf - rConf|)
	confSim := 1.0 - math.Abs(q.Confidence-r.Confidence)
	if confSim < 0 {
		confSim = 0
	}
	score += 0.15 * confSim

	return score
}

// FormatSimilarTrades formats a slice of similar trades into a human-readable
// summary suitable for injection into an LLM prompt.
func FormatSimilarTrades(trades []SimilarTrade) string {
	if len(trades) == 0 {
		return "No similar historical trades found."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Found %d similar historical trades:\n", len(trades)))

	for i, st := range trades {
		r := st.Record
		b.WriteString(fmt.Sprintf(
			"%d. %s %s [%s] | Entry: %.4f → Exit: %.4f | PnL: %.4f (%.2f%%) | Reason: %s | MAE: %.4f MFE: %.4f | Similarity: %.2f\n",
			i+1,
			r.Symbol,
			string(r.Direction),
			r.Strategy,
			r.EntryPrice,
			r.ExitPrice,
			r.PnL,
			r.PnLPct*100,
			r.Reason,
			r.MAE,
			r.MFE,
			st.Similarity,
		))
	}

	return b.String()
}
