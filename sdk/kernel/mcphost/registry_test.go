package mcphost

import (
	"testing"

	"github.com/leef-l/brain/sdk/tool"
)

func TestMCPToolRegistry_RegisterFromAdapter(t *testing.T) {
	r := NewMCPToolRegistry()

	schemas := []tool.Schema{
		{Name: "mcp.fs.read_file", Brain: "mcp"},
		{Name: "mcp.fs.write_file", Brain: "mcp"},
	}
	reg, skip := r.RegisterFromAdapter("fs", schemas)
	if reg != 2 || skip != 0 {
		t.Fatalf("registered=%d, skipped=%d, want 2,0", reg, skip)
	}
	if r.Len() != 2 {
		t.Fatalf("Len()=%d, want 2", r.Len())
	}
}

func TestMCPToolRegistry_ConflictResolution(t *testing.T) {
	r := NewMCPToolRegistry()

	r.RegisterFromAdapter("adapter-a", []tool.Schema{
		{Name: "mcp.tool.do", Brain: "mcp"},
	})
	reg, skip := r.RegisterFromAdapter("adapter-b", []tool.Schema{
		{Name: "mcp.tool.do", Brain: "mcp"}, // conflict
		{Name: "mcp.tool.extra", Brain: "mcp"},
	})
	if reg != 1 || skip != 1 {
		t.Fatalf("registered=%d, skipped=%d, want 1,1", reg, skip)
	}
	if r.Len() != 2 {
		t.Fatalf("Len()=%d, want 2", r.Len())
	}

	// The first adapter should own the conflicting tool.
	_, owner, ok := r.Lookup("mcp.tool.do")
	if !ok {
		t.Fatal("Lookup should find mcp.tool.do")
	}
	if owner != "adapter-a" {
		t.Fatalf("owner=%q, want adapter-a", owner)
	}
}

func TestMCPToolRegistry_Lookup(t *testing.T) {
	r := NewMCPToolRegistry()
	r.RegisterFromAdapter("fs", []tool.Schema{
		{Name: "mcp.fs.read", Brain: "mcp"},
	})

	schema, adapter, ok := r.Lookup("mcp.fs.read")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if schema.Name != "mcp.fs.read" {
		t.Errorf("Name=%q", schema.Name)
	}
	if adapter != "fs" {
		t.Errorf("adapter=%q", adapter)
	}

	_, _, ok = r.Lookup("nonexistent")
	if ok {
		t.Fatal("expected ok=false for unknown tool")
	}
}

func TestMCPToolRegistry_AllSchemas(t *testing.T) {
	r := NewMCPToolRegistry()
	r.RegisterFromAdapter("a", []tool.Schema{
		{Name: "a.tool1"}, {Name: "a.tool2"},
	})
	r.RegisterFromAdapter("b", []tool.Schema{
		{Name: "b.tool1"},
	})

	all := r.AllSchemas()
	if len(all) != 3 {
		t.Fatalf("len=%d, want 3", len(all))
	}
	// Order should be insertion order.
	if all[0].Name != "a.tool1" || all[1].Name != "a.tool2" || all[2].Name != "b.tool1" {
		t.Errorf("unexpected order: %v", all)
	}
}

func TestMCPToolRegistry_ReRegisterAdapter(t *testing.T) {
	r := NewMCPToolRegistry()
	r.RegisterFromAdapter("fs", []tool.Schema{
		{Name: "mcp.fs.old_tool", Brain: "mcp"},
	})
	r.RegisterFromAdapter("other", []tool.Schema{
		{Name: "mcp.other.keep", Brain: "mcp"},
	})

	// Re-register fs with different tools.
	r.ReRegisterAdapter("fs", []tool.Schema{
		{Name: "mcp.fs.new_tool", Brain: "mcp"},
	})

	if r.Len() != 2 {
		t.Fatalf("Len()=%d, want 2", r.Len())
	}

	_, _, ok := r.Lookup("mcp.fs.old_tool")
	if ok {
		t.Error("old_tool should have been removed")
	}

	_, _, ok = r.Lookup("mcp.fs.new_tool")
	if !ok {
		t.Error("new_tool should exist")
	}

	_, _, ok = r.Lookup("mcp.other.keep")
	if !ok {
		t.Error("other adapter's tool should be preserved")
	}
}

func TestMCPToolRegistry_AdapterToolCount(t *testing.T) {
	r := NewMCPToolRegistry()
	r.RegisterFromAdapter("a", []tool.Schema{
		{Name: "a.1"}, {Name: "a.2"}, {Name: "a.3"},
	})
	r.RegisterFromAdapter("b", []tool.Schema{
		{Name: "b.1"},
	})

	if got := r.AdapterToolCount("a"); got != 3 {
		t.Errorf("a count=%d, want 3", got)
	}
	if got := r.AdapterToolCount("b"); got != 1 {
		t.Errorf("b count=%d, want 1", got)
	}
	if got := r.AdapterToolCount("unknown"); got != 0 {
		t.Errorf("unknown count=%d, want 0", got)
	}
}

func TestMCPToolRegistry_String(t *testing.T) {
	r := NewMCPToolRegistry()
	s := r.String()
	if s != "MCPToolRegistry{tools=0}" {
		t.Errorf("String()=%q", s)
	}
}
