package mcphost

import (
	"encoding/json"
	"testing"
)

func TestConvertMCPTool_Basic(t *testing.T) {
	schema := ConvertMCPTool(
		"read_file", "Read a file",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		"mcp.fs.", "filesystem",
	)
	if schema.Name != "mcp.fs.read_file" {
		t.Errorf("Name=%q", schema.Name)
	}
	if schema.Description != "Read a file" {
		t.Errorf("Description=%q", schema.Description)
	}
	if schema.Brain != "filesystem" {
		t.Errorf("Brain=%q", schema.Brain)
	}
}

func TestConvertMCPTool_NilSchema(t *testing.T) {
	schema := ConvertMCPTool("tool", "desc", nil, "mcp.", "brain")
	if string(schema.InputSchema) != `{"type":"object","properties":{}}` {
		t.Errorf("InputSchema=%s", schema.InputSchema)
	}
}

func TestInferAccessMode(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"read_file", "shared-read"},
		{"write_file", "exclusive-write"},
		{"create_dir", "exclusive-write"},
		{"delete_item", "exclusive-write"},
		{"update_config", "exclusive-write"},
		{"move_file", "exclusive-write"},
		{"rename_dir", "exclusive-write"},
		{"patch_doc", "exclusive-write"},
		{"list_directory", "shared-read"},
		{"search_files", "shared-read"},
		{"get_info", "shared-read"},
		{"query_data", "shared-read"},
		{"unknown_action", "shared-read"},
	}
	for _, tt := range tests {
		if got := inferAccessMode(tt.name); got != tt.want {
			t.Errorf("inferAccessMode(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestInferResourceKey(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		prefix string
		want   string
	}{
		{"with path", `{"type":"object","properties":{"path":{"type":"string"}}}`, "mcp.fs.", "workdir:{{path}}"},
		{"with url", `{"type":"object","properties":{"url":{"type":"string"}}}`, "mcp.web.", "url:{{url}}"},
		{"with both", `{"type":"object","properties":{"path":{"type":"string"},"url":{"type":"string"}}}`, "mcp.x.", "workdir:{{path}}"},
		{"no match", `{"type":"object","properties":{"query":{"type":"string"}}}`, "mcp.x.", "mcp:mcp.x.*"},
		{"empty schema", "", "mcp.x.", "mcp:mcp.x.*"},
		{"bad json", "not json", "mcp.x.", "mcp:mcp.x.*"},
	}
	for _, tt := range tests {
		got := inferResourceKey(json.RawMessage(tt.schema), tt.prefix)
		if got != tt.want {
			t.Errorf("%s: inferResourceKey = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestInferConcurrencySpec(t *testing.T) {
	spec := InferConcurrencySpec(
		"write_file",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		"mcp.fs.",
	)
	if spec.AccessMode != "exclusive-write" {
		t.Errorf("AccessMode=%q", spec.AccessMode)
	}
	if spec.ResourceKeyTemplate != "workdir:{{path}}" {
		t.Errorf("ResourceKeyTemplate=%q", spec.ResourceKeyTemplate)
	}
	if spec.Scope != "turn" {
		t.Errorf("Scope=%q", spec.Scope)
	}
	if spec.Capability != "mcp.mcp.fs" {
		t.Errorf("Capability=%q", spec.Capability)
	}
}

func TestConvertMCPToolWithConcurrency_Override(t *testing.T) {
	overrides := []ConcurrencyOverride{
		{
			Tool:                "write_file",
			Capability:          "fs.write",
			ResourceKeyTemplate: "workdir:{{path}}",
			AccessMode:          "exclusive-write",
			Scope:               "turn",
		},
	}
	schema := ConvertMCPToolWithConcurrency(
		"write_file", "Write a file",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		"mcp.fs.", "filesystem",
		overrides,
	)
	if schema.Concurrency == nil {
		t.Fatal("Concurrency should not be nil")
	}
	if schema.Concurrency.Capability != "fs.write" {
		t.Errorf("Capability=%q, want fs.write", schema.Concurrency.Capability)
	}
}

func TestConvertMCPToolWithConcurrency_NoOverride(t *testing.T) {
	schema := ConvertMCPToolWithConcurrency(
		"read_file", "Read a file",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		"mcp.fs.", "filesystem",
		nil,
	)
	if schema.Concurrency == nil {
		t.Fatal("Concurrency should not be nil")
	}
	if schema.Concurrency.AccessMode != "shared-read" {
		t.Errorf("AccessMode=%q, want shared-read", schema.Concurrency.AccessMode)
	}
}

func TestCoalesceHelpers(t *testing.T) {
	if coalesceString("", "default") != "default" {
		t.Error("coalesceString empty")
	}
	if coalesceString("val", "default") != "val" {
		t.Error("coalesceString non-empty")
	}
	if coalesceFloat(0, 5.0) != 5.0 {
		t.Error("coalesceFloat zero")
	}
	if coalesceFloat(3.0, 5.0) != 3.0 {
		t.Error("coalesceFloat non-zero")
	}
}
