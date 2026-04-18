// converter.go — MCP tool to Brain tool conversion and concurrency spec inference.
//
// Design reference: 35-MCP-backed-Runtime设计.md §3.2, §6.1
package mcphost

import (
	"encoding/json"
	"strings"

	"github.com/leef-l/brain/sdk/tool"
)

// ConvertMCPTool converts an MCP tool spec into a Brain tool.Schema with
// appropriate prefix and brain kind.
func ConvertMCPTool(name, description string, inputSchema json.RawMessage, prefix, brainKind string) tool.Schema {
	return tool.Schema{
		Name:        prefix + name,
		Description: description,
		InputSchema: coalesceSchema(inputSchema),
		Brain:       brainKind,
	}
}

// ConvertMCPToolWithConcurrency converts an MCP tool and attaches an
// auto-inferred ToolConcurrencySpec. If overrides contains a matching entry
// for this tool name (unprefixed), the override is used instead.
func ConvertMCPToolWithConcurrency(
	name, description string,
	inputSchema json.RawMessage,
	prefix, brainKind string,
	overrides []ConcurrencyOverride,
) tool.Schema {
	schema := ConvertMCPTool(name, description, inputSchema, prefix, brainKind)

	// Check for explicit override first.
	for _, ov := range overrides {
		if ov.Tool == name {
			schema.Concurrency = &tool.ToolConcurrencySpec{
				Capability:          ov.Capability,
				ResourceKeyTemplate: ov.ResourceKeyTemplate,
				AccessMode:          ov.AccessMode,
				Scope:               coalesceString(ov.Scope, "turn"),
				AcquireTimeout:      coalesceFloat(ov.AcquireTimeout, 5.0),
			}
			return schema
		}
	}

	// Auto-infer concurrency spec.
	spec := InferConcurrencySpec(name, inputSchema, prefix)
	schema.Concurrency = &spec
	return schema
}

// InferConcurrencySpec infers a ToolConcurrencySpec from the MCP tool name
// and inputSchema, using conservative defaults.
//
// Rules (from design doc §6.1):
//   - Tool name contains write/create/delete/update/move/rename/patch → ExclusiveWrite
//   - Tool name contains read/get/list/search/query → SharedRead
//   - Unknown → SharedRead (conservative)
//
// ResourceKeyTemplate inference:
//   - inputSchema has "path" property → "workdir:{{path}}"
//   - inputSchema has "url" property → "url:{{url}}"
//   - Otherwise → "mcp:<prefix>*" (broad key, effectively brain-level lock)
func InferConcurrencySpec(toolName string, inputSchema json.RawMessage, prefix string) tool.ToolConcurrencySpec {
	return tool.ToolConcurrencySpec{
		Capability:          "mcp." + sanitizeName(prefix),
		ResourceKeyTemplate: inferResourceKey(inputSchema, prefix),
		AccessMode:          inferAccessMode(toolName),
		Scope:               "turn",
		AcquireTimeout:      5.0,
	}
}

// inferAccessMode determines the access mode based on the tool name.
func inferAccessMode(name string) string {
	lower := strings.ToLower(name)
	writeKeywords := []string{"write", "create", "delete", "update", "move", "rename", "patch"}
	for _, kw := range writeKeywords {
		if strings.Contains(lower, kw) {
			return "exclusive-write"
		}
	}
	return "shared-read"
}

// inferResourceKey determines the resource key template from the inputSchema.
func inferResourceKey(schema json.RawMessage, prefix string) string {
	if len(schema) == 0 {
		return "mcp:" + prefix + "*"
	}

	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return "mcp:" + prefix + "*"
	}
	if _, ok := s.Properties["path"]; ok {
		return "workdir:{{path}}"
	}
	if _, ok := s.Properties["url"]; ok {
		return "url:{{url}}"
	}
	return "mcp:" + prefix + "*"
}

// coalesceSchema returns the schema as-is or a minimal empty object schema.
func coalesceSchema(s json.RawMessage) json.RawMessage {
	if len(s) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return s
}

func coalesceString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func coalesceFloat(v, def float64) float64 {
	if v <= 0 {
		return def
	}
	return v
}
