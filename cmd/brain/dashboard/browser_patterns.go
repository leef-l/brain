package dashboard

import (
	"encoding/json"
	"net/http"

	"github.com/leef-l/brain/sdk/tool"
)

// BrowserPatternStatsResponse is the payload for
// GET /v1/dashboard/browser/pattern-stats (B-5).
type BrowserPatternStatsResponse struct {
	TotalMatches   int                  `json:"total_matches"`
	TotalSuccesses int                  `json:"total_successes"`
	TotalFailures  int                  `json:"total_failures"`
	Patterns       []BrowserPatternStat `json:"patterns"`
}

// BrowserPatternStat is a single pattern's statistics.
type BrowserPatternStat struct {
	Name      string  `json:"name"`
	Matches   int     `json:"matches"`
	Successes int     `json:"successes"`
	Failures  int     `json:"failures"`
	HitRate   float64 `json:"hit_rate"`
}

func handleBrowserPatternStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	lib := tool.SharedPatternLibrary()
	if lib == nil {
		http.Error(w, `{"error":"pattern library not available"}`, http.StatusServiceUnavailable)
		return
	}

	// B-5: read from the shared pattern library (the persistent source of truth
	// for pattern stats). Note: sdk/tool/pattern_exec_registry.go was a shim
	// (K-9); real stats live in PatternLibrary.
	patterns := lib.ListAll("")
	resp := BrowserPatternStatsResponse{
		Patterns: make([]BrowserPatternStat, 0, len(patterns)),
	}

	for _, p := range patterns {
		matches := p.Stats.MatchCount
		successes := p.Stats.SuccessCount
		failures := p.Stats.FailureCount
		hitRate := 0.0
		if matches > 0 {
			hitRate = float64(successes) / float64(matches)
		}

		resp.TotalMatches += matches
		resp.TotalSuccesses += successes
		resp.TotalFailures += failures
		resp.Patterns = append(resp.Patterns, BrowserPatternStat{
			Name:      p.ID,
			Matches:   matches,
			Successes: successes,
			Failures:  failures,
			HitRate:   hitRate,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
