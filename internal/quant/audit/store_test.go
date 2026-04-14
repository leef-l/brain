package audit

import (
	"context"
	"testing"

	"github.com/leef-l/brain/persistence"
)

func TestMemoryStoreSaveAndQuery(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if err := store.Save(ctx, &SignalTrace{Symbol: "BTC-USDT-SWAP", Outcome: "planned"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, &SignalTrace{Symbol: "ETH-USDT-SWAP", Outcome: "planned"}); err != nil {
		t.Fatal(err)
	}

	traces, err := store.Query(ctx, QueryFilter{Symbol: "BTC-USDT-SWAP", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("len(traces)=%d, want 1", len(traces))
	}
	if traces[0].TraceID == "" {
		t.Fatal("trace id should be assigned")
	}
}

func TestPersistentStoreSaveAndQuery(t *testing.T) {
	store := NewPersistentStore(persistence.NewMemSignalTraceStore(nil))
	ctx := context.Background()

	if err := store.Save(ctx, &SignalTrace{
		TraceID:           "trace-persist-1",
		Symbol:            "BTC-USDT-SWAP",
		Outcome:           "executed",
		Price:             103.5,
		Confidence:        0.82,
		DominantStrategy:  "TrendFollower",
		GlobalRiskAllowed: true,
		AccountResults:    []byte(`[{"status":"filled"}]`),
	}); err != nil {
		t.Fatal(err)
	}

	traces, err := store.Query(ctx, QueryFilter{Symbol: "BTC-USDT-SWAP", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("len(traces)=%d, want 1", len(traces))
	}
	if traces[0].TraceID != "trace-persist-1" {
		t.Fatalf("trace id=%q, want trace-persist-1", traces[0].TraceID)
	}
	if len(traces[0].AccountResults) == 0 {
		t.Fatal("account results should round-trip")
	}
	if traces[0].DominantStrategy != "TrendFollower" || traces[0].Confidence != 0.82 {
		t.Fatalf("strategy metadata lost: %+v", traces[0])
	}
	if !traces[0].GlobalRiskAllowed {
		t.Fatalf("global risk flag should round-trip: %+v", traces[0])
	}
}
