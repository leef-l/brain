package persistence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ── Driver registry tests ───────────────────────────────────────────────

func TestDriversReturnsRegistered(t *testing.T) {
	names := Drivers()
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["mem"] {
		t.Error("expected 'mem' in Drivers()")
	}
	if !found["file"] {
		t.Error("expected 'file' in Drivers()")
	}
	// Verify sorted order.
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Drivers() not sorted: %q > %q", names[i-1], names[i])
		}
	}
}

func TestOpenUnknownDriver(t *testing.T) {
	_, err := Open("nonexistent-driver-xyz", "")
	if err == nil {
		t.Fatal("expected error for unknown driver")
	}
}

// ── mem driver tests ────────────────────────────────────────────────────

func TestMemDriverOpen(t *testing.T) {
	cs, err := Open("mem", "")
	if err != nil {
		t.Fatalf("Open(mem): %v", err)
	}
	defer cs.Close()

	if cs.PlanStore == nil {
		t.Error("PlanStore is nil")
	}
	if cs.ArtifactStore == nil {
		t.Error("ArtifactStore is nil")
	}
	if cs.ArtifactMeta == nil {
		t.Error("ArtifactMeta is nil")
	}
	if cs.RunCheckpointStore == nil {
		t.Error("RunCheckpointStore is nil")
	}
	if cs.UsageLedger == nil {
		t.Error("UsageLedger is nil")
	}
	if cs.ResumeCoordinator == nil {
		t.Error("ResumeCoordinator is nil")
	}
}

func TestMemDriverRoundTrip(t *testing.T) {
	cs, err := Open("mem", "")
	if err != nil {
		t.Fatalf("Open(mem): %v", err)
	}
	defer cs.Close()
	ctx := context.Background()

	// PlanStore round-trip.
	plan := &BrainPlan{
		RunID:        1,
		BrainID:      "test",
		CurrentState: json.RawMessage(`{"step":1}`),
	}
	id, err := cs.PlanStore.Create(ctx, plan)
	if err != nil {
		t.Fatalf("PlanStore.Create: %v", err)
	}
	got, err := cs.PlanStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("PlanStore.Get: %v", err)
	}
	if got.BrainID != "test" {
		t.Errorf("BrainID = %q, want %q", got.BrainID, "test")
	}

	// UsageLedger round-trip.
	rec := &UsageRecord{
		RunID:          1,
		InputTokens:    100,
		CostUSD:        0.01,
		IdempotencyKey: "test-key",
	}
	if err := cs.UsageLedger.Record(ctx, rec); err != nil {
		t.Fatalf("UsageLedger.Record: %v", err)
	}
	sum, err := cs.UsageLedger.Sum(ctx, 1)
	if err != nil {
		t.Fatalf("UsageLedger.Sum: %v", err)
	}
	if sum.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", sum.InputTokens)
	}
}

// ── file driver tests ───────────────────────────────────────────────────

func TestFileDriverOpen(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "brain.json")

	cs, err := Open("file", dsn)
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	defer cs.Close()

	if cs.PlanStore == nil {
		t.Error("PlanStore is nil")
	}
	if cs.ArtifactStore == nil {
		t.Error("ArtifactStore is nil")
	}
	if cs.RunCheckpointStore == nil {
		t.Error("RunCheckpointStore is nil")
	}
	if cs.UsageLedger == nil {
		t.Error("UsageLedger is nil")
	}

	// Verify artifacts directory was created.
	artifactDir := filepath.Join(dir, "artifacts")
	if _, err := os.Stat(artifactDir); os.IsNotExist(err) {
		t.Error("artifacts directory was not created")
	}
}

func TestFileDriverEmptyDSN(t *testing.T) {
	_, err := Open("file", "")
	if err == nil {
		t.Fatal("expected error for empty DSN")
	}
}

func TestFileDriverRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "brain.json")

	cs, err := Open("file", dsn)
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	defer cs.Close()
	ctx := context.Background()

	// PlanStore round-trip.
	plan := &BrainPlan{
		RunID:        42,
		BrainID:      "central",
		CurrentState: json.RawMessage(`{"status":"ok"}`),
	}
	id, err := cs.PlanStore.Create(ctx, plan)
	if err != nil {
		t.Fatalf("PlanStore.Create: %v", err)
	}

	// Re-open from the same file to verify persistence.
	cs2, err := Open("file", dsn)
	if err != nil {
		t.Fatalf("re-Open(file): %v", err)
	}
	defer cs2.Close()

	got, err := cs2.PlanStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("PlanStore.Get after re-open: %v", err)
	}
	if got.BrainID != "central" {
		t.Errorf("BrainID = %q, want %q", got.BrainID, "central")
	}

	// ArtifactStore round-trip.
	content := []byte("hello from file driver")
	ref, err := cs.ArtifactStore.Put(ctx, 1, Artifact{Kind: "test", Content: content})
	if err != nil {
		t.Fatalf("ArtifactStore.Put: %v", err)
	}
	exists, err := cs.ArtifactStore.Exists(ctx, ref)
	if err != nil {
		t.Fatalf("ArtifactStore.Exists: %v", err)
	}
	if !exists {
		t.Error("ArtifactStore.Exists = false after Put")
	}

	// Checkpoint round-trip.
	cp := &Checkpoint{RunID: 42, TurnUUID: "uuid-1", State: "Running", BrainID: "central"}
	if err := cs.RunCheckpointStore.Save(ctx, cp); err != nil {
		t.Fatalf("RunCheckpointStore.Save: %v", err)
	}
	gotCP, err := cs.RunCheckpointStore.Get(ctx, 42)
	if err != nil {
		t.Fatalf("RunCheckpointStore.Get: %v", err)
	}
	if gotCP.BrainID != "central" {
		t.Errorf("Checkpoint.BrainID = %q, want %q", gotCP.BrainID, "central")
	}
}

// ── ClosableStores nil-safety ───────────────────────────────────────────

func TestClosableStoresNilSafe(t *testing.T) {
	// nil receiver must not panic.
	var cs *ClosableStores
	if err := cs.Close(); err != nil {
		t.Errorf("nil ClosableStores.Close: %v", err)
	}

	// nil closer must not panic.
	cs2 := NewClosableStores(Stores{}, nil)
	if err := cs2.Close(); err != nil {
		t.Errorf("nil closer ClosableStores.Close: %v", err)
	}
}

// ── MustOpen panic test ─────────────────────────────────────────────────

func TestMustOpenPanicsOnBadDriver(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustOpen should panic for unknown driver")
		}
	}()
	MustOpen("nonexistent-driver-xyz", "")
}
