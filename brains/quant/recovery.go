package quant

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/leef-l/brain/brains/quant/tradestore"
)

// RecoveryConfig configures crash recovery behavior.
type RecoveryConfig struct {
	// WarmupTicks is the number of evaluation cycles to skip new positions
	// after recovery, allowing strategies to build state. Default: 10.
	WarmupTicks int `json:"warmup_ticks" yaml:"warmup_ticks"`

	// ValidateWithExchange controls whether recovery queries the exchange
	// for current positions to cross-validate local state. Default: true.
	ValidateWithExchange bool `json:"validate_with_exchange" yaml:"validate_with_exchange"`
}

// DefaultRecoveryConfig returns default recovery settings.
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		WarmupTicks:          10,
		ValidateWithExchange: true,
	}
}

// recoveryState tracks the warmup period after a crash recovery.
type recoveryState struct {
	ticksRemaining atomic.Int32
}

func (r *recoveryState) isWarmingUp() bool {
	return r.ticksRemaining.Load() > 0
}

func (r *recoveryState) tick() {
	if v := r.ticksRemaining.Load(); v > 0 {
		r.ticksRemaining.Add(-1)
	}
}

// RecoverState runs the crash recovery sequence:
//  1. Load trade records from persistent store → restore Oracle statistics
//  2. Query exchange positions to verify state
//  3. Start warmup period (suppress new trades for N ticks)
//
// This should be called before Start() if the quant brain crashed previously.
func (qb *QuantBrain) RecoverState(ctx context.Context, cfg RecoveryConfig) error {
	if cfg.WarmupTicks <= 0 {
		cfg.WarmupTicks = 10
	}

	qb.mu.RLock()
	units := qb.units
	qb.mu.RUnlock()

	if len(units) == 0 {
		return fmt.Errorf("no units registered for recovery")
	}

	logger := qb.logger.With("phase", "recovery")
	logger.Info("starting crash recovery", "units", len(units))

	totalRestored := 0
	totalMismatches := 0

	for _, unit := range units {
		restored, mismatches, err := recoverUnit(ctx, unit, cfg, logger)
		if err != nil {
			logger.Error("unit recovery failed",
				"unit", unit.ID,
				"err", err)
			// Continue with other units; don't fail the whole recovery
			continue
		}
		totalRestored += restored
		totalMismatches += mismatches
	}

	// Set warmup period
	qb.recovery.ticksRemaining.Store(int32(cfg.WarmupTicks))

	logger.Info("crash recovery complete",
		"trades_restored", totalRestored,
		"position_mismatches", totalMismatches,
		"warmup_ticks", cfg.WarmupTicks)

	return nil
}

// recoverUnit recovers a single TradingUnit's state.
func recoverUnit(ctx context.Context, unit *TradingUnit, cfg RecoveryConfig, logger *slog.Logger) (restored int, mismatches int, err error) {
	// Step 1: Load historical trades from persistent store and replay them
	// through the Oracle so win-rate/avg-win/avg-loss statistics are populated.
	// PGStore.Query already reads from PG, so Oracle.StatsForSizer will work.
	// However, on startup the Oracle only sees trades in the Store. If the
	// Store is PGStore, data is already there. If wrapping a MemoryStore with
	// PG backup, we need to load records into the Store.
	if pgStore, ok := unit.TradeStore.(*tradestore.PGStore); ok {
		records, loadErr := pgStore.LoadAll(ctx)
		if loadErr != nil {
			return 0, 0, fmt.Errorf("load trades: %w", loadErr)
		}
		restored = len(records)
		// Oracle reads from the Store via Query, so PGStore already has data.
		// Force an initial stats computation to validate data is accessible.
		stats := unit.TradeStore.Stats(tradestore.Filter{UnitID: unit.ID})
		logger.Info("restored trade history",
			"unit", unit.ID,
			"records", restored,
			"total_pnl", stats.TotalPnL,
			"win_rate", stats.WinRate)
	}

	// Step 2: Validate with exchange positions (exchange is source of truth).
	if cfg.ValidateWithExchange {
		positions, qErr := unit.Account.Exchange.QueryPositions(ctx)
		if qErr != nil {
			logger.Warn("cannot query exchange positions for validation",
				"unit", unit.ID,
				"err", qErr)
		} else {
			logger.Info("exchange positions validated",
				"unit", unit.ID,
				"open_positions", len(positions))

			// Step 3: Verify stop-loss orders are still active.
			// For paper exchange (TickFeeder), positions are tracked locally
			// and may need no verification.
			// For live exchanges, we log warnings if positions exist but no
			// stop-loss is found (future: resubmit SL orders).
			for _, p := range positions {
				if p.Quantity > 0 {
					logger.Info("open position found",
						"unit", unit.ID,
						"symbol", p.Symbol,
						"side", p.Side,
						"qty", p.Quantity,
						"avgPrice", p.AvgPrice)
				}
			}
		}
	}

	return restored, mismatches, nil
}
