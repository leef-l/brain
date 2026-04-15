package quant

import (
	"context"
	"encoding/json"
	"testing"
)

// TestHandleDataAlert tests the data alert handler (no LLM needed).
func TestHandleDataAlert(t *testing.T) {
	h := NewHandler(nil, nil) // no LLM needed for alerts

	tests := []struct {
		name       string
		level      string
		alertType  string
		wantAction string
	}{
		{"critical_spike", "critical", "price_spike", "risk_pause"},
		{"critical_gap", "critical", "gap", "risk_pause"},
		{"warning_stale", "warning", "stale", "logged"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := DataAlertRequest{
				Level:  tt.level,
				Type:   tt.alertType,
				Symbol: "BTC-USDT-SWAP",
				Detail: "test alert",
			}
			args, _ := json.Marshal(req)
			result, err := h.HandleDataAlert(context.Background(), args)
			if err != nil {
				t.Fatalf("HandleDataAlert: %v", err)
			}

			var resp DataAlertResponse
			if err := json.Unmarshal(result, &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !resp.Received {
				t.Fatal("expected received=true")
			}
			if resp.Action != tt.wantAction {
				t.Fatalf("action = %q, want %q", resp.Action, tt.wantAction)
			}
		})
	}
}

// TestBuildReviewPrompt tests prompt generation.
func TestBuildReviewPrompt(t *testing.T) {
	req := ReviewTradeRequest{
		Signal: SignalInfo{Direction: "long", Confidence: 0.85},
		Portfolio: PortfolioInfo{
			TotalEquity:   10000,
			DailyPnLPct:   -2.5,
			OpenPositions: 3,
			LargestPosPct: 4.2,
		},
		Market: MarketInfo{
			Symbol:        "BTC-USDT-SWAP",
			Price:         50000,
			VolPercentile: 0.75,
			MarketRegime:  "trend",
			FundingRate:   0.0001,
		},
		Reason: "持仓数>=3",
	}

	prompt := buildReviewPrompt(req)
	if len(prompt) < 100 {
		t.Fatalf("prompt too short: %d chars", len(prompt))
	}
	// Check key fields are in the prompt
	for _, want := range []string{"10000.00", "BTC-USDT-SWAP", "long", "0.85", "-2.50", "trend"} {
		if !contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildDailyReviewPrompt(t *testing.T) {
	req := DailyReviewRequest{
		Date:        "2026-04-14",
		TotalTrades: 15,
		TotalPnL:    250.5,
		TotalPnLPct: 2.5,
		Accounts: []AccountStats{
			{ID: "main", Equity: 10000, DailyPnL: 250.5, Trades: 15, WinRate: 0.6},
		},
		StrategyStats: []StrategyStats{
			{Name: "TrendFollower", Signals: 20, Executed: 8, WinRate: 0.625, AvgPnL: 31.3},
		},
	}

	prompt := buildDailyReviewPrompt(req)
	if len(prompt) < 100 {
		t.Fatalf("prompt too short: %d chars", len(prompt))
	}
	for _, want := range []string{"2026-04-14", "250.50", "TrendFollower"} {
		if !contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchSubstring(s, sub)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
