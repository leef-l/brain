package kernel

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/leef-l/brain/sdk/persistence"
)

// TestNewMemKernel_WiresAllFields asserts that NewMemKernel returns a
// Kernel with every component populated — a regression-catching smoke
// test for the v0.1.0 reference executor.
func TestNewMemKernel_WiresAllFields(t *testing.T) {
	k, err := NewMemKernel(MemKernelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		got  interface{}
	}{
		{"PlanStore", k.PlanStore},
		{"ArtifactStore", k.ArtifactStore},
		{"ArtifactMeta", k.ArtifactMeta},
		{"RunCheckpoint", k.RunCheckpoint},
		{"UsageLedger", k.UsageLedger},
		{"Resume", k.Resume},
		{"ToolRegistry", k.ToolRegistry},
		{"Vault", k.Vault},
		{"AuditLogger", k.AuditLogger},
		{"Metrics", k.Metrics},
		{"Trace", k.Trace},
		{"Logs", k.Logs},
	}
	for _, c := range cases {
		if c.got == nil {
			t.Errorf("NewMemKernel: %s is nil", c.name)
		}
	}
}

// TestNewMemKernel_RegistersBuiltinTools verifies that echo and reject_task
// are registered under the configured brainKind.
func TestNewMemKernel_RegistersBuiltinTools(t *testing.T) {
	k, err := NewMemKernel(MemKernelOptions{BrainKind: "smoke"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := k.ToolRegistry.Lookup("smoke.echo"); !ok {
		t.Error("expected smoke.echo to be registered")
	}
	if _, ok := k.ToolRegistry.Lookup("smoke.reject_task"); !ok {
		t.Error("expected smoke.reject_task to be registered")
	}
}

// TestNewMemKernel_PlanStoreRoundTrip is the same contract exercised by
// `brain doctor` — if this passes, `brain doctor` check #3 passes.
func TestNewMemKernel_PlanStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	k, err := NewMemKernel(MemKernelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := json.Marshal(map[string]string{"probe": "test"})
	id, err := k.PlanStore.Create(ctx, &persistence.BrainPlan{
		BrainID:      "test",
		Version:      1,
		CurrentState: snap,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := k.PlanStore.Get(ctx, id)
	if err != nil || got == nil || got.ID != id {
		t.Fatalf("Get: plan=%v err=%v", got, err)
	}
	if err := k.PlanStore.Archive(ctx, id); err != nil {
		t.Fatalf("Archive: %v", err)
	}
}

// TestNewMemKernel_ArtifactCASRoundTrip is the companion contract for
// `brain doctor` check #7.
func TestNewMemKernel_ArtifactCASRoundTrip(t *testing.T) {
	ctx := context.Background()
	k, err := NewMemKernel(MemKernelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("hello CAS")
	ref, err := k.ArtifactStore.Put(ctx, 0, persistence.Artifact{
		Kind:    "test",
		Content: payload,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	ok, err := k.ArtifactStore.Exists(ctx, ref)
	if err != nil || !ok {
		t.Fatalf("Exists: ok=%v err=%v", ok, err)
	}
	rc, err := k.ArtifactStore.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("content mismatch: got %q want %q", got, payload)
	}
}
