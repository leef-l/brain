package router

import (
	"testing"

	"github.com/leef-l/brain/internal/quant/view"
	"github.com/leef-l/brain/internal/strategy"
)

func TestRouterBuildDispatchPlanAndApplyReviewDecision(t *testing.T) {
	r := New("acct-a", "acct-b")
	snapshot := view.NewFixtureSnapshot("BTC-USDT-SWAP")
	agg := strategy.AggregatedSignal{
		Symbol:     snapshot.Symbol(),
		Direction:  strategy.DirectionLong,
		Confidence: 0.8,
		Signals: []strategy.Signal{{
			Strategy:   "TrendFollower",
			Direction:  strategy.DirectionLong,
			Entry:      snapshot.CurrentPrice(),
			StopLoss:   120,
			TakeProfit: 150,
			Reason:     "trend confirmed",
		}},
		NeedsReview:  true,
		ReviewReason: "open positions >= 3",
	}

	plan := r.BuildDispatchPlan(snapshot, agg, view.PortfolioView{})
	if plan.Symbol != snapshot.Symbol() {
		t.Fatalf("symbol=%q, want %q", plan.Symbol, snapshot.Symbol())
	}
	if !plan.ReviewRequired {
		t.Fatal("plan should require review")
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("candidates=%d, want 2", len(plan.Candidates))
	}

	approved := r.ApplyReviewDecision(plan, ReviewDecision{
		Approved:   true,
		SizeFactor: 0.5,
		Reason:     "scale down",
	})
	if approved.ReviewSizeFactor != 0.5 {
		t.Fatalf("size_factor=%v, want 0.5", approved.ReviewSizeFactor)
	}
	if approved.Candidates[0].Quantity <= 0 {
		t.Fatal("scaled quantity should remain positive")
	}
	if approved.ReviewReason != "scale down" {
		t.Fatalf("review_reason=%q, want scale down", approved.ReviewReason)
	}
}
