package persistence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func tempFilePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "brain.db.json")
}

func TestFileStore_PlanStore_CreateGet(t *testing.T) {
	path := tempFilePath(t)
	fs, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ps := fs.PlanStore()
	ctx := context.Background()

	plan := &BrainPlan{BrainID: "central", Version: 1, CurrentState: json.RawMessage(`{"s":1}`)}
	id, err := ps.Create(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id=%d, want > 0", id)
	}

	got, err := ps.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.BrainID != "central" {
		t.Errorf("BrainID=%q, want central", got.BrainID)
	}
}

func TestFileStore_PlanStore_Persistence(t *testing.T) {
	path := tempFilePath(t)
	ctx := context.Background()

	// Write data.
	fs1, _ := OpenFileStore(path)
	ps1 := fs1.PlanStore()
	ps1.Create(ctx, &BrainPlan{BrainID: "code", Version: 1, CurrentState: json.RawMessage(`{}`)})

	// Reopen and verify data persists.
	fs2, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ps2 := fs2.PlanStore()
	got, err := ps2.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.BrainID != "code" {
		t.Errorf("BrainID=%q after reopen, want code", got.BrainID)
	}
}

func TestFileStore_PlanStore_UpdateArchive(t *testing.T) {
	path := tempFilePath(t)
	fs, _ := OpenFileStore(path)
	ps := fs.PlanStore()
	ctx := context.Background()

	id, _ := ps.Create(ctx, &BrainPlan{BrainID: "c", Version: 1, CurrentState: json.RawMessage(`{}`)})
	err := ps.Update(ctx, id, &BrainPlanDelta{Version: 2, OpType: "replace", Payload: json.RawMessage(`{"v":2}`)})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := ps.Get(ctx, id)
	if got.Version != 2 {
		t.Errorf("Version=%d, want 2", got.Version)
	}

	ps.Archive(ctx, id)
	err = ps.Update(ctx, id, &BrainPlanDelta{Version: 3, OpType: "patch", Payload: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("Update on archived plan should fail")
	}
}

func TestFileStore_PlanStore_ListByRun(t *testing.T) {
	path := tempFilePath(t)
	fs, _ := OpenFileStore(path)
	ps := fs.PlanStore()
	ctx := context.Background()

	ps.Create(ctx, &BrainPlan{RunID: 10, BrainID: "a", Version: 1, CurrentState: json.RawMessage(`{}`)})
	ps.Create(ctx, &BrainPlan{RunID: 10, BrainID: "b", Version: 1, CurrentState: json.RawMessage(`{}`)})
	ps.Create(ctx, &BrainPlan{RunID: 20, BrainID: "c", Version: 1, CurrentState: json.RawMessage(`{}`)})

	plans, _ := ps.ListByRun(ctx, 10)
	if len(plans) != 2 {
		t.Fatalf("len=%d, want 2", len(plans))
	}
}

func TestFileStore_CheckpointStore_SaveGet(t *testing.T) {
	path := tempFilePath(t)
	fs, _ := OpenFileStore(path)
	cs := fs.CheckpointStore()
	ctx := context.Background()

	cp := &Checkpoint{RunID: 1, BrainID: "central", State: "running", TurnUUID: "uuid-1"}
	if err := cs.Save(ctx, cp); err != nil {
		t.Fatal(err)
	}

	got, err := cs.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.BrainID != "central" {
		t.Errorf("BrainID=%q, want central", got.BrainID)
	}
}

func TestFileStore_CheckpointStore_Idempotent(t *testing.T) {
	path := tempFilePath(t)
	fs, _ := OpenFileStore(path)
	cs := fs.CheckpointStore()
	ctx := context.Background()

	cp := &Checkpoint{RunID: 1, BrainID: "c", State: "running", TurnUUID: "uuid-x"}
	cs.Save(ctx, cp)
	cs.Save(ctx, cp) // Same TurnUUID — should be no-op.

	got, _ := cs.Get(ctx, 1)
	if got.ResumeAttempts != 0 {
		t.Error("idempotent save should not modify checkpoint")
	}
}

func TestFileStore_CheckpointStore_MarkResumeAttempt(t *testing.T) {
	path := tempFilePath(t)
	fs, _ := OpenFileStore(path)
	cs := fs.CheckpointStore()
	ctx := context.Background()

	cs.Save(ctx, &Checkpoint{RunID: 5, BrainID: "c", State: "paused", TurnUUID: "u"})
	cs.MarkResumeAttempt(ctx, 5)
	cs.MarkResumeAttempt(ctx, 5)

	got, _ := cs.Get(ctx, 5)
	if got.ResumeAttempts != 2 {
		t.Errorf("ResumeAttempts=%d, want 2", got.ResumeAttempts)
	}
}

func TestFileStore_UsageLedger_RecordSum(t *testing.T) {
	path := tempFilePath(t)
	fs, _ := OpenFileStore(path)
	ledger := fs.Ledger()
	ctx := context.Background()

	ledger.Record(ctx, &UsageRecord{RunID: 1, InputTokens: 100, CostUSD: 0.01, IdempotencyKey: "k1"})
	ledger.Record(ctx, &UsageRecord{RunID: 1, InputTokens: 200, CostUSD: 0.02, IdempotencyKey: "k2"})
	// Duplicate — should be ignored.
	ledger.Record(ctx, &UsageRecord{RunID: 1, InputTokens: 999, CostUSD: 9.99, IdempotencyKey: "k1"})

	sum, _ := ledger.Sum(ctx, 1)
	if sum.InputTokens != 300 {
		t.Errorf("InputTokens=%d, want 300", sum.InputTokens)
	}
	if sum.CostUSD < 0.029 || sum.CostUSD > 0.031 {
		t.Errorf("CostUSD=%.4f, want ~0.03", sum.CostUSD)
	}
}

func TestFileStore_MetaStore_PutGet(t *testing.T) {
	path := tempFilePath(t)
	fs, _ := OpenFileStore(path)
	ms := fs.MetaStore()
	ctx := context.Background()

	meta := &ArtifactMeta{Ref: "sha256/abc123", MimeType: "text/plain", SizeBytes: 42}
	if err := ms.Put(ctx, meta); err != nil {
		t.Fatal(err)
	}

	got, err := ms.Get(ctx, "sha256/abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.MimeType != "text/plain" {
		t.Errorf("MimeType=%q, want text/plain", got.MimeType)
	}
	if got.RefCount != 1 {
		t.Errorf("RefCount=%d, want 1", got.RefCount)
	}
}

func TestFileStore_MetaStore_RefCount(t *testing.T) {
	path := tempFilePath(t)
	fs, _ := OpenFileStore(path)
	ms := fs.MetaStore()
	ctx := context.Background()

	ms.Put(ctx, &ArtifactMeta{Ref: "sha256/def456", SizeBytes: 10})
	ms.IncRefCount(ctx, "sha256/def456")

	got, _ := ms.Get(ctx, "sha256/def456")
	if got.RefCount != 2 {
		t.Errorf("RefCount=%d after inc, want 2", got.RefCount)
	}

	ms.DecRefCount(ctx, "sha256/def456")
	got, _ = ms.Get(ctx, "sha256/def456")
	if got.RefCount != 1 {
		t.Errorf("RefCount=%d after dec, want 1", got.RefCount)
	}
}

func TestFileStore_AtomicFile(t *testing.T) {
	path := tempFilePath(t)
	fs, _ := OpenFileStore(path)
	ps := fs.PlanStore()
	ctx := context.Background()

	ps.Create(ctx, &BrainPlan{BrainID: "x", Version: 1, CurrentState: json.RawMessage(`{}`)})

	// Verify the file exists and is valid JSON.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var db fileDB
	if err := json.Unmarshal(data, &db); err != nil {
		t.Fatalf("invalid JSON on disk: %v", err)
	}
	if len(db.Plans) != 1 {
		t.Errorf("plans on disk=%d, want 1", len(db.Plans))
	}
}
