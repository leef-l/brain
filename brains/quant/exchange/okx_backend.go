package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
)

// OKXBackendConfig holds credentials and settings for the OKX backend.
type OKXBackendConfig struct {
	APIKey     string
	APISecret  string
	Passphrase string
	BaseURL    string // default: https://www.okx.com
}

// OKXBackend implements ExchangeBackend for OKX with simulated data.
// In a production scenario the REST methods would perform real HTTP calls.
type OKXBackend struct {
	cfg       OKXBackendConfig
	guard     *risk.Guard
	portfolio func() risk.PortfolioSnapshot

	mu        sync.RWMutex
	balances  map[string]Balance
	positions map[string]Position
	orders    map[string]OrderResponse
	tickers   map[string]Ticker
	orderSeq  int64
	apiCalls  int64
	apiErrors int64
}

// NewOKXBackend creates a new simulated OKX backend.
func NewOKXBackend(cfg OKXBackendConfig, guard *risk.Guard, portfolio func() risk.PortfolioSnapshot) *OKXBackend {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://www.okx.com"
	}
	if guard == nil {
		g := risk.DefaultGuard()
		guard = &g
	}
	if portfolio == nil {
		portfolio = func() risk.PortfolioSnapshot { return risk.PortfolioSnapshot{} }
	}
	b := &OKXBackend{
		cfg:       cfg,
		guard:     guard,
		portfolio: portfolio,
		balances: map[string]Balance{
			"USDT": {Asset: "USDT", Total: 100000, Available: 100000, Frozen: 0, UpdatedAt: time.Now()},
		},
		positions: make(map[string]Position),
		orders:    make(map[string]OrderResponse),
		tickers: map[string]Ticker{
			"BTC-USDT-SWAP": {Symbol: "BTC-USDT-SWAP", LastPrice: 65000, Bid: 64999, Ask: 65001, Volume24h: 1e9, Timestamp: time.Now()},
			"ETH-USDT-SWAP": {Symbol: "ETH-USDT-SWAP", LastPrice: 3500, Bid: 3499.5, Ask: 3500.5, Volume24h: 5e8, Timestamp: time.Now()},
		},
	}
	return b
}

// Name returns the backend identifier.
func (b *OKXBackend) Name() string { return "okx" }

// GetBalance returns the USDT balance (simulated).
func (b *OKXBackend) GetBalance(_ context.Context) (Balance, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	usdt, ok := b.balances["USDT"]
	if !ok {
		return Balance{}, fmt.Errorf("balance not found")
	}
	return usdt, nil
}

// GetPosition returns a single position by symbol.
func (b *OKXBackend) GetPosition(_ context.Context, symbol string) (Position, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	pos, ok := b.positions[symbol]
	if !ok {
		return Position{}, fmt.Errorf("position not found for %s", symbol)
	}
	return pos, nil
}

// GetAllPositions returns all open positions.
func (b *OKXBackend) GetAllPositions(_ context.Context) ([]Position, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Position, 0, len(b.positions))
	for _, p := range b.positions {
		out = append(out, p)
	}
	return out, nil
}

// PlaceOrder performs a risk check then simulates an order fill.
func (b *OKXBackend) PlaceOrder(ctx context.Context, req OrderRequest) (OrderResponse, error) {
	atomic.AddInt64(&b.apiCalls, 1)

	// ── Risk check ──
	portfolio := b.portfolio()
	riskReq := b.toRiskOrderRequest(req, portfolio)
	decision := b.guard.CheckOrder(riskReq, portfolio)
	if !decision.Allowed {
		resp := OrderResponse{
			OrderID:   b.nextOrderID(),
			Status:    "rejected",
			Error:     fmt.Sprintf("risk: %s", decision.Reason),
			Timestamp: time.Now(),
		}
		b.mu.Lock()
		b.orders[resp.OrderID] = resp
		b.mu.Unlock()
		return resp, nil
	}

	// ── Resolve price ──
	ticker, err := b.GetTicker(ctx, req.Symbol)
	if err != nil {
		atomic.AddInt64(&b.apiErrors, 1)
		return OrderResponse{}, err
	}

	fillPrice := req.Price
	if req.Type == "market" || fillPrice == 0 {
		fillPrice = ticker.LastPrice
	}
	// Simulate slight slippage (±0.05%)
	fillPrice = fillPrice * (1 + (rand.Float64()-0.5)*0.001)

	fee := fillPrice * req.Quantity * 0.0004
	resp := OrderResponse{
		OrderID:   b.nextOrderID(),
		Status:    "filled",
		FillPrice: fillPrice,
		FillQty:   req.Quantity,
		Fee:       fee,
		Timestamp: time.Now(),
	}

	b.mu.Lock()
	b.orders[resp.OrderID] = resp

	// Update position
	pos := b.positions[req.Symbol]
	pos.Symbol = req.Symbol
	pos.Side = b.inferSide(req)
	pos.Quantity = req.Quantity
	pos.AvgPrice = fillPrice
	pos.MarkPrice = ticker.LastPrice
	if pos.AvgPrice > 0 {
		pos.UnrealizedPnL = (pos.MarkPrice - pos.AvgPrice) * pos.Quantity
		if pos.Side == "short" {
			pos.UnrealizedPnL = -pos.UnrealizedPnL
		}
	}
	pos.Leverage = req.Leverage
	if pos.Leverage == 0 {
		pos.Leverage = 1
	}
	pos.UpdatedAt = time.Now()
	b.positions[req.Symbol] = pos

	// Deduct fee from balance
	bal := b.balances["USDT"]
	bal.Total -= fee
	bal.Available -= fee
	bal.UpdatedAt = time.Now()
	b.balances["USDT"] = bal

	b.mu.Unlock()

	return resp, nil
}

// CancelOrder cancels a pending order.
func (b *OKXBackend) CancelOrder(_ context.Context, orderID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.orders[orderID]
	if !ok {
		return fmt.Errorf("order %s not found", orderID)
	}
	if o.Status == "filled" {
		return fmt.Errorf("order %s already filled", orderID)
	}
	o.Status = "cancelled"
	b.orders[orderID] = o
	return nil
}

// GetTicker returns the latest ticker for a symbol.
func (b *OKXBackend) GetTicker(_ context.Context, symbol string) (Ticker, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	t, ok := b.tickers[symbol]
	if !ok {
		// Return a default ticker for unknown symbols
		t = Ticker{
			Symbol:    symbol,
			LastPrice: 100 + rand.Float64()*100,
			Bid:       100 + rand.Float64()*100,
			Ask:       101 + rand.Float64()*100,
			Volume24h: 1e6,
			Timestamp: time.Now(),
		}
	}
	return t, nil
}

// SetTicker updates the ticker for a symbol (useful for tests).
func (b *OKXBackend) SetTicker(t Ticker) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tickers[t.Symbol] = t
}

// APIErrorRate returns the current API error rate.
func (b *OKXBackend) APIErrorRate() float64 {
	calls := atomic.LoadInt64(&b.apiCalls)
	if calls == 0 {
		return 0
	}
	return float64(atomic.LoadInt64(&b.apiErrors)) / float64(calls)
}

// RecordAPIError increments the API error counter.
func (b *OKXBackend) RecordAPIError() {
	atomic.AddInt64(&b.apiErrors, 1)
}

func (b *OKXBackend) nextOrderID() string {
	seq := atomic.AddInt64(&b.orderSeq, 1)
	return fmt.Sprintf("okx-%d-%d", time.Now().UnixMilli(), seq)
}

func (b *OKXBackend) inferSide(req OrderRequest) string {
	if req.ReduceOnly {
		return "flat"
	}
	if req.Side == "buy" {
		return "long"
	}
	return "short"
}

func (b *OKXBackend) toRiskOrderRequest(req OrderRequest, portfolio risk.PortfolioSnapshot) risk.OrderRequest {
	entryPrice := req.Price
	if entryPrice == 0 {
		t, _ := b.GetTicker(context.Background(), req.Symbol)
		entryPrice = t.LastPrice
	}
	notional := entryPrice * req.Quantity

	action := risk.ActionOpen
	if req.ReduceOnly {
		action = risk.ActionReduce
	}

	direction := strategy.DirectionLong
	if req.Side == "sell" {
		direction = strategy.DirectionShort
	}

	return risk.OrderRequest{
		Symbol:        req.Symbol,
		Action:        action,
		Direction:     direction,
		EntryPrice:    entryPrice,
		StopLoss:      req.StopLoss,
		Quantity:      req.Quantity,
		Notional:      notional,
		Leverage:      req.Leverage,
		ATR:           0,
		AccountEquity: portfolio.Equity,
	}
}

// Sign generates an OKX-compatible HMAC signature.
func (b *OKXBackend) Sign(method, path, body string) string {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	preSign := ts + method + path + body
	h := hmac.New(sha256.New, []byte(b.cfg.APISecret))
	h.Write([]byte(preSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
