package learning

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
	"github.com/leef-l/brain/brains/quant/tradestore"
)

func makeTrades(n int, winRate float64) []tradestore.TradeRecord {
	wins := int(float64(n) * winRate)
	var trades []tradestore.TradeRecord
	for i := 0; i < n; i++ {
		pnl := -0.5
		reason := "stop_loss"
		if i < wins {
			pnl = 0.8
			reason = "take_profit"
		}
		trades = append(trades, tradestore.TradeRecord{
			ID:        "t-" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Symbol:    "BTC-USDT-SWAP",
			Direction: strategy.DirectionLong,
			PnL:       pnl,
			EntryTime: time.Now().Add(-time.Duration(n-i) * time.Hour),
			ExitTime:  time.Now().Add(-time.Duration(n-i-1) * time.Hour),
			Reason:    reason,
		})
	}
	return trades
}

func TestWeightAdapter_InsufficientSamples(t *testing.T) {
	wa := NewWeightAdapter(WeightAdapterConfig{MinSamples: 30}, nil)
	store := tradestore.NewMemoryStore()

	// Add only 10 trades — below MinSamples.
	for _, tr := range makeTrades(10, 0.5) {
		_ = store.Save(context.Background(), tr)
	}

	wa.Update(context.Background(), store)

	// Weights should remain at base.
	weights := wa.Weights()
	if weights["TrendFollower"] != 0.30 {
		t.Errorf("expected base weight 0.30, got %.4f", weights["TrendFollower"])
	}
}

func TestWeightAdapter_HighWinRate(t *testing.T) {
	wa := NewWeightAdapter(WeightAdapterConfig{
		MinSamples: 10,
		WindowSize: 100,
	}, nil)
	store := tradestore.NewMemoryStore()

	// 70% win rate.
	for _, tr := range makeTrades(50, 0.70) {
		_ = store.Save(context.Background(), tr)
	}

	wa.Update(context.Background(), store)
	weights := wa.Weights()

	// With 70% win rate, multiplier = 0.5 + 0.70 = 1.20.
	// All strategies get the same multiplier (uniform trades),
	// so weights should stay roughly equal to base after normalization.
	total := 0.0
	for _, w := range weights {
		total += w
	}
	if abs(total-1.0) > 0.01 {
		t.Errorf("weights should sum to 1.0, got %.4f", total)
	}
	t.Logf("weights after 70%% win rate: %v", weights)
}

func TestWeightAdapter_LowWinRate(t *testing.T) {
	wa := NewWeightAdapter(WeightAdapterConfig{
		MinSamples: 10,
		WindowSize: 100,
	}, nil)
	store := tradestore.NewMemoryStore()

	// 20% win rate.
	for _, tr := range makeTrades(50, 0.20) {
		_ = store.Save(context.Background(), tr)
	}

	wa.Update(context.Background(), store)
	weights := wa.Weights()

	// With uniform trades, all strategies get same treatment.
	// Just verify normalization.
	total := 0.0
	for _, w := range weights {
		total += w
	}
	if abs(total-1.0) > 0.01 {
		t.Errorf("weights should sum to 1.0, got %.4f", total)
	}
	t.Logf("weights after 20%% win rate: %v", weights)
}

func TestSymbolScorer_BasicScoring(t *testing.T) {
	ss := NewSymbolScorer(SymbolScorerConfig{
		WindowDays: 30,
		MinTrades:  3,
	}, nil)
	store := tradestore.NewMemoryStore()

	// BTC: 8 trades, 6 wins = 75% win rate.
	for i := 0; i < 8; i++ {
		pnl := -0.5
		reason := "stop_loss"
		if i < 6 {
			pnl = 1.0
			reason = "take_profit"
		}
		_ = store.Save(context.Background(), tradestore.TradeRecord{
			ID:        "btc-" + string(rune('A'+i)),
			Symbol:    "BTC-USDT-SWAP",
			Direction: strategy.DirectionLong,
			PnL:       pnl,
			EntryTime: time.Now().Add(-time.Duration(8-i) * time.Hour),
			ExitTime:  time.Now().Add(-time.Duration(7-i) * time.Hour),
			Reason:    reason,
		})
	}

	// ETH: 5 trades, 1 win = 20% win rate.
	for i := 0; i < 5; i++ {
		pnl := -0.5
		reason := "stop_loss"
		if i == 0 {
			pnl = 0.3
			reason = "take_profit"
		}
		_ = store.Save(context.Background(), tradestore.TradeRecord{
			ID:        "eth-" + string(rune('A'+i)),
			Symbol:    "ETH-USDT-SWAP",
			Direction: strategy.DirectionShort,
			PnL:       pnl,
			EntryTime: time.Now().Add(-time.Duration(5-i) * time.Hour),
			ExitTime:  time.Now().Add(-time.Duration(4-i) * time.Hour),
			Reason:    reason,
		})
	}

	ss.Update(store)

	btcScore := ss.Score("BTC-USDT-SWAP")
	ethScore := ss.Score("ETH-USDT-SWAP")
	unknownScore := ss.Score("DOGE-USDT-SWAP")

	t.Logf("BTC score: %.4f, ETH score: %.4f, unknown: %.4f", btcScore, ethScore, unknownScore)

	if btcScore <= ethScore {
		t.Errorf("BTC (75%% wr) should score higher than ETH (20%% wr): BTC=%.4f ETH=%.4f", btcScore, ethScore)
	}
	if unknownScore != 0.5 {
		t.Errorf("unknown symbol should get neutral 0.5, got %.4f", unknownScore)
	}
}

func TestSymbolScorer_BelowMinTrades(t *testing.T) {
	ss := NewSymbolScorer(SymbolScorerConfig{MinTrades: 10}, nil)
	store := tradestore.NewMemoryStore()

	// Only 3 trades — below MinTrades.
	for i := 0; i < 3; i++ {
		_ = store.Save(context.Background(), tradestore.TradeRecord{
			ID:        "sol-" + string(rune('A'+i)),
			Symbol:    "SOL-USDT-SWAP",
			Direction: strategy.DirectionLong,
			PnL:       1.0,
			EntryTime: time.Now().Add(-time.Duration(3-i) * time.Hour),
			ExitTime:  time.Now().Add(-time.Duration(2-i) * time.Hour),
			Reason:    "take_profit",
		})
	}

	ss.Update(store)

	// Should still be neutral since below MinTrades.
	score := ss.Score("SOL-USDT-SWAP")
	if score != 0.5 {
		t.Errorf("below MinTrades should get neutral 0.5, got %.4f", score)
	}
}

func TestSLTPOptimizer_InsufficientSamples(t *testing.T) {
	opt := NewSLTPOptimizer(SLTPOptimizerConfig{MinSamples: 30}, nil)
	store := tradestore.NewMemoryStore()

	// Only 5 trades — below MinSamples.
	for i := 0; i < 5; i++ {
		_ = store.Save(context.Background(), tradestore.TradeRecord{
			ID:         "sltp-" + string(rune('A'+i)),
			Symbol:     "BTC-USDT-SWAP",
			Direction:  strategy.DirectionLong,
			EntryPrice: 50000,
			ExitPrice:  50500,
			PnL:        0.5,
			EntryTime:  time.Now().Add(-time.Duration(5-i) * time.Hour),
			ExitTime:   time.Now().Add(-time.Duration(4-i) * time.Hour),
			Reason:     "take_profit",
			MAE:        100, // 0.2% of entry
			MFE:        500, // 1.0% of entry
		})
	}

	opt.Update(store)
	rec := opt.Global()
	if rec.StopLossATR != 0 {
		t.Errorf("expected 0 (no recommendation) with insufficient samples, got %.2f", rec.StopLossATR)
	}
	if rec.Confidence != 0 {
		t.Errorf("expected 0 confidence, got %.2f", rec.Confidence)
	}
}

func TestSLTPOptimizer_BasicRecommendation(t *testing.T) {
	opt := NewSLTPOptimizer(SLTPOptimizerConfig{
		MinSamples: 10,
		WindowDays: 30,
	}, nil)
	store := tradestore.NewMemoryStore()

	// Create 40 trades with varying MAE/MFE.
	for i := 0; i < 40; i++ {
		entry := 50000.0
		// MAE: 0.1% to 1.5% of entry (50 to 750 absolute)
		mae := entry * (0.001 + float64(i)*0.00035)
		// MFE: 0.2% to 3.0% of entry (100 to 1500 absolute)
		mfe := entry * (0.002 + float64(i)*0.0007)

		pnl := 0.5
		reason := "take_profit"
		if i%3 == 0 {
			pnl = -0.3
			reason = "stop_loss"
		}

		_ = store.Save(context.Background(), tradestore.TradeRecord{
			ID:         fmt.Sprintf("sltp-%d", i),
			Symbol:     "BTC-USDT-SWAP",
			Direction:  strategy.DirectionLong,
			EntryPrice: entry,
			ExitPrice:  entry + pnl*1000,
			Quantity:   0.001,
			PnL:        pnl,
			EntryTime:  time.Now().Add(-time.Duration(40-i) * time.Hour),
			ExitTime:   time.Now().Add(-time.Duration(39-i) * time.Hour),
			Reason:     reason,
			MAE:        mae,
			MFE:        mfe,
		})
	}

	opt.Update(store)
	rec := opt.Global()

	t.Logf("SL ATR: %.2f, TP ATR: %.2f, Trailing: %v, Confidence: %.2f",
		rec.StopLossATR, rec.TakeProfitATR, rec.UseTrailing, rec.Confidence)

	if rec.StopLossATR <= 0 {
		t.Error("expected positive SL ATR recommendation")
	}
	if rec.TakeProfitATR <= 0 {
		t.Error("expected positive TP ATR recommendation")
	}
	if rec.TakeProfitATR < rec.StopLossATR*1.2 {
		t.Errorf("TP (%.2f) should be >= 1.2x SL (%.2f)", rec.TakeProfitATR, rec.StopLossATR)
	}
	if rec.Confidence <= 0 {
		t.Error("expected positive confidence with 40 samples")
	}

	// Per-symbol should also exist.
	symRec := opt.ForSymbol("BTC-USDT-SWAP")
	if symRec.StopLossATR <= 0 {
		t.Error("expected per-symbol recommendation for BTC")
	}

	// Unknown symbol should fall back to global.
	unknownRec := opt.ForSymbol("DOGE-USDT-SWAP")
	if unknownRec.StopLossATR != rec.StopLossATR {
		t.Error("unknown symbol should fall back to global recommendation")
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
