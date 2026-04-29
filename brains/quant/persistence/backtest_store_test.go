package persistence

import (
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/backtest"
	"github.com/leef-l/brain/brains/quant/strategy"
)

func TestSQLiteBacktestStore(t *testing.T) {
	cs := openTestDB(t)
	store := NewSQLiteBacktestStore(cs.RawDB)

	result := BacktestResult{
		Symbol:       "ETHUSDT",
		Timeframe:    "1h",
		StartTime:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC),
		Bars:         720,
		TotalReturn:  0.15,
		WinRate:      0.55,
		ProfitFactor: 1.8,
		MaxDrawdown:  0.05,
		SharpeRatio:  1.2,
		CalmarRatio:  3.0,
		EquityCurve:  []float64{10000, 10100, 10050, 11500},
		Trades: []backtest.Trade{
			{EntryBar: 1, ExitBar: 5, Direction: strategy.DirectionLong, EntryPrice: 2000, ExitPrice: 2100, PnL: 100, PnLPct: 0.05, EntryTime: 1704067200, ExitTime: 1704412800},
			{EntryBar: 10, ExitBar: 15, Direction: strategy.DirectionShort, EntryPrice: 2100, ExitPrice: 2050, PnL: 50, PnLPct: 0.0238, EntryTime: 1704672000, ExitTime: 1705104000},
		},
	}

	// Save.
	id, err := store.SaveResult(result)
	if err != nil {
		t.Fatalf("SaveResult: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}

	// Get.
	got, err := store.GetResult(id)
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if got.Symbol != result.Symbol {
		t.Errorf("symbol mismatch: want %s got %s", result.Symbol, got.Symbol)
	}
	if got.TotalReturn != result.TotalReturn {
		t.Errorf("total_return mismatch: want %f got %f", result.TotalReturn, got.TotalReturn)
	}
	if got.Bars != result.Bars {
		t.Errorf("bars mismatch: want %d got %d", result.Bars, got.Bars)
	}
	if len(got.EquityCurve) != len(result.EquityCurve) {
		t.Errorf("equity_curve length mismatch: want %d got %d", len(result.EquityCurve), len(got.EquityCurve))
	}
	if len(got.Trades) != len(result.Trades) {
		t.Errorf("trades length mismatch: want %d got %d", len(result.Trades), len(got.Trades))
	}
	if got.TradesCount != len(result.Trades) {
		t.Errorf("trades_count mismatch: want %d got %d", len(result.Trades), got.TradesCount)
	}

	// List.
	summaries, err := store.ListResults("ETHUSDT", 10)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Symbol != "ETHUSDT" {
		t.Errorf("expected ETHUSDT, got %s", summaries[0].Symbol)
	}

	// List with wrong symbol.
	summaries, err = store.ListResults("BTCUSDT", 10)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("expected 0 summaries for BTCUSDT, got %d", len(summaries))
	}

	// Limit.
	summaries, err = store.ListResults("", 0)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary with no filter, got %d", len(summaries))
	}

	// Delete.
	if err := store.DeleteResult(id); err != nil {
		t.Fatalf("DeleteResult: %v", err)
	}
	_, err = store.GetResult(id)
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestSQLiteBacktestStoreUpsert(t *testing.T) {
	cs := openTestDB(t)
	store := NewSQLiteBacktestStore(cs.RawDB)

	result := BacktestResult{
		ID:          "bt-up-1",
		Symbol:      "BTCUSDT",
		Timeframe:   "4h",
		StartTime:   time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC),
		Bars:        180,
		TotalReturn: 0.10,
		WinRate:     0.50,
		EquityCurve: []float64{1000, 1100},
	}

	id, err := store.SaveResult(result)
	if err != nil {
		t.Fatalf("SaveResult: %v", err)
	}
	if id != result.ID {
		t.Fatalf("expected id %s, got %s", result.ID, id)
	}

	// Upsert with new values.
	result.TotalReturn = 0.20
	result.WinRate = 0.60
	result.EquityCurve = []float64{1000, 1200}
	id2, err := store.SaveResult(result)
	if err != nil {
		t.Fatalf("SaveResult upsert: %v", err)
	}
	if id2 != id {
		t.Fatalf("expected same id %s, got %s", id, id2)
	}

	got, err := store.GetResult(id)
	if err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if got.TotalReturn != 0.20 {
		t.Errorf("expected total_return 0.20, got %f", got.TotalReturn)
	}
	if got.WinRate != 0.60 {
		t.Errorf("expected win_rate 0.60, got %f", got.WinRate)
	}
	if len(got.EquityCurve) != 2 || got.EquityCurve[1] != 1200 {
		t.Errorf("expected equity_curve [1000 1200], got %v", got.EquityCurve)
	}
}
