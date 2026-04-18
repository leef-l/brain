package mcphost

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/kernel/mcpadapter"
	"github.com/leef-l/brain/sdk/tool"
)

// ---------------------------------------------------------------------------
// Mock adapter helpers
// ---------------------------------------------------------------------------

// mockReadyEntry creates an adapterEntry with a pre-configured adapter that
// has its tools set via reflection-free approach: we build the entry directly
// and mark it ready with the given schemas.
func mockReadyEntry(name, prefix string, schemas []tool.Schema) *adapterEntry {
	adapter := &mcpadapter.Adapter{
		BinPath:    "(mock)",
		ToolPrefix: prefix,
	}
	return &adapterEntry{
		spec: MCPServerSpec{
			Name:       name,
			BinPath:    "(mock)",
			ToolPrefix: prefix,
			AutoStart:  true,
		},
		adapter: adapter,
		status:  AdapterReady,
		schemas: schemas,
	}
}

func mockStoppedEntry(name, prefix string) *adapterEntry {
	adapter := &mcpadapter.Adapter{
		BinPath:    "(mock)",
		ToolPrefix: prefix,
	}
	return &adapterEntry{
		spec: MCPServerSpec{
			Name:       name,
			BinPath:    "(mock)",
			ToolPrefix: prefix,
			AutoStart:  false,
		},
		adapter: adapter,
		status:  AdapterStopped,
	}
}

func mockFailedEntry(name, prefix string) *adapterEntry {
	adapter := &mcpadapter.Adapter{
		BinPath:    "(mock)",
		ToolPrefix: prefix,
	}
	return &adapterEntry{
		spec: MCPServerSpec{
			Name:       name,
			BinPath:    "(mock)",
			ToolPrefix: prefix,
		},
		adapter: adapter,
		status:  AdapterFailed,
	}
}

func mockDegradedEntry(name, prefix string, schemas []tool.Schema) *adapterEntry {
	adapter := &mcpadapter.Adapter{
		BinPath:    "(mock)",
		ToolPrefix: prefix,
	}
	return &adapterEntry{
		spec: MCPServerSpec{
			Name:       name,
			BinPath:    "(mock)",
			ToolPrefix: prefix,
		},
		adapter: adapter,
		status:  AdapterDegraded,
		schemas: schemas,
	}
}

// ---------------------------------------------------------------------------
// AdapterManager tests
// ---------------------------------------------------------------------------

func TestAdapterManager_AddServer(t *testing.T) {
	m := NewAdapterManager(8)

	err := m.AddServer(MCPServerSpec{
		Name:       "github",
		BinPath:    "/usr/bin/mcp-github",
		ToolPrefix: "mcp.github.",
		AutoStart:  true,
	})
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	// Duplicate should fail.
	err = m.AddServer(MCPServerSpec{
		Name:       "github",
		BinPath:    "/usr/bin/mcp-github",
		ToolPrefix: "mcp.github.",
	})
	if err == nil {
		t.Fatal("expected error for duplicate server name")
	}

	// Empty name should fail.
	err = m.AddServer(MCPServerSpec{
		BinPath:    "/usr/bin/mcp-foo",
		ToolPrefix: "mcp.foo.",
	})
	if err == nil {
		t.Fatal("expected error for empty server name")
	}
}

func TestAdapterManager_ServerNames(t *testing.T) {
	m := NewAdapterManager(0)

	_ = m.AddServer(MCPServerSpec{Name: "a", BinPath: "a", ToolPrefix: "a."})
	_ = m.AddServer(MCPServerSpec{Name: "b", BinPath: "b", ToolPrefix: "b."})
	_ = m.AddServer(MCPServerSpec{Name: "c", BinPath: "c", ToolPrefix: "c."})

	names := m.ServerNames()
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
	if names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Fatalf("unexpected order: %v", names)
	}
}

func TestAdapterManager_Status_Unknown(t *testing.T) {
	m := NewAdapterManager(0)
	_, ok := m.Status("nonexistent")
	if ok {
		t.Fatal("expected ok=false for unknown server")
	}
}

func TestAdapterManager_Status_AfterAdd(t *testing.T) {
	m := NewAdapterManager(0)
	_ = m.AddServer(MCPServerSpec{Name: "fs", BinPath: "fs", ToolPrefix: "fs."})

	st, ok := m.Status("fs")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if st != AdapterStopped {
		t.Fatalf("expected AdapterStopped, got %v", st)
	}
}

func TestAdapterManager_HealthEvent_OnStatusChange(t *testing.T) {
	m := NewAdapterManager(16)

	entry := mockStoppedEntry("test-srv", "mcp.test.")
	m.addEntry("test-srv", entry)

	// Simulate a status change.
	m.setStatus(entry, AdapterStarting, nil)

	select {
	case ev := <-m.HealthEvents():
		if ev.ServerName != "test-srv" {
			t.Fatalf("expected server name test-srv, got %s", ev.ServerName)
		}
		if ev.OldStatus != AdapterStopped {
			t.Fatalf("expected old=Stopped, got %v", ev.OldStatus)
		}
		if ev.NewStatus != AdapterStarting {
			t.Fatalf("expected new=Starting, got %v", ev.NewStatus)
		}
		if ev.Timestamp.IsZero() {
			t.Fatal("expected non-zero timestamp")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for health event")
	}
}

func TestAdapterManager_NoEventOnSameStatus(t *testing.T) {
	m := NewAdapterManager(16)

	entry := mockStoppedEntry("srv", "mcp.srv.")
	m.addEntry("srv", entry)

	// Set to same status — should NOT emit an event.
	m.setStatus(entry, AdapterStopped, nil)

	select {
	case ev := <-m.HealthEvents():
		t.Fatalf("unexpected event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// expected — no event
	}
}

func TestAdapterManager_HealthEventWithError(t *testing.T) {
	m := NewAdapterManager(16)

	entry := mockStoppedEntry("failing", "mcp.fail.")
	m.addEntry("failing", entry)

	testErr := fmt.Errorf("connection refused")
	m.setStatus(entry, AdapterFailed, testErr)

	select {
	case ev := <-m.HealthEvents():
		if ev.Error == nil {
			t.Fatal("expected error in health event")
		}
		if ev.Error.Error() != "connection refused" {
			t.Fatalf("unexpected error: %v", ev.Error)
		}
		if ev.NewStatus != AdapterFailed {
			t.Fatalf("expected Failed, got %v", ev.NewStatus)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

// ---------------------------------------------------------------------------
// BrainHost tests
// ---------------------------------------------------------------------------

func TestBrainHost_ToolSchemas(t *testing.T) {
	m := NewAdapterManager(0)

	githubSchemas := []tool.Schema{
		{Name: "mcp.github.search", Description: "Search repos", Brain: "mcp"},
		{Name: "mcp.github.pr_create", Description: "Create PR", Brain: "mcp"},
	}
	fsSchemas := []tool.Schema{
		{Name: "mcp.fs.read", Description: "Read file", Brain: "mcp"},
	}

	m.addEntry("github", mockReadyEntry("github", "mcp.github.", githubSchemas))
	m.addEntry("fs", mockReadyEntry("fs", "mcp.fs.", fsSchemas))

	host := NewBrainHost(m)
	schemas := host.ToolSchemas()

	if len(schemas) != 3 {
		t.Fatalf("expected 3 schemas, got %d", len(schemas))
	}

	// Verify all expected tools are present.
	nameSet := make(map[string]bool)
	for _, s := range schemas {
		nameSet[s.Name] = true
	}
	for _, want := range []string{"mcp.github.search", "mcp.github.pr_create", "mcp.fs.read"} {
		if !nameSet[want] {
			t.Errorf("missing schema %q", want)
		}
	}
}

func TestBrainHost_ToolSchemas_SkipsStopped(t *testing.T) {
	m := NewAdapterManager(0)

	m.addEntry("running", mockReadyEntry("running", "mcp.run.", []tool.Schema{
		{Name: "mcp.run.do", Description: "do it", Brain: "mcp"},
	}))
	m.addEntry("stopped", mockStoppedEntry("stopped", "mcp.stop."))

	host := NewBrainHost(m)
	schemas := host.ToolSchemas()

	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema (stopped excluded), got %d", len(schemas))
	}
}

func TestBrainHost_InvokeTool_NoAdapter(t *testing.T) {
	m := NewAdapterManager(0)
	host := NewBrainHost(m)

	_, err := host.InvokeTool(context.Background(), "mcp.unknown.tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestBrainHost_FindAdapter(t *testing.T) {
	m := NewAdapterManager(0)
	m.addEntry("github", mockReadyEntry("github", "mcp.github.", nil))
	m.addEntry("fs", mockReadyEntry("fs", "mcp.fs.", nil))

	adapter, mcpName, err := m.findAdapter("mcp.github.search")
	if err != nil {
		t.Fatalf("findAdapter: %v", err)
	}
	if adapter == nil {
		t.Fatal("expected non-nil adapter")
	}
	if mcpName != "search" {
		t.Fatalf("expected mcpName=search, got %q", mcpName)
	}

	adapter, mcpName, err = m.findAdapter("mcp.fs.read")
	if err != nil {
		t.Fatalf("findAdapter: %v", err)
	}
	if mcpName != "read" {
		t.Fatalf("expected mcpName=read, got %q", mcpName)
	}

	_, _, err = m.findAdapter("mcp.unknown.tool")
	if err == nil {
		t.Fatal("expected error for unknown prefix")
	}
}

// ---------------------------------------------------------------------------
// AdapterStatus.String() tests
// ---------------------------------------------------------------------------

func TestAdapterStatus_String(t *testing.T) {
	tests := []struct {
		status AdapterStatus
		want   string
	}{
		{AdapterStarting, "starting"},
		{AdapterReady, "ready"},
		{AdapterDegraded, "degraded"},
		{AdapterFailed, "failed"},
		{AdapterStopped, "stopped"},
		{AdapterStatus(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("AdapterStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// envMapToSlice tests
// ---------------------------------------------------------------------------

func TestEnvMapToSlice(t *testing.T) {
	result := envMapToSlice(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}

	result = envMapToSlice(map[string]string{})
	if result != nil {
		t.Fatalf("expected nil for empty map, got %v", result)
	}

	result = envMapToSlice(map[string]string{"A": "1", "B": "2"})
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}

	// Check that entries are in K=V format (order is not guaranteed).
	set := make(map[string]bool)
	for _, s := range result {
		set[s] = true
	}
	if !set["A=1"] || !set["B=2"] {
		t.Fatalf("unexpected entries: %v", result)
	}
}

// ---------------------------------------------------------------------------
// BrainHost.Manager() accessor test
// ---------------------------------------------------------------------------

func TestBrainHost_Manager(t *testing.T) {
	m := NewAdapterManager(0)
	host := NewBrainHost(m)
	if host.Manager() != m {
		t.Fatal("Manager() should return the same AdapterManager")
	}
}

// ---------------------------------------------------------------------------
// BrainHost.Registry() accessor test
// ---------------------------------------------------------------------------

func TestBrainHost_Registry(t *testing.T) {
	m := NewAdapterManager(0)
	host := NewBrainHost(m)
	if host.Registry() == nil {
		t.Fatal("Registry() should not be nil")
	}
}

// ---------------------------------------------------------------------------
// StopAll test (on mock entries that were never actually started)
// ---------------------------------------------------------------------------

func TestAdapterManager_StopAll(t *testing.T) {
	m := NewAdapterManager(16)
	m.addEntry("a", mockReadyEntry("a", "mcp.a.", nil))
	m.addEntry("b", mockReadyEntry("b", "mcp.b.", nil))

	ctx := context.Background()
	m.StopAll(ctx)

	stA, _ := m.Status("a")
	stB, _ := m.Status("b")
	if stA != AdapterStopped {
		t.Fatalf("expected a=Stopped, got %v", stA)
	}
	if stB != AdapterStopped {
		t.Fatalf("expected b=Stopped, got %v", stB)
	}
}

// ---------------------------------------------------------------------------
// AggregateStatus tests
// ---------------------------------------------------------------------------

func TestAggregateStatus_AllReady(t *testing.T) {
	m := NewAdapterManager(0)
	m.addEntry("a", mockReadyEntry("a", "mcp.a.", nil))
	m.addEntry("b", mockReadyEntry("b", "mcp.b.", nil))

	if got := m.AggregateStatus(); got != BrainHealthHealthy {
		t.Fatalf("expected Healthy, got %v", got)
	}
}

func TestAggregateStatus_AllFailed(t *testing.T) {
	m := NewAdapterManager(0)
	m.addEntry("a", mockFailedEntry("a", "mcp.a."))
	m.addEntry("b", mockFailedEntry("b", "mcp.b."))

	if got := m.AggregateStatus(); got != BrainHealthUnhealthy {
		t.Fatalf("expected Unhealthy, got %v", got)
	}
}

func TestAggregateStatus_AllStopped(t *testing.T) {
	m := NewAdapterManager(0)
	m.addEntry("a", mockStoppedEntry("a", "mcp.a."))

	if got := m.AggregateStatus(); got != BrainHealthUnhealthy {
		t.Fatalf("expected Unhealthy, got %v", got)
	}
}

func TestAggregateStatus_Mixed(t *testing.T) {
	m := NewAdapterManager(0)
	m.addEntry("a", mockReadyEntry("a", "mcp.a.", nil))
	m.addEntry("b", mockFailedEntry("b", "mcp.b."))

	if got := m.AggregateStatus(); got != BrainHealthDegraded {
		t.Fatalf("expected Degraded, got %v", got)
	}
}

func TestAggregateStatus_Empty(t *testing.T) {
	m := NewAdapterManager(0)
	if got := m.AggregateStatus(); got != BrainHealthUnhealthy {
		t.Fatalf("expected Unhealthy for empty manager, got %v", got)
	}
}

func TestAggregateStatus_ReadyAndDegraded(t *testing.T) {
	m := NewAdapterManager(0)
	m.addEntry("a", mockReadyEntry("a", "mcp.a.", nil))
	m.addEntry("b", mockDegradedEntry("b", "mcp.b.", nil))

	if got := m.AggregateStatus(); got != BrainHealthDegraded {
		t.Fatalf("expected Degraded, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// BrainHealthStatus.String() tests
// ---------------------------------------------------------------------------

func TestBrainHealthStatus_String(t *testing.T) {
	tests := []struct {
		status BrainHealthStatus
		want   string
	}{
		{BrainHealthHealthy, "healthy"},
		{BrainHealthDegraded, "degraded"},
		{BrainHealthUnhealthy, "unhealthy"},
		{BrainHealthStatus(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("BrainHealthStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// StatusSnapshot tests
// ---------------------------------------------------------------------------

func TestStatusSnapshot(t *testing.T) {
	m := NewAdapterManager(0)
	m.addEntry("a", mockReadyEntry("a", "mcp.a.", []tool.Schema{
		{Name: "mcp.a.tool1"}, {Name: "mcp.a.tool2"},
	}))
	m.addEntry("b", mockStoppedEntry("b", "mcp.b."))

	snap := m.StatusSnapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(snap))
	}
	if snap[0].Name != "a" || snap[0].Status != "ready" || snap[0].ToolsCount != 2 {
		t.Fatalf("unexpected snap[0]: %+v", snap[0])
	}
	if snap[1].Name != "b" || snap[1].Status != "stopped" || snap[1].ToolsCount != 0 {
		t.Fatalf("unexpected snap[1]: %+v", snap[1])
	}
}

// ---------------------------------------------------------------------------
// BrainHost HealthStatus / HealthSnapshot tests
// ---------------------------------------------------------------------------

func TestBrainHost_HealthStatus(t *testing.T) {
	m := NewAdapterManager(0)
	m.addEntry("a", mockReadyEntry("a", "mcp.a.", nil))
	host := NewBrainHost(m)

	if got := host.HealthStatus(); got != BrainHealthHealthy {
		t.Fatalf("expected Healthy, got %v", got)
	}
}

func TestBrainHost_HealthSnapshot(t *testing.T) {
	m := NewAdapterManager(0)
	m.addEntry("a", mockReadyEntry("a", "mcp.a.", nil))
	host := NewBrainHost(m)

	snap := host.HealthSnapshot()
	if snap["status"] != "healthy" {
		t.Fatalf("expected status=healthy, got %v", snap["status"])
	}
	adapters, ok := snap["adapters"].([]AdapterStatusSnapshot)
	if !ok {
		t.Fatalf("expected adapters to be []AdapterStatusSnapshot")
	}
	if len(adapters) != 1 {
		t.Fatalf("expected 1 adapter, got %d", len(adapters))
	}
}
