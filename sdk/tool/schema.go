package tool

import "encoding/json"

// Schema is the LLM-facing description of a Tool declared in
// 02-BrainKernel设计.md §6.1. It is handed to the LLM provider as part of
// ChatRequest.Tools so the model knows which tools are callable and what
// arguments each one accepts. The llm/ package defines its own lightweight
// ToolSchema mirror to avoid an import cycle — see
// brain骨架实施计划.md §5.2 note.
type Schema struct {
	// Name is the globally unique tool name, e.g. "code.read_file".
	// MUST match the Tool.Name() return value. See
	// 02-BrainKernel设计.md §6.1 "命名规范铁律".
	Name string `json:"name"`

	// Description is a short human-readable explanation shown to the LLM.
	// See 02-BrainKernel设计.md §6.1.
	Description string `json:"description"`

	// InputSchema is the JSON Schema (draft 2020-12) of the tool's
	// arguments object. Stored as raw JSON so this package does not
	// need a JSON Schema library. See 02-BrainKernel设计.md §6.1.
	InputSchema json.RawMessage `json:"input_schema"`

	// OutputSchema is the optional JSON Schema (draft 2020-12) of the
	// tool's success payload. It is not sent to the LLM, but is used by
	// CLI introspection, sidecar tools/list, and future manifest/package
	// metadata. Empty means "unspecified / dynamic".
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`

	// Brain is the brain_kind that registered this Tool (e.g. "code",
	// "browser", "central"). Used by Registry.ListByBrain. See
	// 02-BrainKernel设计.md §6.1 naming convention.
	Brain string `json:"brain"`
}
