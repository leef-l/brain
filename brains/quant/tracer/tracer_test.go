package tracer

import (
	"context"
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
)

func TestMemoryStoreSaveAndQuery(t *testing.T) {
	s := NewMemoryStore(0)
	ctx := context.Background()
	now := time.Now()

	traces := []SignalTrace{
		{
			TraceID: "t1", Timestamp: now.Add(-2 * time.Minute), Symbol: "BTC-USDT-SWAP",
			Price: 50000, Outcome: "executed",
			Signals: []strategy.Signal{{Direction: strategy.DirectionLong, Confidence: 0.8}},
			Aggregated: strategy.AggregatedSignal{Direction: strategy.DirectionLong, Confidence: 0.75},
			GlobalRisk: risk.Decision{Allowed: true, Layer: "global"},
		},
		{
			TraceID: "t2", Timestamp: now.Add(-time.Minute), Symbol: "ETH-USDT-SWAP",
			Price: 3000, Outcome: "rejected_risk",
			Signals: []strategy.Signal{{Direction: strategy.DirectionShort, Confidence: 0.6}},
			GlobalRisk: risk.Decision{Allowed: false, Layer: "global", Reason: "too much exposure"},
		},
		{
			TraceID: "t3", Timestamp: now, Symbol: "BTC-USDT-SWAP",
			Price: 50100, Outcome: "hold",
		},
	}

	for i := range traces {
		if err := s.Save(ctx, &traces[i]); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	// Query all
	all, err := s.Query(ctx, TraceFilter{})
	if err != nil {
		t.Fatalf("Query all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all = %d, want 3", len(all))
	}
	// Newest first
	if all[0].TraceID != "t3" {
		t.Fatalf("first = %s, want t3", all[0].TraceID)
	}

	// Query by symbol
	btc, _ := s.Query(ctx, TraceFilter{Symbol: "BTC-USDT-SWAP"})
	if len(btc) != 2 {
		t.Fatalf("btc = %d, want 2", len(btc))
	}

	// Query by outcome
	executed, _ := s.Query(ctx, TraceFilter{Outcome: "executed"})
	if len(executed) != 1 {
		t.Fatalf("executed = %d, want 1", len(executed))
	}

	// Query with limit
	limited, _ := s.Query(ctx, TraceFilter{Limit: 1})
	if len(limited) != 1 {
		t.Fatalf("limited = %d, want 1", len(limited))
	}

	// Count
	count, _ := s.Count(ctx)
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
}

func TestMemoryStoreEviction(t *testing.T) {
	s := NewMemoryStore(2) // max 2 traces
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = s.Save(ctx, &SignalTrace{
			TraceID:   string(rune('a' + i)),
			Timestamp: time.Now(),
			Symbol:    "BTC",
		})
	}

	count, _ := s.Count(ctx)
	if count != 2 {
		t.Fatalf("count = %d, want 2 (eviction)", count)
	}
}
