package risk

import (
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

func TestAnomalyGuardNoAnomaly(t *testing.T) {
	ag := DefaultAnomalyGuard()
	score := strategy.AnomalyScore{Price: 0.5, Volume: 0.5, OrderBook: 0.5, Combined: 0.5}
	decision := ag.Check(score, ActionOpen)
	if !decision.Allowed {
		t.Fatalf("expected allow, got: %s", decision.Reason)
	}
	if decision.Layer != "anomaly" {
		t.Fatalf("layer = %s, want anomaly", decision.Layer)
	}
}

func TestAnomalyGuardPriceCriticalBlocksOpen(t *testing.T) {
	ag := DefaultAnomalyGuard()
	score := strategy.AnomalyScore{Price: 0.85}
	decision := ag.Check(score, ActionOpen)
	if decision.Allowed {
		t.Fatal("expected block for price anomaly + open")
	}
	if decision.Reason == "" {
		t.Fatal("expected reason")
	}
}

func TestAnomalyGuardPriceCriticalAllowsClose(t *testing.T) {
	ag := DefaultAnomalyGuard()
	score := strategy.AnomalyScore{Price: 0.85}
	decision := ag.Check(score, ActionClose)
	if !decision.Allowed {
		t.Fatalf("expected allow for close, got: %s", decision.Reason)
	}
}

func TestAnomalyGuardVolumeWarning(t *testing.T) {
	ag := DefaultAnomalyGuard()
	score := strategy.AnomalyScore{Volume: 0.9}
	decision := ag.Check(score, ActionOpen)
	if !decision.Allowed {
		t.Fatal("expected allow with reduce warning")
	}
	if decision.Action != "reduce" {
		t.Fatalf("action = %s, want reduce", decision.Action)
	}
}

func TestAnomalyGuardVolumeAllowsClose(t *testing.T) {
	ag := DefaultAnomalyGuard()
	score := strategy.AnomalyScore{Volume: 0.9}
	decision := ag.Check(score, ActionClose)
	if !decision.Allowed {
		t.Fatal("expected allow for close")
	}
}

func TestAnomalyGuardOrderBookPause(t *testing.T) {
	ag := DefaultAnomalyGuard()
	score := strategy.AnomalyScore{OrderBook: 0.95}
	decision := ag.Check(score, ActionOpen)
	if decision.Allowed {
		t.Fatal("expected pause for orderbook anomaly")
	}
	if decision.Action != "pause" {
		t.Fatalf("action = %s, want pause", decision.Action)
	}
	if decision.PauseFor != 5*time.Minute {
		t.Fatalf("pause = %s, want 5m", decision.PauseFor)
	}
}

func TestAnomalyGuardCombinedConfirm(t *testing.T) {
	ag := DefaultAnomalyGuard()
	score := strategy.AnomalyScore{Combined: 0.8}
	decision := ag.Check(score, ActionOpen)
	if !decision.Allowed {
		t.Fatal("expected allow with confirm flag")
	}
	if decision.Action != "confirm" {
		t.Fatalf("action = %s, want confirm", decision.Action)
	}
}

func TestAnomalyGuardPriorityOrderBookOverPrice(t *testing.T) {
	ag := DefaultAnomalyGuard()
	// Both price and orderbook cross thresholds; orderbook is more severe.
	score := strategy.AnomalyScore{Price: 0.9, OrderBook: 0.95}
	decision := ag.Check(score, ActionOpen)
	if decision.Action != "pause" {
		t.Fatalf("action = %s, want pause (orderbook beats price)", decision.Action)
	}
}

func TestAnomalyGuardIsAnomalous(t *testing.T) {
	ag := DefaultAnomalyGuard()
	tests := []struct {
		score strategy.AnomalyScore
		want  bool
	}{
		{strategy.AnomalyScore{Price: 0.9}, true},
		{strategy.AnomalyScore{Volume: 0.9}, true},
		{strategy.AnomalyScore{OrderBook: 0.95}, true},
		{strategy.AnomalyScore{Combined: 0.8}, true},
		{strategy.AnomalyScore{Price: 0.7, Volume: 0.8, Combined: 0.7}, false},
	}
	for _, tt := range tests {
		anomalous, reason := ag.IsAnomalous(tt.score)
		if anomalous != tt.want {
			t.Fatalf("score %+v: anomalous = %v, want %v (reason: %s)", tt.score, anomalous, tt.want, reason)
		}
	}
}

func TestAnomalyGuardSymbolPause(t *testing.T) {
	ag := DefaultAnomalyGuard()
	if ag.IsSymbolPaused("BTC-USDT-SWAP") {
		t.Fatal("expected no pause initially")
	}

	ag.SetSymbolPause("BTC-USDT-SWAP", 5*time.Minute)
	if !ag.IsSymbolPaused("BTC-USDT-SWAP") {
		t.Fatal("expected pause after setting")
	}

	// Different symbol should not be paused
	if ag.IsSymbolPaused("ETH-USDT-SWAP") {
		t.Fatal("expected no pause for different symbol")
	}
}

func TestAnomalyGuardSymbolPauseExpiry(t *testing.T) {
	ag := DefaultAnomalyGuard()
	ag.SetSymbolPause("BTC-USDT-SWAP", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if ag.IsSymbolPaused("BTC-USDT-SWAP") {
		t.Fatal("expected pause to expire")
	}
}

// ── Guard.CheckOrder integration tests ───────────────────────────

func TestGuardCheckOrderWithAnomalyScore(t *testing.T) {
	guard := DefaultGuard()
	guard.Anomaly = DefaultAnomalyGuard()

	decision := guard.CheckOrder(OrderRequest{
		Symbol:     "BTC-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   40,
		Leverage:   3,
		ATR:        3,
		AnomalyScore: &strategy.AnomalyScore{Price: 0.85},
	}, PortfolioSnapshot{Equity: 1000})

	if decision.Allowed {
		t.Fatal("expected block due to price anomaly")
	}
}

func TestGuardCheckOrderSymbolPaused(t *testing.T) {
	guard := DefaultGuard()
	guard.Anomaly = DefaultAnomalyGuard()
	guard.Anomaly.SetSymbolPause("BTC-USDT-SWAP", 5*time.Minute)

	decision := guard.CheckOrder(OrderRequest{
		Symbol:     "BTC-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   40,
		Leverage:   3,
		ATR:        3,
	}, PortfolioSnapshot{Equity: 1000})

	if decision.Allowed {
		t.Fatal("expected block because symbol is paused")
	}
}

func TestGuardCheckOrderAnomalyOrderBookSetsPause(t *testing.T) {
	guard := DefaultGuard()
	guard.Anomaly = DefaultAnomalyGuard()

	decision := guard.CheckOrder(OrderRequest{
		Symbol:     "BTC-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   40,
		Leverage:   3,
		ATR:        3,
		AnomalyScore: &strategy.AnomalyScore{OrderBook: 0.95},
	}, PortfolioSnapshot{Equity: 1000})

	if decision.Allowed {
		t.Fatal("expected block due to orderbook anomaly")
	}
	if decision.Action != "pause" {
		t.Fatalf("action = %s, want pause", decision.Action)
	}
	if !guard.Anomaly.IsSymbolPaused("BTC-USDT-SWAP") {
		t.Fatal("expected symbol to be paused after orderbook anomaly")
	}
}

func TestGuardCheckOrderNoAnomalyScorePasses(t *testing.T) {
	guard := DefaultGuard()
	guard.Anomaly = DefaultAnomalyGuard()

	decision := guard.CheckOrder(OrderRequest{
		Symbol:     "SOL-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   40,
		Leverage:   3,
		ATR:        3,
	}, PortfolioSnapshot{Equity: 1000})

	if !decision.Allowed {
		t.Fatalf("expected allow, got: %s", decision.Reason)
	}
}
