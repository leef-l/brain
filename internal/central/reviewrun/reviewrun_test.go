package reviewrun

import (
	"context"
	"testing"

	"github.com/leef-l/brain/internal/central/control"
	"github.com/leef-l/brain/internal/central/review"
	"github.com/leef-l/brain/internal/quantcontracts"
)

func TestRunProducesTraceAndControlRecord(t *testing.T) {
	runner := New(review.New(review.Config{}), control.New(control.Config{}))

	result, err := runner.Run(context.Background(), Request{
		Trade: review.Request{
			Symbol:    "BTC-USDT-SWAP",
			Direction: quantcontracts.DirectionLong,
			Candidates: []quantcontracts.DispatchCandidate{
				{AccountID: "paper", Allowed: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.Decision.Approved {
		t.Fatalf("expected approval, got %+v", result.Decision)
	}
	if result.Trace.Outcome != "approved" {
		t.Fatalf("expected approved trace outcome, got %+v", result.Trace)
	}
	if result.Control.Action != "review_decision" {
		t.Fatalf("expected control to record review decision, got %+v", result.Control)
	}
}
