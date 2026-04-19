package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ── Helper ──────────────────────────────────────────────────────────────

func openSQLiteTest(t *testing.T) *ClosableStores {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	cs, err := Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("Open(sqlite): %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// ── Driver registration ─────────────────────────────────────────────────

func TestSQLiteDriverRegistered(t *testing.T) {
	names := Drivers()
	found := false
	for _, n := range names {
		if n == "sqlite" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'sqlite' in Drivers()")
	}
}

func TestSQLiteDriverOpen(t *testing.T) {
	cs := openSQLiteTest(t)
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

// ── Plan CRUD ───────────────────────────────────────────────────────────

func TestSQLitePlanCreate(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	plan := &BrainPlan{
		RunID:        1,
		BrainID:      "central",
		CurrentState: json.RawMessage(`{"step":1}`),
	}
	id, err := cs.PlanStore.Create(ctx, plan)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	got, err := cs.PlanStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BrainID != "central" {
		t.Errorf("BrainID = %q, want %q", got.BrainID, "central")
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if string(got.CurrentState) != `{"step":1}` {
		t.Errorf("CurrentState = %s, want %s", got.CurrentState, `{"step":1}`)
	}
}

func TestSQLitePlanCreateWithID(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	plan := &BrainPlan{
		ID:           42,
		RunID:        1,
		BrainID:      "test",
		CurrentState: json.RawMessage(`{}`),
	}
	id, err := cs.PlanStore.Create(ctx, plan)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}

	got, err := cs.PlanStore.Get(ctx, 42)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BrainID != "test" {
		t.Errorf("BrainID = %q, want %q", got.BrainID, "test")
	}
}

func TestSQLitePlanUpdate(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	plan := &BrainPlan{
		RunID:        1,
		BrainID:      "central",
		CurrentState: json.RawMessage(`{"step":1}`),
	}
	id, err := cs.PlanStore.Create(ctx, plan)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update with version 2.
	delta := &BrainPlanDelta{
		Version: 2,
		OpType:  "replace",
		Payload: json.RawMessage(`{"step":2}`),
		Actor:   "test",
	}
	if err := cs.PlanStore.Update(ctx, id, delta); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := cs.PlanStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
	if string(got.CurrentState) != `{"step":2}` {
		t.Errorf("CurrentState = %s, want %s", got.CurrentState, `{"step":2}`)
	}
}

func TestSQLitePlanUpdateVersionMismatch(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	plan := &BrainPlan{RunID: 1, BrainID: "test"}
	id, _ := cs.PlanStore.Create(ctx, plan)

	delta := &BrainPlanDelta{Version: 99, OpType: "replace"}
	err := cs.PlanStore.Update(ctx, id, delta)
	if err == nil {
		t.Fatal("expected error for version mismatch")
	}
}

func TestSQLitePlanArchive(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	plan := &BrainPlan{RunID: 1, BrainID: "test"}
	id, _ := cs.PlanStore.Create(ctx, plan)

	if err := cs.PlanStore.Archive(ctx, id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	got, err := cs.PlanStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after archive: %v", err)
	}
	if !got.Archived {
		t.Error("expected Archived = true")
	}

	// Update on archived plan should fail.
	delta := &BrainPlanDelta{Version: 2, OpType: "replace"}
	err = cs.PlanStore.Update(ctx, id, delta)
	if err == nil {
		t.Fatal("expected error when updating archived plan")
	}
}

func TestSQLitePlanListByRun(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		plan := &BrainPlan{
			RunID:   42,
			BrainID: fmt.Sprintf("brain-%d", i),
		}
		if _, err := cs.PlanStore.Create(ctx, plan); err != nil {
			t.Fatalf("Create[%d]: %v", i, err)
		}
	}
	// Create one for a different run.
	if _, err := cs.PlanStore.Create(ctx, &BrainPlan{RunID: 99, BrainID: "other"}); err != nil {
		t.Fatalf("Create other: %v", err)
	}

	list, err := cs.PlanStore.ListByRun(ctx, 42)
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("ListByRun len = %d, want 3", len(list))
	}
}

func TestSQLitePlanGetNotFound(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	_, err := cs.PlanStore.Get(ctx, 999)
	if err == nil {
		t.Fatal("expected error for missing plan")
	}
}

// ── Checkpoint Save/Load ────────────────────────────────────────────────

func TestSQLiteCheckpointSaveLoad(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	cp := &Checkpoint{
		RunID:    100,
		BrainID:  "central",
		State:    "Running",
		TurnUUID: "uuid-1",
		TurnIndex: 5,
	}
	if err := cs.RunCheckpointStore.Save(ctx, cp); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := cs.RunCheckpointStore.Get(ctx, 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BrainID != "central" {
		t.Errorf("BrainID = %q, want %q", got.BrainID, "central")
	}
	if got.State != "Running" {
		t.Errorf("State = %q, want %q", got.State, "Running")
	}
	if got.TurnIndex != 5 {
		t.Errorf("TurnIndex = %d, want 5", got.TurnIndex)
	}
}

func TestSQLiteCheckpointIdempotent(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	cp := &Checkpoint{
		RunID:    100,
		BrainID:  "central",
		State:    "Running",
		TurnUUID: "uuid-1",
	}
	if err := cs.RunCheckpointStore.Save(ctx, cp); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Save again with same TurnUUID — should be no-op.
	if err := cs.RunCheckpointStore.Save(ctx, cp); err != nil {
		t.Fatalf("Save idempotent: %v", err)
	}
}

func TestSQLiteCheckpointOverwrite(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	cp1 := &Checkpoint{
		RunID:    100,
		BrainID:  "central",
		State:    "Running",
		TurnUUID: "uuid-1",
		TurnIndex: 0,
	}
	if err := cs.RunCheckpointStore.Save(ctx, cp1); err != nil {
		t.Fatalf("Save cp1: %v", err)
	}

	// Save with different TurnUUID — should overwrite.
	cp2 := &Checkpoint{
		RunID:    100,
		BrainID:  "central",
		State:    "Paused",
		TurnUUID: "uuid-2",
		TurnIndex: 1,
	}
	if err := cs.RunCheckpointStore.Save(ctx, cp2); err != nil {
		t.Fatalf("Save cp2: %v", err)
	}

	got, err := cs.RunCheckpointStore.Get(ctx, 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != "Paused" {
		t.Errorf("State = %q, want %q", got.State, "Paused")
	}
	if got.TurnUUID != "uuid-2" {
		t.Errorf("TurnUUID = %q, want %q", got.TurnUUID, "uuid-2")
	}
}

func TestSQLiteCheckpointGetNotFound(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	_, err := cs.RunCheckpointStore.Get(ctx, 999)
	if err == nil {
		t.Fatal("expected error for missing checkpoint")
	}
}

func TestSQLiteCheckpointMarkResumeAttempt(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	cp := &Checkpoint{
		RunID:    100,
		BrainID:  "central",
		State:    "Paused",
		TurnUUID: "uuid-1",
	}
	if err := cs.RunCheckpointStore.Save(ctx, cp); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := cs.RunCheckpointStore.MarkResumeAttempt(ctx, 100); err != nil {
		t.Fatalf("MarkResumeAttempt: %v", err)
	}

	got, err := cs.RunCheckpointStore.Get(ctx, 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ResumeAttempts != 1 {
		t.Errorf("ResumeAttempts = %d, want 1", got.ResumeAttempts)
	}
}

// ── Usage Record / TotalCost ────────────────────────────────────────────

func TestSQLiteUsageRecord(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	rec := &UsageRecord{
		RunID:          1,
		Provider:       "anthropic",
		Model:          "claude-4",
		InputTokens:    100,
		OutputTokens:   50,
		CostUSD:        0.01,
		IdempotencyKey: "key-1",
	}
	if err := cs.UsageLedger.Record(ctx, rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	sum, err := cs.UsageLedger.Sum(ctx, 1)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", sum.InputTokens)
	}
	if sum.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", sum.OutputTokens)
	}
	if sum.TurnIndex != -1 {
		t.Errorf("TurnIndex = %d, want -1 (aggregate)", sum.TurnIndex)
	}
}

func TestSQLiteUsageRecordIdempotent(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	rec := &UsageRecord{
		RunID:          1,
		InputTokens:    100,
		CostUSD:        0.01,
		IdempotencyKey: "key-dup",
	}
	if err := cs.UsageLedger.Record(ctx, rec); err != nil {
		t.Fatalf("Record 1: %v", err)
	}
	// Duplicate — should be silently ignored.
	if err := cs.UsageLedger.Record(ctx, rec); err != nil {
		t.Fatalf("Record 2 (dup): %v", err)
	}

	sum, err := cs.UsageLedger.Sum(ctx, 1)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	// Should only count once.
	if sum.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (not 200 from double-count)", sum.InputTokens)
	}
}

func TestSQLiteUsageMultipleRecords(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		rec := &UsageRecord{
			RunID:          1,
			InputTokens:    100,
			OutputTokens:   50,
			CostUSD:        0.01,
			IdempotencyKey: fmt.Sprintf("key-%d", i),
		}
		if err := cs.UsageLedger.Record(ctx, rec); err != nil {
			t.Fatalf("Record[%d]: %v", i, err)
		}
	}

	sum, err := cs.UsageLedger.Sum(ctx, 1)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500", sum.InputTokens)
	}
	if sum.OutputTokens != 250 {
		t.Errorf("OutputTokens = %d, want 250", sum.OutputTokens)
	}
}

func TestSQLiteUsageSumEmptyRun(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	sum, err := cs.UsageLedger.Sum(ctx, 999)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum.InputTokens != 0 || sum.CostUSD != 0 {
		t.Errorf("expected zero aggregate for empty run, got input=%d cost=%f",
			sum.InputTokens, sum.CostUSD)
	}
}

// ── Concurrent read/write safety ────────────────────────────────────────

func TestSQLiteConcurrentPlanCreate(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			plan := &BrainPlan{
				RunID:   int64(i),
				BrainID: fmt.Sprintf("brain-%d", i),
			}
			_, err := cs.PlanStore.Create(ctx, plan)
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Create error: %v", err)
	}
}

func TestSQLiteConcurrentUsageRecord(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := &UsageRecord{
				RunID:          1,
				InputTokens:    10,
				CostUSD:        0.001,
				IdempotencyKey: fmt.Sprintf("concurrent-key-%d", i),
			}
			if err := cs.UsageLedger.Record(ctx, rec); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Record error: %v", err)
	}

	sum, err := cs.UsageLedger.Sum(ctx, 1)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum.InputTokens != int64(n*10) {
		t.Errorf("InputTokens = %d, want %d", sum.InputTokens, n*10)
	}
}

func TestSQLiteConcurrentCheckpoint(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n*2)

	// Concurrent saves to different run IDs.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cp := &Checkpoint{
				RunID:    int64(i + 1),
				BrainID:  "test",
				State:    "Running",
				TurnUUID: fmt.Sprintf("uuid-%d", i),
			}
			if err := cs.RunCheckpointStore.Save(ctx, cp); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()

	// Concurrent reads.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := cs.RunCheckpointStore.Get(ctx, int64(i+1))
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent checkpoint error: %v", err)
	}
}

// ── ArtifactMeta ────────────────────────────────────────────────────────

func TestSQLiteArtifactMetaPutGet(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	runID := int64(1)
	turnIdx := 0
	meta := &ArtifactMeta{
		Ref:       Ref("sha256/abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		MimeType:  "text/plain",
		SizeBytes: 42,
		RunID:     &runID,
		TurnIndex: &turnIdx,
		Caption:   "test artifact",
	}
	if err := cs.ArtifactMeta.Put(ctx, meta); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := cs.ArtifactMeta.Get(ctx, meta.Ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MimeType != "text/plain" {
		t.Errorf("MimeType = %q, want %q", got.MimeType, "text/plain")
	}
	if got.SizeBytes != 42 {
		t.Errorf("SizeBytes = %d, want 42", got.SizeBytes)
	}
	if got.Caption != "test artifact" {
		t.Errorf("Caption = %q, want %q", got.Caption, "test artifact")
	}
}

func TestSQLiteArtifactMetaRefCount(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	ref := Ref("sha256/0000000000000000000000000000000000000000000000000000000000000000")
	meta := &ArtifactMeta{
		Ref:      ref,
		MimeType: "application/octet-stream",
	}
	if err := cs.ArtifactMeta.Put(ctx, meta); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Inc twice.
	if err := cs.ArtifactMeta.IncRefCount(ctx, ref); err != nil {
		t.Fatalf("IncRefCount 1: %v", err)
	}
	if err := cs.ArtifactMeta.IncRefCount(ctx, ref); err != nil {
		t.Fatalf("IncRefCount 2: %v", err)
	}

	got, _ := cs.ArtifactMeta.Get(ctx, ref)
	if got.RefCount != 2 {
		t.Errorf("RefCount = %d, want 2", got.RefCount)
	}

	// Dec once.
	if err := cs.ArtifactMeta.DecRefCount(ctx, ref); err != nil {
		t.Fatalf("DecRefCount: %v", err)
	}

	got, _ = cs.ArtifactMeta.Get(ctx, ref)
	if got.RefCount != 1 {
		t.Errorf("RefCount = %d, want 1", got.RefCount)
	}
}

func TestSQLiteArtifactMetaDecZero(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	ref := Ref("sha256/1111111111111111111111111111111111111111111111111111111111111111")
	meta := &ArtifactMeta{Ref: ref, MimeType: "text/plain"}
	if err := cs.ArtifactMeta.Put(ctx, meta); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// RefCount is 0, DecRefCount should fail.
	err := cs.ArtifactMeta.DecRefCount(ctx, ref)
	if err == nil {
		t.Fatal("expected error when decrementing zero refcount")
	}
}

// ── Full round-trip via driver ──────────────────────────────────────────

func TestSQLiteDriverRoundTrip(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()

	// Plan + Update + Archive.
	plan := &BrainPlan{RunID: 1, BrainID: "full-test", CurrentState: json.RawMessage(`{"a":1}`)}
	id, err := cs.PlanStore.Create(ctx, plan)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	delta := &BrainPlanDelta{Version: 2, OpType: "patch", Payload: json.RawMessage(`{"a":2}`), Actor: "test"}
	if err := cs.PlanStore.Update(ctx, id, delta); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := cs.PlanStore.Archive(ctx, id); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	got, _ := cs.PlanStore.Get(ctx, id)
	if !got.Archived || got.Version != 2 {
		t.Errorf("unexpected plan state: archived=%v version=%d", got.Archived, got.Version)
	}

	// Checkpoint.
	cp := &Checkpoint{RunID: 1, BrainID: "full-test", State: "Running", TurnUUID: "u1"}
	if err := cs.RunCheckpointStore.Save(ctx, cp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	gotCP, _ := cs.RunCheckpointStore.Get(ctx, 1)
	if gotCP.State != "Running" {
		t.Errorf("checkpoint state = %q, want Running", gotCP.State)
	}

	// Usage.
	rec := &UsageRecord{RunID: 1, InputTokens: 500, CostUSD: 0.05, IdempotencyKey: "final"}
	if err := cs.UsageLedger.Record(ctx, rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	sum, _ := cs.UsageLedger.Sum(ctx, 1)
	if sum.InputTokens != 500 {
		t.Errorf("sum input = %d, want 500", sum.InputTokens)
	}
}

// ── RunStore tests ─────────────────────────────────────────────────────

func TestSQLiteRunStoreCreateGet(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	rs := cs.RunStore

	run := &Run{
		RunKey:  "run-001",
		BrainID: "central",
		Prompt:  "hello",
		Status:  "running",
		Mode:    "interactive",
		Events: []RunEvent{
			{Type: "run.created", Message: "created"},
		},
	}
	id, err := rs.Create(ctx, run)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	got, err := rs.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RunKey != "run-001" {
		t.Errorf("RunKey = %q, want run-001", got.RunKey)
	}
	if got.BrainID != "central" {
		t.Errorf("BrainID = %q, want central", got.BrainID)
	}
	if len(got.Events) != 1 {
		t.Errorf("events count = %d, want 1", len(got.Events))
	}
}

func TestSQLiteRunStoreGetByKey(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	rs := cs.RunStore

	run := &Run{RunKey: "key-abc", BrainID: "central", Status: "running"}
	_, err := rs.Create(ctx, run)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := rs.GetByKey(ctx, "key-abc")
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	if got.RunKey != "key-abc" {
		t.Errorf("RunKey = %q, want key-abc", got.RunKey)
	}
}

func TestSQLiteRunStoreUpdateAndFinish(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	rs := cs.RunStore

	run := &Run{RunKey: "run-upd", BrainID: "central", Status: "running"}
	id, _ := rs.Create(ctx, run)

	err := rs.Update(ctx, id, func(r *Run) {
		r.TurnUUID = "turn-42"
		r.PlanID = 99
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := rs.Get(ctx, id)
	if got.TurnUUID != "turn-42" {
		t.Errorf("TurnUUID = %q, want turn-42", got.TurnUUID)
	}

	result := json.RawMessage(`{"ok":true}`)
	err = rs.Finish(ctx, id, "completed", result, "")
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	got, _ = rs.Get(ctx, id)
	if got.Status != "completed" {
		t.Errorf("Status = %q, want completed", got.Status)
	}
	if len(got.Events) == 0 {
		t.Error("expected at least one event after Finish")
	}
}

func TestSQLiteRunStoreAppendEventAndList(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	rs := cs.RunStore

	for i := 0; i < 3; i++ {
		run := &Run{RunKey: fmt.Sprintf("run-list-%d", i), BrainID: "central", Status: "running"}
		rs.Create(ctx, run)
	}

	// Append event to first run
	err := rs.AppendEvent(ctx, 1, RunEvent{Type: "custom", Message: "test event"})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	runs, err := rs.List(ctx, 10, "all")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runs) != 3 {
		t.Errorf("List count = %d, want 3", len(runs))
	}

	// Filter by status
	rs.Finish(ctx, 1, "completed", nil, "")
	runs, _ = rs.List(ctx, 10, "running")
	if len(runs) != 2 {
		t.Errorf("List(running) = %d, want 2", len(runs))
	}
}

// ── AuditLogger tests ──────────────────────────────────────────────────

func TestSQLiteAuditLoggerLogAndQuery(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	al := cs.AuditLogger

	ev := &AuditEvent{
		EventID:     "evt-001",
		ExecutionID: "exec-1",
		EventType:   "tool.exec",
		Actor:       "brain-central",
		StatusCode:  "success",
		Details:     "ran bash",
		Data:        json.RawMessage(`{"cmd":"ls"}`),
	}
	if err := al.Log(ctx, ev); err != nil {
		t.Fatalf("Log: %v", err)
	}

	results, err := al.Query(ctx, AuditFilter{ExecutionID: "exec-1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Query count = %d, want 1", len(results))
	}
	if results[0].EventType != "tool.exec" {
		t.Errorf("EventType = %q, want tool.exec", results[0].EventType)
	}
	if results[0].Actor != "brain-central" {
		t.Errorf("Actor = %q, want brain-central", results[0].Actor)
	}
}

func TestSQLiteAuditLoggerQueryFilter(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	al := cs.AuditLogger

	for i := 0; i < 5; i++ {
		actor := "brain-a"
		if i >= 3 {
			actor = "brain-b"
		}
		al.Log(ctx, &AuditEvent{
			EventID:     fmt.Sprintf("evt-%d", i),
			ExecutionID: "exec-2",
			EventType:   "tool.exec",
			Actor:       actor,
			StatusCode:  "success",
		})
	}

	results, _ := al.Query(ctx, AuditFilter{Actor: "brain-b"})
	if len(results) != 2 {
		t.Errorf("Query(actor=brain-b) = %d, want 2", len(results))
	}

	results, _ = al.Query(ctx, AuditFilter{Limit: 3})
	if len(results) != 3 {
		t.Errorf("Query(limit=3) = %d, want 3", len(results))
	}
}

// ── LearningStore tests ─────────────────────────────────────────────────

func TestSQLiteLearningStoreProfileRoundTrip(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	ls := cs.LearningStore

	// Save profile
	err := ls.SaveProfile(ctx, &LearningProfile{BrainKind: "code", ColdStart: true})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	// Save task score
	err = ls.SaveTaskScore(ctx, &LearningTaskScore{
		BrainKind: "code", TaskType: "refactor", SampleCount: 10,
		AccuracyValue: 0.8, AccuracyAlpha: 0.2,
		SpeedValue: 0.6, SpeedAlpha: 0.2,
		CostValue: 0.5, CostAlpha: 0.2,
		StabilityValue: 0.9, StabilityAlpha: 0.2,
	})
	if err != nil {
		t.Fatalf("SaveTaskScore: %v", err)
	}

	profiles, err := ls.ListProfiles(ctx)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(profiles) != 1 || profiles[0].BrainKind != "code" {
		t.Fatalf("profiles = %+v, want [code]", profiles)
	}

	scores, err := ls.ListTaskScores(ctx, "code")
	if err != nil {
		t.Fatalf("ListTaskScores: %v", err)
	}
	if len(scores) != 1 || scores[0].SampleCount != 10 {
		t.Fatalf("scores = %+v, want [{SampleCount:10}]", scores)
	}

	// Upsert
	ls.SaveTaskScore(ctx, &LearningTaskScore{
		BrainKind: "code", TaskType: "refactor", SampleCount: 20,
		AccuracyValue: 0.9, AccuracyAlpha: 0.2,
	})
	scores, _ = ls.ListTaskScores(ctx, "code")
	if scores[0].SampleCount != 20 {
		t.Errorf("after upsert, SampleCount = %d, want 20", scores[0].SampleCount)
	}
}

func TestSQLiteLearningStoreSequenceRoundTrip(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	ls := cs.LearningStore

	seq := &LearningSequence{
		SequenceID: "seq-1",
		TotalScore: 0.85,
		Steps: []LearningSeqStep{
			{BrainKind: "code", TaskType: "plan", DurationMs: 1000, Score: 0.9},
			{BrainKind: "data", TaskType: "fetch", DurationMs: 2000, Score: 0.8},
		},
	}
	err := ls.SaveSequence(ctx, seq)
	if err != nil {
		t.Fatalf("SaveSequence: %v", err)
	}

	seqs, err := ls.ListSequences(ctx, 10)
	if err != nil {
		t.Fatalf("ListSequences: %v", err)
	}
	if len(seqs) != 1 {
		t.Fatalf("sequences = %d, want 1", len(seqs))
	}
	if len(seqs[0].Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(seqs[0].Steps))
	}
}

func TestSQLiteLearningStorePreferenceRoundTrip(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	ls := cs.LearningStore

	err := ls.SavePreference(ctx, &LearningPreference{
		Category: "verbosity", Value: "concise", Weight: 0.8,
	})
	if err != nil {
		t.Fatalf("SavePreference: %v", err)
	}

	pref, err := ls.GetPreference(ctx, "verbosity")
	if err != nil {
		t.Fatalf("GetPreference: %v", err)
	}
	if pref == nil || pref.Value != "concise" {
		t.Fatalf("pref = %+v, want concise", pref)
	}

	// Not found
	pref, _ = ls.GetPreference(ctx, "nonexistent")
	if pref != nil {
		t.Errorf("expected nil for nonexistent, got %+v", pref)
	}

	// List all
	ls.SavePreference(ctx, &LearningPreference{Category: "format", Value: "json", Weight: 0.5})
	prefs, _ := ls.ListPreferences(ctx)
	if len(prefs) != 2 {
		t.Errorf("ListPreferences = %d, want 2", len(prefs))
	}
}

// ── SharedMessageStore tests ────────────────────────────────────────────

func TestSQLiteSharedMessageStoreSaveAndList(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	sms := cs.SharedMessageStore

	err := sms.Save(ctx, &SharedMessage{
		FromBrain: "central",
		ToBrain:   "code",
		Messages:  json.RawMessage(`[{"role":"user","content":"hello"}]`),
		Count:     1,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	msgs, err := sms.ListByBrains(ctx, "central", "code", 10)
	if err != nil {
		t.Fatalf("ListByBrains: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("count = %d, want 1", len(msgs))
	}
	if msgs[0].Count != 1 {
		t.Errorf("Count = %d, want 1", msgs[0].Count)
	}

	// ListRecent
	sms.Save(ctx, &SharedMessage{FromBrain: "code", ToBrain: "verifier", Count: 2})
	recent, _ := sms.ListRecent(ctx, 10)
	if len(recent) != 2 {
		t.Errorf("ListRecent = %d, want 2", len(recent))
	}
}

func TestSQLiteAuditLoggerPurge(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	al := cs.AuditLogger

	al.Log(ctx, &AuditEvent{
		EventID:    "old-evt",
		EventType:  "test",
		StatusCode: "success",
	})

	// Purge with 0 days should remove everything
	deleted, err := al.Purge(ctx, 0)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Purge deleted = %d, want 1", deleted)
	}

	results, _ := al.Query(ctx, AuditFilter{})
	if len(results) != 0 {
		t.Errorf("after purge, count = %d, want 0", len(results))
	}
}

// ── P3.0 bootstrap — AnomalyTemplate / SiteAnomalyProfile / PatternFailureSample
// / HumanDemoSequence / SitemapSnapshot tests ──────────────────────────

func TestSQLiteAnomalyTemplateRoundTrip(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	ls := cs.LearningStore

	tpl := &AnomalyTemplate{
		SignatureType:     "ui_injection",
		SignatureSubtype:  "urgent_cta",
		SignatureSite:     "example.com",
		SignatureSeverity: "high",
		RecoveryActions:   json.RawMessage(`[{"tool":"browser.close_modal"}]`),
		MatchCount:        3,
		SuccessCount:      2,
		FailureCount:      1,
	}
	if err := ls.SaveAnomalyTemplate(ctx, tpl); err != nil {
		t.Fatalf("SaveAnomalyTemplate: %v", err)
	}
	if tpl.ID == 0 {
		t.Fatal("expected non-zero ID after save")
	}

	got, err := ls.GetAnomalyTemplate(ctx, tpl.ID)
	if err != nil {
		t.Fatalf("GetAnomalyTemplate: %v", err)
	}
	if got == nil || got.SignatureSubtype != "urgent_cta" || got.MatchCount != 3 {
		t.Fatalf("GetAnomalyTemplate = %+v", got)
	}
	if string(got.RecoveryActions) != `[{"tool":"browser.close_modal"}]` {
		t.Errorf("RecoveryActions = %s", got.RecoveryActions)
	}

	list, err := ls.ListAnomalyTemplates(ctx)
	if err != nil {
		t.Fatalf("ListAnomalyTemplates: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d, want 1", len(list))
	}

	// Upsert by ID: same template, updated counts.
	tpl.SuccessCount = 5
	if err := ls.SaveAnomalyTemplate(ctx, tpl); err != nil {
		t.Fatalf("SaveAnomalyTemplate (update): %v", err)
	}
	list2, _ := ls.ListAnomalyTemplates(ctx)
	if len(list2) != 1 {
		t.Fatalf("after update, list = %d, want 1", len(list2))
	}
	if list2[0].SuccessCount != 5 {
		t.Errorf("SuccessCount = %d, want 5", list2[0].SuccessCount)
	}

	// Delete
	if err := ls.DeleteAnomalyTemplate(ctx, tpl.ID); err != nil {
		t.Fatalf("DeleteAnomalyTemplate: %v", err)
	}
	got2, _ := ls.GetAnomalyTemplate(ctx, tpl.ID)
	if got2 != nil {
		t.Errorf("after delete, got = %+v, want nil", got2)
	}
}

func TestSQLiteSiteAnomalyProfileUpsert(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	ls := cs.LearningStore

	p := &SiteAnomalyProfile{
		SiteOrigin:          "https://shop.example.com",
		AnomalyType:         "ui_injection",
		AnomalySubtype:      "urgent_cta",
		Frequency:           7,
		AvgDurationMs:       1200,
		RecoverySuccessRate: 0.71,
	}
	if err := ls.UpsertSiteAnomalyProfile(ctx, p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Upsert again → should merge into same row (key: site+type+subtype).
	p.Frequency = 12
	p.RecoverySuccessRate = 0.83
	if err := ls.UpsertSiteAnomalyProfile(ctx, p); err != nil {
		t.Fatalf("Upsert (2): %v", err)
	}

	list, err := ls.ListSiteAnomalyProfiles(ctx, "https://shop.example.com")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d, want 1 (upsert should not dup)", len(list))
	}
	if list[0].Frequency != 12 || list[0].RecoverySuccessRate != 0.83 {
		t.Errorf("after upsert, got = %+v", list[0])
	}

	// Filter by empty site returns all.
	ls.UpsertSiteAnomalyProfile(ctx, &SiteAnomalyProfile{
		SiteOrigin: "https://other.example.com", AnomalyType: "timeout", AnomalySubtype: "navigation",
		Frequency: 1,
	})
	all, _ := ls.ListSiteAnomalyProfiles(ctx, "")
	if len(all) != 2 {
		t.Errorf("list all = %d, want 2", len(all))
	}
}

func TestSQLitePatternFailureSampleRoundTrip(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	ls := cs.LearningStore

	s1 := &PatternFailureSample{
		PatternID:       "pat-login-v2",
		SiteOrigin:      "https://a.example.com",
		AnomalySubtype:  "captcha",
		FailureStep:     3,
		PageFingerprint: json.RawMessage(`{"dom_hash":"abc"}`),
	}
	if err := ls.SavePatternFailureSample(ctx, s1); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if s1.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	ls.SavePatternFailureSample(ctx, &PatternFailureSample{
		PatternID: "pat-login-v2", SiteOrigin: "https://b.example.com", FailureStep: 1,
	})
	ls.SavePatternFailureSample(ctx, &PatternFailureSample{
		PatternID: "pat-checkout", SiteOrigin: "https://c.example.com", FailureStep: 5,
	})

	list, err := ls.ListPatternFailureSamples(ctx, "pat-login-v2")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list(pat-login-v2) = %d, want 2", len(list))
	}

	// Empty filter → all samples.
	all, _ := ls.ListPatternFailureSamples(ctx, "")
	if len(all) != 3 {
		t.Errorf("list all = %d, want 3", len(all))
	}

	// Fingerprint round-trips intact.
	if string(list[1].PageFingerprint) != `{"dom_hash":"abc"}` {
		t.Errorf("fingerprint lost: %s", list[1].PageFingerprint)
	}
}

func TestSQLiteHumanDemoSequenceRoundTrip(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	ls := cs.LearningStore

	seq := &HumanDemoSequence{
		RunID:     "run-1",
		BrainKind: "browser",
		Goal:      "recover from paywall",
		Site:      "https://news.example.com",
		URL:       "https://news.example.com/article/42",
		Actions:   json.RawMessage(`[{"tool":"browser.click","selector":"#close"}]`),
		Approved:  false,
	}
	if err := ls.SaveHumanDemoSequence(ctx, seq); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ls.SaveHumanDemoSequence(ctx, &HumanDemoSequence{
		RunID: "run-2", BrainKind: "browser", Approved: true,
		Actions: json.RawMessage(`[]`),
	})

	all, err := ls.ListHumanDemoSequences(ctx, false)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("list all = %d, want 2", len(all))
	}

	approved, _ := ls.ListHumanDemoSequences(ctx, true)
	if len(approved) != 1 {
		t.Fatalf("approved = %d, want 1", len(approved))
	}
	if !approved[0].Approved {
		t.Error("approved filter returned non-approved row")
	}
	if string(all[1].Actions) != `[{"tool":"browser.click","selector":"#close"}]` {
		t.Errorf("actions lost: %s", all[1].Actions)
	}
}

func TestSQLiteSitemapSnapshotRoundTripAndPurge(t *testing.T) {
	cs := openSQLiteTest(t)
	ctx := context.Background()
	ls := cs.LearningStore

	snap := &SitemapSnapshot{
		SiteOrigin: "https://shop.example.com",
		Depth:      2,
		URLs:       json.RawMessage(`["/a","/b","/c"]`),
	}
	if err := ls.SaveSitemapSnapshot(ctx, snap); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := ls.GetSitemapSnapshot(ctx, "https://shop.example.com", 2)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if string(got.URLs) != `["/a","/b","/c"]` {
		t.Errorf("URLs lost: %s", got.URLs)
	}

	// Missing key returns nil without error.
	missing, err := ls.GetSitemapSnapshot(ctx, "https://shop.example.com", 99)
	if err != nil {
		t.Fatalf("Get(missing): %v", err)
	}
	if missing != nil {
		t.Errorf("missing snapshot = %+v, want nil", missing)
	}

	// Second save at same (site, depth) → Get returns the newer row.
	snap2 := &SitemapSnapshot{
		SiteOrigin: "https://shop.example.com",
		Depth:      2,
		URLs:       json.RawMessage(`["/a","/b","/c","/d"]`),
	}
	ls.SaveSitemapSnapshot(ctx, snap2)
	got2, _ := ls.GetSitemapSnapshot(ctx, "https://shop.example.com", 2)
	if string(got2.URLs) != `["/a","/b","/c","/d"]` {
		t.Errorf("expected newer snapshot, got %s", got2.URLs)
	}

	// Purge with a future cutoff removes everything.
	future := time.Now().UTC().Add(1 * time.Hour)
	n, err := ls.PurgeSitemapSnapshots(ctx, future)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n != 2 {
		t.Errorf("Purge n = %d, want 2", n)
	}

	after, _ := ls.GetSitemapSnapshot(ctx, "https://shop.example.com", 2)
	if after != nil {
		t.Errorf("after purge, snapshot = %+v, want nil", after)
	}
}

// Schema migration idempotency: reopening the same DSN must not error.
func TestSQLiteSchemaIdempotent(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "idem.db")

	cs, err := Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("Open (1): %v", err)
	}
	// Write one row so we can verify the reopen still sees it.
	err = cs.LearningStore.SaveAnomalyTemplate(context.Background(), &AnomalyTemplate{
		SignatureType: "persist_check",
	})
	if err != nil {
		t.Fatalf("SaveAnomalyTemplate: %v", err)
	}
	cs.Close()

	cs2, err := Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("Open (2): %v", err)
	}
	defer cs2.Close()

	list, err := cs2.LearningStore.ListAnomalyTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListAnomalyTemplates after reopen: %v", err)
	}
	if len(list) != 1 || list[0].SignatureType != "persist_check" {
		t.Errorf("after reopen, list = %+v", list)
	}
}
