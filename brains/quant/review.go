package quant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
)

// ReviewConfig configures the LLM review trigger thresholds.
type ReviewConfig struct {
	// Enabled controls whether LLM review is active.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// TriggerConcurrent triggers review if open positions >= this count.
	TriggerConcurrent int `json:"trigger_concurrent" yaml:"trigger_concurrent"` // default: 3

	// TriggerPositionPct triggers review if largest position > this % of equity.
	TriggerPositionPct float64 `json:"trigger_position_pct" yaml:"trigger_position_pct"` // default: 5

	// TriggerDailyLoss triggers review if daily loss > this % of equity.
	TriggerDailyLoss float64 `json:"trigger_daily_loss" yaml:"trigger_daily_loss"` // default: 3

	// Timeout is the max wait time for LLM response. Default: 10s.
	Timeout time.Duration `json:"timeout" yaml:"timeout"`

	// MaxTokens limits the LLM output budget. Default: 500.
	MaxTokens int `json:"max_tokens" yaml:"max_tokens"`
}

// DefaultReviewConfig returns sensible review thresholds.
func DefaultReviewConfig() ReviewConfig {
	return ReviewConfig{
		Enabled:            false, // off by default until central brain is connected
		TriggerConcurrent:  3,
		TriggerPositionPct: 5,
		TriggerDailyLoss:   3,
		Timeout:            10 * time.Second,
		MaxTokens:          500,
	}
}

// ReviewRequest is sent to the central brain for review.
type ReviewRequest struct {
	Signal    strategy.AggregatedSignal `json:"signal"`
	Portfolio PortfolioSummary          `json:"portfolio"`
	Market    MarketSummary             `json:"market"`
	Reason    string                    `json:"reason"`
}

// PortfolioSummary is a simplified view for the LLM prompt.
type PortfolioSummary struct {
	TotalEquity    float64 `json:"total_equity"`
	DailyPnLPct    float64 `json:"daily_pnl_pct"`
	OpenPositions  int     `json:"open_positions"`
	LargestPosPct  float64 `json:"largest_pos_pct"`
	TotalExposure  float64 `json:"total_exposure"`
}

// MarketSummary captures key market conditions for the LLM.
type MarketSummary struct {
	Symbol        string  `json:"symbol"`
	Price         float64 `json:"price"`
	VolPercentile float64 `json:"vol_percentile"`
	MarketRegime  string  `json:"market_regime"`
	FundingRate   float64 `json:"funding_rate"`
}

// ReviewDecision is the LLM's response.
type ReviewDecision struct {
	Approved   bool    `json:"approved"`
	SizeFactor float64 `json:"size_factor"` // 0.0-1.0, adjust position size
	Reason     string  `json:"reason"`
}

// Reviewer is the interface for LLM review integration.
// The default NullReviewer auto-approves everything.
// When the central brain is connected, use KernelReviewer.
type Reviewer interface {
	Review(ctx context.Context, req ReviewRequest) (ReviewDecision, error)
}

// NullReviewer auto-approves all reviews (used when LLM is not available).
type NullReviewer struct{}

func (NullReviewer) Review(_ context.Context, _ ReviewRequest) (ReviewDecision, error) {
	return ReviewDecision{
		Approved:   true,
		SizeFactor: 1.0,
		Reason:     "llm_not_configured",
	}, nil
}

// KernelReviewer calls the central brain via the kernel's subtask API.
// It implements the Reviewer interface for production use.
type KernelReviewer struct {
	// Caller is a function that sends a request to the central brain
	// and returns the raw JSON response. This is injected by the kernel
	// at startup to avoid import cycles.
	Caller  func(ctx context.Context, instruction string, payload []byte, timeoutSec int) ([]byte, error)
	Config  ReviewConfig
	Logger  *slog.Logger
}

// NewKernelReviewer creates a reviewer that calls the central brain.
func NewKernelReviewer(
	caller func(ctx context.Context, instruction string, payload []byte, timeoutSec int) ([]byte, error),
	cfg ReviewConfig,
	logger *slog.Logger,
) *KernelReviewer {
	if logger == nil {
		logger = slog.Default()
	}
	return &KernelReviewer{
		Caller: caller,
		Config: cfg,
		Logger: logger,
	}
}

// Review sends the request to the central brain with timeout.
func (r *KernelReviewer) Review(ctx context.Context, req ReviewRequest) (ReviewDecision, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return fallbackDecision("marshal_error"), fmt.Errorf("marshal request: %w", err)
	}

	timeout := r.Config.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := r.Caller(timeoutCtx, "review_trade", payload, r.Config.MaxTokens)
	if err != nil {
		r.Logger.Warn("llm review timeout/error, auto-approving",
			"err", err)
		return fallbackDecision("llm_timeout_fallback"), nil
	}

	var decision ReviewDecision
	if err := json.Unmarshal(resp, &decision); err != nil {
		r.Logger.Warn("llm review parse error, auto-approving",
			"err", err, "raw", string(resp))
		return fallbackDecision("llm_parse_error"), nil
	}

	// Clamp size factor
	if decision.SizeFactor <= 0 {
		decision.SizeFactor = 0
	}
	if decision.SizeFactor > 1.0 {
		decision.SizeFactor = 1.0
	}

	return decision, nil
}

func fallbackDecision(reason string) ReviewDecision {
	return ReviewDecision{
		Approved:   false,
		SizeFactor: 0,
		Reason:     reason,
	}
}

// BuildPrompt generates the Chinese prompt for the LLM review.
func BuildPrompt(req ReviewRequest) string {
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
2. 如果 YES，仓位系数？(0.0-1.0)
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
		req.Market.MarketRegime,
		req.Market.FundingRate,
	)
}

// SetReviewer sets the LLM reviewer on the QuantBrain.
func (qb *QuantBrain) SetReviewer(r Reviewer) {
	qb.reviewer = r
}

// integrateReview runs the LLM review for a trade decision and applies the result.
// Returns (proceed bool, sizeFactor float64).
func (qb *QuantBrain) integrateReview(ctx context.Context, td *TradeDecision, snap risk.GlobalSnapshot) (bool, float64) {
	if qb.reviewer == nil {
		return true, 1.0
	}

	req := ReviewRequest{
		Signal: td.Signal,
		Portfolio: PortfolioSummary{
			TotalEquity:   snap.TotalEquity,
			OpenPositions: len(snap.Positions),
		},
		Market: MarketSummary{
			Symbol: td.Symbol,
			Price:  td.OrderReq.EntryPrice,
		},
		Reason: td.ReviewReason,
	}

	decision, err := qb.reviewer.Review(ctx, req)
	if err != nil {
		qb.logger.Warn("llm review error, rejecting trade for safety",
			"err", err)
		return false, 0
	}

	if !decision.Approved {
		qb.logger.Info("llm review rejected trade",
			"symbol", td.Symbol,
			"reason", decision.Reason)
		return false, 0
	}

	return true, decision.SizeFactor
}
