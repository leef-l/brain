package advisor

import (
	"context"
	"strings"
	"testing"

	"github.com/leef-l/brain/brains/quant/backtest"
	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
)

func TestNewLLMAdvisor(t *testing.T) {
	mock := llm.NewMockProvider("mock")
	proxy := &kernel.LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return mock },
	}
	advisor := NewLLMAdvisor(proxy, "test-model")
	if advisor == nil {
		t.Fatal("expected non-nil advisor")
	}
	if advisor.Model != "test-model" {
		t.Errorf("model: got %q, want %q", advisor.Model, "test-model")
	}
	if advisor.PromptTemplate == "" {
		t.Error("expected default prompt template")
	}
}

func TestAdvise(t *testing.T) {
	mock := llm.NewMockProvider("mock")
	mock.QueueText(`{"action":"enter_long","confidence":0.85,"reason":"strong trend","risk_level":"medium","suggested_size":0.5}`)

	proxy := &kernel.LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return mock },
	}

	advisor := NewLLMAdvisor(proxy, "test-model")
	state := MarketState{
		Symbol:  "BTC-USDT",
		Price:   65000,
		Regime:  "trend",
		Signals: []strategy.Signal{
			{Strategy: "trend_follower", Direction: strategy.DirectionLong, Confidence: 0.8, Reason: "EMA cross up"},
		},
		RiskStatus: RiskStatus{
			Exposure:     10000,
			Leverage:     2.0,
			MaxLeverage:  10.0,
			DailyLossPct: 0.01,
			IsPaused:     false,
		},
		PortfolioSnapshot: risk.PortfolioSnapshot{
			Equity: 100000,
			Positions: []risk.Position{
				{Symbol: "BTC-USDT", Direction: strategy.DirectionLong, Quantity: 0.5, Notional: 10000},
			},
		},
	}

	ctx := context.Background()
	advice, err := advisor.Advise(ctx, state)
	if err != nil {
		t.Fatalf("Advise error: %v", err)
	}

	if advice.Action != "enter_long" {
		t.Errorf("action: got %q, want %q", advice.Action, "enter_long")
	}
	if advice.Confidence != 0.85 {
		t.Errorf("confidence: got %f, want %f", advice.Confidence, 0.85)
	}
	if advice.RiskLevel != "medium" {
		t.Errorf("risk_level: got %q, want %q", advice.RiskLevel, "medium")
	}
	if advice.SuggestedSize != 0.5 {
		t.Errorf("suggested_size: got %f, want %f", advice.SuggestedSize, 0.5)
	}
	if advice.Reason != "strong trend" {
		t.Errorf("reason: got %q, want %q", advice.Reason, "strong trend")
	}
}

func TestAdviseWithBacktest(t *testing.T) {
	mock := llm.NewMockProvider("mock")
	mock.QueueText(`{"action":"hold","confidence":0.3,"reason":"poor backtest","risk_level":"high","suggested_size":0.0}`)

	proxy := &kernel.LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return mock },
	}

	advisor := NewLLMAdvisor(proxy, "test-model")
	state := MarketState{
		Symbol: "ETH-USDT",
		Price:  3500,
		Regime: "range",
	}
	bt := &backtest.BacktestResult{
		Symbol:      "ETH-USDT",
		SharpeRatio: 0.5,
		WinRate:     0.3,
		MaxDrawdown: 0.25,
	}

	ctx := context.Background()
	advice, err := advisor.AdviseWithBacktest(ctx, state, bt)
	if err != nil {
		t.Fatalf("AdviseWithBacktest error: %v", err)
	}

	if advice.Action != "hold" {
		t.Errorf("action: got %q, want %q", advice.Action, "hold")
	}
	if advice.Confidence != 0.3 {
		t.Errorf("confidence: got %f, want %f", advice.Confidence, 0.3)
	}
	if advice.RiskLevel != "high" {
		t.Errorf("risk_level: got %q, want %q", advice.RiskLevel, "high")
	}
}

func TestAdviseEmptyResponse(t *testing.T) {
	mock := llm.NewMockProvider("mock")
	// queue empty -> will return error
	proxy := &kernel.LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return mock },
	}
	advisor := NewLLMAdvisor(proxy, "test-model")
	ctx := context.Background()
	_, err := advisor.Advise(ctx, MarketState{})
	if err == nil {
		t.Fatal("expected error for empty response queue")
	}
}

func TestParseAdviceJSON(t *testing.T) {
	text := `Some preamble...
{
  "action": "enter_short",
  "confidence": 0.75,
  "reason": "breakdown",
  "risk_level": "high",
  "suggested_size": 0.25
}`
	advice, err := parseAdvice(text)
	if err != nil {
		t.Fatalf("parseAdvice error: %v", err)
	}
	if advice.Action != "enter_short" {
		t.Errorf("action: got %q, want %q", advice.Action, "enter_short")
	}
	if advice.Confidence != 0.75 {
		t.Errorf("confidence: got %f, want %f", advice.Confidence, 0.75)
	}
	if advice.RiskLevel != "high" {
		t.Errorf("risk_level: got %q, want %q", advice.RiskLevel, "high")
	}
	if advice.SuggestedSize != 0.25 {
		t.Errorf("suggested_size: got %f, want %f", advice.SuggestedSize, 0.25)
	}
}

func TestParseAdviceMarkdownJSON(t *testing.T) {
	text := "```json\n{\"action\":\"reduce\",\"confidence\":0.6,\"reason\":\"too much exposure\",\"risk_level\":\"medium\",\"suggested_size\":0.1}\n```"
	advice, err := parseAdvice(text)
	if err != nil {
		t.Fatalf("parseAdvice error: %v", err)
	}
	if advice.Action != "reduce" {
		t.Errorf("action: got %q, want %q", advice.Action, "reduce")
	}
}

func TestParseAdviceFallback(t *testing.T) {
	text := "The market looks dangerous, I suggest we reduce exposure."
	advice, err := parseAdvice(text)
	if err != nil {
		t.Fatalf("parseAdvice error: %v", err)
	}
	if advice.Action != "reduce" {
		t.Errorf("action: got %q, want %q", advice.Action, "reduce")
	}
	if advice.RiskLevel != "high" {
		t.Errorf("risk_level: got %q, want %q", advice.RiskLevel, "high")
	}
}

func TestParseAdviceFallbackHold(t *testing.T) {
	text := "Conditions are stable, hold position."
	advice, err := parseAdvice(text)
	if err != nil {
		t.Fatalf("parseAdvice error: %v", err)
	}
	if advice.Action != "hold" {
		t.Errorf("action: got %q, want %q", advice.Action, "hold")
	}
	if advice.RiskLevel != "low" {
		t.Errorf("risk_level: got %q, want %q", advice.RiskLevel, "low")
	}
}

func TestNormalizeAdviceBounds(t *testing.T) {
	raw := &struct {
		Action        string  `json:"action"`
		Confidence    float64 `json:"confidence"`
		Reason        string  `json:"reason"`
		RiskLevel     string  `json:"risk_level"`
		SuggestedSize float64 `json:"suggested_size"`
	}{
		Action:        "INVALID",
		Confidence:    1.5,
		Reason:        "",
		RiskLevel:     "EXTREME",
		SuggestedSize: -0.5,
	}
	advice := normalizeAdvice(raw)
	if advice.Action != "hold" {
		t.Errorf("action: got %q, want %q", advice.Action, "hold")
	}
	if advice.Confidence != 1.0 {
		t.Errorf("confidence: got %f, want %f", advice.Confidence, 1.0)
	}
	if advice.RiskLevel != "medium" {
		t.Errorf("risk_level: got %q, want %q", advice.RiskLevel, "medium")
	}
	if advice.SuggestedSize != 0.0 {
		t.Errorf("suggested_size: got %f, want %f", advice.SuggestedSize, 0.0)
	}
	if advice.Reason != "No reason provided." {
		t.Errorf("reason: got %q, want %q", advice.Reason, "No reason provided.")
	}
}

func TestBuildPrompt(t *testing.T) {
	advisor := &LLMAdvisor{PromptTemplate: defaultPromptTemplate}
	state := MarketState{
		Symbol: "BTC-USDT",
		Price:  60000,
		Regime: "breakout",
		Signals: []strategy.Signal{
			{Strategy: "breakout", Direction: strategy.DirectionLong, Confidence: 0.9, Reason: "vol expansion"},
		},
		RiskStatus: RiskStatus{
			Exposure:     5000,
			Leverage:     1.0,
			MaxLeverage:  5.0,
			DailyLossPct: 0.005,
			IsPaused:     false,
		},
		PortfolioSnapshot: risk.PortfolioSnapshot{
			Equity: 50000,
		},
	}

	prompt := advisor.buildPrompt(state, nil)
	if !strings.Contains(prompt, "BTC-USDT") {
		t.Error("prompt missing symbol")
	}
	if !strings.Contains(prompt, "breakout") {
		t.Error("prompt missing regime")
	}
	if !strings.Contains(prompt, "vol expansion") {
		t.Error("prompt missing signal reason")
	}
	if !strings.Contains(prompt, "No backtest data available.") {
		t.Error("prompt missing backtest placeholder")
	}
}

func TestBuildPromptWithBacktest(t *testing.T) {
	advisor := &LLMAdvisor{PromptTemplate: defaultPromptTemplate}
	state := MarketState{Symbol: "ETH", Price: 3000, Regime: "trend"}
	bt := &backtest.BacktestResult{
		Symbol:      "ETH",
		SharpeRatio: 1.2,
		WinRate:     0.55,
		MaxDrawdown: 0.1,
		TotalReturn: 0.25,
		Trades:      make([]backtest.Trade, 10),
	}

	prompt := advisor.buildPrompt(state, bt)
	if !strings.Contains(prompt, "Sharpe: 1.20") {
		t.Error("prompt missing Sharpe ratio")
	}
	if !strings.Contains(prompt, "Trades: 10") {
		t.Error("prompt missing trade count")
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"{\"a\":1}", "{\"a\":1}"},
		{"pre{\"a\":1}post", "{\"a\":1}"},
		{"```json\n{\"a\":1}\n```", "{\"a\":1}"},
		{"```\n{\"a\":1}\n```", "{\"a\":1}"},
		{"no json here", ""},
	}

	for _, c := range cases {
		got := extractJSON(c.input)
		if got != c.expected {
			t.Errorf("extractJSON(%q): got %q, want %q", c.input, got, c.expected)
		}
	}
}
