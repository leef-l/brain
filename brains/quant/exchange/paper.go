package exchange

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/leef-l/brain/brains/quant/execution"
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
	Capabilities  Capabilities // override defaults if needed
}

// NewPaperExchange creates a paper trading exchange.
func NewPaperExchange(cfg PaperConfig) *PaperExchange {
	paper := execution.NewPaperBackend(
		execution.WithPaperSlippageBps(5),
		execution.WithPaperFeeBps(4),
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
	for _, pos := range snap.Positions {
		unrealized += pos.UnrealizedPnL
	}
	return BalanceInfo{
		Equity:       p.equity + unrealized,
		Available:    p.equity,
		UnrealizedPL: unrealized,
		Currency:     p.caps.BaseCurrency,
	}, nil
}

func (p *PaperExchange) QueryPositions(_ context.Context) ([]PositionInfo, error) {
	snap := p.client.Snapshot()
	positions := make([]PositionInfo, 0, len(snap.Positions))
	for _, pos := range snap.Positions {
		positions = append(positions, PositionInfo{
			Symbol:       pos.Symbol,
			Side:         pos.PosSide,
			Quantity:     pos.Quantity,
			AvgPrice:     pos.AvgPrice,
			UnrealizedPL: pos.UnrealizedPnL,
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

func floatToStr(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
