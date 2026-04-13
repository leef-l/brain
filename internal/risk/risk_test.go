package risk

import (
	"testing"
	"time"

	"github.com/leef-l/brain/internal/strategy"
)

func TestGuardRejectsTooMuchLeverage(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckOrder(OrderRequest{
		Symbol:     "BTC-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   100,
		Leverage:   25,
		ATR:        3,
	}, PortfolioSnapshot{Equity: 1000})
	if decision.Allowed {
		t.Fatalf("decision allowed, want reject")
	}
}

func TestGuardPortfolioCorrelatedSymbol(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckPortfolio(OrderRequest{
		Symbol:     "ETH-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 200,
		StopLoss:   190,
		Notional:   50,
		Leverage:   5,
		ATR:        5,
	}, PortfolioSnapshot{
		Equity: 1000,
		Positions: []Position{
			{Symbol: "BTC-USDT-SWAP", Direction: strategy.DirectionLong, Notional: 100},
		},
		CorrelatedGroups: map[string][]string{
			"majors": []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP"},
		},
	})
	if decision.Allowed {
		t.Fatalf("decision allowed, want reject")
	}
}

func TestGuardCircuitPause(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckCircuitBreaker(CircuitSnapshot{
		VolatilityPercentile: 99.1,
	})
	if decision.Allowed {
		t.Fatalf("decision allowed, want pause")
	}
	if decision.Action != "pause" {
		t.Fatalf("action = %s, want pause", decision.Action)
	}
}

func TestPositionSizer(t *testing.T) {
	sizer := DefaultPositionSizer()
	result, err := sizer.Size(SizeRequest{
		AccountEquity: 10000,
		Signal: strategy.Signal{
			Entry: 100,
		},
		WinRate: 0.55,
		AvgWin:  2.0,
		AvgLoss: 1.0,
	})
	if err != nil {
		t.Fatalf("Size() error: %v", err)
	}
	if result.RiskFraction < 0.005 || result.RiskFraction > 0.05 {
		t.Fatalf("risk fraction = %.4f out of bounds", result.RiskFraction)
	}
	if result.Quantity <= 0 {
		t.Fatalf("quantity = %.4f, want positive", result.Quantity)
	}
}

func TestGuardEvaluatePasses(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.Evaluate(OrderRequest{
		Symbol:     "SOL-USDT-SWAP",
		Action:     ActionOpen,
		Direction:  strategy.DirectionLong,
		EntryPrice: 100,
		StopLoss:   95,
		Notional:   40,
		Leverage:   3,
		ATR:        3,
	}, PortfolioSnapshot{
		Equity:    1000,
		Positions: nil,
	}, CircuitSnapshot{})
	if !decision.Allowed {
		t.Fatalf("decision rejected: %s (%s)", decision.Reason, decision.Layer)
	}
}

func TestGuardCircuitMemoryAlert(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckCircuitBreaker(CircuitSnapshot{
		MemoryGB: 40,
	})
	if decision.Action != "alert" {
		t.Fatalf("action = %s, want alert", decision.Action)
	}
	if decision.Allowed {
		t.Fatalf("decision allowed, want alert rejection")
	}
}

func TestGuardPauseDuration(t *testing.T) {
	guard := DefaultGuard()
	decision := guard.CheckCircuitBreaker(CircuitSnapshot{ExecutorFailureStreak: 3})
	if decision.PauseFor != 10*time.Minute {
		t.Fatalf("pause = %s, want 10m", decision.PauseFor)
	}
}
