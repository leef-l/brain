package quant

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
	"github.com/leef-l/brain/brains/quant/tradestore"
)

// TradingUnit is an independent trading pipeline:
//   Account + StrategyPool + Aggregator + Guard + Sizer → Orders
//
// Each TradingUnit runs its own strategy/risk/execution loop. Multiple
// TradingUnits can share the same data source (Ring Buffer) but operate
// on different accounts with different configurations.
type TradingUnit struct {
	ID         string
	Account    *Account
	Pool       *strategy.Pool
	Aggregator *strategy.RegimeAwareAggregator
	Guard      *risk.AdaptiveGuard
	Sizer      *risk.BayesianSizer
	TradeStore tradestore.Store
	Oracle     *tradestore.Oracle
	Logger     *slog.Logger

	// Symbols this unit trades. If empty, trades all available symbols.
	Symbols []string

	// Timeframe is the primary timeframe for strategy evaluation.
	Timeframe string

	// MaxLeverage overrides the account's exchange max leverage (if lower).
	MaxLeverage int

	// Enabled controls whether the unit actively trades.
	Enabled bool

	// RouteConfig controls per-account routing behavior (WeightFactor, filters).
	RouteConfig RouteConfig

	// BudgetEquity: when > 0, overrides exchange balance for position sizing.
	BudgetEquity float64
}

// TradingUnitConfig is the configuration for creating a TradingUnit.
type TradingUnitConfig struct {
	ID          string
	Account     *Account
	Symbols     []string
	Timeframe   string
	MaxLeverage int
	// Strategy/risk overrides; nil = use defaults
	Pool       *strategy.Pool
	Aggregator *strategy.RegimeAwareAggregator
	Guard      *risk.AdaptiveGuard
	Sizer      *risk.BayesianSizer
	// TradeStore override; nil = use MemoryStore (data lost on restart).
	// Pass a PGStore for persistent trade history.
	TradeStore  tradestore.Store
	// RouteConfig for per-account routing (weight factor, allowed strategies/symbols).
	RouteConfig RouteConfig
	// BudgetEquity overrides QueryBalance for position sizing.
	// When > 0, the unit uses this as its equity cap instead of the
	// exchange's real balance. This lets multiple units share one
	// exchange account while each operating within its own budget.
	BudgetEquity float64
}

// NewTradingUnit creates a TradingUnit with defaults for unset fields.
func NewTradingUnit(cfg TradingUnitConfig, logger *slog.Logger) *TradingUnit {
	if logger == nil {
		logger = slog.Default()
	}

	pool := cfg.Pool
	if pool == nil {
		pool = strategy.DefaultPool()
	}

	agg := cfg.Aggregator
	if agg == nil {
		agg = strategy.NewRegimeAwareAggregator()
	}

	// Deep-copy guard so per-unit adjustments don't pollute shared state.
	guard := cfg.Guard
	if guard == nil {
		guard = risk.DefaultAdaptiveGuard()
	}
	guardCopy := *guard // value copy of AdaptiveGuard (includes Base by value)
	guard = &guardCopy

	sizer := cfg.Sizer
	if sizer == nil {
		sizer = risk.DefaultBayesianSizer()
	}

	tf := cfg.Timeframe
	if tf == "" {
		tf = "1H"
	}

	maxLev := cfg.MaxLeverage
	if maxLev <= 0 && cfg.Account != nil {
		maxLev = cfg.Account.MaxLeverage()
	}
	// Clamp to exchange limit
	if cfg.Account != nil && maxLev > cfg.Account.MaxLeverage() {
		maxLev = cfg.Account.MaxLeverage()
	}

	// Adapt guard's MaxLeverage based on account capabilities.
	// Safe: we're modifying the per-unit copy, not the shared original.
	if cfg.Account != nil && !cfg.Account.CanShort() {
		guard.Base.MaxLeverage = 1
	}
	if maxLev > 0 && maxLev < guard.Base.MaxLeverage {
		guard.Base.MaxLeverage = maxLev
	}

	// Trade store + Oracle
	var ts tradestore.Store
	if cfg.TradeStore != nil {
		ts = cfg.TradeStore
	} else {
		ts = tradestore.NewMemoryStore()
	}
	oracle := tradestore.NewOracle(ts)
	agg.SetOracle(oracle)

	return &TradingUnit{
		ID:           cfg.ID,
		Account:      cfg.Account,
		Pool:         pool,
		Aggregator:   agg,
		Guard:        guard,
		Sizer:        sizer,
		TradeStore:   ts,
		Oracle:       oracle,
		Logger:       logger.With("unit", cfg.ID),
		Symbols:      cfg.Symbols,
		Timeframe:    tf,
		MaxLeverage:  maxLev,
		Enabled:      true,
		RouteConfig:  cfg.RouteConfig,
		BudgetEquity: cfg.BudgetEquity,
	}
}

// ShouldTrade returns whether this unit should trade the given symbol.
func (u *TradingUnit) ShouldTrade(symbol string) bool {
	if !u.Enabled {
		return false
	}
	// Check route-level symbol filter (from account config).
	if len(u.RouteConfig.AllowedSymbols) > 0 {
		found := false
		for _, s := range u.RouteConfig.AllowedSymbols {
			if s == symbol {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	// Check unit-level symbol list.
	if len(u.Symbols) == 0 {
		return true // trade all
	}
	for _, s := range u.Symbols {
		if s == symbol {
			return true
		}
	}
	return false
}

// Evaluate runs the full pipeline for a single symbol:
// strategies → aggregate → risk check → size → order.
// Returns nil if no trade should be taken.
func (u *TradingUnit) Evaluate(ctx context.Context, view strategy.MarketView) (*TradeDecision, error) {
	if !u.Enabled {
		return nil, nil
	}

	// Check exchange is open
	if !u.Account.Exchange.IsOpen() {
		return nil, nil
	}

	// Fast path: skip full pipeline if THIS unit already has an open trade
	// for this symbol. We check TradeStore (unit-scoped) rather than
	// QueryPositions (exchange-scoped) because multiple units share the
	// same physical exchange account — one unit's position would block
	// all siblings from trading that symbol.
	if u.TradeStore != nil {
		openTrades := u.TradeStore.Query(tradestore.Filter{
			UnitID:   u.ID,
			Symbol:   view.Symbol(),
			OpenOnly: true,
			Limit:    1,
		})
		if len(openTrades) > 0 {
			return nil, nil
		}
	}

	// Run strategies
	signals := u.Pool.Compute(view)

	// Filter signals by allowed strategies (from route config).
	if len(u.RouteConfig.AllowedStrategies) > 0 {
		allowed := make(map[string]bool, len(u.RouteConfig.AllowedStrategies))
		for _, s := range u.RouteConfig.AllowedStrategies {
			allowed[s] = true
		}
		filtered := signals[:0]
		for _, sig := range signals {
			if allowed[sig.Strategy] {
				filtered = append(filtered, sig)
			}
		}
		signals = filtered
	}

	// Build review context from current positions
	review, portfolio, err := u.buildReviewContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("build review context: %w", err)
	}

	// Aggregate signals
	agg := u.Aggregator.Aggregate(view, signals, review)

	if agg.Direction == strategy.DirectionHold {
		return nil, nil
	}

	// Filter: prevent opening opposite direction on same symbol when already
	// holding a position. This avoids wasteful dual-direction hedging in paper trading.
	for _, rp := range portfolio.Positions {
		if rp.Symbol == view.Symbol() && rp.Quantity > 0 && rp.Direction != agg.Direction {
			u.Logger.Debug("skipping opposite-direction signal on symbol with existing position",
				"symbol", view.Symbol(),
				"existing", rp.Direction,
				"signal", agg.Direction)
			return nil, nil
		}
	}

	// Filter: if exchange can't short, skip short signals
	if agg.Direction == strategy.DirectionShort && !u.Account.CanShort() {
		u.Logger.Debug("skipping short signal, exchange does not support shorting",
			"symbol", view.Symbol())
		return nil, nil
	}

	// Size the position
	balance, err := u.Account.Exchange.QueryBalance(ctx)
	if err != nil {
		return nil, fmt.Errorf("query balance: %w", err)
	}

	// Use BudgetEquity (from config initial_equity) if set,
	// so each unit only trades within its configured budget.
	equity := balance.Equity
	if u.BudgetEquity > 0 {
		equity = u.BudgetEquity
	}

	// Query historical stats from Oracle for better sizing
	winRate, avgWin, avgLoss, samples := u.Oracle.StatsForSizer(view.Symbol(), agg.Direction)
	if samples == 0 {
		winRate = 0.5
		avgWin = 1.5
		avgLoss = 1.0
	}

	sizeReq := risk.BayesianSizeRequest{
		AccountEquity: equity,
		Signal:        bestSignal(agg),
		WinRate:       winRate,
		AvgWin:        avgWin,
		AvgLoss:       avgLoss,
		Samples:       samples,
	}
	sized, err := u.Sizer.Size(sizeReq)
	if err != nil {
		return nil, fmt.Errorf("position sizing: %w", err)
	}

	// Apply leverage to position size. The sizer computes margin-based values:
	//   Notional = equity * fraction  (e.g. 80 USDT margin)
	//   Quantity = Notional / price   (margin-sized quantity)
	// With leverage, the actual position is larger:
	//   Leveraged quantity = Quantity * leverage (e.g. 80 * 20 / price)
	// We keep Notional as margin for risk guard checks, but scale up Quantity
	// for the actual order placed on the exchange.
	if u.MaxLeverage > 1 {
		sized.Quantity *= float64(u.MaxLeverage)
	}

	// Adjust SL/TP distances based on leverage.
	// Higher leverage = smaller price move to hit the same % account impact,
	// so we tighten SL/TP proportionally. Base calibration assumes 3x leverage.
	u.adjustSLTPForLeverage(&agg)

	// Build risk check request
	sig := bestSignal(agg)
	if sig.Entry <= 0 {
		u.Logger.Warn("no valid signal with entry price, skipping",
			"symbol", view.Symbol(), "direction", agg.Direction)
		return nil, nil
	}
	// Derive ATR from actual stop distance set by the strategy.
	// The strategy computes SL as entry ± atrDist*slMult, so dividing
	// stopDistance back by slMult gives a reasonable ATR proxy.
	// This avoids the previous fixed 1% estimate that was too large for
	// short timeframes (1m/5m), causing the risk guard to reject valid signals.
	stopDist := math.Abs(sig.Entry - sig.StopLoss)
	estimatedATR := stopDist // conservative: treat stop distance itself as ATR
	if estimatedATR <= 0 {
		estimatedATR = sig.Entry * 0.005
	}

	orderReq := risk.OrderRequest{
		Symbol:        view.Symbol(),
		Action:        risk.ActionOpen,
		Direction:     agg.Direction,
		EntryPrice:    sig.Entry,
		StopLoss:      sig.StopLoss,
		Quantity:      sized.Quantity,
		Notional:      sized.Notional,
		Leverage:      u.MaxLeverage,
		ATR:           estimatedATR,
		AccountEquity: equity,
	}

	// Risk check with adaptive guard
	decision := u.Guard.Evaluate(orderReq, portfolio, risk.CircuitSnapshot{}, view)
	if !decision.Allowed {
		u.Logger.Info("risk guard rejected",
			"symbol", view.Symbol(),
			"direction", agg.Direction,
			"layer", decision.Layer,
			"reason", decision.Reason)
		return nil, nil
	}

	return &TradeDecision{
		UnitID:     u.ID,
		Symbol:     view.Symbol(),
		Signal:     agg,
		SizeResult: sized,
		OrderReq:   orderReq,
		NeedsReview: agg.NeedsReview,
		ReviewReason: agg.ReviewReason,
	}, nil
}

// Execute places the order for a TradeDecision.
func (u *TradingUnit) Execute(ctx context.Context, td *TradeDecision) (*exchange.OrderResult, error) {
	if td == nil {
		return nil, nil
	}

	side := OrderSideBuy
	posSide := PosSideLong
	if td.Signal.Direction == strategy.DirectionShort {
		side = OrderSideSell
		posSide = PosSideShort
	}

	sig := bestSignal(td.Signal)
	params := exchange.PlaceOrderParams{
		Symbol:     td.Symbol,
		Side:       side,
		PosSide:    posSide,
		Type:       "market",
		Price:      sig.Entry,
		Quantity:   td.SizeResult.Quantity,
		StopLoss:   sig.StopLoss,
		TakeProfit: sig.TakeProfit,
		Leverage:   u.MaxLeverage,
		ClientID:   fmt.Sprintf("%s-%s-%d", u.ID, td.Symbol, time.Now().UnixMilli()),
	}

	result, err := u.Account.Exchange.PlaceOrder(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("place order: %w", err)
	}

	u.Logger.Info("order placed",
		"symbol", td.Symbol,
		"direction", td.Signal.Direction,
		"qty", td.SizeResult.Quantity,
		"status", result.Status,
		"orderID", result.OrderID)

	return &result, nil
}

// adjustSLTPForLeverage scales SL/TP distances based on leverage.
// Strategies are calibrated for ~3x leverage. At higher leverage (10x, 20x),
// the same price move causes a larger account impact, so we tighten SL/TP.
// Scale factor = baseLeverage / actualLeverage (e.g. 3/20 = 0.15x distance).
// Clamped to [0.3, 1.0] to avoid extremely tight stops that get noise-swept.
func (u *TradingUnit) adjustSLTPForLeverage(agg *strategy.AggregatedSignal) {
	lev := u.MaxLeverage
	if lev <= 3 {
		return // strategies already calibrated for low leverage
	}
	// Scale: 3x base → at 10x shrink to 0.3, at 20x shrink to 0.15 → clamp 0.3
	scale := 3.0 / float64(lev)
	if scale < 0.3 {
		scale = 0.3
	}

	for i := range agg.Signals {
		sig := &agg.Signals[i]
		if sig.Entry <= 0 || sig.StopLoss <= 0 {
			continue
		}
		slDist := sig.Entry - sig.StopLoss // positive for long
		tpDist := sig.TakeProfit - sig.Entry

		sig.StopLoss = sig.Entry - slDist*scale
		sig.TakeProfit = sig.Entry + tpDist*scale
	}
}

// buildReviewContext queries the exchange for current positions and builds
// the ReviewContext and PortfolioSnapshot needed by aggregator and guard.
func (u *TradingUnit) buildReviewContext(ctx context.Context) (strategy.ReviewContext, risk.PortfolioSnapshot, error) {
	positions, err := u.Account.Exchange.QueryPositions(ctx)
	if err != nil {
		return strategy.ReviewContext{}, risk.PortfolioSnapshot{}, err
	}

	balance, err := u.Account.Exchange.QueryBalance(ctx)
	if err != nil {
		return strategy.ReviewContext{}, risk.PortfolioSnapshot{}, err
	}

	// Use BudgetEquity if configured.
	eqForCalc := balance.Equity
	if u.BudgetEquity > 0 {
		eqForCalc = u.BudgetEquity
	}

	riskPositions := make([]risk.Position, len(positions))
	largestPct := 0.0
	for i, p := range positions {
		// Use MarkPrice for accurate current value; fall back to AvgPrice if unavailable.
		markPrice := p.MarkPrice
		if markPrice <= 0 {
			markPrice = p.AvgPrice
		}
		notional := p.Quantity * markPrice
		pct := 0.0
		if eqForCalc > 0 {
			pct = notional / eqForCalc * 100
		}
		if pct > largestPct {
			largestPct = pct
		}

		dir := strategy.DirectionLong
		if p.Side == "short" {
			dir = strategy.DirectionShort
		}
		riskPositions[i] = risk.Position{
			Symbol:     p.Symbol,
			Direction:  dir,
			Quantity:   p.Quantity,
			Notional:   notional,
			EntryPrice: p.AvgPrice,
			MarkPrice:  p.MarkPrice,
			Leverage:   p.Leverage,
		}
	}

	review := strategy.ReviewContext{
		OpenPositions:      len(positions),
		LargestPositionPct: largestPct,
	}

	portfolio := risk.PortfolioSnapshot{
		Equity:    eqForCalc,
		Positions: riskPositions,
	}

	return review, portfolio, nil
}

// bestSignal picks the highest-confidence signal matching the aggregated direction.
func bestSignal(agg strategy.AggregatedSignal) strategy.Signal {
	var best strategy.Signal
	for _, s := range agg.Signals {
		if s.Direction == agg.Direction && s.Confidence > best.Confidence {
			best = s
		}
	}
	return best
}

// TradeDecision is the output of TradingUnit.Evaluate — everything needed
// to place an order, pending optional human review.
type TradeDecision struct {
	UnitID       string
	Symbol       string
	Signal       strategy.AggregatedSignal
	SizeResult   risk.SizeResult
	OrderReq     risk.OrderRequest
	NeedsReview  bool
	ReviewReason string
}

// Order side/pos side constants (convenience aliases).
const (
	OrderSideBuy   = "buy"
	OrderSideSell  = "sell"
	PosSideLong    = "long"
	PosSideShort   = "short"
)
