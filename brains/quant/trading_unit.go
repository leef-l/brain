package quant

import (
	"context"
	"fmt"
	"log/slog"
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
		ID:          cfg.ID,
		Account:     cfg.Account,
		Pool:        pool,
		Aggregator:  agg,
		Guard:       guard,
		Sizer:       sizer,
		TradeStore:  ts,
		Oracle:      oracle,
		Logger:      logger.With("unit", cfg.ID),
		Symbols:     cfg.Symbols,
		Timeframe:   tf,
		MaxLeverage: maxLev,
		Enabled:     true,
		RouteConfig: cfg.RouteConfig,
	}
}

// ShouldTrade returns whether this unit should trade the given symbol.
func (u *TradingUnit) ShouldTrade(symbol string) bool {
	if !u.Enabled {
		return false
	}
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

	// Run strategies
	signals := u.Pool.Compute(view)

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

	// Query historical stats from Oracle for better sizing
	winRate, avgWin, avgLoss, samples := u.Oracle.StatsForSizer(view.Symbol(), agg.Direction)
	if samples == 0 {
		winRate = 0.5
		avgWin = 1.5
		avgLoss = 1.0
	}

	sizeReq := risk.BayesianSizeRequest{
		AccountEquity: balance.Equity,
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

	// Build risk check request
	sig := bestSignal(agg)
	orderReq := risk.OrderRequest{
		Symbol:        view.Symbol(),
		Action:        risk.ActionOpen,
		Direction:     agg.Direction,
		EntryPrice:    sig.Entry,
		StopLoss:      sig.StopLoss,
		Quantity:      sized.Quantity,
		Notional:      sized.Notional,
		Leverage:      u.MaxLeverage,
		ATR:           sig.Entry * 0.01, // rough ATR estimate from entry
		AccountEquity: balance.Equity,
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
		Symbol:   td.Symbol,
		Side:     side,
		PosSide:  posSide,
		Type:     "market",
		Quantity: td.SizeResult.Quantity,
		StopLoss: sig.StopLoss,
		TakeProfit: sig.TakeProfit,
		Leverage: u.MaxLeverage,
		ClientID: fmt.Sprintf("%s-%s-%d", u.ID, td.Symbol, time.Now().UnixMilli()),
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

	riskPositions := make([]risk.Position, len(positions))
	largestPct := 0.0
	for i, p := range positions {
		notional := p.Quantity * p.AvgPrice
		pct := 0.0
		if balance.Equity > 0 {
			pct = notional / balance.Equity * 100
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
		Equity:    balance.Equity,
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
