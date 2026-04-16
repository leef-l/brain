package quant

import (
	"sync"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// PositionHealthConfig configures the EWMA-based position health tracker.
type PositionHealthConfig struct {
	// Alpha is the EWMA smoothing factor. Higher = more weight on recent signals.
	// Range: (0, 1]. Default: 0.3 (roughly a 3-period half-life).
	Alpha float64 `json:"alpha" yaml:"alpha"`

	// ExitThreshold: when health drops below this value, trigger exit.
	// Range: (0, 1). Default: 0.3.
	ExitThreshold float64 `json:"exit_threshold" yaml:"exit_threshold"`

	// InitialHealth is the starting health value when a position is opened.
	// Default: 0.8.
	InitialHealth float64 `json:"initial_health" yaml:"initial_health"`

	// RegimeBoost increases health in trend regime (position aligns with trend).
	// Applied as multiplier: health *= (1 + RegimeBoost). Default: 0.15.
	// Range: [0, 0.5].
	RegimeBoost float64 `json:"regime_boost" yaml:"regime_boost"`

	// RegimePenalty decreases health in counter-trend regime.
	// Applied as multiplier: health *= (1 - RegimePenalty). Default: 0.10.
	// Range: [0, 0.5].
	RegimePenalty float64 `json:"regime_penalty" yaml:"regime_penalty"`

	// VolHighDamping dampens exit sensitivity in high-volatility conditions.
	// When vol_percentile > 0.7, health decays slower. Default: 0.8.
	// Range: (0, 1].
	VolHighDamping float64 `json:"vol_high_damping" yaml:"vol_high_damping"`
}

// DefaultPositionHealthConfig returns sensible defaults.
func DefaultPositionHealthConfig() PositionHealthConfig {
	return PositionHealthConfig{
		Alpha:          0.3,
		ExitThreshold:  0.3,
		InitialHealth:  0.8,
		RegimeBoost:    0.15,
		RegimePenalty:  0.10,
		VolHighDamping: 0.8,
	}
}

// positionHealth tracks the health of a single position over time.
// Accessed via sync.Map pointer — mutations are in-place, no Store() needed.
type positionHealth struct {
	Health     float64   // current EWMA health value [0, 1]
	OpenedAt   time.Time // when the position was opened
	LastUpdate time.Time // last signal update time
}

// PositionHealthTracker maintains EWMA health scores for all open positions.
// Thread-safe via sync.Map (values are *positionHealth pointers).
type PositionHealthTracker struct {
	cfg PositionHealthConfig
	// key: "unitID:symbol", value: *positionHealth
	entries sync.Map
}

// NewPositionHealthTracker creates a tracker with the given config.
// Invalid parameters are clamped to safe defaults.
func NewPositionHealthTracker(cfg PositionHealthConfig) *PositionHealthTracker {
	if cfg.Alpha <= 0 || cfg.Alpha > 1 {
		cfg.Alpha = 0.3
	}
	if cfg.ExitThreshold <= 0 || cfg.ExitThreshold >= 1 {
		cfg.ExitThreshold = 0.3
	}
	if cfg.InitialHealth <= 0 || cfg.InitialHealth > 1 {
		cfg.InitialHealth = 0.8
	}
	if cfg.RegimeBoost < 0 || cfg.RegimeBoost > 0.5 {
		cfg.RegimeBoost = 0.15
	}
	if cfg.RegimePenalty < 0 || cfg.RegimePenalty > 0.5 {
		cfg.RegimePenalty = 0.10
	}
	if cfg.VolHighDamping <= 0 || cfg.VolHighDamping > 1 {
		cfg.VolHighDamping = 0.8
	}
	return &PositionHealthTracker{cfg: cfg}
}

// Register initializes health tracking for a new position.
// Uses LoadOrStore to avoid overwriting a concurrent registration.
func (t *PositionHealthTracker) Register(key string) {
	t.entries.LoadOrStore(key, &positionHealth{
		Health:     t.cfg.InitialHealth,
		OpenedAt:   time.Now(),
		LastUpdate: time.Now(),
	})
}

// Remove stops tracking a position (after close).
func (t *PositionHealthTracker) Remove(key string) {
	t.entries.Delete(key)
}

// signalAlignment computes how much the current signals support the existing
// position direction. Returns a value in [-1, 1]:
//
//	+1 = all signals strongly confirm the position
//	 0 = signals are neutral / hold
//	-1 = all signals strongly oppose the position
//
// Hold/Flat signals are treated as truly neutral (weight 0) — they express
// no directional opinion, so they should not penalize or reward health.
func signalAlignment(signals []strategy.Signal, posDir strategy.Direction) float64 {
	if len(signals) == 0 {
		return 0
	}

	totalWeight := 0.0
	alignmentSum := 0.0

	for _, sig := range signals {
		conf := sig.Confidence
		if conf <= 0 {
			continue
		}

		switch {
		case sig.Direction == posDir:
			// Signal confirms position direction.
			totalWeight += conf
			alignmentSum += conf
		case sig.Direction == strategy.DirectionHold || sig.Direction == strategy.DirectionFlat:
			// Neutral: strategy has no directional opinion.
			// Does not contribute to totalWeight (avoids diluting the score
			// when most strategies are simply idle).
		default:
			// Signal opposes position direction.
			totalWeight += conf
			alignmentSum -= conf
		}
	}

	if totalWeight == 0 {
		return 0 // all signals are hold/flat — no directional info
	}
	return alignmentSum / totalWeight // normalized to [-1, 1]
}

// Update recalculates health for a position based on current signals and
// market context. Returns (currentHealth, shouldExit).
//
// Parameters:
//   - key: "unitID:symbol"
//   - signals: current strategy signals from Pool.Compute()
//   - posDir: the direction of the existing position
//   - regime: dominant market regime ("trend", "range", "breakout", "panic").
//     When FeatureView is unavailable, pass "unknown" — no regime adjustment applied.
//   - volPercentile: current volatility percentile [0, 1].
//     When FeatureView is unavailable, pass 0.5 — vol damping will not trigger.
func (t *PositionHealthTracker) Update(
	key string,
	signals []strategy.Signal,
	posDir strategy.Direction,
	regime string,
	volPercentile float64,
) (float64, bool) {
	raw, ok := t.entries.Load(key)
	if !ok {
		// Position not tracked — register via LoadOrStore to avoid race.
		t.Register(key)
		return t.cfg.InitialHealth, false
	}
	ph := raw.(*positionHealth)

	// Step 1: compute signal alignment [-1, 1]
	alignment := signalAlignment(signals, posDir)

	// Step 2: convert alignment to a health observation [0, 1]
	// alignment -1 → observation 0, alignment 0 → 0.5, alignment +1 → 1.0
	observation := (alignment + 1.0) / 2.0

	// Step 3: EWMA update
	alpha := t.cfg.Alpha
	newHealth := alpha*observation + (1-alpha)*ph.Health

	// Step 4: regime adjustment
	// "unknown" and "breakout" apply no adjustment.
	switch regime {
	case "trend":
		if alignment > 0 {
			newHealth *= (1 + t.cfg.RegimeBoost)
		}
	case "range":
		newHealth *= (1 - t.cfg.RegimePenalty*0.5)
	case "panic":
		newHealth *= (1 - t.cfg.RegimePenalty)
	}

	// Step 5: volatility adjustment
	// High volatility → signals are noisier → slow down health decay.
	if volPercentile > 0.7 && newHealth < ph.Health {
		decay := ph.Health - newHealth
		newHealth = ph.Health - decay*t.cfg.VolHighDamping
	}

	// Clamp to [0, 1]
	if newHealth > 1.0 {
		newHealth = 1.0
	}
	if newHealth < 0 {
		newHealth = 0
	}

	// In-place mutation: sync.Map stores *positionHealth pointer,
	// so modifying the pointed-to struct is safe without re-Store().
	ph.Health = newHealth
	ph.LastUpdate = time.Now()

	return newHealth, newHealth < t.cfg.ExitThreshold
}

// Health returns the current health value for a position, or -1 if not tracked.
func (t *PositionHealthTracker) Health(key string) float64 {
	raw, ok := t.entries.Load(key)
	if !ok {
		return -1
	}
	return raw.(*positionHealth).Health
}
