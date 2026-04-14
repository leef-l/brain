package router

import (
	"math"
	"strings"
	"time"

	"github.com/leef-l/brain/internal/quant/view"
	"github.com/leef-l/brain/internal/strategy"
)

// DispatchCandidate is the per-account execution intent before the executor
// layer turns it into an order payload.
type DispatchCandidate struct {
	AccountID       string  `json:"account_id"`
	Symbol          string  `json:"symbol"`
	Side            string  `json:"side"`
	Quantity        float64 `json:"quantity"`
	EntryPrice      float64 `json:"entry_price,omitempty"`
	StopLossPrice   float64 `json:"stop_loss_price,omitempty"`
	TakeProfitPrice float64 `json:"take_profit_price,omitempty"`
	Reason          string  `json:"reason,omitempty"`
	Weight          float64 `json:"weight,omitempty"`
}

// DispatchPlan is the single execution-intent object owned by Quant Brain.
type DispatchPlan struct {
	Symbol           string              `json:"symbol"`
	Direction        strategy.Direction  `json:"direction"`
	SnapshotSeq      uint64              `json:"snapshot_seq"`
	Confidence       float64             `json:"confidence"`
	ReviewRequired   bool                `json:"review_required"`
	ReviewReason     string              `json:"review_reason,omitempty"`
	RejectionReason  string              `json:"rejection_reason,omitempty"`
	RejectedStage    string              `json:"rejected_stage,omitempty"`
	ReviewSizeFactor float64             `json:"review_size_factor,omitempty"`
	Candidates       []DispatchCandidate `json:"candidates,omitempty"`
	CreatedAt        int64               `json:"created_at"`
}

// ReviewDecision is the minimal result returned by Central Brain review.
type ReviewDecision struct {
	Approved   bool    `json:"approved"`
	SizeFactor float64 `json:"size_factor"`
	Reason     string  `json:"reason,omitempty"`
	Reviewer   string  `json:"reviewer,omitempty"`
	ReviewedAt int64   `json:"reviewed_at,omitempty"`
}

// Router turns a signal into a plan without touching transport concerns.
type Router struct {
	Accounts      []string
	BaseQuantity  float64
	MaxCandidates int
}

func New(accounts ...string) *Router {
	if len(accounts) == 0 {
		accounts = []string{"paper"}
	}
	return &Router{
		Accounts:      append([]string(nil), accounts...),
		BaseQuantity:  1,
		MaxCandidates: len(accounts),
	}
}

func (r *Router) BuildDispatchPlan(snapshot view.MarketSnapshot, agg strategy.AggregatedSignal, portfolio view.PortfolioView) DispatchPlan {
	plan := DispatchPlan{
		Symbol:      snapshot.Symbol(),
		Direction:   agg.Direction,
		SnapshotSeq: snapshot.WriteSeqValue,
		Confidence:  agg.Confidence,
		CreatedAt:   time.Now().UTC().UnixMilli(),
	}

	if agg.Direction == strategy.DirectionHold {
		plan.RejectionReason = firstNonEmpty(agg.RejectionReason, "no executable direction")
		plan.RejectedStage = "aggregate"
		return plan
	}

	side := sideForDirection(agg.Direction)
	quantity := r.quantityFor(agg.Confidence, portfolio)
	candidates := r.Accounts
	if r.MaxCandidates > 0 && len(candidates) > r.MaxCandidates {
		candidates = candidates[:r.MaxCandidates]
	}
	var sourceSignal strategy.Signal
	if len(agg.Signals) > 0 {
		sourceSignal = agg.Signals[0]
	}
	for _, accountID := range candidates {
		plan.Candidates = append(plan.Candidates, DispatchCandidate{
			AccountID:       accountID,
			Symbol:          snapshot.Symbol(),
			Side:            side,
			Quantity:        quantity,
			EntryPrice:      snapshot.CurrentPrice(),
			StopLossPrice:   sourceSignal.StopLoss,
			TakeProfitPrice: sourceSignal.TakeProfit,
			Reason:          firstNonEmpty(agg.ReviewReason, sourceSignal.Reason),
			Weight:          weightForAccount(portfolio, len(candidates)),
		})
	}

	if agg.NeedsReview {
		plan.ReviewRequired = true
		plan.ReviewReason = agg.ReviewReason
	}
	if portfolio.PausedTrading {
		plan.ReviewRequired = false
		plan.RejectionReason = "trading paused"
		plan.RejectedStage = "control"
		plan.Candidates = nil
	}
	return plan
}

func (r *Router) ApplyReviewDecision(plan DispatchPlan, decision ReviewDecision) DispatchPlan {
	plan.ReviewSizeFactor = normalizeFactor(decision.SizeFactor)
	if !decision.Approved {
		plan.RejectedStage = "review"
		plan.RejectionReason = firstNonEmpty(decision.Reason, "review rejected")
		plan.Candidates = nil
		return plan
	}
	if plan.ReviewSizeFactor <= 0 {
		plan.ReviewSizeFactor = 1
	}
	for i := range plan.Candidates {
		plan.Candidates[i].Quantity = math.Max(0, plan.Candidates[i].Quantity*plan.ReviewSizeFactor)
	}
	if decision.Reason != "" {
		plan.ReviewReason = decision.Reason
	}
	return plan
}

func (r *Router) quantityFor(confidence float64, portfolio view.PortfolioView) float64 {
	qty := r.BaseQuantity
	if qty <= 0 {
		qty = 1
	}
	qty *= clamp(confidence, 0.25, 1.0)
	if portfolio.OpenPositions > 0 {
		qty *= 1 / (1 + float64(portfolio.OpenPositions)/5)
	}
	return math.Round(qty*1000) / 1000
}

func weightForAccount(portfolio view.PortfolioView, total int) float64 {
	if total <= 0 {
		return 1
	}
	weight := 1 / float64(total)
	if portfolio.TotalEquity > 0 {
		weight = math.Min(weight, 0.25)
	}
	return math.Round(weight*1000) / 1000
}

func sideForDirection(direction strategy.Direction) string {
	switch direction {
	case strategy.DirectionShort:
		return "sell"
	default:
		return "buy"
	}
}

func normalizeFactor(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 1
	}
	if v <= 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
