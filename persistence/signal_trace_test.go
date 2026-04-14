package persistence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemSignalTraceStore_SaveQuery(t *testing.T) {
	store := NewMemSignalTraceStore(func() time.Time {
		return time.UnixMilli(1700000000000).UTC()
	})
	ctx := context.Background()

	trace := &SignalTrace{
		TraceID:         "trace-paper-001",
		Symbol:          "BTC-USDT-SWAP",
		SnapshotSeq:     7,
		Outcome:         "executed",
		ReviewApproved:  true,
		DraftCandidates: json.RawMessage(`[{"account_id":"paper","quantity":1}]`),
	}
	if err := store.Save(ctx, trace); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get(ctx, "trace-paper-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Symbol != "BTC-USDT-SWAP" {
		t.Fatalf("Symbol=%q, want BTC-USDT-SWAP", got.Symbol)
	}

	matches, err := store.Query(ctx, SignalTraceFilter{Symbol: "BTC-USDT-SWAP", Limit: 1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("len(matches)=%d, want 1", len(matches))
	}
	if matches[0].TraceID != trace.TraceID {
		t.Fatalf("TraceID=%q, want %q", matches[0].TraceID, trace.TraceID)
	}
}

func TestFileSignalTraceStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brain.json")
	ctx := context.Background()

	fs, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	store := fs.SignalTraceStore()

	trace := &SignalTrace{
		TraceID:          "trace-file-001",
		Symbol:           "ETH-USDT-SWAP",
		SnapshotSeq:      11,
		Outcome:          "rejected",
		RejectedStage:    "global_risk",
		GlobalRiskReason: "portfolio limit",
	}
	if err := store.Save(ctx, trace); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.SignalTraceStore().Get(ctx, "trace-file-001")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.RejectedStage != "global_risk" {
		t.Fatalf("RejectedStage=%q, want global_risk", got.RejectedStage)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var db fileDB
	if err := json.Unmarshal(data, &db); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(db.SignalTraces) != 1 {
		t.Fatalf("len(db.SignalTraces)=%d, want 1", len(db.SignalTraces))
	}
}
