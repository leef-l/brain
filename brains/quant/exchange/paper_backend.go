package exchange

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PaperBackend implements ExchangeBackend using the existing paper trading engine.
type PaperBackend struct {
	inner   *PaperExchange
	mu      sync.RWMutex
	tickers map[string]Ticker
}

// NewPaperBackend creates a PaperBackend wrapping a PaperExchange.
func NewPaperBackend(inner *PaperExchange) *PaperBackend {
	return &PaperBackend{
		inner:   inner,
		tickers: make(map[string]Ticker),
	}
}

// Name returns the backend identifier.
func (p *PaperBackend) Name() string { return "paper" }

// GetBalance returns the current paper account balance.
func (p *PaperBackend) GetBalance(ctx context.Context) (Balance, error) {
	bal, err := p.inner.QueryBalance(ctx)
	if err != nil {
		return Balance{}, err
	}
	return Balance{
		Asset:     bal.Currency,
		Total:     bal.Equity,
		Available: bal.Available,
		Frozen:    bal.Margin,
		UpdatedAt: time.Now(),
	}, nil
}

// GetPosition returns a single position by symbol.
func (p *PaperBackend) GetPosition(ctx context.Context, symbol string) (Position, error) {
	positions, err := p.inner.QueryPositions(ctx)
	if err != nil {
		return Position{}, err
	}
	for _, pos := range positions {
		if pos.Symbol == symbol {
			return Position{
				Symbol:        pos.Symbol,
				Side:          pos.Side,
				Quantity:      pos.Quantity,
				AvgPrice:      pos.AvgPrice,
				MarkPrice:     pos.MarkPrice,
				UnrealizedPnL: pos.UnrealizedPL,
				Leverage:      pos.Leverage,
				UpdatedAt:     pos.UpdatedAt,
			}, nil
		}
	}
	return Position{}, fmt.Errorf("position not found for %s", symbol)
}

// GetAllPositions returns all open positions.
func (p *PaperBackend) GetAllPositions(ctx context.Context) ([]Position, error) {
	positions, err := p.inner.QueryPositions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Position, len(positions))
	for i, pos := range positions {
		out[i] = Position{
			Symbol:        pos.Symbol,
			Side:          pos.Side,
			Quantity:      pos.Quantity,
			AvgPrice:      pos.AvgPrice,
			MarkPrice:     pos.MarkPrice,
			UnrealizedPnL: pos.UnrealizedPL,
			Leverage:      pos.Leverage,
			UpdatedAt:     pos.UpdatedAt,
		}
	}
	return out, nil
}

// PlaceOrder routes an order through the paper execution engine.
func (p *PaperBackend) PlaceOrder(ctx context.Context, req OrderRequest) (OrderResponse, error) {
	price := req.Price
	if (req.Type == "market" || req.Type == "") && price == 0 {
		if t, err := p.GetTicker(ctx, req.Symbol); err == nil {
			price = t.LastPrice
		}
	}

	params := PlaceOrderParams{
		Symbol:      req.Symbol,
		Side:        req.Side,
		PosSide:     inferPosSide(req),
		Type:        req.Type,
		Quantity:    req.Quantity,
		Price:       price,
		StopLoss:    req.StopLoss,
		TakeProfit:  req.TakeProfit,
		Leverage:    req.Leverage,
		ReduceOnly:  req.ReduceOnly,
		ClientID:    req.ClientID,
	}
	result, err := p.inner.PlaceOrder(ctx, params)
	if err != nil {
		return OrderResponse{}, err
	}
	fillQty := result.FillQty
	return OrderResponse{
		OrderID:   result.OrderID,
		Status:    result.Status,
		FillPrice: result.FillPrice,
		FillQty:   fillQty,
		Fee:       result.Fee,
		Error:     result.Error,
		Timestamp: result.Timestamp,
	}, nil
}

// CancelOrder cancels an open order.
func (p *PaperBackend) CancelOrder(ctx context.Context, orderID string) error {
	return p.inner.CancelOrder(ctx, "", orderID)
}

// GetTicker returns the latest ticker for a symbol.
func (p *PaperBackend) GetTicker(_ context.Context, symbol string) (Ticker, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	t, ok := p.tickers[symbol]
	if !ok {
		return Ticker{}, fmt.Errorf("ticker not found for %s", symbol)
	}
	return t, nil
}

// SetTicker sets a ticker price for the paper backend (useful for tests).
func (p *PaperBackend) SetTicker(t Ticker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tickers[t.Symbol] = t
}

func inferPosSide(req OrderRequest) string {
	if req.ReduceOnly {
		// Closing position: opposite of trade side
		if req.Side == "buy" {
			return "short"
		}
		return "long"
	}
	if req.Side == "buy" {
		return "long"
	}
	return "short"
}


