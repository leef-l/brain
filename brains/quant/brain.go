package quant

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/brains/quant/adapter"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
	"github.com/leef-l/brain/brains/quant/tracer"
	"github.com/leef-l/brain/brains/quant/tradestore"
)

// CandleSource provides historical candle data to strategies that need it
// (e.g. BreakoutMomentum for high/low extremes). Implemented by DataBrain.
type CandleSource interface {
	Candles(instID, timeframe string) []CandleData
}

// CandleData is the candle format from the data brain provider.
// We use an alias-friendly struct to avoid importing provider directly.
type CandleData struct {
	Timestamp int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

// QuantBrain is the decision engine that consumes data brain output and
// drives multiple TradingUnits. It reads MarketSnapshots from the shared
// Ring Buffer, converts them to MarketViews, and runs each TradingUnit's
// strategy→aggregate→risk→execute pipeline.
type QuantBrain struct {
	config  Config
	logger  *slog.Logger
	buffers *ringbuf.BufferManager
	candles CandleSource // optional, for passing candle history to strategies

	units       []*TradingUnit
	globalGuard *risk.GlobalRiskGuard // cross-account risk limits
	traceStore  tracer.Store          // decision audit trail
	reviewer    Reviewer              // LLM review (nil = auto-approve)
	mu          sync.RWMutex

	// state
	running  atomic.Bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	recovery recoveryState // warmup suppression after crash recovery

	// metrics
	metrics QuantMetrics
}

// QuantMetrics holds runtime counters.
type QuantMetrics struct {
	CyclesTotal      atomic.Int64
	SignalsGenerated  atomic.Int64
	TradesAttempted   atomic.Int64
	TradesExecuted    atomic.Int64
	TradesRejected    atomic.Int64
	ReviewsFlagged    atomic.Int64
	CycleLatencyMs   atomic.Int64
}

// Config configures the quant brain.
type Config struct {
	// CycleInterval is how often the brain evaluates all symbols.
	// Default: 5 seconds.
	CycleInterval time.Duration `json:"cycle_interval" yaml:"cycle_interval"`

	// DefaultTimeframe is the primary timeframe if TradingUnit doesn't specify one.
	DefaultTimeframe string `json:"default_timeframe" yaml:"default_timeframe"`
}

// New creates a QuantBrain. buffers is the data brain's BufferManager.
func New(cfg Config, buffers *ringbuf.BufferManager, logger *slog.Logger) *QuantBrain {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.CycleInterval <= 0 {
		cfg.CycleInterval = 5 * time.Second
	}
	if cfg.DefaultTimeframe == "" {
		cfg.DefaultTimeframe = "1H"
	}

	return &QuantBrain{
		config:      cfg,
		logger:      logger,
		buffers:     buffers,
		globalGuard: risk.NewGlobalRiskGuard(risk.DefaultGlobalRiskConfig()),
		traceStore:  tracer.NewMemoryStore(10000), // default in-memory, override via SetTraceStore
	}
}

// SetCandleSource sets the candle data provider (typically DataBrain).
func (qb *QuantBrain) SetCandleSource(cs CandleSource) {
	qb.mu.Lock()
	defer qb.mu.Unlock()
	qb.candles = cs
}

// SetTraceStore overrides the default in-memory trace store (e.g. with PGTraceStore).
func (qb *QuantBrain) SetTraceStore(ts tracer.Store) {
	qb.mu.Lock()
	defer qb.mu.Unlock()
	qb.traceStore = ts
}

// SetGlobalRiskConfig overrides the default global risk configuration.
func (qb *QuantBrain) SetGlobalRiskConfig(cfg risk.GlobalRiskConfig) {
	qb.mu.Lock()
	defer qb.mu.Unlock()
	qb.globalGuard = risk.NewGlobalRiskGuard(cfg)
}

// AddUnit registers a TradingUnit. Must be called before Start.
func (qb *QuantBrain) AddUnit(unit *TradingUnit) {
	qb.mu.Lock()
	defer qb.mu.Unlock()
	qb.units = append(qb.units, unit)
	qb.logger.Info("trading unit added",
		"unit", unit.ID,
		"account", unit.Account.ID,
		"exchange", unit.Account.Exchange.Name(),
		"symbols", len(unit.Symbols))
}

// Units returns all registered TradingUnits.
func (qb *QuantBrain) Units() []*TradingUnit {
	qb.mu.RLock()
	defer qb.mu.RUnlock()
	return append([]*TradingUnit(nil), qb.units...)
}

// Start launches the evaluation loop.
func (qb *QuantBrain) Start(ctx context.Context) error {
	if qb.running.Load() {
		return fmt.Errorf("quant brain already running")
	}
	if qb.buffers == nil {
		return fmt.Errorf("no buffer manager provided")
	}

	qb.mu.RLock()
	unitCount := len(qb.units)
	qb.mu.RUnlock()
	if unitCount == 0 {
		return fmt.Errorf("no trading units registered")
	}

	ctx, cancel := context.WithCancel(ctx)
	qb.cancel = cancel

	qb.wg.Add(1)
	go func() {
		defer qb.wg.Done()
		qb.evaluationLoop(ctx)
	}()

	qb.running.Store(true)
	qb.logger.Info("quant brain started",
		"units", unitCount,
		"cycle", qb.config.CycleInterval)
	return nil
}

// Stop gracefully stops the quant brain.
func (qb *QuantBrain) Stop(ctx context.Context) error {
	if !qb.running.Load() {
		return nil
	}

	qb.logger.Info("stopping quant brain")
	if qb.cancel != nil {
		qb.cancel()
	}

	done := make(chan struct{})
	go func() {
		qb.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		qb.logger.Warn("stop timeout")
	}

	qb.running.Store(false)
	qb.logger.Info("quant brain stopped")
	return nil
}

// evaluationLoop is the main loop that periodically evaluates all symbols.
func (qb *QuantBrain) evaluationLoop(ctx context.Context) {
	ticker := time.NewTicker(qb.config.CycleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			qb.safeRunCycle(ctx)
		}
	}
}

// safeRunCycle wraps runCycle with panic recovery so a single cycle panic
// does not kill the entire evaluation loop.
func (qb *QuantBrain) safeRunCycle(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			qb.logger.Error("cycle panic recovered",
				"panic", fmt.Sprintf("%v", r))
		}
	}()
	qb.runCycle(ctx)
}

// runCycle evaluates all symbols across all trading units.
func (qb *QuantBrain) runCycle(ctx context.Context) {
	start := time.Now()
	cycle := qb.metrics.CyclesTotal.Add(1)

	symbols := qb.buffers.Instruments()
	qb.mu.RLock()
	units := qb.units
	qb.mu.RUnlock()

	// Log diagnostics periodically (every 60 cycles ≈ 5 min at 5s interval).
	diagnose := cycle%60 == 1
	if diagnose {
		qb.logger.Info("cycle heartbeat",
			"cycle", cycle,
			"symbols", len(symbols),
			"units", len(units),
			"warmup", qb.recovery.isWarmingUp())
	}

	for _, symbol := range symbols {
		snap, ok := qb.buffers.Latest(symbol)
		if !ok || snap.CurrentPrice <= 0 {
			continue
		}

		for _, unit := range units {
			if !unit.ShouldTrade(symbol) {
				continue
			}

			// Feed price tick to exchanges that support it (e.g. PaperExchange)
			// to trigger stop-loss / take-profit on existing orders.
			if feeder, ok := unit.Account.Exchange.(exchange.TickFeeder); ok {
				results, err := feeder.ProcessPriceTick(ctx, symbol, snap.CurrentPrice)
				if err != nil {
					qb.logger.Warn("ProcessPriceTick failed",
						"unit", unit.ID,
						"symbol", symbol,
						"err", err)
				}
				for _, r := range results {
					if r.Status == "filled" {
						qb.logger.Info("order triggered",
							"unit", unit.ID,
							"symbol", symbol,
							"orderID", r.OrderID,
							"price", r.FillPrice)
					}
				}
			}

			tf := unit.Timeframe
			if tf == "" {
				tf = qb.config.DefaultTimeframe
			}

			view := adapter.NewSnapshotView(snap, tf)

			// Attach candle history if available (needed by BreakoutMomentum etc.)
			qb.mu.RLock()
			candleSrc := qb.candles
			qb.mu.RUnlock()
			if candleSrc != nil {
				for _, ctf := range []string{"1m", "5m", "15m", "1H", "4H"} {
					if rawCandles := candleSrc.Candles(symbol, ctf); len(rawCandles) > 0 {
						stratCandles := make([]strategy.Candle, len(rawCandles))
						for j, c := range rawCandles {
							stratCandles[j] = strategy.Candle{
								Timestamp: c.Timestamp,
								Open:      c.Open,
								High:      c.High,
								Low:       c.Low,
								Close:     c.Close,
								Volume:    c.Volume,
							}
						}
						view.SetCandles(ctf, stratCandles)
					}
				}
			}

			// Periodic signal diagnostics: log raw strategy output so operators
			// can tell whether strategies produce signals that get aggregated away
			// or never fire at all.
			if diagnose {
				signals := unit.Pool.Compute(view)
				for _, sig := range signals {
					if sig.Direction != strategy.DirectionHold {
						qb.logger.Info("strategy signal (diag)",
							"unit", unit.ID,
							"symbol", symbol,
							"strategy", sig.Strategy,
							"direction", sig.Direction,
							"confidence", fmt.Sprintf("%.4f", sig.Confidence),
							"reason", sig.Reason)
					}
				}
				if len(signals) > 0 {
					holdCount := 0
					for _, s := range signals {
						if s.Direction == strategy.DirectionHold {
							holdCount++
						}
					}
					if holdCount == len(signals) {
						reasons := make([]string, 0, len(signals))
						for _, s := range signals {
							reasons = append(reasons, s.Strategy+": "+s.Reason)
						}
						qb.logger.Info("all strategies hold (diag)",
							"unit", unit.ID,
							"symbol", symbol,
							"reasons", reasons)
					}
				}
			}

			qb.evaluateUnit(ctx, unit, view)
		}
	}

	qb.metrics.CycleLatencyMs.Store(time.Since(start).Milliseconds())

	// Tick warmup counter down
	if qb.recovery.isWarmingUp() {
		qb.recovery.tick()
	}
}

// buildGlobalSnapshot aggregates positions and equity across all units.
// Returns an error if no account could be queried (snapshot would be meaningless).
func (qb *QuantBrain) buildGlobalSnapshot(ctx context.Context) (risk.GlobalSnapshot, error) {
	qb.mu.RLock()
	units := qb.units
	qb.mu.RUnlock()

	var snap risk.GlobalSnapshot
	snap.DailyPnL = make(map[string]float64)

	successCount := 0
	for _, u := range units {
		positions, err := u.Account.Exchange.QueryPositions(ctx)
		if err != nil {
			qb.logger.Warn("global snapshot: position query failed, skipping account",
				"unit", u.ID, "account", u.Account.ID, "err", err)
			continue
		}
		balance, err := u.Account.Exchange.QueryBalance(ctx)
		if err != nil {
			qb.logger.Warn("global snapshot: balance query failed, skipping account",
				"unit", u.ID, "account", u.Account.ID, "err", err)
			continue
		}
		successCount++
		snap.TotalEquity += balance.Equity
		for _, p := range positions {
			// Use MarkPrice for accurate current value; fall back to AvgPrice.
			markPrice := p.MarkPrice
			if markPrice <= 0 {
				markPrice = p.AvgPrice
			}
			snap.Positions = append(snap.Positions, risk.Position{
				Symbol:    p.Symbol,
				Direction: dirFromSide(p.Side),
				Quantity:  p.Quantity,
				Notional:  p.Quantity * markPrice,
			})
		}
		// Daily PnL tracked per unit from trade store stats
		stats := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID})
		snap.DailyPnL[u.ID] = stats.TotalPnL
	}
	if successCount == 0 && len(units) > 0 {
		return snap, fmt.Errorf("all %d account queries failed", len(units))
	}
	return snap, nil
}

func dirFromSide(side string) strategy.Direction {
	if side == "short" {
		return strategy.DirectionShort
	}
	return strategy.DirectionLong
}

// evaluateUnit runs one TradingUnit against one symbol's MarketView.
func (qb *QuantBrain) evaluateUnit(ctx context.Context, unit *TradingUnit, view *adapter.SnapshotView) {
	// During warmup period after crash recovery, skip new trade evaluation
	// to let strategies rebuild state. Existing SL/TP triggers still run
	// (handled in runCycle's TickFeeder processing).
	if qb.recovery.isWarmingUp() {
		return
	}

	td, err := unit.Evaluate(ctx, view)
	if err != nil {
		qb.logger.Error("evaluate failed",
			"unit", unit.ID,
			"symbol", view.Symbol(),
			"err", err)
		return
	}

	if td == nil {
		return // no trade signal
	}

	qb.metrics.SignalsGenerated.Add(1)

	// Build global snapshot ONCE — reused for LLM review and global risk check.
	// Building it multiple times risks inconsistent state between checks.
	globalSnap, snapErr := qb.buildGlobalSnapshot(ctx)
	if snapErr != nil {
		qb.logger.Error("global snapshot unavailable, skipping trade evaluation",
			"unit", unit.ID,
			"symbol", view.Symbol(),
			"err", snapErr)
		return
	}

	// Build trace for audit trail
	trace := &tracer.SignalTrace{
		TraceID:    fmt.Sprintf("%s-%s-%d", unit.ID, td.Symbol, time.Now().UnixMilli()),
		Timestamp:  time.Now(),
		Symbol:     td.Symbol,
		Price:      view.CurrentPrice(),
		Features:   view.FeatureVector(),
		Signals:    td.Signal.Signals,
		Aggregated: td.Signal,
	}

	// If needs review, send to LLM reviewer (or skip if no reviewer)
	if td.NeedsReview {
		qb.metrics.ReviewsFlagged.Add(1)

		qb.mu.RLock()
		reviewer := qb.reviewer
		qb.mu.RUnlock()

		if reviewer != nil {
			proceed, sizeFactor := qb.integrateReview(ctx, reviewer, td, globalSnap)
			if !proceed {
				trace.Outcome = "rejected_review"
				qb.saveTrace(ctx, trace)
				return
			}
			// Apply LLM size factor and sync back to OrderReq for correct risk check.
			if sizeFactor > 0 && sizeFactor < 1.0 {
				td.SizeResult.Quantity *= sizeFactor
				td.SizeResult.Notional *= sizeFactor
				td.OrderReq.Quantity = td.SizeResult.Quantity
				td.OrderReq.Notional = td.SizeResult.Notional
			}
		} else {
			// No reviewer configured, skip the trade
			trace.Outcome = "needs_review"
			qb.saveTrace(ctx, trace)
			qb.logger.Warn("trade needs review, skipping (no reviewer)",
				"unit", unit.ID,
				"symbol", td.Symbol,
				"direction", td.Signal.Direction,
				"reason", td.ReviewReason)
			return
		}
	}

	// Global risk guard: cross-account limits check (uses same snapshot as review)
	qb.mu.RLock()
	globalGuard := qb.globalGuard
	qb.mu.RUnlock()
	globalDecision := globalGuard.Evaluate(td.OrderReq, globalSnap)
	trace.GlobalRisk = globalDecision
	if !globalDecision.Allowed {
		qb.metrics.TradesRejected.Add(1)
		trace.Outcome = "rejected_global"
		qb.saveTrace(ctx, trace)
		qb.logger.Warn("global risk guard rejected",
			"unit", unit.ID,
			"symbol", td.Symbol,
			"reason", globalDecision.Reason)
		return
	}

	// Execute the trade
	qb.metrics.TradesAttempted.Add(1)
	execStart := time.Now()
	result, err := unit.Execute(ctx, td)
	execLatency := time.Since(execStart)

	acctResult := tracer.AccountTraceResult{
		AccountID: unit.Account.ID,
		UnitID:    unit.ID,
		Latency:   execLatency,
	}

	if err != nil {
		qb.metrics.TradesRejected.Add(1)
		acctResult.Status = "execution_error"
		acctResult.RiskResult = risk.Decision{Allowed: false, Reason: err.Error()}
		trace.AccountResults = append(trace.AccountResults, acctResult)
		trace.Outcome = "execution_error"
		qb.saveTrace(ctx, trace)
		qb.logger.Error("execute failed",
			"unit", unit.ID,
			"symbol", td.Symbol,
			"err", err)
		return
	}

	if result != nil && result.Status == "filled" {
		qb.metrics.TradesExecuted.Add(1)
		acctResult.Status = "filled"
		acctResult.OrderID = result.OrderID
		acctResult.Quantity = td.SizeResult.Quantity

		// Persist trade entry record for Oracle statistics.
		sig := bestSignalFromAgg(td.Signal)
		entryPrice := sig.Entry
		if entryPrice <= 0 {
			entryPrice = td.OrderReq.EntryPrice // fallback to order request price
		}
		if err := unit.TradeStore.Save(ctx, tradestore.TradeRecord{
			ID:         result.OrderID,
			UnitID:     unit.ID,
			Symbol:     td.Symbol,
			Direction:  td.Signal.Direction,
			EntryPrice: entryPrice,
			Quantity:   td.SizeResult.Quantity,
			EntryTime:  result.Timestamp,
		}); err != nil {
			qb.logger.Error("trade store save failed",
				"unit", unit.ID,
				"orderID", result.OrderID,
				"err", err)
		}
	} else {
		acctResult.Status = "skipped"
	}

	trace.AccountResults = append(trace.AccountResults, acctResult)
	trace.Outcome = "executed"
	qb.saveTrace(ctx, trace)
}

// saveTrace persists a signal trace to the trace store.
func (qb *QuantBrain) saveTrace(ctx context.Context, trace *tracer.SignalTrace) {
	qb.mu.RLock()
	ts := qb.traceStore
	qb.mu.RUnlock()
	if ts == nil {
		return
	}
	if err := ts.Save(ctx, trace); err != nil {
		qb.logger.Error("save trace failed", "trace", trace.TraceID, "err", err)
	}
}

func bestSignalFromAgg(agg strategy.AggregatedSignal) strategy.Signal {
	var best strategy.Signal
	for _, s := range agg.Signals {
		if s.Direction == agg.Direction && s.Confidence > best.Confidence {
			best = s
		}
	}
	return best
}

// TraceStore returns the underlying trace store for external queries.
func (qb *QuantBrain) TraceStore() tracer.Store {
	qb.mu.RLock()
	defer qb.mu.RUnlock()
	return qb.traceStore
}

// Health returns a health summary.
func (qb *QuantBrain) Health() map[string]any {
	qb.mu.RLock()
	unitCount := len(qb.units)
	qb.mu.RUnlock()

	return map[string]any{
		"running":           qb.running.Load(),
		"units":             unitCount,
		"cycles_total":      qb.metrics.CyclesTotal.Load(),
		"signals_generated": qb.metrics.SignalsGenerated.Load(),
		"trades_attempted":  qb.metrics.TradesAttempted.Load(),
		"trades_executed":   qb.metrics.TradesExecuted.Load(),
		"trades_rejected":   qb.metrics.TradesRejected.Load(),
		"reviews_flagged":   qb.metrics.ReviewsFlagged.Load(),
		"cycle_latency_ms":  qb.metrics.CycleLatencyMs.Load(),
		"warmup_remaining":  qb.recovery.ticksRemaining.Load(),
	}
}
