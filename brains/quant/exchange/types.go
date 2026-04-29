package exchange

import (
	"context"
	"time"
)

// Balance holds account balance for a single asset.
type Balance struct {
	Asset     string
	Total     float64
	Available float64
	Frozen    float64
	UpdatedAt time.Time
}

// Position holds a single open position.
type Position struct {
	Symbol        string
	Side          string // "long" / "short"
	Quantity      float64
	AvgPrice      float64
	MarkPrice     float64
	UnrealizedPnL float64
	Leverage      int
	UpdatedAt     time.Time
}

// OrderRequest is the venue-neutral order request for ExchangeBackend.
type OrderRequest struct {
	Symbol     string
	Side       string // "buy" / "sell"
	Type       string // "market" / "limit"
	Quantity   float64
	Price      float64 // 0 for market orders
	StopLoss   float64
	TakeProfit float64
	Leverage   int
	ReduceOnly bool
	ClientID   string
}

// OrderResponse is the exchange's response to an order placement.
type OrderResponse struct {
	OrderID   string
	Status    string // "filled", "open", "rejected", "cancelled"
	FillPrice float64
	FillQty   float64
	Fee       float64
	Error     string
	Timestamp time.Time
}

// Ticker holds the latest market data for a symbol.
type Ticker struct {
	Symbol    string
	LastPrice float64
	Bid       float64
	Ask       float64
	Volume24h float64
	Timestamp time.Time
}

// ExchangeBackend is the simplified backend interface for live trading (Q-14~Q-16).
type ExchangeBackend interface {
	Name() string
	GetBalance(ctx context.Context) (Balance, error)
	GetPosition(ctx context.Context, symbol string) (Position, error)
	PlaceOrder(ctx context.Context, req OrderRequest) (OrderResponse, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetTicker(ctx context.Context, symbol string) (Ticker, error)
}
