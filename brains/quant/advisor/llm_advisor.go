package advisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leef-l/brain/brains/quant/backtest"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
)

// Advice is the structured trading recommendation returned by LLMAdvisor.
type Advice struct {
	Action        string  // "hold", "enter_long", "enter_short", "reduce", "close"
	Confidence    float64 // 0~1
	Reason        string
	RiskLevel     string  // "low", "medium", "high"
	SuggestedSize float64 // 建议仓位比例
}

// LLMAdvisor provides trading recommendations via an LLM proxy.
type LLMAdvisor struct {
	Proxy          *kernel.LLMProxy
	Model          string
	PromptTemplate string
}

// NewLLMAdvisor creates a new LLMAdvisor.
func NewLLMAdvisor(proxy *kernel.LLMProxy, model string) *LLMAdvisor {
	return &LLMAdvisor{
		Proxy:          proxy,
		Model:          model,
		PromptTemplate: defaultPromptTemplate,
	}
}

// Advise returns a trading recommendation based on current market state.
func (a *LLMAdvisor) Advise(ctx context.Context, state MarketState) (*Advice, error) {
	return a.AdviseWithBacktest(ctx, state, nil)
}

// AdviseWithBacktest returns a trading recommendation including backtest history.
func (a *LLMAdvisor) AdviseWithBacktest(ctx context.Context, state MarketState, bt *backtest.BacktestResult) (*Advice, error) {
	prompt := a.buildPrompt(state, bt)

	req := &llm.ChatRequest{
		BrainID: "quant",
		Messages: []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: prompt}}},
		},
		Model:     a.Model,
		MaxTokens: 2048,
	}

	resp, err := a.Proxy.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm advisor: complete failed: %w", err)
	}

	if len(resp.Content) == 0 {
		return nil, fmt.Errorf("llm advisor: empty response")
	}

	text := extractText(resp)
	return parseAdvice(text)
}

func extractText(resp *llm.ChatResponse) string {
	var sb strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" || block.Type == "thinking" {
			sb.WriteString(block.Text)
		}
	}
	return sb.String()
}

func (a *LLMAdvisor) buildPrompt(state MarketState, bt *backtest.BacktestResult) string {
	tmpl := a.PromptTemplate
	if tmpl == "" {
		tmpl = defaultPromptTemplate
	}

	// Build signal summary
	var signalLines []string
	for _, sig := range state.Signals {
		signalLines = append(signalLines, fmt.Sprintf(
			"- %s: direction=%s confidence=%.2f reason=%q",
			sig.Strategy, sig.Direction, sig.Confidence, sig.Reason,
		))
	}
	signalsStr := strings.Join(signalLines, "\n")
	if signalsStr == "" {
		signalsStr = "No active signals."
	}

	// Risk status
	riskStr := fmt.Sprintf(
		"Exposure: %.4f | Leverage: %.2f/%.2f | DailyLoss: %.2f%% | Paused: %v",
		state.RiskStatus.Exposure,
		state.RiskStatus.Leverage,
		state.RiskStatus.MaxLeverage,
		state.RiskStatus.DailyLossPct*100,
		state.RiskStatus.IsPaused,
	)

	// Portfolio
	portStr := fmt.Sprintf(
		"Equity: %.2f | Positions: %d | RealizedLossToday: %.2f%%",
		state.PortfolioSnapshot.Equity,
		len(state.PortfolioSnapshot.Positions),
		state.PortfolioSnapshot.RealizedLossTodayPct*100,
	)

	// Backtest
	btStr := "No backtest data available."
	if bt != nil {
		btStr = fmt.Sprintf(
			"Sharpe: %.2f | WinRate: %.1f%% | MaxDD: %.2f%% | Trades: %d | Return: %.2f%%",
			bt.SharpeRatio, bt.WinRate*100, bt.MaxDrawdown*100, len(bt.Trades), bt.TotalReturn*100,
		)
	}

	return fmt.Sprintf(tmpl,
		state.Symbol,
		state.Price,
		state.Regime,
		signalsStr,
		riskStr,
		portStr,
		btStr,
	)
}

func parseAdvice(text string) (*Advice, error) {
	text = strings.TrimSpace(text)

	// Try to extract JSON from markdown code blocks or raw JSON
	jsonStr := extractJSON(text)
	if jsonStr != "" {
		var raw struct {
			Action        string  `json:"action"`
			Confidence    float64 `json:"confidence"`
			Reason        string  `json:"reason"`
			RiskLevel     string  `json:"risk_level"`
			SuggestedSize float64 `json:"suggested_size"`
		}
		if err := json.Unmarshal([]byte(jsonStr), &raw); err == nil {
			return normalizeAdvice(&raw), nil
		}
	}

	// Fallback: heuristic parsing
	adv := &Advice{
		Action:     "hold",
		Confidence: 0.5,
		Reason:     text,
		RiskLevel:  "medium",
	}

	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "enter_long") || (strings.Contains(lower, "long") && strings.Contains(lower, "enter")):
		adv.Action = "enter_long"
	case strings.Contains(lower, "enter_short") || (strings.Contains(lower, "short") && strings.Contains(lower, "enter")):
		adv.Action = "enter_short"
	case strings.Contains(lower, "reduce"):
		adv.Action = "reduce"
	case strings.Contains(lower, "close"):
		adv.Action = "close"
	case strings.Contains(lower, "hold"):
		adv.Action = "hold"
	}

	if strings.Contains(lower, "high risk") || strings.Contains(lower, "dangerous") || strings.Contains(lower, "volatile") {
		adv.RiskLevel = "high"
	} else if strings.Contains(lower, "low risk") || strings.Contains(lower, "safe") || strings.Contains(lower, "stable") {
		adv.RiskLevel = "low"
	}

	return adv, nil
}

func extractJSON(text string) string {
	// Look for markdown code block with json
	if idx := strings.Index(text, "```json"); idx != -1 {
		start := idx + len("```json")
		if end := strings.Index(text[start:], "```"); end != -1 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	// Look for any markdown code block
	if idx := strings.Index(text, "```"); idx != -1 {
		start := idx + len("```")
		if end := strings.Index(text[start:], "```"); end != -1 {
			candidate := strings.TrimSpace(text[start : start+end])
			if strings.HasPrefix(candidate, "{") {
				return candidate
			}
		}
	}
	// Look for raw JSON object
	if idx := strings.Index(text, "{"); idx != -1 {
		endIdx := strings.LastIndex(text, "}")
		if endIdx > idx {
			return text[idx : endIdx+1]
		}
	}
	return ""
}

func normalizeAdvice(raw *struct {
	Action        string  `json:"action"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason"`
	RiskLevel     string  `json:"risk_level"`
	SuggestedSize float64 `json:"suggested_size"`
}) *Advice {
	action := strings.ToLower(strings.TrimSpace(raw.Action))
	validActions := map[string]bool{"hold": true, "enter_long": true, "enter_short": true, "reduce": true, "close": true}
	if !validActions[action] {
		action = "hold"
	}

	riskLevel := strings.ToLower(strings.TrimSpace(raw.RiskLevel))
	validRisks := map[string]bool{"low": true, "medium": true, "high": true}
	if !validRisks[riskLevel] {
		riskLevel = "medium"
	}

	conf := raw.Confidence
	if conf < 0 {
		conf = 0
	} else if conf > 1 {
		conf = 1
	}

	size := raw.SuggestedSize
	if size < 0 {
		size = 0
	} else if size > 1 {
		size = 1
	}

	reason := raw.Reason
	if reason == "" {
		reason = "No reason provided."
	}

	return &Advice{
		Action:        action,
		Confidence:    conf,
		Reason:        reason,
		RiskLevel:     riskLevel,
		SuggestedSize: size,
	}
}

const defaultPromptTemplate = `You are a quantitative trading advisor.

Current Market:
- Symbol: %s
- Price: %.4f
- Regime: %s

Strategy Signals:
%s

Risk Limits:
%s

Portfolio:
%s

Backtest Performance:
%s

Respond with a JSON object containing:
{
  "action": "hold|enter_long|enter_short|reduce|close",
  "confidence": 0.0-1.0,
  "reason": "brief explanation",
  "risk_level": "low|medium|high",
  "suggested_size": 0.0-1.0
}
`
