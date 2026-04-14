package execution

import (
	"context"
	"testing"

	coreexec "github.com/leef-l/brain/internal/execution"
	"github.com/leef-l/brain/internal/quantcontracts"
)

func TestPaperExecutorAdapterExecutePlan(t *testing.T) {
	adapter := NewPaperExecutorWithOptions([]string{"acct-a"}, WithPriceProvider(func(context.Context, string) (float64, bool) {
		return 100, true
	}))

	plan := quantcontracts.DispatchPlan{
		TraceID:   "trace-1",
		Symbol:    "BTC-USDT-SWAP",
		Direction: quantcontracts.DirectionLong,
		Candidates: []quantcontracts.DispatchCandidate{
			{
				AccountID:    "acct-a",
				Symbol:       "BTC-USDT-SWAP",
				Direction:    quantcontracts.DirectionLong,
				ProposedQty:  1.5,
				Allowed:      true,
				WeightFactor: 1,
				RiskReason:   "ok",
			},
		},
	}

	results, err := adapter.ExecutePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d, want 1", len(results))
	}
	if results[0].Status != coreexec.OrderStatusFilled {
		t.Fatalf("status=%q, want %q", results[0].Status, coreexec.OrderStatusFilled)
	}

	snap, ok := adapter.Account("acct-a")
	if !ok {
		t.Fatal("account snapshot missing")
	}
	if snap.PositionCount != 1 {
		t.Fatalf("position_count=%d, want 1", snap.PositionCount)
	}
	if snap.Status != AccountStatusActive {
		t.Fatalf("status=%q, want active", snap.Status)
	}
}
