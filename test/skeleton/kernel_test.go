package skeleton

import (
	"context"
	"io"
	"testing"

	"github.com/leef-l/brain/kernel"
	"github.com/leef-l/brain/persistence"
)

// ---------------------------------------------------------------------------
// NewMemKernel 组装完整性
// ---------------------------------------------------------------------------

func TestNewMemKernelWiresAllFields(t *testing.T) {
	k, err := kernel.NewMemKernel(kernel.MemKernelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if k.PlanStore == nil {
		t.Error("PlanStore is nil")
	}
	if k.ArtifactStore == nil {
		t.Error("ArtifactStore is nil")
	}
	if k.ArtifactMeta == nil {
		t.Error("ArtifactMeta is nil")
	}
	if k.RunCheckpoint == nil {
		t.Error("RunCheckpoint is nil")
	}
	if k.UsageLedger == nil {
		t.Error("UsageLedger is nil")
	}
	if k.Resume == nil {
		t.Error("Resume is nil")
	}
	if k.ToolRegistry == nil {
		t.Error("ToolRegistry is nil")
	}
	if k.Vault == nil {
		t.Error("Vault is nil")
	}
	if k.AuditLogger == nil {
		t.Error("AuditLogger is nil")
	}
	if k.Metrics == nil {
		t.Error("Metrics is nil")
	}
	if k.Trace == nil {
		t.Error("Trace is nil")
	}
	if k.Logs == nil {
		t.Error("Logs is nil")
	}
}

// ---------------------------------------------------------------------------
// 内置工具注册
// ---------------------------------------------------------------------------

func TestNewMemKernelRegistersBuiltinTools(t *testing.T) {
	k, err := kernel.NewMemKernel(kernel.MemKernelOptions{BrainKind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	echo, ok := k.ToolRegistry.Lookup("test.echo")
	if !ok {
		t.Fatal("echo tool not registered")
	}
	if echo.Name() != "test.echo" {
		t.Errorf("echo.Name = %q", echo.Name())
	}

	reject, ok := k.ToolRegistry.Lookup("test.reject_task")
	if !ok {
		t.Fatal("reject_task tool not registered")
	}
	if reject.Name() != "test.reject_task" {
		t.Errorf("reject.Name = %q", reject.Name())
	}
}

// ---------------------------------------------------------------------------
// PlanStore 往返
// ---------------------------------------------------------------------------

func TestNewMemKernelPlanStoreRoundTrip(t *testing.T) {
	k, err := kernel.NewMemKernel(kernel.MemKernelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	plan := &persistence.BrainPlan{
		RunID:   1,
		BrainID: "central",
		Version: 1,
	}
	planID, err := k.PlanStore.Create(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if planID == 0 {
		t.Fatal("planID should not be 0")
	}
	got, err := k.PlanStore.Get(ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != 1 {
		t.Errorf("RunID = %d", got.RunID)
	}
}

// ---------------------------------------------------------------------------
// ArtifactStore CAS 往返
// ---------------------------------------------------------------------------

func TestNewMemKernelArtifactCASRoundTrip(t *testing.T) {
	k, err := kernel.NewMemKernel(kernel.MemKernelOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	art := persistence.Artifact{
		Kind:    "code",
		Content: []byte("hello world"),
		Caption: "test artifact",
	}
	ref, err := k.ArtifactStore.Put(ctx, 1, art)
	if err != nil {
		t.Fatal(err)
	}
	if ref == "" {
		t.Fatal("ref should not be empty")
	}

	exists, err := k.ArtifactStore.Exists(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("Exists should return true after Put")
	}

	reader, err := k.ArtifactStore.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, _ := io.ReadAll(reader)
	if string(data) != "hello world" {
		t.Errorf("Get = %q", string(data))
	}
}
