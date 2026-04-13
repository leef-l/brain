package sidecar

import (
	"encoding/json"

	"github.com/leef-l/brain/tool"
)

// ToolSchemaProvider is an optional BrainHandler extension that exposes full
// tool schemas for tools/list and future manifest/package metadata.
type ToolSchemaProvider interface {
	BrainHandler
	ToolSchemas() []tool.Schema
}

type toolSpec struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

func RegistryToolNames(reg tool.Registry) []string {
	tools := reg.List()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return names
}

func RegistryToolSchemas(reg tool.Registry) []tool.Schema {
	tools := reg.List()
	schemas := make([]tool.Schema, 0, len(tools))
	for _, t := range tools {
		schemas = append(schemas, t.Schema())
	}
	return schemas
}

func toolSpecsForHandler(handler BrainHandler) []toolSpec {
	if provider, ok := handler.(ToolSchemaProvider); ok {
		schemas := provider.ToolSchemas()
		specs := make([]toolSpec, 0, len(schemas))
		for _, s := range schemas {
			specs = append(specs, toolSpec{
				Name:         s.Name,
				Description:  s.Description,
				InputSchema:  append(json.RawMessage(nil), s.InputSchema...),
				OutputSchema: append(json.RawMessage(nil), s.OutputSchema...),
			})
		}
		return specs
	}

	tools := handler.Tools()
	specs := make([]toolSpec, 0, len(tools))
	for _, t := range tools {
		specs = append(specs, toolSpec{Name: t})
	}
	return specs
}
