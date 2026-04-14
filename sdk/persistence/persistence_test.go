package persistence

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// ── C-P-02: ComputeKey dedup ─────────────────────────────────────────────────

// TestComputeKeyDedup_C_P_02 verifies that 1000 distinct random byte slices
// produce 1000 distinct Refs and that the same input always maps to the
// same Ref.
func TestComputeKeyDedup_C_P_02(t *testing.T) {
	const n = 1000
	seen := make(map[Ref]struct{}, n)

	for i := 0; i < n; i++ {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			t.Fatalf("rand.Read: %v", err)
		}
		ref := ComputeKey(buf)
		// Same input → same ref (determinism).
		if ComputeKey(buf) != ref {
			t.Fatalf("ComputeKey is not deterministic for input #%d", i)
		}
		// Must not have appeared before (no collision).
		if _, dup := seen[ref]; dup {
			t.Fatalf("ComputeKey collision detected at input #%d", i)
		}
		seen[ref] = struct{}{}
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct refs, got %d", n, len(seen))
	}
}

// ── C-P-03: MemArtifactStore concurrent Put dedup ────────────────────────────

// TestMemArtifactStoreConcurrentPutDedup_C_P_03 launches 200 goroutines that
// all call Put with byte-identical content and asserts exactly 1 byte copy
// stored plus a RefCount equal to 200.
func TestMemArtifactStoreConcurrentPutDedup_C_P_03(t *testing.T) {
	meta := NewMemArtifactMetaStore(nil)
	store := NewMemArtifactStore(meta, nil)
	ctx := context.Background()

	content := []byte("deduplicated-payload")
	const workers = 200

	var wg sync.WaitGroup
	refs := make([]Ref, workers)
	errs := make([]error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			refs[i], errs[i] = store.Put(ctx, 1, Artifact{Kind: "text", Content: content})
		}()
	}
	wg.Wait()

	// All goroutines must succeed and return the same Ref.
	expectedRef := ComputeKey(content)
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Put returned error: %v", i, err)
		}
		if refs[i] != expectedRef {
			t.Errorf("goroutine %d: got ref %q, want %q", i, refs[i], expectedRef)
		}
	}

	// Byte backend must hold exactly one copy.
	exists, err := store.Exists(ctx, expectedRef)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("byte backend: expected ref to exist after 200 Puts")
	}

	// RefCount must equal workers (one per Put).
	m, err := meta.Get(ctx, expectedRef)
	if err != nil {
		t.Fatalf("meta.Get: %v", err)
	}
	if m.RefCount != workers {
		t.Errorf("RefCount = %d, want %d", m.RefCount, workers)
	}
}

// ── C-P-05: GC does not misfire on RefCount transitions ──────────────────────

// TestMemArtifactMetaStoreGCDoesNotMisfire_C_P_05 exercises the RefCount
// lifecycle: 1→2→1→0 leaving the row present (GC is caller's job), then a
// Dec on a zero-count row must return CodeInvariantViolated.
func TestMemArtifactMetaStoreGCDoesNotMisfire_C_P_05(t *testing.T) {
	s := NewMemArtifactMetaStore(nil)
	ctx := context.Background()
	ref := Ref("sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Insert with RefCount=1.
	if err := s.Put(ctx, &ArtifactMeta{Ref: ref, RefCount: 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Inc → 2.
	if err := s.IncRefCount(ctx, ref); err != nil {
		t.Fatalf("IncRefCount: %v", err)
	}

	// Dec → 1.
	if err := s.DecRefCount(ctx, ref); err != nil {
		t.Fatalf("DecRefCount (2→1): %v", err)
	}

	// Dec → 0.
	if err := s.DecRefCount(ctx, ref); err != nil {
		t.Fatalf("DecRefCount (1→0): %v", err)
	}

	// Row must still be present (GC is caller's responsibility).
	m, err := s.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get after dec to 0: %v", err)
	}
	if m.RefCount != 0 {
		t.Errorf("RefCount = %d, want 0", m.RefCount)
	}
	if !s.Exists(ref) {
		t.Error("Exists = false; row must survive until GC sweeps it")
	}

	// Dec again on zero → must return CodeInvariantViolated.
	err = s.DecRefCount(ctx, ref)
	if err == nil {
		t.Fatal("expected CodeInvariantViolated, got nil")
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok {
		t.Fatalf("expected *brainerrors.BrainError, got %T: %v", err, err)
	}
	if be.ErrorCode != brainerrors.CodeInvariantViolated {
		t.Errorf("ErrorCode = %q, want %q", be.ErrorCode, brainerrors.CodeInvariantViolated)
	}
}

// ── C-P-06: MemPlanStore turn-transaction atomicity ───────────────────────────

// TestMemPlanStoreTurnTransactionAtomicity_C_P_06 checks that an Update with a
// wrong Version leaves the plan's Version and CurrentState completely unchanged.
func TestMemPlanStoreTurnTransactionAtomicity_C_P_06(t *testing.T) {
	s := NewMemPlanStore(nil)
	ctx := context.Background()

	initialState := json.RawMessage(`{"items":[]}`)
	id, err := s.Create(ctx, &BrainPlan{RunID: 1, CurrentState: initialState})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	before, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get before: %v", err)
	}

	// Use wrong Version (skip version 2, send 5 instead).
	badDelta := &BrainPlanDelta{
		Version: 5,
		OpType:  "replace",
		Payload: json.RawMessage(`{"items":["corrupted"]}`),
	}
	if updateErr := s.Update(ctx, id, badDelta); updateErr == nil {
		t.Fatal("Update with wrong Version should fail but returned nil")
	}

	after, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after failed Update: %v", err)
	}
	if after.Version != before.Version {
		t.Errorf("Version changed: got %d, want %d", after.Version, before.Version)
	}
	if string(after.CurrentState) != string(before.CurrentState) {
		t.Errorf("CurrentState changed: got %q, want %q", after.CurrentState, before.CurrentState)
	}
}

// ── C-P-09: MemPlanStore optimistic lock conflict ────────────────────────────

// TestMemPlanStoreOptimisticLockConflict_C_P_09 races two goroutines that both
// attempt an Update with delta.Version=2 — exactly one must win, the other
// must get CodeDBDeadlock, and the final Version must be 2.
func TestMemPlanStoreOptimisticLockConflict_C_P_09(t *testing.T) {
	s := NewMemPlanStore(nil)
	ctx := context.Background()

	id, err := s.Create(ctx, &BrainPlan{RunID: 99})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var (
		successCount int64
		deadlockCount int64
		otherErrCount int64
		wg            sync.WaitGroup
	)
	wg.Add(2)
	run := func() {
		defer wg.Done()
		delta := &BrainPlanDelta{
			Version: 2,
			OpType:  "replace",
			Payload: json.RawMessage(`{"v":2}`),
		}
		e := s.Update(ctx, id, delta)
		if e == nil {
			atomic.AddInt64(&successCount, 1)
			return
		}
		if be, ok := e.(*brainerrors.BrainError); ok && be.ErrorCode == brainerrors.CodeDBDeadlock {
			atomic.AddInt64(&deadlockCount, 1)
		} else {
			atomic.AddInt64(&otherErrCount, 1)
		}
	}
	go run()
	go run()
	wg.Wait()

	if successCount != 1 {
		t.Errorf("successCount = %d, want 1", successCount)
	}
	if deadlockCount != 1 {
		t.Errorf("deadlockCount = %d, want 1", deadlockCount)
	}
	if otherErrCount != 0 {
		t.Errorf("otherErrCount = %d, want 0", otherErrCount)
	}

	final, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get final: %v", err)
	}
	if final.Version != 2 {
		t.Errorf("final Version = %d, want 2", final.Version)
	}
}

// ── C-P-10: MemRunCheckpointStore idempotent Save ────────────────────────────

// TestMemRunCheckpointIdempotentSave_C_P_10 verifies that saving the same
// TurnUUID twice is a strict no-op (UpdatedAt unchanged), and that a new
// TurnUUID advances UpdatedAt.
func TestMemRunCheckpointIdempotentSave_C_P_10(t *testing.T) {
	var tick int64
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clockFn := func() time.Time {
		n := atomic.AddInt64(&tick, 1)
		return base.Add(time.Duration(n) * time.Second)
	}

	s := NewMemRunCheckpointStore(clockFn)
	ctx := context.Background()

	cp1 := &Checkpoint{RunID: 42, TurnUUID: "uuid-1", State: "Running"}
	if err := s.Save(ctx, cp1); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	got1, err := s.Get(ctx, 42)
	if err != nil {
		t.Fatalf("Get after first Save: %v", err)
	}
	t0 := got1.UpdatedAt

	// Save identical TurnUUID — must be a strict no-op (clock not consumed).
	if err := s.Save(ctx, cp1); err != nil {
		t.Fatalf("Save identical TurnUUID: %v", err)
	}
	got2, err := s.Get(ctx, 42)
	if err != nil {
		t.Fatalf("Get after idempotent Save: %v", err)
	}
	if !got2.UpdatedAt.Equal(t0) {
		t.Errorf("UpdatedAt changed on idempotent Save: got %v, want %v", got2.UpdatedAt, t0)
	}

	// Save with a different TurnUUID — UpdatedAt must advance.
	cp2 := &Checkpoint{RunID: 42, TurnUUID: "uuid-2", State: "Running"}
	if err := s.Save(ctx, cp2); err != nil {
		t.Fatalf("Save new TurnUUID: %v", err)
	}
	got3, err := s.Get(ctx, 42)
	if err != nil {
		t.Fatalf("Get after new TurnUUID Save: %v", err)
	}
	if !got3.UpdatedAt.After(t0) {
		t.Errorf("UpdatedAt did not advance after new TurnUUID: got %v, still at %v", got3.UpdatedAt, t0)
	}
}

// ── C-P-14: MemUsageLedger no double-charge on Resume replay ─────────────────

// TestMemUsageLedgerCrossProcessResumeNoDoubleCharge_C_P_14 records a
// UsageRecord twice with the same IdempotencyKey and asserts Count==1 and
// Sum reflects only a single charge.
func TestMemUsageLedgerCrossProcessResumeNoDoubleCharge_C_P_14(t *testing.T) {
	l := NewMemUsageLedger(nil)
	ctx := context.Background()

	rec := &UsageRecord{
		RunID:          7,
		TurnIndex:      0,
		Provider:       "anthropic",
		Model:          "claude-sonnet-4-6",
		InputTokens:    1000,
		OutputTokens:   200,
		CostUSD:        0.05,
		IdempotencyKey: "turn-7-llm",
	}

	if err := l.Record(ctx, rec); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	// Simulate Resume replay with same IdempotencyKey.
	if err := l.Record(ctx, rec); err != nil {
		t.Fatalf("second Record (replay): %v", err)
	}

	if c := l.Count(); c != 1 {
		t.Errorf("Count = %d, want 1", c)
	}

	sum, err := l.Sum(ctx, 7)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum.CostUSD != rec.CostUSD {
		t.Errorf("CostUSD = %v, want %v (double-charge detected)", sum.CostUSD, rec.CostUSD)
	}
	if sum.InputTokens != rec.InputTokens {
		t.Errorf("InputTokens = %d, want %d", sum.InputTokens, rec.InputTokens)
	}
}

// ── TestMemPlanStoreArchivedRejectsUpdate ────────────────────────────────────

// TestMemPlanStoreArchivedRejectsUpdate creates a plan, archives it, and
// asserts that subsequent Update returns CodeWorkflowPrecondition.
func TestMemPlanStoreArchivedRejectsUpdate(t *testing.T) {
	s := NewMemPlanStore(nil)
	ctx := context.Background()

	id, err := s.Create(ctx, &BrainPlan{RunID: 5})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Archive(ctx, id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	err = s.Update(ctx, id, &BrainPlanDelta{
		Version: 2,
		OpType:  "replace",
		Payload: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("Update on archived plan should fail but returned nil")
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok {
		t.Fatalf("expected *brainerrors.BrainError, got %T", err)
	}
	if be.ErrorCode != brainerrors.CodeWorkflowPrecondition {
		t.Errorf("ErrorCode = %q, want %q", be.ErrorCode, brainerrors.CodeWorkflowPrecondition)
	}
}

// ── TestFSArtifactStorePutGet ─────────────────────────────────────────────────

// TestFSArtifactStorePutGet verifies the full Put/Get/Exists cycle on the
// filesystem-backed store, and that a duplicate Put returns the same Ref
// with refcount bumped to 2.
func TestFSArtifactStorePutGet(t *testing.T) {
	root := t.TempDir()
	meta := NewMemArtifactMetaStore(nil)
	store := NewFSArtifactStore(root, meta, nil)
	ctx := context.Background()

	content := []byte("hello from FSArtifactStore")
	ref, err := store.Put(ctx, 1, Artifact{Kind: "text", Content: content})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref == "" {
		t.Fatal("Put returned empty Ref")
	}

	// Get must return identical bytes.
	rc, err := store.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Get returned %q, want %q", got, content)
	}

	// Exists must return true.
	exists, err := store.Exists(ctx, ref)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("Exists = false after Put")
	}

	// Second Put of the same content must return the same Ref.
	ref2, err := store.Put(ctx, 2, Artifact{Kind: "text", Content: content})
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if ref2 != ref {
		t.Errorf("second Put returned different Ref: got %q, want %q", ref2, ref)
	}

	// RefCount must be 2 now.
	m, err := meta.Get(ctx, ref)
	if err != nil {
		t.Fatalf("meta.Get: %v", err)
	}
	if m.RefCount != 2 {
		t.Errorf("RefCount = %d, want 2", m.RefCount)
	}
}

// ── TestParseRefValidation ───────────────────────────────────────────────────

// TestParseRefValidation exercises ParseRef with valid and invalid inputs.
func TestParseRefValidation(t *testing.T) {
	validDigest := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	validRef := "sha256/" + validDigest

	cases := []struct {
		name    string
		input   string
		wantErr bool // must be CodeInvalidParams
	}{
		{"valid", validRef, false},
		{"empty string", "", true},
		{"missing slash", "sha256", true},
		{"unsupported algo", "md5/" + validDigest, true},
		{"wrong digest length (short)", "sha256/" + validDigest[:10], true},
		{"uppercase hex char", "sha256/" + "BA7816BF" + validDigest[8:], true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseRef(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseRef(%q) = nil, want error", tc.input)
				}
				be, ok := err.(*brainerrors.BrainError)
				if !ok {
					t.Fatalf("expected *brainerrors.BrainError, got %T: %v", err, err)
				}
				if be.ErrorCode != brainerrors.CodeInvalidParams {
					t.Errorf("ErrorCode = %q, want %q", be.ErrorCode, brainerrors.CodeInvalidParams)
				}
			} else {
				if err != nil {
					t.Fatalf("ParseRef(%q) returned unexpected error: %v", tc.input, err)
				}
			}
		})
	}
}

// ── TestMemResumeCoordinatorAttemptCap ───────────────────────────────────────

// TestMemResumeCoordinatorAttemptCap verifies that the fourth Resume call
// returns CodeInvariantViolated and CanResume returns false once the cap is
// reached.
func TestMemResumeCoordinatorAttemptCap(t *testing.T) {
	cpStore := NewMemRunCheckpointStore(nil)
	coord := NewMemResumeCoordinator(cpStore)
	ctx := context.Background()

	const runID = 42
	// Save an initial checkpoint.
	if err := cpStore.Save(ctx, &Checkpoint{
		RunID:    runID,
		TurnUUID: "turn-0",
		State:    "Running",
	}); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}

	// Three successful Resume calls (0→1→2→3 attempts).
	for i := 0; i < MaxResumeAttempts; i++ {
		cp, err := coord.Resume(ctx, runID)
		if err != nil {
			t.Fatalf("Resume %d: unexpected error: %v", i+1, err)
		}
		if cp == nil {
			t.Fatalf("Resume %d: returned nil checkpoint", i+1)
		}
	}

	// Fourth attempt must be refused.
	_, err := coord.Resume(ctx, runID)
	if err == nil {
		t.Fatal("fourth Resume should fail but returned nil")
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok {
		t.Fatalf("expected *brainerrors.BrainError, got %T: %v", err, err)
	}
	if be.ErrorCode != brainerrors.CodeInvariantViolated {
		t.Errorf("ErrorCode = %q, want %q", be.ErrorCode, brainerrors.CodeInvariantViolated)
	}

	// CanResume must return false.
	can, err := coord.CanResume(ctx, runID)
	if err != nil {
		t.Fatalf("CanResume: %v", err)
	}
	if can {
		t.Error("CanResume = true after cap reached, want false")
	}
}
