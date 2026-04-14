package review

import (
	"context"
	"testing"

	"github.com/leef-l/brain/internal/quantcontracts"
)

func TestEvaluateRejectsEmptySymbol(t *testing.T) {
	svc := New(Config{})

	decision, err := svc.Evaluate(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if decision.Approved {
		t.Fatalf("expected rejection, got %+v", decision)
	}
}

func TestEvaluateApprovesMinimalTrade(t *testing.T) {
	svc := New(Config{})

	decision, err := svc.Evaluate(context.Background(), Request{
		Symbol:    "BTC-USDT-SWAP",
		Direction: quantcontracts.DirectionLong,
		Candidates: []quantcontracts.DispatchCandidate{
			{AccountID: "paper", Allowed: true},
		},
	})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if !decision.Approved {
		t.Fatalf("expected approval, got %+v", decision)
	}
	if decision.SizeFactor != 1 {
		t.Fatalf("expected default size factor 1, got %+v", decision)
	}
}
