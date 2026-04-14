// Command brain-code is the CodeBrain sidecar binary.
//
// CodeBrain is a specialist brain that reads, writes, and edits code files.
// It runs its own Agent Loop internally, calling llm.complete via reverse
// RPC to the Kernel, and executing tools locally.
// See 02-BrainKernel设计.md §3.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolguard"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

type codeHandler struct {
	registry tool.Registry
	caller   sidecar.KernelCaller
}

func newCodeHandler() *codeHandler {
	var reg tool.Registry = tool.NewMemRegistry()
	reg.Register(tool.NewReadFileTool("code"))
	reg.Register(tool.NewWriteFileTool("code"))
	reg.Register(tool.NewDeleteFileTool("code"))
	reg.Register(tool.NewSearchTool("code"))
	reg.Register(tool.NewShellExecTool("code", nil))

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-code: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindCode))...)
	}
	return &codeHandler{registry: reg}
}

func (h *codeHandler) Kind() agent.Kind { return agent.KindCode }
func (h *codeHandler) Version() string  { return brain.SDKVersion }
func (h *codeHandler) Tools() []string  { return sidecar.RegistryToolNames(h.registry) }
func (h *codeHandler) ToolSchemas() []tool.Schema {
	return sidecar.RegistryToolSchemas(h.registry)
}

// SetKernelCaller implements sidecar.RichBrainHandler.
func (h *codeHandler) SetKernelCaller(caller sidecar.KernelCaller) {
	h.caller = caller
}

func (h *codeHandler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		return h.handleToolsCall(ctx, params)
	case "brain/execute":
		return h.handleExecute(ctx, params)
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

// handleExecute runs the Code Brain's Agent Loop for a delegated task.
func (h *codeHandler) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("parse request: %v", err),
		}, nil
	}

	if h.caller == nil {
		// No reverse RPC — return stub response.
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: "code brain ready (no LLM proxy available)",
			Turns:   0,
		}, nil
	}

	systemPrompt := "You are a specialist code brain. Your job is to write, edit, and debug code. " +
		"You have tools for reading files, writing files, searching code, and executing shell commands. " +
		"Complete the task described in the user message. " +
		"Be precise and efficient. Write clean, working code. " +
		"When done, summarize what you did."

	maxTurns := 10
	if req.Budget != nil && req.Budget.MaxTurns > 0 {
		maxTurns = req.Budget.MaxTurns
	}

	registry, err := h.buildRegistry(req.Execution)
	if err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  err.Error(),
		}, nil
	}

	return sidecar.RunAgentLoop(ctx, h.caller, registry, systemPrompt, req.Instruction, maxTurns), nil
}

func (h *codeHandler) handleToolsCall(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return sidecar.DispatchToolCall(ctx, params, h.registry, h.buildRegistry)
}

func main() {
	if _, err := license.CheckSidecar("brain-code", license.VerifyOptions{}); err != nil {
		fmt.Fprintf(os.Stderr, "brain-code: license: %v\n", err)
		os.Exit(1)
	}
	if err := sidecar.Run(newCodeHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "brain-code: %v\n", err)
		os.Exit(1)
	}
}

func (h *codeHandler) buildRegistry(spec *executionpolicy.ExecutionSpec) (tool.Registry, error) {
	bounds, err := toolguard.NewBoundaries(spec)
	if err != nil {
		return nil, err
	}

	var reg tool.Registry = tool.NewMemRegistry()
	reg.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(tool.NewReadFileTool("code"), bounds.Sandbox), bounds.FilePolicy))
	reg.Register(toolguard.WrapFilePolicy(tool.WrapSandbox(tool.NewWriteFileTool("code"), bounds.Sandbox), bounds.FilePolicy))
	reg.Register(toolguard.WrapDeletePolicy(tool.WrapSandbox(tool.NewDeleteFileTool("code"), bounds.Sandbox), bounds.FilePolicy))
	reg.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(tool.NewSearchTool("code"), bounds.Sandbox), bounds.FilePolicy))

	sh := tool.NewShellExecTool("code", bounds.Sandbox)
	if bounds.CommandSandbox != nil {
		sh.SetCommandSandbox(bounds.CommandSandbox)
	}
	reg.Register(toolguard.WrapCommandPolicy(tool.WrapSandbox(sh, bounds.Sandbox), bounds.CommandSandbox, bounds.SandboxConfig, bounds.FilePolicy))

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-code: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindCode))...)
	}
	return reg, nil
}
