// Package quant implements the Central Brain's quantitative trading handlers:
//   - review_trade: LLM-based trade approval
//   - daily_review: end-of-day analysis
//   - data_alert: data quality alerting
package quant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leef-l/brain/central/llm"
)

// Handler processes all quantitative trading requests for the Central Brain.
type Handler struct {
	llm    *llm.Client
	logger *slog.Logger
}

// NewHandler creates a quant handler with the given LLM client.
func NewHandler(llmClient *llm.Client, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		llm:    llmClient,
		logger: logger,
	}
}

// ──────────────────────────────────────────────────────────────────
// review_trade: LLM-based trade approval
// ──────────────────────────────────────────────────────────────────

// ReviewTradeRequest is the input from the Quant Brain.
type ReviewTradeRequest struct {
	Signal    SignalInfo    `json:"signal"`
	Portfolio PortfolioInfo `json:"portfolio"`
	Market    MarketInfo   `json:"market"`
	Reason    string       `json:"reason"`
}

type SignalInfo struct {
	Direction  string  `json:"direction"`
	Confidence float64 `json:"confidence"`
}

type PortfolioInfo struct {
	TotalEquity   float64 `json:"total_equity"`
	DailyPnLPct   float64 `json:"daily_pnl_pct"`
	OpenPositions int     `json:"open_positions"`
	LargestPosPct float64 `json:"largest_pos_pct"`
}

type MarketInfo struct {
	Symbol        string  `json:"symbol"`
	Price         float64 `json:"price"`
	VolPercentile float64 `json:"vol_percentile"`
	MarketRegime  string  `json:"market_regime"`
	FundingRate   float64 `json:"funding_rate"`
}

// ReviewTradeResponse is the LLM's decision.
type ReviewTradeResponse struct {
	Approved   bool    `json:"approved"`
	SizeFactor float64 `json:"size_factor"`
	Reason     string  `json:"reason"`
}

// HandleReviewTrade processes a trade review request via LLM.
func (h *Handler) HandleReviewTrade(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req ReviewTradeRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("parse review request: %w", err)
	}

	prompt := buildReviewPrompt(req)
	h.logger.Info("review_trade invoked",
		"symbol", req.Market.Symbol,
		"direction", req.Signal.Direction,
		"confidence", req.Signal.Confidence)

	messages := []llm.Message{
		{Role: "system", Content: "你是一个量化交易风控审查员。根据投资组合状态和市场环境，判断待审查交易是否应该执行。始终以JSON格式回复。"},
		{Role: "user", Content: prompt},
	}

	var resp ReviewTradeResponse
	if err := h.llm.ChatJSON(ctx, messages, &resp); err != nil {
		h.logger.Warn("llm review failed, rejecting trade for safety", "err", err)
		resp = ReviewTradeResponse{
			Approved:   false,
			SizeFactor: 0,
			Reason:     "llm_error_reject: " + err.Error(),
		}
	}

	// Clamp size factor
	if resp.SizeFactor <= 0 {
		resp.SizeFactor = 0
	}
	if resp.SizeFactor > 1.0 {
		resp.SizeFactor = 1.0
	}

	h.logger.Info("review_trade result",
		"approved", resp.Approved,
		"size_factor", resp.SizeFactor,
		"reason", resp.Reason)

	return json.Marshal(resp)
}

func buildReviewPrompt(req ReviewTradeRequest) string {
	regime := req.Market.MarketRegime
	if regime == "" {
		regime = "未知"
	}
	return fmt.Sprintf(`[投资组合状态]
总权益: %.2f USDT
今日损益: %.2f%%
持仓数: %d
最大单仓占比: %.1f%%

[待审查交易]
品种: %s | 方向: %s
信心度: %.2f
触发原因: %s

[市场环境]
当前价格: %.2f
波动率百分位: %.0f%%
市场状态: %s
资金费率: %.6f

判断:
1. 该交易是否应该执行？(YES/NO)
2. 如果 YES，仓位系数？(0.0-1.0，1.0=满仓，0.5=半仓)
3. 理由（30字内）

请以JSON格式回复: {"approved": true/false, "size_factor": 0.0-1.0, "reason": "..."}`,
		req.Portfolio.TotalEquity,
		req.Portfolio.DailyPnLPct,
		req.Portfolio.OpenPositions,
		req.Portfolio.LargestPosPct,
		req.Market.Symbol,
		req.Signal.Direction,
		req.Signal.Confidence,
		req.Reason,
		req.Market.Price,
		req.Market.VolPercentile*100,
		regime,
		req.Market.FundingRate,
	)
}

// ──────────────────────────────────────────────────────────────────
// daily_review: end-of-day analysis
// ──────────────────────────────────────────────────────────────────

// DailyReviewRequest is the input for daily review.
type DailyReviewRequest struct {
	Date            string          `json:"date"`
	Accounts        []AccountStats  `json:"accounts"`
	StrategyStats   []StrategyStats `json:"strategy_stats"`
	MarketSummary   string          `json:"market_summary"`
	DataQuality     string          `json:"data_quality"`
	TotalTrades     int             `json:"total_trades"`
	TotalPnL        float64         `json:"total_pnl"`
	TotalPnLPct     float64         `json:"total_pnl_pct"`
}

type AccountStats struct {
	ID         string  `json:"id"`
	Equity     float64 `json:"equity"`
	DailyPnL   float64 `json:"daily_pnl"`
	Trades     int     `json:"trades"`
	WinRate    float64 `json:"win_rate"`
}

type StrategyStats struct {
	Name      string  `json:"name"`
	Signals   int     `json:"signals"`
	Executed  int     `json:"executed"`
	WinRate   float64 `json:"win_rate"`
	AvgPnL    float64 `json:"avg_pnl"`
}

// DailyReviewResponse is the LLM's analysis.
type DailyReviewResponse struct {
	Assessment     string         `json:"assessment"`
	BestTrades     string         `json:"best_trades"`
	WorstTrades    string         `json:"worst_trades"`
	StrategyNotes  string         `json:"strategy_notes"`
	RiskNotes      string         `json:"risk_notes"`
	MarketRegime   string         `json:"market_regime"`
	Actions        []ReviewAction `json:"actions,omitempty"`
}

type ReviewAction struct {
	Type        string `json:"type"`    // "adjust_weight", "pause_strategy", "alert"
	Target      string `json:"target"`  // strategy name or symbol
	Params      string `json:"params"`  // action-specific parameters
	AutoExecute bool   `json:"auto_execute"`
}

// HandleDailyReview runs the end-of-day analysis.
func (h *Handler) HandleDailyReview(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req DailyReviewRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("parse daily review request: %w", err)
	}

	if req.Date == "" {
		req.Date = time.Now().UTC().Format("2006-01-02")
	}

	prompt := buildDailyReviewPrompt(req)
	h.logger.Info("daily_review invoked",
		"date", req.Date,
		"total_trades", req.TotalTrades,
		"total_pnl", req.TotalPnL)

	messages := []llm.Message{
		{Role: "system", Content: "你是一个量化交易系统的日报分析员。根据当日交易数据生成分析报告和操作建议。始终以JSON格式回复。"},
		{Role: "user", Content: prompt},
	}

	var resp DailyReviewResponse
	if err := h.llm.ChatJSON(ctx, messages, &resp); err != nil {
		h.logger.Error("daily review llm failed", "err", err)
		resp = DailyReviewResponse{
			Assessment: "LLM 分析失败，请查看原始数据",
			RiskNotes:  err.Error(),
		}
	}

	h.logger.Info("daily_review complete",
		"date", req.Date,
		"assessment_len", len(resp.Assessment),
		"actions", len(resp.Actions))

	return json.Marshal(resp)
}

func buildDailyReviewPrompt(req DailyReviewRequest) string {
	accountsJSON, _ := json.MarshalIndent(req.Accounts, "", "  ")
	strategyJSON, _ := json.MarshalIndent(req.StrategyStats, "", "  ")

	return fmt.Sprintf(`[日期] %s

[账户概况]
总交易: %d | 总盈亏: %.2f USDT (%.2f%%)
%s

[策略表现]
%s

[市场环境]
%s

[数据质量]
%s

请分析今日交易情况，以JSON格式输出:
{
  "assessment": "整体评价（50字内）",
  "best_trades": "最佳交易分析",
  "worst_trades": "最差交易分析",
  "strategy_notes": "策略表现点评和建议",
  "risk_notes": "风险提醒",
  "market_regime": "当前市场状态判断（trend/range/breakout/panic）",
  "actions": [
    {"type": "adjust_weight|pause_strategy|alert", "target": "策略名", "params": "具体参数", "auto_execute": false}
  ]
}`,
		req.Date,
		req.TotalTrades, req.TotalPnL, req.TotalPnLPct,
		string(accountsJSON),
		string(strategyJSON),
		req.MarketSummary,
		req.DataQuality,
	)
}

// ──────────────────────────────────────────────────────────────────
// data_alert: data quality alerting
// ──────────────────────────────────────────────────────────────────

// DataAlertRequest is the input from the Data Brain.
type DataAlertRequest struct {
	Level    string `json:"level"`      // "warning", "critical"
	Type     string `json:"alert_type"` // "price_spike", "gap", "stale"
	Symbol   string `json:"symbol"`
	Detail   string `json:"detail"`
	EventTS  int64  `json:"event_ts"`
}

// DataAlertResponse acknowledges the alert.
type DataAlertResponse struct {
	Received    bool   `json:"received"`
	Action      string `json:"action"` // "logged", "risk_pause", "ignored"
	Description string `json:"description"`
}

// HandleDataAlert processes a data quality alert.
func (h *Handler) HandleDataAlert(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req DataAlertRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, fmt.Errorf("parse data alert: %w", err)
	}

	h.logger.Warn("data alert received",
		"level", req.Level,
		"type", req.Type,
		"symbol", req.Symbol,
		"detail", req.Detail)

	resp := DataAlertResponse{Received: true}

	switch {
	case req.Level == "critical" && req.Type == "price_spike":
		resp.Action = "risk_pause"
		resp.Description = fmt.Sprintf("严重价格异常 %s，建议暂停该品种交易", req.Symbol)
		h.logger.Error("CRITICAL: price spike detected, recommend trading pause",
			"symbol", req.Symbol,
			"detail", req.Detail)

	case req.Level == "critical":
		resp.Action = "risk_pause"
		resp.Description = fmt.Sprintf("严重数据告警 [%s] %s", req.Type, req.Symbol)

	default:
		resp.Action = "logged"
		resp.Description = "告警已记录"
	}

	return json.Marshal(resp)
}
