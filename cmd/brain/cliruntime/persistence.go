package cliruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/sdk/llm"
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
	return SaveRunCheckpointWithMessages(ctx, k, rec, state, turnIndex, turnUUID, nil, nil)
}

// SaveRunCheckpointWithMessages 保存 checkpoint 并将 messages、system prompt 和 tools 存入 CAS。
func SaveRunCheckpointWithMessages(ctx context.Context, k *Runtime, rec *RunRecord, state string, turnIndex int, turnUUID string, messages []llm.Message, system []llm.SystemBlock, tools ...llm.ToolSchema) error {
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

	// 将 messages 存入 CAS（如果 ArtifactStore 可用）
	if k.Kernel.ArtifactStore != nil && len(messages) > 0 {
		data, err := json.Marshal(messages)
		if err == nil {
			ref, err := k.Kernel.ArtifactStore.Put(ctx, rec.StoreRunID, persistence.Artifact{
				Kind:    "messages",
				Content: data,
				Caption: fmt.Sprintf("turn-%d messages", turnIndex),
			})
			if err == nil {
				cp.MessagesRef = ref
			}
		}
	}

	// 将 system prompt 存入 CAS
	if k.Kernel.ArtifactStore != nil && len(system) > 0 {
		data, err := json.Marshal(system)
		if err == nil {
			ref, err := k.Kernel.ArtifactStore.Put(ctx, rec.StoreRunID, persistence.Artifact{
				Kind:    "system",
				Content: data,
				Caption: fmt.Sprintf("turn-%d system", turnIndex),
			})
			if err == nil {
				cp.SystemRef = ref
			}
		}
	}

	// 将 tools 存入 CAS
	if k.Kernel.ArtifactStore != nil && len(tools) > 0 {
		data, err := json.Marshal(tools)
		if err == nil {
			ref, err := k.Kernel.ArtifactStore.Put(ctx, rec.StoreRunID, persistence.Artifact{
				Kind:    "tools",
				Content: data,
				Caption: fmt.Sprintf("turn-%d tools", turnIndex),
			})
			if err == nil {
				cp.ToolsRef = ref
			}
		}
	}

	if err := k.Kernel.RunCheckpoint.Save(ctx, cp); err != nil {
		return err
	}
	return k.RunStore.SetCheckpoint(rec.ID, turnUUID)
}

// LoadCheckpointMessages 从 CAS 恢复 checkpoint 中的 messages。
func LoadCheckpointMessages(ctx context.Context, k *Runtime, cp *persistence.Checkpoint) ([]llm.Message, error) {
	if k == nil || k.Kernel == nil || k.Kernel.ArtifactStore == nil || cp == nil || cp.MessagesRef == "" {
		return nil, nil
	}
	reader, err := k.Kernel.ArtifactStore.Get(ctx, cp.MessagesRef)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var messages []llm.Message
	if err := json.NewDecoder(reader).Decode(&messages); err != nil {
		return nil, fmt.Errorf("decode checkpoint messages: %w", err)
	}
	return messages, nil
}

// LoadCheckpointSystem 从 CAS 恢复 checkpoint 中的 system prompt。
func LoadCheckpointSystem(ctx context.Context, k *Runtime, cp *persistence.Checkpoint) ([]llm.SystemBlock, error) {
	if k == nil || k.Kernel == nil || k.Kernel.ArtifactStore == nil || cp == nil || cp.SystemRef == "" {
		return nil, nil
	}
	reader, err := k.Kernel.ArtifactStore.Get(ctx, cp.SystemRef)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var system []llm.SystemBlock
	if err := json.NewDecoder(reader).Decode(&system); err != nil {
		return nil, fmt.Errorf("decode checkpoint system: %w", err)
	}
	return system, nil
}

// LoadCheckpointTools 从 CAS 恢复 checkpoint 中的 tool schemas。
func LoadCheckpointTools(ctx context.Context, k *Runtime, cp *persistence.Checkpoint) ([]llm.ToolSchema, error) {
	if k == nil || k.Kernel == nil || k.Kernel.ArtifactStore == nil || cp == nil || cp.ToolsRef == "" {
		return nil, nil
	}
	reader, err := k.Kernel.ArtifactStore.Get(ctx, cp.ToolsRef)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var tools []llm.ToolSchema
	if err := json.NewDecoder(reader).Decode(&tools); err != nil {
		return nil, fmt.Errorf("decode checkpoint tools: %w", err)
	}
	return tools, nil
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
