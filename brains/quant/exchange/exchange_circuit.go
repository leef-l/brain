// Package exchange implements venue-agnostic trading interfaces.
// exchange_circuit.go adds exchange-level circuit breaking so that
// repeated API failures automatically degrade to paper trading
// instead of burning through the account with failed orders.
package exchange

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// CircuitState represents the current state of the circuit breaker.
type CircuitState int32

const (
	// CircuitNormal — orders flow through normally.
	CircuitNormal CircuitState = 0
	// CircuitTriggered — the exchange has been failing continuously;
	// all subsequent orders are rejected or diverted to paper.
	CircuitTriggered CircuitState = 1
	// CircuitRecovering — we are in the cool-down window and will
	// attempt a single probe order on the next request.
	CircuitRecovering CircuitState = 2
)

func (s CircuitState) String() string {
	switch s {
	case CircuitNormal:
		return "normal"
	case CircuitTriggered:
		return "triggered"
	case CircuitRecovering:
		return "recovering"
	default:
		return "unknown"
	}
}

// ExchangeCircuitBreaker tracks consecutive failures for a single
// exchange account.  When the failure streak exceeds
// MaxConsecutiveFailures the breaker trips and all further orders
// are diverted to the paper exchange until the cool-down period
// elapses.
type ExchangeCircuitBreaker struct {
	MaxConsecutiveFailures int           // e.g. 5
	CoolDownPeriod         time.Duration // e.g. 5 * time.Minute

	// mutable state — accessed atomically for hot paths.
	consecutiveFailures atomic.Int32
	lastFailure         atomic.Int64 // unix-ms timestamp
	state               atomic.Int32 // CircuitState

	// paperExchange is the fallback used when the real exchange is
	// in triggered state.  It must implement the Exchange interface.
	paperExchange Exchange

	mu     sync.RWMutex
	logger *slog.Logger

	// metrics for observability.
	totalFailures   int64
	totalTrips      int64
	lastTripTime    time.Time
	lastRecoverTime time.Time
}

// CircuitBreakerConfig holds tunables.
type CircuitBreakerConfig struct {
	MaxConsecutiveFailures int
	CoolDownPeriod         time.Duration
}

// DefaultCircuitBreakerConfig returns sensible defaults.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		MaxConsecutiveFailures: 5,
		CoolDownPeriod:         5 * time.Minute,
	}
}

// NewExchangeCircuitBreaker creates a breaker with the given fallback
// paper exchange.
func NewExchangeCircuitBreaker(
	cfg CircuitBreakerConfig,
	paper Exchange,
	logger *slog.Logger,
) *ExchangeCircuitBreaker {
	if cfg.MaxConsecutiveFailures <= 0 {
		cfg.MaxConsecutiveFailures = DefaultCircuitBreakerConfig().MaxConsecutiveFailures
	}
	if cfg.CoolDownPeriod <= 0 {
		cfg.CoolDownPeriod = DefaultCircuitBreakerConfig().CoolDownPeriod
	}
	if logger == nil {
		logger = slog.Default()
	}

	b := &ExchangeCircuitBreaker{
		MaxConsecutiveFailures: cfg.MaxConsecutiveFailures,
		CoolDownPeriod:         cfg.CoolDownPeriod,
		paperExchange:          paper,
		logger:                 logger,
	}
	b.state.Store(int32(CircuitNormal))
	return b
}

// ── State queries (thread-safe) ───────────────────────────────

// State returns the current circuit state.
func (b *ExchangeCircuitBreaker) State() CircuitState {
	return CircuitState(b.state.Load())
}

// ConsecutiveFailures returns the current failure streak.
func (b *ExchangeCircuitBreaker) ConsecutiveFailures() int {
	return int(b.consecutiveFailures.Load())
}

// IsNormal returns true when orders should go to the real exchange.
func (b *ExchangeCircuitBreaker) IsNormal() bool {
	return b.State() == CircuitNormal
}

// IsTriggered returns true when the circuit is open (paper mode).
func (b *ExchangeCircuitBreaker) IsTriggered() bool {
	s := b.State()
	return s == CircuitTriggered || s == CircuitRecovering
}

// PaperExchange returns the fallback paper exchange.  May be nil.
func (b *ExchangeCircuitBreaker) PaperExchange() Exchange {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.paperExchange
}

// ── Failure / success tracking ────────────────────────────────

// RecordFailure increments the failure counter and trips the breaker
// if the streak exceeds MaxConsecutiveFailures.
func (b *ExchangeCircuitBreaker) RecordFailure() {
	streak := b.consecutiveFailures.Add(1)
	b.lastFailure.Store(time.Now().UnixMilli())

	b.mu.Lock()
	b.totalFailures++
	b.mu.Unlock()

	if int(streak) >= b.MaxConsecutiveFailures {
		b.trip()
	}
}

// RecordSuccess resets the failure counter and transitions from
// triggered/recovering back to normal.
func (b *ExchangeCircuitBreaker) RecordSuccess() {
	oldStreak := b.consecutiveFailures.Swap(0)
	if oldStreak == 0 {
		return // nothing changed
	}

	oldState := CircuitState(b.state.Swap(int32(CircuitNormal)))
	if oldState != CircuitNormal {
		b.mu.Lock()
		b.lastRecoverTime = time.Now()
		b.mu.Unlock()

		b.logger.Info("exchange circuit: recovered",
			"previous_state", oldState.String(),
			"failure_streak", oldStreak)
	}
}

// trip transitions the circuit to triggered state.
func (b *ExchangeCircuitBreaker) trip() {
	old := CircuitState(b.state.Swap(int32(CircuitTriggered)))
	if old == CircuitTriggered {
		return // already tripped
	}

	b.mu.Lock()
	b.totalTrips++
	b.lastTripTime = time.Now()
	b.mu.Unlock()

	b.logger.Warn("exchange circuit: TRIGGERED",
		"failure_streak", b.consecutiveFailures.Load(),
		"cool_down", b.CoolDownPeriod.String(),
		"fallback", "paper")
}

// MaybeRecover checks whether the cool-down period has elapsed and
// transitions triggered → recovering so that the next order acts as
// a probe.  Returns the (possibly updated) state.
func (b *ExchangeCircuitBreaker) MaybeRecover() CircuitState {
	if b.State() != CircuitTriggered {
		return b.State()
	}

	lastFail := time.UnixMilli(b.lastFailure.Load())
	if time.Since(lastFail) < b.CoolDownPeriod {
		return CircuitTriggered
	}

	// Cool-down elapsed — move to recovering.  The next order will
	// be sent to the real exchange as a probe.
	b.state.Store(int32(CircuitRecovering))
	b.logger.Info("exchange circuit: entering recovery (probe mode)")
	return CircuitRecovering
}

// ── Order routing ─────────────────────────────────────────────

// RouteOrder decides whether an order should go to the real exchange
// or the paper fallback.  It also handles the recovery probe logic.
//
// Returns:
//   - realExchange = true  → caller should use the real exchange.
//   - realExchange = false → caller should use PaperExchange() (or skip).
//   - err            → order should be rejected (circuit open, no paper).
func (b *ExchangeCircuitBreaker) RouteOrder() (realExchange bool, err error) {
	state := b.MaybeRecover() // may transition triggered→recovering

	switch state {
	case CircuitNormal:
		return true, nil

	case CircuitRecovering:
		// Probe mode: send ONE order to the real exchange.  If it
		// succeeds we transition to normal; if it fails we go back
		// to triggered (RecordFailure handles that).
		b.logger.Info("exchange circuit: probe order to real exchange")
		return true, nil

	case CircuitTriggered:
		b.mu.RLock()
		paper := b.paperExchange
		b.mu.RUnlock()
		if paper == nil {
			return false, fmt.Errorf("exchange circuit open and no paper fallback configured")
		}
		return false, nil

	default:
		return false, fmt.Errorf("unknown circuit state: %d", state)
	}
}

// ──── Metrics / observability ─────────────────────────────────

// Metrics exposes counters for health-check / sidecar tools.
type CircuitMetrics struct {
	State               string        `json:"state"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	TotalFailures       int64         `json:"total_failures"`
	TotalTrips          int64         `json:"total_trips"`
	LastTripTime        *time.Time    `json:"last_trip_time,omitempty"`
	LastRecoverTime     *time.Time    `json:"last_recover_time,omitempty"`
	CoolDownRemaining   time.Duration `json:"cool_down_remaining,omitempty"`
}

// Metrics returns a snapshot of circuit-breaker counters.
func (b *ExchangeCircuitBreaker) Metrics() CircuitMetrics {
	b.mu.RLock()
	defer b.mu.RUnlock()

	m := CircuitMetrics{
		State:               b.State().String(),
		ConsecutiveFailures: int(b.consecutiveFailures.Load()),
		TotalFailures:       b.totalFailures,
		TotalTrips:          b.totalTrips,
	}
	if !b.lastTripTime.IsZero() {
		t := b.lastTripTime
		m.LastTripTime = &t
	}
	if !b.lastRecoverTime.IsZero() {
		t := b.lastRecoverTime
		m.LastRecoverTime = &t
	}
	if b.State() == CircuitTriggered {
		lastFail := time.UnixMilli(b.lastFailure.Load())
		remaining := b.CoolDownPeriod - time.Since(lastFail)
		if remaining > 0 {
			m.CoolDownRemaining = remaining
		}
	}
	return m
}
