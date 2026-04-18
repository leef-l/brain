package env

import (
	"context"
	"encoding/json"

	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

type confirmTool struct {
	inner   tool.Tool
	sandbox *tool.Sandbox
	prompt  ApprovalPrompter
}

// WrapConfirm wraps a tool to require user approval before execution.
func WrapConfirm(t tool.Tool, sb *tool.Sandbox, prompt ApprovalPrompter) tool.Tool {
	return &confirmTool{inner: t, sandbox: sb, prompt: prompt}
}

func (c *confirmTool) Name() string        { return c.inner.Name() }
func (c *confirmTool) Schema() tool.Schema { return c.inner.Schema() }
func (c *confirmTool) Risk() tool.Risk     { return c.inner.Risk() }

func (c *confirmTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	req := ApprovalRequest{
		Kind:     ApprovalTool,
		ToolName: c.inner.Name(),
		ToolRisk: c.inner.Risk(),
		Args:     args,
	}
	runtimeaudit.Emit(ctx, runtimeaudit.Event{
		Type:    "approval.requested",
		Message: "tool execution approval requested",
		Data:    json.RawMessage(marshalJSON(map[string]interface{}{"kind": req.Kind, "tool": req.ToolName, "risk": req.ToolRisk})),
	})

	if c.prompt == nil || !c.prompt(ctx, req) {
		runtimeaudit.Emit(ctx, runtimeaudit.Event{
			Type:    "approval.denied",
			Message: "tool execution denied by user",
			Data:    json.RawMessage(marshalJSON(map[string]interface{}{"kind": req.Kind, "tool": req.ToolName})),
		})
		return &tool.Result{
			Output:  json.RawMessage(`"user denied tool execution"`),
			IsError: true,
		}, nil
	}
	runtimeaudit.Emit(ctx, runtimeaudit.Event{
		Type:    "approval.allowed",
		Message: "tool execution approved",
		Data:    json.RawMessage(marshalJSON(map[string]interface{}{"kind": req.Kind, "tool": req.ToolName})),
	})
	return c.inner.Execute(ctx, args)
}

func marshalJSON(v interface{}) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
