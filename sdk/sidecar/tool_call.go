package sidecar

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
)

// RegistryBuilder constructs a request-scoped tool registry under an optional
// execution boundary.
type RegistryBuilder func(*executionpolicy.ExecutionSpec) (tool.Registry, error)

// DispatchToolCall handles a tools/call request against either a prebuilt
// registry or a request-scoped registry rebuilt from req.Execution.
func DispatchToolCall(ctx context.Context, params json.RawMessage, fallback tool.Registry, build RegistryBuilder) (interface{}, error) {
	var req protocol.ToolCallRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return toolCallFailure(req.Name, "parse_request", fmt.Sprintf("parse error: %v", err)), nil
	}

	reg := fallback
	if req.Execution != nil && build != nil {
		var err error
		reg, err = build(req.Execution)
		if err != nil {
			return toolCallFailure(req.Name, "build_registry", fmt.Sprintf("build registry: %v", err)), nil
		}
	}
	if reg == nil {
		return toolCallFailure(req.Name, "registry_unavailable", "tool registry unavailable"), nil
	}

	t, ok := reg.Lookup(req.Name)
	if !ok {
		return toolCallFailure(req.Name, "tool_not_found", fmt.Sprintf("tool not found: %s", req.Name)), nil
	}

	result, err := t.Execute(ctx, req.Arguments)
	if err != nil {
		return toolCallFailure(req.Name, "tool_execution_failed", fmt.Sprintf("tool error: %v", err)), nil
	}
	if result == nil {
		return toolCallFailure(req.Name, "tool_execution_failed", fmt.Sprintf("tool %s returned nil result", req.Name)), nil
	}

	return &protocol.ToolCallResult{
		Tool:    req.Name,
		Output:  append(json.RawMessage(nil), result.Output...),
		Content: toolCallOutputContent(result.Output),
		IsError: result.IsError,
	}, nil
}

func toolCallFailure(toolName, code, message string) *protocol.ToolCallResult {
	return &protocol.ToolCallResult{
		Tool:    toolName,
		Content: []protocol.ToolCallContent{{Type: "text", Text: message}},
		IsError: true,
		Error: &protocol.ToolCallError{
			Code:    code,
			Message: message,
		},
	}
}

func toolCallOutputContent(raw json.RawMessage) []protocol.ToolCallContent {
	if len(raw) == 0 {
		return nil
	}
	return []protocol.ToolCallContent{{Type: "text", Text: string(raw)}}
}
