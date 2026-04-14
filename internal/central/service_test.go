package central

import (
	"context"
	"testing"

	"github.com/leef-l/brain/internal/central/control"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/persistence"
)

func TestServiceReviewTradeAndControl(t *testing.T) {
	svc := New(Config{
		Control: control.Config{AutoPauseOnCriticalAlert: true},
	})

	decision, err := svc.ReviewTrade(context.Background(), quantcontracts.ReviewTradeRequest{
		TraceID:   "t-1",
		Symbol:    "BTC-USDT-SWAP",
		Direction: quantcontracts.DirectionLong,
		Candidates: []quantcontracts.DispatchCandidate{
			{AccountID: "paper", Allowed: true},
		},
	})
	if err != nil {
		t.Fatalf("ReviewTrade returned error: %v", err)
	}
	if !decision.Approved {
		t.Fatalf("expected approved decision, got %+v", decision)
	}

	res, err := svc.DataAlert(context.Background(), quantcontracts.DataAlert{Level: "critical", Type: "feed_lag", Symbol: "BTC-USDT-SWAP"})
	if err != nil {
		t.Fatalf("DataAlert returned error: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected data alert to be recorded, got %+v", res)
	}

	state := svc.State()
	if got := state.Control.PausedInstruments["BTC-USDT-SWAP"]; got == "" {
		t.Fatalf("expected paused instrument to be recorded, got %+v", state.Control)
	}
}

func TestServiceRestoreStateFromStore(t *testing.T) {
	ctx := context.Background()
	store := persistence.NewMemCentralStateStore(nil)

	svc := New(Config{
		Control:    control.Config{AutoPauseOnCriticalAlert: true},
		StateStore: store,
	})
	if _, err := svc.ReviewTrade(ctx, quantcontracts.ReviewTradeRequest{
		TraceID:   "trace-1",
		Symbol:    "BTC-USDT-SWAP",
		Direction: quantcontracts.DirectionLong,
		Candidates: []quantcontracts.DispatchCandidate{
			{AccountID: "paper", Allowed: true},
		},
	}); err != nil {
		t.Fatalf("ReviewTrade returned error: %v", err)
	}
	if _, err := svc.DataAlert(ctx, quantcontracts.DataAlert{
		Level:  "critical",
		Type:   "feed_lag",
		Symbol: "BTC-USDT-SWAP",
		Reason: "vol spike",
	}); err != nil {
		t.Fatalf("DataAlert returned error: %v", err)
	}

	restored := New(Config{StateStore: store})
	if err := restored.RestoreState(ctx); err != nil {
		t.Fatalf("RestoreState returned error: %v", err)
	}

	state := restored.State()
	if got := state.Control.PausedInstruments["BTC-USDT-SWAP"]; got != "vol spike" {
		t.Fatalf("paused instrument=%q, want vol spike", got)
	}
	if !state.Review.LastDecision.Approved {
		t.Fatalf("expected review decision to be restored, got %+v", state.Review.LastDecision)
	}
	if state.Review.LastRequest.TraceID != "trace-1" {
		t.Fatalf("trace id=%q, want trace-1", state.Review.LastRequest.TraceID)
	}
}
