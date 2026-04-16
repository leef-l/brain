package exchange

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/leef-l/brain/brains/quant/execution"
	"github.com/leef-l/brain/brains/quant/tradestore"
)

// PaperExchange wraps execution.PaperBackend behind the Exchange interface.
// It simulates a 24/7 crypto-like exchange with configurable capabilities.
type PaperExchange struct {
	client *execution.Client
	caps   Capabilities
	equity float64 // simulated account equity
}

// PaperConfig configures the paper exchange.
type PaperConfig struct {
	InitialEquity float64
	SlippageBps   float64      // 滑点(基点), 0=use default(5)
	FeeBps        float64      // 手续费(基点), 0=use default(4)
	Capabilities  Capabilities // override defaults if needed
}

// NewPaperExchange creates a paper trading exchange.
func NewPaperExchange(cfg PaperConfig) *PaperExchange {
	slippage := cfg.SlippageBps
	if slippage <= 0 {
		slippage = 5 // default 0.05%
	}
	fee := cfg.FeeBps
	if fee <= 0 {
		fee = 4 // default 0.04%
	}
	paper := execution.NewPaperBackend(
		execution.WithPaperSlippageBps(slippage),
		execution.WithPaperFeeBps(fee),
	)
	client := execution.NewClient(paper)

	caps := cfg.Capabilities
	if caps.MaxLeverage == 0 {
		// Default: crypto-like capabilities
		caps = Capabilities{
			CanShort:         true,
			MaxLeverage:      125,
			HasFundingRate:   true,
			HasOrderBook:     false, // paper doesn't simulate order book
			MinOrderSize:     0.001,
			TickSize:         0.01,
			SettlementDays:   0,
			BaseCurrency:     "USDT",
			CrossAssetAnchor: "BTC",
		}
	}

	equity := cfg.InitialEquity
	if equity <= 0 {
		equity = 10000
	}

	return &PaperExchange{
		client: client,
		caps:   caps,
		equity: equity,
	}
}

func (p *PaperExchange) Name() string              { return "paper" }
func (p *PaperExchange) Capabilities() Capabilities { return p.caps }
func (p *PaperExchange) IsOpen() bool               { return true } // always open

func (p *PaperExchange) QueryBalance(_ context.Context) (BalanceInfo, error) {
	snap := p.client.Snapshot()
	unrealized := 0.0
	totalMargin := 0.0
	for _, pos := range snap.Positions {
		unrealized += pos.UnrealizedPnL
		totalMargin += pos.Margin
	}
	equity := p.equity + unrealized
	available := equity - totalMargin
	if available < 0 {
		available = 0
	}
	return BalanceInfo{
		Equity:       equity,
		Available:    available,
		Margin:       totalMargin,
		UnrealizedPL: unrealized,
		Currency:     p.caps.BaseCurrency,
	}, nil
}

func (p *PaperExchange) QueryPositions(_ context.Context) ([]PositionInfo, error) {
	snap := p.client.Snapshot()
	positions := make([]PositionInfo, 0, len(snap.Positions))
	for _, pos := range snap.Positions {
		notional := pos.Quantity * pos.MarkPrice
		positions = append(positions, PositionInfo{
			Symbol:       pos.Symbol,
			Side:         pos.PosSide,
			Quantity:     pos.Quantity,
			AvgPrice:     pos.AvgPrice,
			MarkPrice:    pos.MarkPrice,
			Notional:     notional,
			Margin:       pos.Margin,
			UnrealizedPL: pos.UnrealizedPnL,
			Leverage:     pos.Leverage,
			UpdatedAt:    time.UnixMilli(pos.UpdatedAt),
		})
	}
	return positions, nil
}

func (p *PaperExchange) PlaceOrder(ctx context.Context, params PlaceOrderParams) (OrderResult, error) {
	intent := execution.OrderIntent{
		Symbol:      params.Symbol,
		Side:        params.Side,
		PosSide:     params.PosSide,
		OrderType:   params.Type,
		Leverage:    params.Leverage,
		Quantity:    strconv.FormatFloat(params.Quantity, 'f', -1, 64),
		StopLoss:    floatToStr(params.StopLoss),
		TakeProfit:  floatToStr(params.TakeProfit),
		TimeInForce: params.TimeInForce,
		ClientOrdID: params.ClientID,
		ReduceOnly:  params.ReduceOnly,
		Timestamp:   time.Now().UnixMilli(),
	}
	if params.Price > 0 {
		intent.Price = strconv.FormatFloat(params.Price, 'f', -1, 64)
	}

	result, err := p.client.Execute(ctx, intent)
	if err != nil {
		return OrderResult{Error: err.Error()}, err
	}

	fillQty, _ := strconv.ParseFloat(result.FillQty, 64)
	return OrderResult{
		OrderID:   result.OrderID,
		Status:    result.Status,
		FillPrice: result.FillPrice,
		FillQty:   fillQty,
		Fee:       result.Fee,
		Error:     result.Error,
		Timestamp: time.UnixMilli(result.Timestamp),
	}, nil
}

func (p *PaperExchange) CancelOrder(_ context.Context, _, orderID string) error {
	state := p.client.State()
	if state == nil {
		return fmt.Errorf("no execution state")
	}
	_, err := state.CancelOrder(time.Now().UnixMilli(), orderID)
	return err
}

// CancelOpenOrders cancels all open orders for a symbol. Used after
// signal reversal exit to clean up orphaned SL/TP child orders.
func (p *PaperExchange) CancelOpenOrders(_ context.Context, symbol string) int {
	state := p.client.State()
	if state == nil {
		return 0
	}
	now := time.Now().UnixMilli()
	openOrders := state.ListOpenOrders(symbol)
	cancelled := 0
	for _, o := range openOrders {
		if _, err := state.CancelOrder(now, o.Intent.ID); err == nil {
			cancelled++
		}
	}
	return cancelled
}

// ProcessPriceTick forwards a price update to the paper backend for
// stop-loss / take-profit evaluation.
func (p *PaperExchange) ProcessPriceTick(ctx context.Context, symbol string, price float64) ([]OrderResult, error) {
	results, err := p.client.ProcessPriceTick(ctx, symbol, price)
	if err != nil {
		return nil, err
	}
	out := make([]OrderResult, len(results))
	for i, r := range results {
		fillQty, _ := strconv.ParseFloat(r.FillQty, 64)
		out[i] = OrderResult{
			OrderID:   r.OrderID,
			Status:    r.Status,
			FillPrice: r.FillPrice,
			FillQty:   fillQty,
			Fee:       r.Fee,
			Error:     r.Error,
			Timestamp: time.UnixMilli(r.Timestamp),
		}
	}
	return out, nil
}

// SaveState persists the paper exchange's in-memory positions and open orders
// to PostgreSQL so state survives restarts.
func (p *PaperExchange) SaveState(ctx context.Context, accountID string, store *tradestore.PaperPGStore) error {
	snap := p.client.Snapshot()
	nextID := p.client.State().NextID()

	// Save positions + open orders + ID counter atomically in one transaction.
	if err := store.SaveStateAtomic(ctx, accountID, snap.Positions, snap.Orders, nextID); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Save equity snapshot (separate — non-critical, append-only).
	bal, _ := p.QueryBalance(ctx)
	if err := store.SaveSnapshot(ctx, tradestore.AccountSnapshot{
		AccountID:    accountID,
		Equity:       bal.Equity,
		Available:    bal.Available,
		Margin:       bal.Margin,
		UnrealizedPL: bal.UnrealizedPL,
		Currency:     bal.Currency,
		Positions:    len(snap.Positions),
	}); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}

	return nil
}

// RestoreState loads positions and open orders from PostgreSQL into the
// paper exchange's MemoryState. After restore, the first ProcessPriceTick
// will recalculate unrealized PnL using live market prices.
//
// All data is loaded from PG first; MemoryState is only modified if all
// loads succeed (atomic restore: either everything or nothing).
func (p *PaperExchange) RestoreState(ctx context.Context, accountID string, store *tradestore.PaperPGStore, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	state := p.client.State()
	if state == nil {
		return fmt.Errorf("no execution state")
	}

	// Phase 1: Load all data from PG (read-only, no state mutation).
	positions, err := store.LoadPositions(ctx, accountID)
	if err != nil {
		return fmt.Errorf("load positions: %w", err)
	}

	orders, err := store.LoadOpenOrders(ctx, accountID)
	if err != nil {
		return fmt.Errorf("load open orders: %w", err)
	}

	nextID, err := store.LoadNextID(ctx, accountID)
	if err != nil {
		return fmt.Errorf("load next id: %w", err)
	}

	// Phase 2: Apply all data to MemoryState (only if all loads succeeded).
	state.RestorePositions(positions)
	logger.Info("restored paper positions", "account", accountID, "count", len(positions))

	state.RestoreOrders(orders)
	logger.Info("restored paper orders", "account", accountID, "count", len(orders))

	if nextID > 0 {
		state.SetNextID(nextID)
		logger.Info("restored order ID counter", "account", accountID, "next_id", nextID)
	}

	// Note: MarkPrice, UnrealizedPnL, and Margin will be recalculated on
	// the first ProcessPriceTick with live prices. The values stored in PG
	// are from the last tick before shutdown.

	return nil
}

// Snapshot returns a copy of the paper exchange's execution state.
func (p *PaperExchange) Snapshot() execution.StateSnapshot {
	return p.client.Snapshot()
}

func floatToStr(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
