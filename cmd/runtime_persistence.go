package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/loop"
	"github.com/leef-l/brain/persistence"
)

func loadPersistedRun(id string) (*cliRuntime, *persistedRunRecord, *persistence.Checkpoint, error) {
	runtime, err := newDefaultCLIRuntime("central")
	if err != nil {
		return nil, nil, nil, err
	}
	rec, ok := runtime.RunStore.get(id)
	if !ok {
		return runtime, nil, nil, fmt.Errorf("run %s not found", id)
	}
	var cp *persistence.Checkpoint
	if runtime.Kernel != nil && runtime.Kernel.RunCheckpoint != nil {
		ctx, cancel := bgCtx()
		defer cancel()
		cp, err = runtime.Kernel.RunCheckpoint.Get(ctx, rec.StoreRunID)
		if err != nil {
			cp = nil
		}
	}
	return runtime, rec, cp, nil
}

func saveRunCheckpoint(ctx context.Context, k *cliRuntime, rec *persistedRunRecord, state string, turnIndex int, turnUUID string) error {
	if k == nil || k.Kernel == nil || k.Kernel.RunCheckpoint == nil || rec == nil {
		return nil
	}
	if turnUUID == "" {
		turnUUID = fmt.Sprintf("%s-%s", rec.ID, state)
	}
	cp := &persistence.Checkpoint{
		RunID:     rec.StoreRunID,
		TurnIndex: turnIndex,
		BrainID:   rec.BrainID,
		State:     state,
		TurnUUID:  turnUUID,
	}
	if err := k.Kernel.RunCheckpoint.Save(ctx, cp); err != nil {
		return err
	}
	return k.RunStore.setCheckpoint(rec.ID, turnUUID)
}

func saveRunUsage(ctx context.Context, k *cliRuntime, rec *persistedRunRecord, provider, model string, result *loop.RunResult) error {
	if k == nil || k.Kernel == nil || k.Kernel.UsageLedger == nil || rec == nil || result == nil {
		return nil
	}
	for _, turn := range result.Turns {
		if turn == nil || turn.Response == nil || turn.Turn == nil {
			continue
		}
		usage := turn.Response.Usage
		if err := k.Kernel.UsageLedger.Record(ctx, &persistence.UsageRecord{
			RunID:          rec.StoreRunID,
			TurnIndex:      turn.Turn.Index,
			Provider:       provider,
			Model:          model,
			InputTokens:    int64(usage.InputTokens),
			OutputTokens:   int64(usage.OutputTokens),
			CacheRead:      int64(usage.CacheReadTokens),
			CacheCreation:  int64(usage.CacheCreationTokens),
			CostUSD:        usage.CostUSD,
			IdempotencyKey: turn.Turn.UUID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func saveRunPlan(ctx context.Context, k *cliRuntime, rec *persistedRunRecord, payload map[string]interface{}) (int64, error) {
	if k == nil || k.Kernel == nil || k.Kernel.PlanStore == nil || rec == nil {
		return 0, nil
	}
	raw, _ := json.Marshal(payload)
	plan := &persistence.BrainPlan{
		RunID:        rec.StoreRunID,
		BrainID:      rec.BrainID,
		Version:      1,
		CurrentState: raw,
	}
	planID, err := k.Kernel.PlanStore.Create(ctx, plan)
	if err != nil {
		return 0, err
	}
	if err := k.RunStore.setPlanID(rec.ID, planID); err != nil {
		return 0, err
	}
	return planID, nil
}
