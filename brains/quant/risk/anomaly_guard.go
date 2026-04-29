package risk

import (
	"sync"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// AnomalyGuard detects abnormal market conditions and blocks or flags
// suspicious orders based on the 192-dim feature vector anomaly scores
// ([184:187]).
type AnomalyGuard struct {
	symbolPausedUntil map[string]time.Time
	mu                sync.Mutex
}

// DefaultAnomalyGuard returns an AnomalyGuard with sensible defaults.
func DefaultAnomalyGuard() *AnomalyGuard {
	return &AnomalyGuard{
		symbolPausedUntil: make(map[string]time.Time),
	}
}

// Check evaluates an anomaly score against an intended action and returns a
// Decision. The most restrictive rule wins when multiple thresholds are
// crossed.
func (ag *AnomalyGuard) Check(score strategy.AnomalyScore, action Action) Decision {
	// OrderBook anomaly is the most restrictive (complete symbol pause).
	if score.OrderBook > 0.9 {
		return pause("anomaly", "orderbook anomaly above 0.9", 5*time.Minute)
	}

	// Price anomaly blocks new positions only.
	if score.Price > 0.8 && (action == ActionOpen || action == ActionReverse) {
		return deny("anomaly", "price anomaly above 0.8: block new positions", false)
	}

	// Volume anomaly warns and recommends reducing position for new trades.
	if score.Volume > 0.85 && (action == ActionOpen || action == ActionReverse) {
		return Decision{
			Allowed: true,
			Layer:   "anomaly",
			Reason:  "volume anomaly above 0.85: reduce position to 50%",
			Action:  "reduce",
		}
	}

	// Combined anomaly flags the trade for manual confirmation.
	if score.Combined > 0.75 {
		return Decision{
			Allowed: true,
			Layer:   "anomaly",
			Reason:  "combined anomaly above 0.75: manual confirmation required",
			Action:  "confirm",
		}
	}

	return allow("anomaly")
}

// IsAnomalous returns true if any anomaly threshold is breached, together with
// a descriptive reason. Rules are checked from most to least severe.
func (ag *AnomalyGuard) IsAnomalous(score strategy.AnomalyScore) (bool, string) {
	switch {
	case score.OrderBook > 0.9:
		return true, "orderbook anomaly above 0.9"
	case score.Price > 0.8:
		return true, "price anomaly above 0.8"
	case score.Volume > 0.85:
		return true, "volume anomaly above 0.85"
	case score.Combined > 0.75:
		return true, "combined anomaly above 0.75"
	default:
		return false, ""
	}
}

// IsSymbolPaused reports whether a symbol is currently under an anomaly pause.
func (ag *AnomalyGuard) IsSymbolPaused(symbol string) bool {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if until, ok := ag.symbolPausedUntil[symbol]; ok {
		if time.Now().Before(until) {
			return true
		}
		delete(ag.symbolPausedUntil, symbol)
	}
	return false
}

// SetSymbolPause records a pause for the given symbol and duration.
func (ag *AnomalyGuard) SetSymbolPause(symbol string, d time.Duration) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	ag.symbolPausedUntil[symbol] = time.Now().Add(d)
}
