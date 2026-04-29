package exchange

import (
	"context"
	"testing"
)

func TestPaperBackendName(t *testing.T) {
	pe := NewPaperExchange(PaperConfig{AccountID: "test", InitialEquity: 10000})
	pb := NewPaperBackend(pe)
	if got := pb.Name(); got != "paper" {
		t.Fatalf("expected paper, got %s", got)
	}
}

func TestPaperBackendGetBalance(t *testing.T) {
	pe := NewPaperExchange(PaperConfig{AccountID: "test", InitialEquity: 10000})
	pb := NewPaperBackend(pe)
	bal, err := pb.GetBalance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bal.Total != 10000 {
		t.Fatalf("expected 10000, got %f", bal.Total)
	}
}

func TestPaperBackendPlaceAndGetPosition(t *testing.T) {
	pe := NewPaperExchange(PaperConfig{AccountID: "test", InitialEquity: 10000})
	pb := NewPaperBackend(pe)
	pb.SetTicker(Ticker{Symbol: "BTC-USDT-SWAP", LastPrice: 65000})

	ctx := context.Background()
	_, err := pb.PlaceOrder(ctx, OrderRequest{
		Symbol:   "BTC-USDT-SWAP",
		Side:     "buy",
		Type:     "market",
		Quantity: 0.1,
		Leverage: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	pos, err := pb.GetPosition(ctx, "BTC-USDT-SWAP")
	if err != nil {
		t.Fatal(err)
	}
	if pos.Symbol != "BTC-USDT-SWAP" {
		t.Fatalf("unexpected symbol %s", pos.Symbol)
	}
}

func TestPaperBackendGetAllPositions(t *testing.T) {
	pe := NewPaperExchange(PaperConfig{AccountID: "test", InitialEquity: 10000})
	pb := NewPaperBackend(pe)
	pb.SetTicker(Ticker{Symbol: "BTC-USDT-SWAP", LastPrice: 65000})

	ctx := context.Background()
	_, _ = pb.PlaceOrder(ctx, OrderRequest{Symbol: "BTC-USDT-SWAP", Side: "buy", Type: "market", Quantity: 0.1, Leverage: 10})

	positions, err := pb.GetAllPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(positions))
	}
}

func TestPaperBackendCancelOrder(t *testing.T) {
	pe := NewPaperExchange(PaperConfig{AccountID: "test", InitialEquity: 10000})
	pb := NewPaperBackend(pe)
	pb.SetTicker(Ticker{Symbol: "BTC-USDT-SWAP", LastPrice: 65000})

	ctx := context.Background()
	resp, err := pb.PlaceOrder(ctx, OrderRequest{Symbol: "BTC-USDT-SWAP", Side: "buy", Type: "limit", Quantity: 0.1, Price: 60000})
	if err != nil {
		t.Fatal(err)
	}
	if err := pb.CancelOrder(ctx, resp.OrderID); err != nil {
		t.Fatal(err)
	}
}
