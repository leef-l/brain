package quant

import (
	"context"
	"log/slog"
	"time"

	"github.com/leef-l/brain/brains/quant/adapter"
	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
	"github.com/leef-l/brain/brains/quant/tracer"
	"github.com/leef-l/brain/brains/quant/tradestore"
)

// AccountRouter distributes a single aggregated signal to all eligible
// TradingUnits. Instead of each unit independently evaluating strategies,
// the router runs one shared evaluation, then dispatches the result to
// each unit for per-account risk check, sizing, and execution.
//
// This replaces the previous per-unit independent evaluation pattern
// and ensures consistent signal handling across accounts.
type AccountRouter struct {
	logger *slog.Logger
}

// NewAccountRouter creates a new router.
func NewAccountRouter(logger *slog.Logger) *AccountRouter {
	if logger == nil {
		logger = slog.Default()
	}
	return &AccountRouter{logger: logger}
}

// RouteConfig controls per-account routing behavior.
type RouteConfig struct {
	// WeightFactor scales position size for this account.
	// 1.0 = standard, 0.5 = half size. Default: 1.0.
	WeightFactor float64 `json:"weight_factor" yaml:"weight_factor"`

	// AllowedStrategies restricts which strategies this account uses.
	// Empty = all strategies allowed.
	AllowedStrategies []string `json:"allowed_strategies,omitempty" yaml:"allowed_strategies,omitempty"`

	// AllowedSymbols restricts which symbols this account trades.
	// Empty = all symbols allowed.
	AllowedSymbols []string `json:"allowed_symbols,omitempty" yaml:"allowed_symbols,omitempty"`
}

// DefaultRouteConfig returns a standard route config.
func DefaultRouteConfig() RouteConfig {
	return RouteConfig{WeightFactor: 1.0}
}

// DispatchResult records one account's handling of a dispatched signal.
type DispatchResult struct {
	UnitID    string
	AccountID string
	Executed  bool
	OrderID   string
	Quantity  float64
	Reason    string // why skipped/rejected
	Latency   time.Duration
}

// Dispatch sends a pre-computed AggregatedSignal to a set of TradingUnits.
// Each unit independently sizes and executes based on its own account/risk config.
//
// Parameters:
//   - signal: the aggregated signal from shared strategy evaluation
//   - view: the market snapshot view
//   - units: all eligible TradingUnits to dispatch to
//   - globalGuard: cross-account risk check
//   - globalSnap: aggregated cross-account snapshot
//
// Returns per-account trace results for the signal audit trail.
func (r *AccountRouter) Dispatch(
	ctx context.Context,
	signal strategy.AggregatedSignal,
	view *adapter.SnapshotView,
	units []*TradingUnit,
	globalGuard *risk.GlobalRiskGuard,
	globalSnap risk.GlobalSnapshot,
) []DispatchResult {
	var results []DispatchResult

	for _, unit := range units {
		if !unit.Enabled {
			continue
		}
		if !unit.ShouldTrade(view.Symbol()) {
			continue
		}

		// Check if account supports this direction
		if signal.Direction == strategy.DirectionShort && !unit.Account.CanShort() {
			results = append(results, DispatchResult{
				UnitID:    unit.ID,
				AccountID: unit.Account.ID,
				Reason:    "exchange does not support shorting",
			})
			continue
		}

		start := time.Now()
		dr := r.dispatchToUnit(ctx, signal, view, unit, globalGuard, globalSnap)
		dr.Latency = time.Since(start)
		results = append(results, dr)
	}

	return results
}

// dispatchToUnit handles one unit: balance query → size → risk → execute.
func (r *AccountRouter) dispatchToUnit(
	ctx context.Context,
	signal strategy.AggregatedSignal,
	view *adapter.SnapshotView,
	unit *TradingUnit,
	globalGuard *risk.GlobalRiskGuard,
	globalSnap risk.GlobalSnapshot,
) DispatchResult {
	dr := DispatchResult{
		UnitID:    unit.ID,
		AccountID: unit.Account.ID,
	}

	// Query balance
	balance, err := unit.Account.Exchange.QueryBalance(ctx)
	if err != nil {
		dr.Reason = "balance query failed: " + err.Error()
		return dr
	}

	// Query Oracle stats for sizing
	winRate, avgWin, avgLoss, samples := unit.Oracle.StatsForSizer(view.Symbol(), signal.Direction)
	if samples == 0 {
		winRate = 0.5
		avgWin = 1.5
		avgLoss = 1.0
	}

	sig := bestSignalFromAgg(signal)
	if sig.Entry <= 0 {
		dr.Reason = "no valid signal with entry price"
		return dr
	}

	sizeReq := risk.BayesianSizeRequest{
		AccountEquity: balance.Equity,
		Signal:        sig,
		WinRate:       winRate,
		AvgWin:        avgWin,
		AvgLoss:       avgLoss,
		Samples:       samples,
	}
	sized, err := unit.Sizer.Size(sizeReq)
	if err != nil {
		dr.Reason = "sizing failed: " + err.Error()
		return dr
	}

	// Apply WeightFactor from route config
	if unit.RouteConfig.WeightFactor > 0 && unit.RouteConfig.WeightFactor != 1.0 {
		sized.Quantity *= unit.RouteConfig.WeightFactor
		sized.Notional *= unit.RouteConfig.WeightFactor
	}

	if sized.Quantity <= 0 || sized.Notional <= 0 {
		dr.Reason = "position size zero after weight factor"
		return dr
	}

	// Build risk order request
	orderReq := risk.OrderRequest{
		Symbol:        view.Symbol(),
		Action:        risk.ActionOpen,
		Direction:     signal.Direction,
		EntryPrice:    sig.Entry,
		StopLoss:      sig.StopLoss,
		Quantity:      sized.Quantity,
		Notional:      sized.Notional,
		Leverage:      unit.MaxLeverage,
		ATR:           sig.Entry * 0.01,
		AccountEquity: balance.Equity,
	}

	// Per-unit risk check
	review, portfolio, err := unit.buildReviewContext(ctx)
	_ = review
	if err != nil {
		dr.Reason = "review context failed: " + err.Error()
		return dr
	}
	decision := unit.Guard.Evaluate(orderReq, portfolio, risk.CircuitSnapshot{}, view)
	if !decision.Allowed {
		dr.Reason = "unit risk rejected: " + decision.Reason
		return dr
	}

	// Global risk check
	if globalGuard != nil {
		gd := globalGuard.Evaluate(orderReq, globalSnap)
		if !gd.Allowed {
			dr.Reason = "global risk rejected: " + gd.Reason
			return dr
		}
	}

	// Execute
	td := &TradeDecision{
		UnitID:     unit.ID,
		Symbol:     view.Symbol(),
		Signal:     signal,
		SizeResult: sized,
		OrderReq:   orderReq,
	}
	result, err := unit.Execute(ctx, td)
	if err != nil {
		dr.Reason = "execution failed: " + err.Error()
		return dr
	}

	if result != nil && result.Status == "filled" {
		dr.Executed = true
		dr.OrderID = result.OrderID
		dr.Quantity = sized.Quantity

		// Persist trade record
		if err := unit.TradeStore.Save(ctx, tradestore.TradeRecord{
			ID:         result.OrderID,
			AccountID:  unit.Account.ID,
			UnitID:     unit.ID,
			Symbol:     view.Symbol(),
			Direction:  signal.Direction,
			EntryPrice: sig.Entry,
			Quantity:   sized.Quantity,
			EntryTime:  result.Timestamp,
		}); err != nil {
			r.logger.Error("trade store save failed",
				"unit", unit.ID,
				"orderID", result.OrderID,
				"err", err)
		}

		r.logger.Info("routed order filled",
			"unit", unit.ID,
			"symbol", view.Symbol(),
			"direction", signal.Direction,
			"qty", sized.Quantity)
	}

	return dr
}

// ToTraceResults converts DispatchResults to tracer.AccountTraceResult for audit.
func ToTraceResults(results []DispatchResult) []tracer.AccountTraceResult {
	out := make([]tracer.AccountTraceResult, len(results))
	for i, r := range results {
		status := "skipped"
		if r.Executed {
			status = "filled"
		} else if r.Reason != "" {
			status = "rejected"
		}
		out[i] = tracer.AccountTraceResult{
			AccountID: r.AccountID,
			UnitID:    r.UnitID,
			OrderID:   r.OrderID,
			Status:    status,
			Quantity:  r.Quantity,
			Latency:   r.Latency,
		}
	}
	return out
}
