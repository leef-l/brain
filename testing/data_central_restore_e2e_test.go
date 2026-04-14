package braintesting

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/internal/central"
	"github.com/leef-l/brain/internal/central/control"
	datamodel "github.com/leef-l/brain/internal/data/model"
	dataprovider "github.com/leef-l/brain/internal/data/provider"
	dataservice "github.com/leef-l/brain/internal/data/service"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/persistence"
)

func TestDataStatePersistence_E2E(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "brain.json")

	fileStore, err := persistence.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	cfg := dataservice.Config{
		DefaultTimeframe: "1m",
		MonotonicTopics:  map[string]bool{"trade": true},
		StateStore:       fileStore.DataStateStore(),
	}
	svc := dataservice.New(cfg)
	if err := svc.RegisterProvider(dataprovider.NewStaticProvider("okx")); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}

	event := datamodel.MarketEvent{
		Provider:  "okx",
		Topic:     "trade",
		Symbol:    "BTC-USDT-SWAP",
		Timestamp: 1700000000000,
		Price:     101.5,
		Payload:   []byte("tick-1"),
	}
	written, result, err := svc.Ingest(ctx, event)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("validation result=%+v, want accept", result)
	}

	reopened, err := persistence.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	restored := dataservice.New(dataservice.Config{
		DefaultTimeframe: "1m",
		MonotonicTopics:  map[string]bool{"trade": true},
		StateStore:       reopened.DataStateStore(),
	})
	if err := restored.RestoreState(ctx); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}

	snapshot, ok := restored.LatestSnapshot("BTC-USDT-SWAP")
	if !ok || snapshot.WriteSeq != written.WriteSeq {
		t.Fatalf("restored snapshot=%+v ok=%v, want write_seq=%d", snapshot, ok, written.WriteSeq)
	}
	_, dup, err := restored.Ingest(ctx, event)
	if err != nil {
		t.Fatalf("duplicate Ingest: %v", err)
	}
	if dup.Action != datamodel.ValidationSkip {
		t.Fatalf("duplicate validation action=%q, want skip", dup.Action)
	}
}

func TestCentralStatePersistence_E2E(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "brain.json")

	fileStore, err := persistence.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	svc := central.New(central.Config{
		Control:    control.Config{AutoPauseOnCriticalAlert: true},
		StateStore: fileStore.CentralStateStore(),
	})

	if _, err := svc.ReviewTrade(ctx, quantcontracts.ReviewTradeRequest{
		TraceID:   "trace-1",
		Symbol:    "BTC-USDT-SWAP",
		Direction: quantcontracts.DirectionLong,
		Candidates: []quantcontracts.DispatchCandidate{
			{AccountID: "paper", Allowed: true},
		},
	}); err != nil {
		t.Fatalf("ReviewTrade: %v", err)
	}
	if _, err := svc.DataAlert(ctx, quantcontracts.DataAlert{
		Level:  "critical",
		Type:   "feed_lag",
		Symbol: "BTC-USDT-SWAP",
		Reason: "vol spike",
	}); err != nil {
		t.Fatalf("DataAlert: %v", err)
	}

	reopened, err := persistence.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	restored := central.New(central.Config{
		StateStore: reopened.CentralStateStore(),
	})
	if err := restored.RestoreState(ctx); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}

	state := restored.State()
	if !state.Review.LastDecision.Approved {
		t.Fatalf("expected restored review decision, got %+v", state.Review.LastDecision)
	}
	if got := state.Control.PausedInstruments["BTC-USDT-SWAP"]; got != "vol spike" {
		t.Fatalf("paused instrument=%q, want vol spike", got)
	}
}
