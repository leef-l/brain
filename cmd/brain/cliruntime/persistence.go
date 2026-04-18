package cliruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/persistence"
)

func LoadPersistedRun(id, dataDir string) (*Runtime, *RunRecord, *persistence.Checkpoint, error) {
	rt, err := NewDefaultRuntime("central", dataDir)
	if err != nil {
		return nil, nil, nil, err
	}
	rec, ok := rt.RunStore.Get(id)
	if !ok {
		return rt, nil, nil, fmt.Errorf("run %s not found", id)
	}
	var cp *persistence.Checkpoint
	if rt.Kernel != nil && rt.Kernel.RunCheckpoint != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*1e9)
		defer cancel()
		cp, err = rt.Kernel.RunCheckpoint.Get(ctx, rec.StoreRunID)
		if err != nil {
			cp = nil
		}
	}
	return rt, rec, cp, nil
}

func SaveRunCheckpoint(ctx context.Context, k *Runtime, rec *RunRecord, state string, turnIndex int, turnUUID string) error {
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
	return k.RunStore.SetCheckpoint(rec.ID, turnUUID)
}

func SaveRunUsage(ctx context.Context, k *Runtime, rec *RunRecord, provider, model string, result *loop.RunResult) error {
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

func SaveRunPlan(ctx context.Context, k *Runtime, rec *RunRecord, payload map[string]interface{}) (int64, error) {
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
	if err := k.RunStore.SetPlanID(rec.ID, planID); err != nil {
		return 0, err
	}
	return planID, nil
}
