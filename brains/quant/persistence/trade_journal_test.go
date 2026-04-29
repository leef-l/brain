package persistence

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
	sdkpersistence "github.com/leef-l/brain/sdk/persistence"
)

func openTestDB(t *testing.T) *sdkpersistence.ClosableStores {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	cs, err := sdkpersistence.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestSQLiteTradeJournal(t *testing.T) {
	cs := openTestDB(t)
	journal := NewSQLiteTradeJournal(cs.RawDB)

	// Record a winning trade.
	trade1 := TradeRecord{
		ID:         "trade-1",
		Symbol:     "BTCUSDT",
		Direction:  strategy.DirectionLong,
		EntryPrice: 50000,
		ExitPrice:  51000,
		Quantity:   1.0,
		PnL:        1000,
		PnLPct:     0.02,
		EntryTime:  time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		ExitTime:   time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Strategy:   "trend",
		Regime:     "bull",
		Metadata:   map[string]any{"note": "test"},
	}
	if err := journal.RecordTrade(trade1); err != nil {
		t.Fatalf("RecordTrade: %v", err)
	}

	// Record a losing trade.
	trade2 := TradeRecord{
		ID:         "trade-2",
		Symbol:     "BTCUSDT",
		Direction:  strategy.DirectionShort,
		EntryPrice: 51000,
		ExitPrice:  52000,
		Quantity:   0.5,
		PnL:        -500,
		PnLPct:     -0.0196,
		EntryTime:  time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC),
		ExitTime:   time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC),
		Strategy:   "mean_reversion",
		Regime:     "bear",
	}
	if err := journal.RecordTrade(trade2); err != nil {
		t.Fatalf("RecordTrade: %v", err)
	}

	// List all trades.
	trades, err := journal.ListTrades(TradeFilter{})
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(trades))
	}

	// Filter by symbol.
	trades, err = journal.ListTrades(TradeFilter{Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades for BTCUSDT, got %d", len(trades))
	}

	// Filter by strategy.
	trades, err = journal.ListTrades(TradeFilter{Strategy: "trend"})
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if len(trades) != 1 || trades[0].ID != "trade-1" {
		t.Fatalf("expected 1 trend trade, got %+v", trades)
	}

	// Filter by time range.
	trades, err = journal.ListTrades(TradeFilter{
		Since: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if len(trades) != 1 || trades[0].ID != "trade-2" {
		t.Fatalf("expected 1 trade on Jan 2, got %+v", trades)
	}

	// Get stats.
	stats, err := journal.GetTradeStats("BTCUSDT", time.Time{})
	if err != nil {
		t.Fatalf("GetTradeStats: %v", err)
	}
	if stats.TotalTrades != 2 {
		t.Errorf("expected 2 total trades, got %d", stats.TotalTrades)
	}
	if stats.TotalPnL != 500 {
		t.Errorf("expected total PnL 500, got %f", stats.TotalPnL)
	}
	if stats.WinRate != 0.5 {
		t.Errorf("expected win rate 0.5, got %f", stats.WinRate)
	}

	// Update trade (upsert).
	trade1.ExitPrice = 51500
	trade1.PnL = 1500
	trade1.PnLPct = 0.03
	if err := journal.RecordTrade(trade1); err != nil {
		t.Fatalf("RecordTrade update: %v", err)
	}
	stats, err = journal.GetTradeStats("BTCUSDT", time.Time{})
	if err != nil {
		t.Fatalf("GetTradeStats after update: %v", err)
	}
	if stats.TotalPnL != 1000 {
		t.Errorf("expected total PnL 1000 after update, got %f", stats.TotalPnL)
	}

	// Verify metadata round-trip.
	trades, err = journal.ListTrades(TradeFilter{Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	found := false
	for _, tr := range trades {
		if tr.ID == "trade-1" {
			found = true
			if tr.Metadata == nil || tr.Metadata["note"] != "test" {
				t.Errorf("metadata round-trip failed: got %+v", tr.Metadata)
			}
		}
	}
	if !found {
		t.Error("trade-1 not found for metadata check")
	}
}
