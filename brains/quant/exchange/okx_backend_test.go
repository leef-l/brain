package exchange

import (
	"context"
	"testing"

	"github.com/leef-l/brain/brains/quant/risk"
)

func TestOKXBackendName(t *testing.T) {
	b := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	if got := b.Name(); got != "okx" {
		t.Fatalf("expected okx, got %s", got)
	}
}

func TestOKXBackendGetBalance(t *testing.T) {
	b := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	bal, err := b.GetBalance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bal.Asset != "USDT" || bal.Total <= 0 {
		t.Fatalf("unexpected balance: %+v", bal)
	}
}

func TestOKXBackendPlaceOrderRiskRejection(t *testing.T) {
	guard := risk.Guard{
		MaxSinglePositionPct: 0.1, // very restrictive
	}
	portfolio := func() risk.PortfolioSnapshot {
		return risk.PortfolioSnapshot{Equity: 100000}
	}
	b := NewOKXBackend(OKXBackendConfig{}, &guard, portfolio)
	resp, err := b.PlaceOrder(context.Background(), OrderRequest{
		Symbol:   "BTC-USDT-SWAP",
		Side:     "buy",
		Type:     "market",
		Quantity: 10,
		StopLoss: 60000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "rejected" {
		t.Fatalf("expected rejected due to risk, got %s", resp.Status)
	}
}

func TestOKXBackendPlaceOrderSuccess(t *testing.T) {
	b := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	resp, err := b.PlaceOrder(context.Background(), OrderRequest{
		Symbol:   "BTC-USDT-SWAP",
		Side:     "buy",
		Type:     "market",
		Quantity: 0.1,
		Leverage: 10,
		StopLoss: 60000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "filled" {
		t.Fatalf("expected filled, got %s", resp.Status)
	}
	if resp.FillPrice <= 0 {
		t.Fatal("expected positive fill price")
	}
}

func TestOKXBackendGetPosition(t *testing.T) {
	b := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	ctx := context.Background()
	_, _ = b.PlaceOrder(ctx, OrderRequest{Symbol: "BTC-USDT-SWAP", Side: "buy", Type: "market", Quantity: 0.1, StopLoss: 60000})

	pos, err := b.GetPosition(ctx, "BTC-USDT-SWAP")
	if err != nil {
		t.Fatal(err)
	}
	if pos.Symbol != "BTC-USDT-SWAP" {
		t.Fatalf("unexpected symbol %s", pos.Symbol)
	}
}

func TestOKXBackendCancelOrder(t *testing.T) {
	b := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	ctx := context.Background()
	resp, _ := b.PlaceOrder(ctx, OrderRequest{Symbol: "BTC-USDT-SWAP", Side: "buy", Type: "market", Quantity: 0.1})

	if err := b.CancelOrder(ctx, resp.OrderID); err != nil {
		t.Fatal(err)
	}
	// Cancelling unknown order should error
	if err := b.CancelOrder(ctx, "unknown"); err == nil {
		t.Fatal("expected error for unknown order")
	}
}

func TestOKXBackendGetTicker(t *testing.T) {
	b := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	tick, err := b.GetTicker(context.Background(), "BTC-USDT-SWAP")
	if err != nil {
		t.Fatal(err)
	}
	if tick.LastPrice <= 0 {
		t.Fatal("expected positive last price")
	}
}

func TestOKXBackendSign(t *testing.T) {
	b := NewOKXBackend(OKXBackendConfig{APISecret: "secret"}, nil, nil)
	sig := b.Sign("GET", "/api/v5/account/balance", "")
	if sig == "" {
		t.Fatal("expected non-empty signature")
	}
}
