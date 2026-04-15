package tradestore

import (
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

func TestMemoryStore(t *testing.T) {
	store := NewMemoryStore()

	// Save some trades
	now := time.Now()
	store.Save(TradeRecord{
		ID: "1", UnitID: "unit-a", Symbol: "BTC-USDT-SWAP",
		Direction: strategy.DirectionLong, PnL: 100, PnLPct: 0.05,
		EntryTime: now.Add(-time.Hour), ExitTime: now,
	})
	store.Save(TradeRecord{
		ID: "2", UnitID: "unit-a", Symbol: "BTC-USDT-SWAP",
		Direction: strategy.DirectionLong, PnL: -50, PnLPct: -0.025,
		EntryTime: now.Add(-2 * time.Hour), ExitTime: now.Add(-time.Hour),
	})
	store.Save(TradeRecord{
		ID: "3", UnitID: "unit-a", Symbol: "ETH-USDT-SWAP",
		Direction: strategy.DirectionShort, PnL: 200, PnLPct: 0.10,
		EntryTime: now.Add(-3 * time.Hour), ExitTime: now,
	})

	// Query all
	all := store.Query(Filter{})
	if len(all) != 3 {
		t.Fatalf("all = %d, want 3", len(all))
	}

	// Query by symbol
	btc := store.Query(Filter{Symbol: "BTC-USDT-SWAP"})
	if len(btc) != 2 {
		t.Fatalf("btc = %d, want 2", len(btc))
	}

	// Query by direction
	longs := store.Query(Filter{Direction: strategy.DirectionLong})
	if len(longs) != 2 {
		t.Fatalf("longs = %d, want 2", len(longs))
	}

	// Stats
	stats := store.Stats(Filter{Symbol: "BTC-USDT-SWAP"})
	if stats.TotalTrades != 2 {
		t.Fatalf("total = %d, want 2", stats.TotalTrades)
	}
	if stats.Wins != 1 || stats.Losses != 1 {
		t.Fatalf("wins=%d losses=%d, want 1/1", stats.Wins, stats.Losses)
	}
	if stats.WinRate != 0.5 {
		t.Fatalf("winRate = %.2f, want 0.50", stats.WinRate)
	}
	if stats.AvgWin != 100 {
		t.Fatalf("avgWin = %.2f, want 100", stats.AvgWin)
	}
	if stats.AvgLoss != 50 {
		t.Fatalf("avgLoss = %.2f, want 50", stats.AvgLoss)
	}
}

func TestOracle(t *testing.T) {
	store := NewMemoryStore()
	oracle := NewOracle(store)

	// Not enough trades
	_, ok := oracle.HistoricalWinRate("BTC-USDT-SWAP", strategy.DirectionLong, nil)
	if ok {
		t.Fatal("should return false with < 5 trades")
	}

	// Add 10 trades: 7 wins, 3 losses
	now := time.Now()
	for i := 0; i < 10; i++ {
		pnl := 100.0
		if i < 3 {
			pnl = -50
		}
		store.Save(TradeRecord{
			Symbol:    "BTC-USDT-SWAP",
			Direction: strategy.DirectionLong,
			PnL:       pnl,
			ExitTime:  now,
		})
	}

	winRate, ok := oracle.HistoricalWinRate("BTC-USDT-SWAP", strategy.DirectionLong, nil)
	if !ok {
		t.Fatal("should return true with 10 trades")
	}
	if winRate != 0.7 {
		t.Fatalf("winRate = %.2f, want 0.70", winRate)
	}

	// StatsForSizer
	wr, avgWin, avgLoss, samples := oracle.StatsForSizer("BTC-USDT-SWAP", strategy.DirectionLong)
	if samples != 10 {
		t.Fatalf("samples = %d, want 10", samples)
	}
	if wr != 0.7 {
		t.Fatalf("wr = %.2f, want 0.7", wr)
	}
	if avgWin != 100 {
		t.Fatalf("avgWin = %.2f, want 100", avgWin)
	}
	if avgLoss != 50 {
		t.Fatalf("avgLoss = %.2f, want 50", avgLoss)
	}
}
