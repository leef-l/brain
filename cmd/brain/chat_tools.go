package main

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

// registerToolsForMode populates a registry with tools appropriate for
// the given permission mode. All tools are constrained to the sandbox;
// command-like tools additionally require an OS-level command sandbox.
func registerToolsForMode(reg tool.Registry, mode chatMode, brainKind string, baseEnv *executionEnvironment, prompt approvalPrompter) {
	env := *baseEnv
	env.mode = mode
	env.approver = prompt
	env.interactive = true
	if env.cmdSandbox == nil {
		env.cmdSandbox = tool.NewCommandSandbox(env.sandbox, env.sandboxCfg)
	}

	reg.Register(env.manageTool(tool.NewReadFileTool(brainKind), toolClassRead))
	reg.Register(env.manageTool(tool.NewSearchTool(brainKind), toolClassRead))
	reg.Register(tool.NewCheckOutputTool())

	if mode == modePlan {
		return
	}

	reg.Register(env.manageTool(tool.NewWriteFileTool(brainKind), toolClassEdit))
	reg.Register(env.manageTool(tool.NewDeleteFileTool(brainKind), toolClassDelete))
	reg.Register(newManagedShellTool(brainKind, &env))
	reg.Register(newManagedRunTestsTool(&env))
}

// buildToolSchemas extracts ToolSchema list from a registry.
func buildToolSchemas(reg tool.Registry) []llm.ToolSchema {
	var schemas []llm.ToolSchema
	for _, t := range reg.List() {
		s := t.Schema()
		schemas = append(schemas, llm.ToolSchema{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		})
	}
	return schemas
}

// confirmTool wraps a tool to require user y/n before execution.
type confirmTool struct {
	inner   tool.Tool
	sandbox *tool.Sandbox
	prompt  approvalPrompter
}

func wrapConfirm(t tool.Tool, sb *tool.Sandbox, prompt approvalPrompter) tool.Tool {
	return &confirmTool{inner: t, sandbox: sb, prompt: prompt}
}

func (c *confirmTool) Name() string        { return c.inner.Name() }
func (c *confirmTool) Schema() tool.Schema { return c.inner.Schema() }
func (c *confirmTool) Risk() tool.Risk     { return c.inner.Risk() }

func (c *confirmTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	// Sandbox path checks are now handled by SandboxTool's Approver.
	// confirmTool only handles tool execution confirmation.
	req := approvalRequest{
		kind:     approvalTool,
		toolName: c.inner.Name(),
		toolRisk: c.inner.Risk(),
		args:     args,
	}
	runtimeaudit.Emit(ctx, runtimeaudit.Event{
		Type:    "approval.requested",
		Message: "tool execution approval requested",
		Data:    json.RawMessage(fmtJSON(map[string]interface{}{"kind": req.kind, "tool": req.toolName, "risk": req.toolRisk})),
	})

	if c.prompt == nil || !c.prompt(ctx, req) {
		runtimeaudit.Emit(ctx, runtimeaudit.Event{
			Type:    "approval.denied",
			Message: "tool execution denied by user",
			Data:    json.RawMessage(fmtJSON(map[string]interface{}{"kind": req.kind, "tool": req.toolName})),
		})
		return &tool.Result{
			Output:  json.RawMessage(`"user denied tool execution"`),
			IsError: true,
		}, nil
	}
	runtimeaudit.Emit(ctx, runtimeaudit.Event{
		Type:    "approval.allowed",
		Message: "tool execution approved",
		Data:    json.RawMessage(fmtJSON(map[string]interface{}{"kind": req.kind, "tool": req.toolName})),
	})
	return c.inner.Execute(ctx, args)
}

// extractOutsidePath checks if tool args contain a path outside the sandbox.
func extractOutsidePath(args json.RawMessage, sb *tool.Sandbox) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(args, &fields) != nil {
		return ""
	}
	for _, key := range []string{"path", "working_dir"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var pathVal string
		if json.Unmarshal(raw, &pathVal) != nil || pathVal == "" {
			continue
		}
		abs, err := sb.Check(pathVal)
		if err != nil {
			return filepath.Dir(abs)
		}
	}
	return ""
}
