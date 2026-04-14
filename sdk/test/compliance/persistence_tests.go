package compliance

import (
	"context"
	"encoding/json"
	"fmt"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/persistence"
	braintesting "github.com/leef-l/brain/sdk/testing"
)

func registerPersistenceTests(r *braintesting.MemComplianceRunner) {
	now := func() { /* placeholder */ }
	_ = now

	// C-P-01: PlanStore Create returns positive ID.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-01", Description: "PlanStore Create returns ID", Category: "persistence",
	}, func(ctx context.Context) error {
		store := persistence.NewMemPlanStore(nil)
		plan := &persistence.BrainPlan{
			BrainID:      "central",
			Version:      1,
			CurrentState: json.RawMessage(`{"status":"init"}`),
		}
		id, err := store.Create(ctx, plan)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-01: Create: %v", err)))
		}
		if id <= 0 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-01: ID=%d, want > 0", id)))
		}
		return nil
	})

	// C-P-02: PlanStore Get retrieves created plan.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-02", Description: "PlanStore Get round-trip", Category: "persistence",
	}, func(ctx context.Context) error {
		store := persistence.NewMemPlanStore(nil)
		plan := &persistence.BrainPlan{
			BrainID:      "central",
			Version:      1,
			CurrentState: json.RawMessage(`{"key":"value"}`),
		}
		id, _ := store.Create(ctx, plan)
		got, err := store.Get(ctx, id)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-02: Get: %v", err)))
		}
		if got.BrainID != "central" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-02: BrainID mismatch"))
		}
		return nil
	})

	// C-P-03: PlanStore Get non-existent returns error.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-03", Description: "PlanStore Get non-existent → error", Category: "persistence",
	}, func(ctx context.Context) error {
		store := persistence.NewMemPlanStore(nil)
		_, err := store.Get(ctx, 99999)
		if err == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-03: expected error"))
		}
		return nil
	})

	// C-P-04: PlanStore Update applies delta.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-04", Description: "PlanStore Update applies delta", Category: "persistence",
	}, func(ctx context.Context) error {
		store := persistence.NewMemPlanStore(nil)
		plan := &persistence.BrainPlan{
			BrainID:      "central",
			Version:      1,
			CurrentState: json.RawMessage(`{"step":1}`),
		}
		id, _ := store.Create(ctx, plan)
		delta := &persistence.BrainPlanDelta{
			PlanID:  id,
			Version: 2,
			OpType:  "replace",
			Payload: json.RawMessage(`{"step":2}`),
			Actor:   "central",
		}
		if err := store.Update(ctx, id, delta); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-04: Update: %v", err)))
		}
		got, _ := store.Get(ctx, id)
		if got.Version != 2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-04: Version=%d, want 2", got.Version)))
		}
		return nil
	})

	// C-P-05: PlanStore Archive sets archived flag.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-05", Description: "PlanStore Archive", Category: "persistence",
	}, func(ctx context.Context) error {
		store := persistence.NewMemPlanStore(nil)
		plan := &persistence.BrainPlan{BrainID: "central", Version: 1, CurrentState: json.RawMessage(`{}`)}
		id, _ := store.Create(ctx, plan)
		if err := store.Archive(ctx, id); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-05: Archive: %v", err)))
		}
		got, _ := store.Get(ctx, id)
		if !got.Archived {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-05: not archived"))
		}
		return nil
	})

	// C-P-06: PlanStore Update on archived plan fails.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-06", Description: "PlanStore Update archived → error", Category: "persistence",
	}, func(ctx context.Context) error {
		store := persistence.NewMemPlanStore(nil)
		plan := &persistence.BrainPlan{BrainID: "central", Version: 1, CurrentState: json.RawMessage(`{}`)}
		id, _ := store.Create(ctx, plan)
		store.Archive(ctx, id)
		delta := &persistence.BrainPlanDelta{PlanID: id, Version: 2, OpType: "patch", Payload: json.RawMessage(`{}`)}
		err := store.Update(ctx, id, delta)
		if err == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-06: should fail on archived plan"))
		}
		return nil
	})

	// C-P-07: PlanStore ListByRun.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-07", Description: "PlanStore ListByRun", Category: "persistence",
	}, func(ctx context.Context) error {
		store := persistence.NewMemPlanStore(nil)
		p1 := &persistence.BrainPlan{RunID: 100, BrainID: "central", Version: 1, CurrentState: json.RawMessage(`{}`)}
		p2 := &persistence.BrainPlan{RunID: 100, BrainID: "code", Version: 1, CurrentState: json.RawMessage(`{}`)}
		p3 := &persistence.BrainPlan{RunID: 200, BrainID: "central", Version: 1, CurrentState: json.RawMessage(`{}`)}
		store.Create(ctx, p1)
		store.Create(ctx, p2)
		store.Create(ctx, p3)
		plans, err := store.ListByRun(ctx, 100)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-07: ListByRun: %v", err)))
		}
		if len(plans) != 2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-07: len=%d, want 2", len(plans))))
		}
		return nil
	})

	// C-P-08: ArtifactStore Put/Get round-trip.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-08", Description: "ArtifactStore Put/Get round-trip", Category: "persistence",
	}, func(ctx context.Context) error {
		meta := persistence.NewMemArtifactMetaStore(nil)
		store := persistence.NewMemArtifactStore(meta, nil)
		artifact := persistence.Artifact{
			Kind:    "stdout",
			Content: []byte("hello world"),
			Caption: "test output",
		}
		ref, err := store.Put(ctx, 1, artifact)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-08: Put: %v", err)))
		}
		if ref == "" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-08: empty ref"))
		}
		reader, err := store.Get(ctx, ref)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-08: Get: %v", err)))
		}
		reader.Close()
		return nil
	})

	// C-P-09: ArtifactStore idempotent Put.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-09", Description: "ArtifactStore idempotent Put", Category: "persistence",
	}, func(ctx context.Context) error {
		meta := persistence.NewMemArtifactMetaStore(nil)
		store := persistence.NewMemArtifactStore(meta, nil)
		artifact := persistence.Artifact{Kind: "stdout", Content: []byte("same")}
		ref1, _ := store.Put(ctx, 1, artifact)
		ref2, _ := store.Put(ctx, 1, artifact)
		if ref1 != ref2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-09: refs differ for same content"))
		}
		return nil
	})

	// C-P-10: ArtifactStore Exists.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-10", Description: "ArtifactStore Exists", Category: "persistence",
	}, func(ctx context.Context) error {
		meta := persistence.NewMemArtifactMetaStore(nil)
		store := persistence.NewMemArtifactStore(meta, nil)
		artifact := persistence.Artifact{Kind: "test", Content: []byte("data")}
		ref, _ := store.Put(ctx, 1, artifact)
		exists, err := store.Exists(ctx, ref)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-10: Exists: %v", err)))
		}
		if !exists {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-10: should exist"))
		}
		return nil
	})

	// C-P-11: ArtifactStore Ref format is "sha256/<hex>".
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-11", Description: "ArtifactStore Ref sha256 format", Category: "persistence",
	}, func(ctx context.Context) error {
		meta := persistence.NewMemArtifactMetaStore(nil)
		store := persistence.NewMemArtifactStore(meta, nil)
		artifact := persistence.Artifact{Kind: "test", Content: []byte("ref-check")}
		ref, _ := store.Put(ctx, 1, artifact)
		if len(ref) < 10 || string(ref[:7]) != "sha256/" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-11: ref=%q, want sha256/ prefix", ref)))
		}
		return nil
	})

	// C-P-12: RunCheckpointStore round-trip.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-12", Description: "RunCheckpointStore round-trip", Category: "persistence",
	}, func(ctx context.Context) error {
		store := persistence.NewMemRunCheckpointStore(nil)
		cp := &persistence.Checkpoint{
			RunID:    1,
			BrainID:  "central",
			State:    "running",
			TurnUUID: "uuid-cp12",
		}
		if err := store.Save(ctx, cp); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-12: Save: %v", err)))
		}
		got, err := store.Get(ctx, 1)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-12: Get: %v", err)))
		}
		if got.RunID != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-12: RunID mismatch"))
		}
		return nil
	})

	// C-P-13: UsageLedger record and query.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-13", Description: "UsageLedger record and query", Category: "persistence",
	}, func(ctx context.Context) error {
		ledger := persistence.NewMemUsageLedger(nil)
		entry := &persistence.UsageRecord{
			RunID:          1,
			Model:          "claude-opus-4-6",
			InputTokens:    100,
			OutputTokens:   50,
			CostUSD:        0.01,
			IdempotencyKey: "test-key-1",
		}
		if err := ledger.Record(ctx, entry); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-13: Record: %v", err)))
		}
		sum, err := ledger.Sum(ctx, 1)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-13: Sum: %v", err)))
		}
		if sum.CostUSD < 0.01 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-13: CostUSD=%.4f, want ≥0.01", sum.CostUSD)))
		}
		return nil
	})

	// C-P-14: ResumeCoordinator resume from checkpoint.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-14", Description: "ResumeCoordinator resume", Category: "persistence",
	}, func(ctx context.Context) error {
		cpStore := persistence.NewMemRunCheckpointStore(nil)
		coord := persistence.NewMemResumeCoordinator(cpStore)
		cp := &persistence.Checkpoint{
			RunID:    100,
			BrainID:  "central",
			State:    "running",
			TurnUUID: "uuid-cp14",
		}
		cpStore.Save(ctx, cp)
		got, err := coord.Resume(ctx, 100)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-P-14: Resume: %v", err)))
		}
		if got.RunID != 100 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-14: RunID mismatch"))
		}
		return nil
	})

	// C-P-15: ResumeCoordinator non-existent run returns error.
	r.Register(braintesting.ComplianceTest{
		ID: "C-P-15", Description: "ResumeCoordinator non-existent → error", Category: "persistence",
	}, func(ctx context.Context) error {
		cpStore := persistence.NewMemRunCheckpointStore(nil)
		coord := persistence.NewMemResumeCoordinator(cpStore)
		_, err := coord.Resume(ctx, 99999)
		if err == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-P-15: expected error"))
		}
		return nil
	})
}
