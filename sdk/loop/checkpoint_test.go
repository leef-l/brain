package loop

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/llm"
)

func TestMemoryCheckpointStore(t *testing.T) {
	store := NewMemoryCheckpointStore()
	ctx := context.Background()

	cp := &Checkpoint{
		Version:     1,
		RunID:       "run-1",
		CurrentTurn: 3,
		SavedAt:     time.Now(),
		Messages: []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hello"}}},
		},
	}

	// Save
	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Load
	loaded, err := store.Load(ctx, "run-1")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.RunID != "run-1" {
		t.Fatalf("expected run-1, got %s", loaded.RunID)
	}
	if loaded.CurrentTurn != 3 {
		t.Fatalf("expected current_turn 3, got %d", loaded.CurrentTurn)
	}

	// Delete
	if err := store.Delete(ctx, "run-1"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	_, err = store.Load(ctx, "run-1")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestFileCheckpointStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCheckpointStore(dir)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	ctx := context.Background()

	cp := &Checkpoint{
		Version:     1,
		RunID:       "run-2",
		CurrentTurn: 5,
		SavedAt:     time.Now(),
		Messages: []llm.Message{
			{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "world"}}},
		},
	}

	if err := store.Save(ctx, cp); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := store.Load(ctx, "run-2")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.CurrentTurn != 5 {
		t.Fatalf("expected current_turn 5, got %d", loaded.CurrentTurn)
	}

	if err := store.Delete(ctx, "run-2"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestCheckpointAfterTurn(t *testing.T) {
	store := NewMemoryCheckpointStore()
	run := &Run{ID: "run-3", State: StateRunning, CurrentTurn: 2}
	messages := []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "test"}}}}
	turns := []*TurnResult{
		{Turn: &Turn{RunID: "run-3", Index: 1}, NextState: StateRunning},
		{Turn: &Turn{RunID: "run-3", Index: 2}, NextState: StateRunning},
	}

	CheckpointAfterTurn(store, run, messages, turns)

	loaded, err := store.Load(context.Background(), "run-3")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.CurrentTurn != 2 {
		t.Fatalf("expected current_turn 2, got %d", loaded.CurrentTurn)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(loaded.Messages))
	}
	if len(loaded.TurnResults) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(loaded.TurnResults))
	}
}

func TestCheckpointAfterTurnNilStore(t *testing.T) {
	// nil store 不应 panic
	run := &Run{ID: "run-4", State: StateRunning}
	CheckpointAfterTurn(nil, run, nil, nil)
}

func TestRestoreFromCheckpoint(t *testing.T) {
	store := NewMemoryCheckpointStore()
	ctx := context.Background()

	// 预先保存一个 checkpoint
	restoredRun := &Run{ID: "run-5", State: StateRunning, CurrentTurn: 3, Budget: Budget{MaxTurns: 10}}
	runJSON, _ := json.Marshal(restoredRun)
	cp := &Checkpoint{
		Version:     1,
		RunID:       "run-5",
		CurrentTurn: 3,
		RunJSON:     runJSON,
		Messages: []llm.Message{
			{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "msg1"}}},
			{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "msg2"}}},
		},
		TurnResults: []*TurnResult{
			{Turn: &Turn{RunID: "run-5", Index: 1}, NextState: StateRunning},
			{Turn: &Turn{RunID: "run-5", Index: 2}, NextState: StateRunning},
			{Turn: &Turn{RunID: "run-5", Index: 3}, NextState: StateRunning},
		},
	}
	store.Save(ctx, cp)

	// 恢复
	run := &Run{ID: "run-5", State: StateCrashed, CurrentTurn: 0}
	messages, turns, ok := RestoreFromCheckpoint(store, run)
	if !ok {
		t.Fatal("expected restore to succeed")
	}
	if run.CurrentTurn != 3 {
		t.Fatalf("expected current_turn 3 after restore, got %d", run.CurrentTurn)
	}
	if run.State != StateRunning {
		t.Fatalf("expected state running after restore, got %s", run.State)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}
}

func TestRestoreFromCheckpointNotFound(t *testing.T) {
	store := NewMemoryCheckpointStore()
	run := &Run{ID: "run-missing", State: StateCrashed}
	_, _, ok := RestoreFromCheckpoint(store, run)
	if ok {
		t.Fatal("expected restore to fail for missing checkpoint")
	}
}

func TestRestoreFromCheckpointNilStore(t *testing.T) {
	run := &Run{ID: "run-6", State: StateCrashed}
	_, _, ok := RestoreFromCheckpoint(nil, run)
	if ok {
		t.Fatal("expected restore to fail with nil store")
	}
}
