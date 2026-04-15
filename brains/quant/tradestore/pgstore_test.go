package tradestore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// Integration test — requires PG_TEST_URL env var.
// Example: PG_TEST_URL="postgres://user:pass@localhost:5432/testdb" go test ./brains/quant/tradestore/...

func pgTestStore(t *testing.T) *PGStore {
	t.Helper()
	url := os.Getenv("PG_TEST_URL")
	if url == "" {
		t.Skip("PG_TEST_URL not set, skipping PGStore integration test")
	}

	ctx := context.Background()
	s, err := NewPGStoreFromURL(ctx, url)
	if err != nil {
		t.Fatalf("NewPGStoreFromURL: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		s.Close()
		t.Fatalf("Migrate: %v", err)
	}

	// Clean up test data before and after
	_, _ = s.pool.Exec(ctx, "DELETE FROM trade_records WHERE unit_id LIKE 'test-%'")
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, "DELETE FROM trade_records WHERE unit_id LIKE 'test-%'")
		s.Close()
	})

	return s
}

func TestPGStore_SaveAndQuery(t *testing.T) {
	s := pgTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond) // PG has microsecond precision

	// Save 3 trades
	records := []TradeRecord{
		{
			ID: "pg-test-1", UnitID: "test-unit-a", Symbol: "BTC-USDT-SWAP",
			Direction: strategy.DirectionLong, EntryPrice: 50000, ExitPrice: 51000,
			Quantity: 0.1, PnL: 100, PnLPct: 0.02,
			EntryTime: now.Add(-2 * time.Hour), ExitTime: now.Add(-time.Hour),
			Reason: "take_profit",
		},
		{
			ID: "pg-test-2", UnitID: "test-unit-a", Symbol: "BTC-USDT-SWAP",
			Direction: strategy.DirectionLong, EntryPrice: 52000, ExitPrice: 51500,
			Quantity: 0.1, PnL: -50, PnLPct: -0.01,
			EntryTime: now.Add(-time.Hour), ExitTime: now,
			Reason: "stop_loss",
		},
		{
			ID: "pg-test-3", UnitID: "test-unit-a", Symbol: "ETH-USDT-SWAP",
			Direction: strategy.DirectionShort, EntryPrice: 3000, ExitPrice: 2800,
			Quantity: 1.0, PnL: 200, PnLPct: 0.067,
			EntryTime: now.Add(-3 * time.Hour), ExitTime: now,
			Reason: "take_profit",
		},
	}

	for _, r := range records {
		if err := s.Save(ctx, r); err != nil {
			t.Fatalf("Save(%s): %v", r.ID, err)
		}
	}

	// Query all for this unit
	all := s.Query(Filter{UnitID: "test-unit-a"})
	if len(all) != 3 {
		t.Fatalf("all = %d, want 3", len(all))
	}

	// Query by symbol
	btc := s.Query(Filter{UnitID: "test-unit-a", Symbol: "BTC-USDT-SWAP"})
	if len(btc) != 2 {
		t.Fatalf("btc = %d, want 2", len(btc))
	}

	// Query by direction
	shorts := s.Query(Filter{UnitID: "test-unit-a", Direction: strategy.DirectionShort})
	if len(shorts) != 1 {
		t.Fatalf("shorts = %d, want 1", len(shorts))
	}

	// Stats
	stats := s.Stats(Filter{UnitID: "test-unit-a", Symbol: "BTC-USDT-SWAP"})
	if stats.TotalTrades != 2 {
		t.Fatalf("total = %d, want 2", stats.TotalTrades)
	}
	if stats.Wins != 1 || stats.Losses != 1 {
		t.Fatalf("wins=%d losses=%d, want 1/1", stats.Wins, stats.Losses)
	}
}

func TestPGStore_Upsert(t *testing.T) {
	s := pgTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)

	// Save entry (no exit yet)
	if err := s.Save(ctx, TradeRecord{
		ID: "pg-test-upsert", UnitID: "test-unit-b", Symbol: "BTC-USDT-SWAP",
		Direction: strategy.DirectionLong, EntryPrice: 50000,
		Quantity: 0.1, EntryTime: now,
	}); err != nil {
		t.Fatalf("Save entry: %v", err)
	}

	// Verify entry saved
	recs := s.Query(Filter{UnitID: "test-unit-b"})
	if len(recs) != 1 {
		t.Fatalf("after entry: got %d records, want 1", len(recs))
	}
	if recs[0].PnL != 0 {
		t.Fatalf("entry PnL = %f, want 0", recs[0].PnL)
	}

	// Update with exit info (upsert)
	if err := s.Save(ctx, TradeRecord{
		ID: "pg-test-upsert", UnitID: "test-unit-b", Symbol: "BTC-USDT-SWAP",
		Direction: strategy.DirectionLong, EntryPrice: 50000, ExitPrice: 51000,
		Quantity: 0.1, PnL: 100, PnLPct: 0.02,
		EntryTime: now, ExitTime: now.Add(time.Hour),
		Reason: "take_profit",
	}); err != nil {
		t.Fatalf("Save exit: %v", err)
	}

	// Verify upsert
	recs = s.Query(Filter{UnitID: "test-unit-b"})
	if len(recs) != 1 {
		t.Fatalf("after upsert: got %d records, want 1", len(recs))
	}
	if recs[0].PnL != 100 {
		t.Fatalf("upserted PnL = %f, want 100", recs[0].PnL)
	}
	if recs[0].Reason != "take_profit" {
		t.Fatalf("upserted reason = %q, want take_profit", recs[0].Reason)
	}
}

func TestPGStore_LoadAll(t *testing.T) {
	s := pgTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)
	for i := 0; i < 5; i++ {
		_ = s.Save(ctx, TradeRecord{
			ID: fmt.Sprintf("pg-test-loadall-%d", i), UnitID: "test-unit-c",
			Symbol: "BTC-USDT-SWAP", Direction: strategy.DirectionLong,
			PnL: float64(i * 10), EntryTime: now.Add(time.Duration(-i) * time.Hour),
		})
	}

	all, err := s.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	// At least our 5 records (may have more from other tests)
	count := 0
	for _, r := range all {
		if r.UnitID == "test-unit-c" {
			count++
		}
	}
	if count != 5 {
		t.Fatalf("LoadAll test-unit-c = %d, want 5", count)
	}
}
