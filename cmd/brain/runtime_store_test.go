package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRuntimeStore_PersistsRunRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	store, err := openRuntimeStore(path)
	if err != nil {
		t.Fatalf("openRuntimeStore: %v", err)
	}

	rec, err := store.create("central", "hello", string(modeAcceptEdits), "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.setCheckpoint(rec.ID, "turn-1"); err != nil {
		t.Fatalf("setCheckpoint: %v", err)
	}
	if err := store.setPlanID(rec.ID, 42); err != nil {
		t.Fatalf("setPlanID: %v", err)
	}
	if err := store.appendEvent(rec.ID, "tool.exec", "shell_exec", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("appendEvent: %v", err)
	}
	if _, err := store.finish(rec.ID, "completed", json.RawMessage(`{"reply":"done"}`), ""); err != nil {
		t.Fatalf("finish: %v", err)
	}

	reopened, err := openRuntimeStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.get(rec.ID)
	if !ok {
		t.Fatalf("run %s not found after reopen", rec.ID)
	}
	if got.Status != "completed" {
		t.Fatalf("status=%q, want completed", got.Status)
	}
	if got.PlanID != 42 {
		t.Fatalf("planID=%d, want 42", got.PlanID)
	}
	if got.TurnUUID != "turn-1" {
		t.Fatalf("turnUUID=%q, want turn-1", got.TurnUUID)
	}
	if len(got.Events) < 2 {
		t.Fatalf("events=%d, want at least 2", len(got.Events))
	}
}

func TestFileCLIRuntimeBackend_OpenWiresPersistentKernel(t *testing.T) {
	rt, err := (&fileCLIRuntimeBackend{dataDir: t.TempDir()}).Open("central")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if rt.Kernel == nil {
		t.Fatal("Kernel is nil")
	}
	if rt.Kernel.PlanStore == nil {
		t.Fatal("PlanStore is nil")
	}
	if rt.Kernel.RunCheckpoint == nil {
		t.Fatal("RunCheckpoint is nil")
	}
	if rt.Kernel.UsageLedger == nil {
		t.Fatal("UsageLedger is nil")
	}
	if rt.Kernel.ArtifactStore == nil {
		t.Fatal("ArtifactStore is nil")
	}
	if rt.Kernel.ArtifactMeta == nil {
		t.Fatal("ArtifactMeta is nil")
	}
	if rt.Kernel.ToolRegistry == nil {
		t.Fatal("ToolRegistry is nil")
	}
	if rt.RunStore == nil {
		t.Fatal("RunStore is nil")
	}
}
