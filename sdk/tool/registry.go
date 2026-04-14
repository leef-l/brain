// Package tool defines the Tool Registry contract for the Brain Kernel.
//
// The Tool interface, Registry interface, Schema struct, and Risk taxonomy
// are the frozen v1 contracts for how Brains expose callable tools to the
// LLM loop. Normative behavior — runtime dispatch, JSON-Schema validation,
// per-tool timeout, tool_result sanitizer pipeline — lives in
// 22-Agent-Loop规格.md §7/§8/§10/§11. This package only declares the
// surface area described in 02-BrainKernel设计.md §6.
//
// This package MUST NOT import any other brain/ subpackage: llm/ and tool/
// are peers and each defines its own lightweight ToolSchema equivalent to
// avoid an import cycle (see brain骨架实施计划.md §5.2 note).
package tool

// Registry is the process-wide Tool registry described in
// 02-BrainKernel设计.md §6.1. Each Brain calls Register during its
// bootstrap; the Kernel's LLM loop then resolves tool calls via Lookup.
// Tool names MUST be globally unique across all Brains — the naming
// convention is "<brain_kind>.<verb>" (e.g. "code.read_file",
// "central.code_brain"). See 02-BrainKernel设计.md §6.1 "命名规范铁律".
type Registry interface {
	// Register adds a Tool to the registry. It MUST reject duplicate
	// Name() values across all previously registered tools. See
	// 02-BrainKernel设计.md §6.1.
	Register(tool Tool) error

	// Lookup returns the Tool registered under name and a found flag.
	// It MUST NOT return a nil Tool with a true flag. See
	// 02-BrainKernel设计.md §6.1.
	Lookup(name string) (Tool, bool)

	// List returns every registered Tool. Order is unspecified. See
	// 02-BrainKernel设计.md §6.1.
	List() []Tool

	// ListByBrain returns every Tool whose Schema().Brain equals the
	// given brainKind. See 02-BrainKernel设计.md §6.1 naming convention.
	ListByBrain(brainKind string) []Tool
}
