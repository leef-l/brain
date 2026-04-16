package quant

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/brains/quant/adapter"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/learning"
	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
	"github.com/leef-l/brain/brains/quant/tracer"
	"github.com/leef-l/brain/brains/quant/tradestore"
)

// SnapshotSource provides real-time market snapshots. Implemented by both
// ringbuf.BufferManager (local) and remote.BufferManager (cross-sidecar IPC).
type SnapshotSource interface {
	Instruments() []string
	Latest(instID string) (ringbuf.MarketSnapshot, bool)
}

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
	config     Config
	signalExit SignalExitConfig
	logger     *slog.Logger
	buffers    SnapshotSource
	candles    CandleSource // optional, for passing candle history to strategies

	units         []*TradingUnit
	globalGuard   *risk.GlobalRiskGuard // cross-account risk limits
	traceStore    tracer.Store          // decision audit trail
	reviewer      Reviewer              // LLM review (nil = auto-approve)
	weightAdapter *learning.WeightAdapter   // L1: strategy weight adaptation
	symbolScorer  *learning.SymbolScorer   // L1: symbol preference scoring
	sltpOptimizer *learning.SLTPOptimizer  // L1: SL/TP ATR multiplier optimizer
	mu            sync.RWMutex

	// Anti-churn: cooldown tracking for signal_exit
	// key: "unitID:symbol", value: timestamp of last signal_exit close
	exitCooldowns sync.Map // map[string]time.Time
	// key: "unitID:symbol", value: timestamp when position was opened
	openTimes     sync.Map // map[string]time.Time
	// Position health tracker: EWMA-based smooth exit decisions
	healthTracker *PositionHealthTracker

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

// New creates a QuantBrain. buffers provides market snapshots — either a local
// ringbuf.BufferManager or a remote.BufferManager that reads from Data sidecar.
func New(cfg Config, buffers SnapshotSource, logger *slog.Logger) *QuantBrain {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.CycleInterval <= 0 {
		cfg.CycleInterval = 5 * time.Second
	}
	if cfg.DefaultTimeframe == "" {
		cfg.DefaultTimeframe = "1H"
	}

	sigExit := DefaultSignalExitConfig()
	return &QuantBrain{
		config:        cfg,
		signalExit:    sigExit,
		logger:        logger,
		buffers:       buffers,
		globalGuard:   risk.NewGlobalRiskGuard(risk.DefaultGlobalRiskConfig()),
		traceStore:    tracer.NewMemoryStore(10000), // default in-memory, override via SetTraceStore
		healthTracker: NewPositionHealthTracker(sigExit.PositionHealth),
	}
}

// SetSignalExitConfig configures signal-reversal-based position closing.
func (qb *QuantBrain) SetSignalExitConfig(cfg SignalExitConfig) {
	qb.mu.Lock()
	defer qb.mu.Unlock()
	qb.signalExit = cfg
	qb.healthTracker = NewPositionHealthTracker(cfg.PositionHealth)
}

// SetSnapshotSource replaces the snapshot data source at runtime.
// Used by Quant sidecar to swap in a RemoteBufferManager after
// KernelCaller becomes available.
func (qb *QuantBrain) SetSnapshotSource(src SnapshotSource) {
	qb.mu.Lock()
	defer qb.mu.Unlock()
	qb.buffers = src
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

// SetLearning configures L1 adaptive learning components.
func (qb *QuantBrain) SetLearning(wa *learning.WeightAdapter, ss *learning.SymbolScorer, opt *learning.SLTPOptimizer) {
	qb.mu.Lock()
	defer qb.mu.Unlock()
	qb.weightAdapter = wa
	qb.symbolScorer = ss
	qb.sltpOptimizer = opt
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

// PositionHealth returns the current health value for a position key
// ("unitID:symbol"), or -1 if not tracked.
func (qb *QuantBrain) PositionHealth(key string) float64 {
	return qb.healthTracker.Health(key)
}

// Start launches the evaluation loop.
func (qb *QuantBrain) Start(ctx context.Context) error {
	if qb.running.Load() {
		return fmt.Errorf("quant brain already running")
	}

	qb.mu.RLock()
	hasBuffers := qb.buffers != nil
	unitCount := len(qb.units)
	qb.mu.RUnlock()

	if !hasBuffers {
		return fmt.Errorf("no buffer manager provided")
	}
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

	// Start L1 learning loop (every 5 minutes).
	qb.mu.RLock()
	hasLearning := qb.weightAdapter != nil || qb.symbolScorer != nil || qb.sltpOptimizer != nil
	qb.mu.RUnlock()
	if hasLearning {
		qb.wg.Add(1)
		go func() {
			defer qb.wg.Done()
			qb.learningLoop(ctx)
		}()
	}

	qb.running.Store(true)
	qb.logger.Info("quant brain started",
		"units", unitCount,
		"cycle", qb.config.CycleInterval,
		"learning", hasLearning)
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

// learningLoop periodically updates L1 adaptive parameters (strategy weights
// and symbol scores) from trade history. Runs every 5 minutes.
func (qb *QuantBrain) learningLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run once immediately on start.
	qb.updateLearning(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			qb.updateLearning(ctx)
		}
	}
}

func (qb *QuantBrain) updateLearning(ctx context.Context) {
	qb.mu.RLock()
	wa := qb.weightAdapter
	ss := qb.symbolScorer
	opt := qb.sltpOptimizer
	units := qb.units
	qb.mu.RUnlock()

	// Collect a trade store from the first unit that has one.
	var store tradestore.Store
	for _, u := range units {
		if u.TradeStore != nil {
			store = u.TradeStore
			break
		}
	}
	if store == nil {
		return
	}

	// Update strategy weights.
	if wa != nil {
		wa.Update(ctx, store)
		// Apply new weights to all units' aggregators.
		newWeights := wa.Weights()
		for _, u := range units {
			u.Aggregator.SetWeights(newWeights)
		}
	}

	// Update symbol scores.
	if ss != nil {
		ss.Update(store)
	}

	// Update SL/TP recommendations.
	if opt != nil {
		opt.Update(store)
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

	qb.mu.RLock()
	buffers := qb.buffers
	units := qb.units
	qb.mu.RUnlock()
	symbols := buffers.Instruments()

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
		snap, ok := buffers.Latest(symbol)
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
						// Update TradeStore with exit info.
						qb.closeTradeRecord(ctx, unit, symbol, r)
					}
				}
			}

			// Track MAE/MFE for open trades on this symbol.
			qb.trackMAEMFE(ctx, unit, symbol, snap.CurrentPrice)

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

	// Signal reversal exit: if enabled, check whether the current signal
	// direction conflicts with an existing position and close it first.
	qb.mu.RLock()
	sigExit := qb.signalExit
	qb.mu.RUnlock()
	if sigExit.Enabled {
		qb.checkSignalExit(ctx, unit, view, sigExit)
	}

	// Cooldown check: skip opening if this symbol was recently closed by signal_exit.
	if sigExit.CooldownAfterExit > 0 {
		cooldownKey := unit.ID + ":" + view.Symbol()
		if lastExit, ok := qb.exitCooldowns.Load(cooldownKey); ok {
			if time.Since(lastExit.(time.Time)) < sigExit.CooldownAfterExit {
				return
			}
			// Cooldown expired, clean up
			qb.exitCooldowns.Delete(cooldownKey)
		}
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

	// Apply L1 SL/TP optimization: adjust SL/TP based on historical MAE/MFE.
	qb.applySLTPOptimization(td)

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
			proceed, sizeFactor := qb.integrateReview(ctx, reviewer, td, globalSnap, unit)
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

		// Record open time for anti-churn minimum hold duration.
		openKey := unit.ID + ":" + td.Symbol
		qb.openTimes.Store(openKey, time.Now())
		// Register position health tracking for smooth exit decisions.
		qb.healthTracker.Register(openKey)

		// Persist trade entry record for Oracle statistics.
		sig := bestSignalFromAgg(td.Signal)
		entryPrice := sig.Entry
		if entryPrice <= 0 {
			entryPrice = td.OrderReq.EntryPrice // fallback to order request price
		}
		if err := unit.TradeStore.Save(ctx, tradestore.TradeRecord{
			ID:         result.OrderID,
			AccountID:  unit.Account.ID,
			UnitID:     unit.ID,
			Symbol:     td.Symbol,
			Direction:  td.Signal.Direction,
			EntryPrice: entryPrice,
			Quantity:   td.SizeResult.Quantity,
			EntryTime:  result.Timestamp,
			Leverage:   unit.MaxLeverage,
			StopLoss:   sig.StopLoss,
			TakeProfit: sig.TakeProfit,
			ATR:        td.OrderReq.ATR,
			Confidence: td.Signal.Confidence,
			Strategy:   sig.Strategy,
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

// checkSignalExit uses the Position Health EWMA tracker to decide whether
// to close an existing position. Each cycle, signals are fed into the health
// tracker which smoothly decays the "position health" score. When health
// drops below the exit threshold, the position is closed.
//
// This replaces the old binary reversal check (which caused 60% of trades
// to close at exactly MinHoldDuration) with a continuous, regime-aware
// assessment that naturally filters signal noise.
func (qb *QuantBrain) checkSignalExit(ctx context.Context, unit *TradingUnit, view strategy.MarketView, cfg SignalExitConfig) {
	symbol := view.Symbol()
	healthKey := unit.ID + ":" + symbol

	// Query current positions for this symbol.
	positions, err := unit.Account.Exchange.QueryPositions(ctx)
	if err != nil {
		return
	}
	var existing *exchange.PositionInfo
	for i, p := range positions {
		if p.Symbol == symbol && p.Quantity > 0 {
			existing = &positions[i]
			break
		}
	}
	if existing == nil {
		// No position — clean up any stale health entry
		qb.healthTracker.Remove(healthKey)
		return
	}

	// Enforce minimum hold duration: don't signal_exit a position that was
	// just opened. This prevents the open→close→open churn loop on short TFs.
	if cfg.MinHoldDuration > 0 {
		if openT, ok := qb.openTimes.Load(healthKey); ok {
			if time.Since(openT.(time.Time)) < cfg.MinHoldDuration {
				return
			}
		}
	}

	// Run strategies to get current signals.
	signals := unit.Pool.Compute(view)

	existingDir := strategy.DirectionLong
	if existing.Side == "short" {
		existingDir = strategy.DirectionShort
	}

	// Extract regime and volatility from feature view (if available).
	regime := "unknown"
	volPercentile := 0.5
	if view.HasFeatureView() {
		f := view.Feature()
		regime = f.MarketRegime().Dominant()
		volPercentile = f.VolPrediction().VolPercentile
	}

	// Update health tracker — this is the core EWMA calculation.
	health, shouldExit := qb.healthTracker.Update(healthKey, signals, existingDir, regime, volPercentile)

	if !shouldExit {
		// Health is still above threshold — hold position.
		// Log at debug level for monitoring.
		if health < cfg.PositionHealth.ExitThreshold*1.5 {
			qb.logger.Debug("position health declining",
				"unit", unit.ID, "symbol", symbol,
				"health", fmt.Sprintf("%.3f", health),
				"threshold", fmt.Sprintf("%.3f", cfg.PositionHealth.ExitThreshold),
				"regime", regime)
		}
		return
	}

	// Health below threshold — close the position.
	closeSide := "sell"
	if existingDir == strategy.DirectionShort {
		closeSide = "buy"
	}

	params := exchange.PlaceOrderParams{
		Symbol:     symbol,
		Side:       closeSide,
		PosSide:    existing.Side,
		Type:       "market",
		Price:      view.CurrentPrice(),
		Quantity:   existing.Quantity,
		Leverage:   unit.MaxLeverage,
		ReduceOnly: true,
		ClientID:   fmt.Sprintf("%s-%s-exit-%d", unit.ID, symbol, time.Now().UnixMilli()),
	}

	result, err := unit.Account.Exchange.PlaceOrder(ctx, params)
	if err != nil {
		qb.logger.Warn("signal exit order failed",
			"unit", unit.ID, "symbol", symbol, "err", err)
		return
	}

	if result.Status == "filled" {
		qb.logger.Info("position health exit",
			"unit", unit.ID,
			"symbol", symbol,
			"direction", existingDir,
			"health", fmt.Sprintf("%.3f", health),
			"regime", regime,
			"exit_price", result.FillPrice)

		qb.closeTradeRecordWithReason(ctx, unit, symbol, result, "signal_exit")

		// Record cooldown and clean up tracking.
		qb.exitCooldowns.Store(healthKey, time.Now())
		qb.openTimes.Delete(healthKey)
		qb.healthTracker.Remove(healthKey)

		// Cancel orphaned SL/TP child orders for this symbol.
		if canceller, ok := unit.Account.Exchange.(exchange.BulkCanceller); ok {
			n := canceller.CancelOpenOrders(ctx, symbol)
			if n > 0 {
				qb.logger.Info("cancelled orphaned orders after health exit",
					"unit", unit.ID, "symbol", symbol, "cancelled", n)
			}
		} else {
			qb.logger.Warn("exchange does not support BulkCanceller, SL/TP orders may be orphaned after signal exit",
				"unit", unit.ID, "symbol", symbol, "exchange", unit.Account.Exchange.Name())
		}
	}
}

// closeTradeRecord finds the open trade record for a symbol and updates it
// with exit price and PnL when a stop-loss or take-profit order triggers.
// closeTradeRecord updates the trade store when SL/TP triggers.
// Reason is auto-detected from PnL (stop_loss vs take_profit).
func (qb *QuantBrain) closeTradeRecord(ctx context.Context, unit *TradingUnit, symbol string, r exchange.OrderResult) {
	reason := "" // auto-detect
	qb.closeTradeRecordWithReason(ctx, unit, symbol, r, reason)
}

// closeTradeRecordWithReason updates the trade store with an explicit reason.
// If reason is empty, it is auto-detected from PnL.
func (qb *QuantBrain) closeTradeRecordWithReason(ctx context.Context, unit *TradingUnit, symbol string, r exchange.OrderResult, reason string) {
	records := unit.TradeStore.Query(tradestore.Filter{
		UnitID: unit.ID,
		Symbol: symbol,
		Limit:  10,
	})
	for _, rec := range records {
		if rec.ExitPrice != 0 {
			continue // already closed
		}
		var pnl float64
		switch rec.Direction {
		case strategy.DirectionLong:
			pnl = rec.Quantity * (r.FillPrice - rec.EntryPrice)
		case strategy.DirectionShort:
			pnl = rec.Quantity * (rec.EntryPrice - r.FillPrice)
		}
		pnlPct := 0.0
		if rec.EntryPrice > 0 {
			pnlPct = (r.FillPrice - rec.EntryPrice) / rec.EntryPrice * 100
			if rec.Direction == strategy.DirectionShort {
				pnlPct = -pnlPct
			}
		}

		if reason == "" {
			if pnl > 0 {
				reason = "take_profit"
			} else {
				reason = "stop_loss"
			}
		}

		if err := unit.TradeStore.Update(ctx, rec.ID, tradestore.TradeUpdate{
			ExitPrice: r.FillPrice,
			PnL:       pnl,
			PnLPct:    pnlPct,
			ExitTime:  r.Timestamp,
			Reason:    reason,
		}); err != nil {
			qb.logger.Warn("update trade record failed",
				"unit", unit.ID, "tradeID", rec.ID, "err", err)
		} else {
			qb.logger.Info("trade closed",
				"unit", unit.ID,
				"symbol", symbol,
				"direction", rec.Direction,
				"entry", rec.EntryPrice,
				"exit", r.FillPrice,
				"pnl", pnl,
				"reason", reason)
		}

		// Clean up position tracking for any close reason (SL/TP/signal_exit).
		closeKey := unit.ID + ":" + symbol
		qb.healthTracker.Remove(closeKey)
		qb.openTimes.Delete(closeKey)
		return
	}
}

// applySLTPOptimization adjusts the trade decision's SL/TP using the
// SLTPOptimizer's recommendations. Only applies when confidence is sufficient
// and the recommended value differs meaningfully from the strategy's value.
func (qb *QuantBrain) applySLTPOptimization(td *TradeDecision) {
	qb.mu.RLock()
	opt := qb.sltpOptimizer
	qb.mu.RUnlock()
	if opt == nil {
		return
	}

	rec := opt.ForSymbol(td.Symbol)
	if rec.Confidence < 0.3 || rec.StopLossATR <= 0 {
		return // not enough data yet
	}

	// Find the best signal and adjust its SL/TP.
	for i := range td.Signal.Signals {
		sig := &td.Signal.Signals[i]
		if sig.Direction != td.Signal.Direction || sig.Entry <= 0 {
			continue
		}
		if sig.StopLoss <= 0 || sig.TakeProfit <= 0 {
			continue
		}

		// Current SL/TP distances as ratios of entry price.
		currentSLDist := math.Abs(sig.Entry-sig.StopLoss) / sig.Entry
		currentTPDist := math.Abs(sig.TakeProfit-sig.Entry) / sig.Entry

		// Recommended distances (ATR multiplier × ~0.5% baseline ATR ratio).
		const atrRatio = 0.005
		recSLDist := rec.StopLossATR * atrRatio
		recTPDist := rec.TakeProfitATR * atrRatio

		// Blend: 70% strategy + 30% optimizer (conservative blend).
		blendedSL := currentSLDist*0.7 + recSLDist*0.3
		blendedTP := currentTPDist*0.7 + recTPDist*0.3

		// Apply blended SL/TP.
		switch sig.Direction {
		case strategy.DirectionLong:
			sig.StopLoss = sig.Entry * (1 - blendedSL)
			sig.TakeProfit = sig.Entry * (1 + blendedTP)
		case strategy.DirectionShort:
			sig.StopLoss = sig.Entry * (1 + blendedSL)
			sig.TakeProfit = sig.Entry * (1 - blendedTP)
		}
	}

	// Also update OrderReq.StopLoss if it was set.
	best := bestSignalFromAgg(td.Signal)
	if best.StopLoss > 0 {
		td.OrderReq.StopLoss = best.StopLoss
	}
}

// trackMAEMFE updates Maximum Adverse/Favorable Excursion for open trades.
// Called on every price tick per symbol. MAE/MFE are stored as absolute
// price distances from entry (always >= 0).
func (qb *QuantBrain) trackMAEMFE(ctx context.Context, unit *TradingUnit, symbol string, currentPrice float64) {
	if unit.TradeStore == nil || currentPrice <= 0 {
		return
	}
	// Find open trades for this symbol.
	records := unit.TradeStore.Query(tradestore.Filter{
		UnitID: unit.ID,
		Symbol: symbol,
		Limit:  5,
	})
	for _, rec := range records {
		if !rec.ExitTime.IsZero() || rec.EntryPrice <= 0 {
			continue // already closed
		}
		var adverse, favorable float64
		switch rec.Direction {
		case strategy.DirectionLong:
			adverse = rec.EntryPrice - currentPrice  // price dropped below entry
			favorable = currentPrice - rec.EntryPrice // price rose above entry
		case strategy.DirectionShort:
			adverse = currentPrice - rec.EntryPrice  // price rose above entry
			favorable = rec.EntryPrice - currentPrice // price dropped below entry
		default:
			continue
		}
		if adverse < 0 {
			adverse = 0
		}
		if favorable < 0 {
			favorable = 0
		}
		// Only write if either value is a new high-water mark.
		if adverse > rec.MAE || favorable > rec.MFE {
			_ = unit.TradeStore.UpdateMAEMFE(ctx, rec.ID, adverse, favorable)
		}
	}
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
