package tool

import (
	"context"
	"encoding/json"
)

// EchoTool is a simple built-in tool for testing and smoke verification.
// It echoes back its input arguments without modification. See
// 02-BrainKernel设计.md §6.3 for the builtin tools specification.
type EchoTool struct {
	brainKind string
}

// NewEchoTool constructs an EchoTool registered to the given brainKind.
// The tool name will be "{brainKind}.echo".
func NewEchoTool(brainKind string) *EchoTool {
	return &EchoTool{
		brainKind: brainKind,
	}
}

// Name returns the globally unique tool name in the format "{brainKind}.echo".
// See 02-BrainKernel设计.md §6.1 "命名规范铁律".
func (t *EchoTool) Name() string {
	return t.brainKind + ".echo"
}

// Schema returns the LLM-facing schema for the echo tool. The input accepts
// an optional message parameter. See 02-BrainKernel设计.md §6.1.
func (t *EchoTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Echo back the input arguments without modification",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "message": {
      "type": "string"
    }
  }
}`),
		OutputSchema: dynamicJSONOutputSchema,
		Brain:        t.brainKind,
	}
}

// Risk returns RiskSafe because this tool is a side-effect-free read.
// See 02-BrainKernel设计.md §6.
func (t *EchoTool) Risk() Risk {
	return RiskSafe
}

// Execute runs the echo tool. It parses args as a JSON object and returns
// the raw args as output with IsError=false. See 02-BrainKernel设计.md §6.1
// and 22-Agent-Loop规格.md §7.
func (t *EchoTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	return &Result{
		Output:  args,
		IsError: false,
	}, nil
}
