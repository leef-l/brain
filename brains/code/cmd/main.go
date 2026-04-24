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

	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolguard"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

type codeHandler struct {
	registry tool.Registry
	caller   sidecar.KernelCaller
	learner  *kernel.DefaultBrainLearner
}

func newCodeHandler() *codeHandler {
	var reg tool.Registry = tool.NewMemRegistry()
	reg.Register(tool.NewReadFileTool("code"))
	reg.Register(tool.NewWriteFileTool("code"))
	reg.Register(tool.NewEditFileTool("code"))
	reg.Register(tool.NewDeleteFileTool("code"))
	reg.Register(tool.NewListFilesTool("code"))
	reg.Register(tool.NewSearchTool("code"))
	reg.Register(tool.NewShellExecTool("code", nil))
	reg.Register(tool.NewNoteTool("code"))

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-code: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindCode))...)
	}
	return &codeHandler{
		registry: reg,
		learner:  kernel.NewDefaultBrainLearner(agent.KindCode),
	}
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
	sidecar.SetProgressContext(caller, string(h.Kind()))
}

func (h *codeHandler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		return h.handleToolsCall(ctx, params)
	case "brain/execute":
		return h.handleExecute(ctx, params)
	case "brain/metrics":
		return h.learner.ExportMetrics(), nil
	case "brain/learn":
		return nil, h.handleLearn(ctx, params)
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

func (h *codeHandler) handleLearn(ctx context.Context, params json.RawMessage) error {
	var req struct {
		TaskType string  `json:"task_type"`
		Success  bool    `json:"success"`
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return err
	}
	return h.learner.RecordOutcome(ctx, kernel.TaskOutcome{
		TaskType: req.TaskType,
		Success:  req.Success,
		Duration: time.Duration(req.Duration * float64(time.Second)),
	})
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
		"You have tools for reading files, writing files, editing files (prefer code.edit_file over code.write_file for small changes to save tokens), " +
		"listing files, searching code, and executing shell commands. " +
		"Complete the task described in the user message. " +
		"Be precise and efficient. Write clean, working code.\n\n" +
		"FOR COMPLEX TASKS (3+ steps): start by calling code.note with action=add to plan your steps, " +
		"then mark each step done (action=done) as you complete it. This prevents getting lost mid-task. " +
		"For simple tasks, skip planning and just execute.\n\n" +
		"When done, summarize what you did."

	budget := req.Budget
	if budget == nil {
		budget = &sidecar.ExecuteBudget{MaxTurns: 10}
	} else if budget.MaxTurns <= 0 {
		budget.MaxTurns = 10
	}

	registry, err := h.buildRegistry(req.Execution)
	if err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  err.Error(),
		}, nil
	}

	start := time.Now()
	result := sidecar.RunAgentLoopFull(ctx, h.caller, registry, systemPrompt, req.Instruction, budget, req.Context)
	h.learner.RecordOutcome(ctx, kernel.TaskOutcome{
		TaskType:  "code.execute",
		Success:   result.Status == "completed",
		Duration:  time.Since(start),
		ToolCalls: result.Turns,
	})
	return result, nil
}

func (h *codeHandler) handleToolsCall(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return sidecar.DispatchToolCall(ctx, params, h.registry, h.buildRegistry)
}

func main() {
	listen := ""
	for i, arg := range os.Args[1:] {
		if arg == "--listen" && i+1 < len(os.Args[1:]) {
			listen = os.Args[i+2]
		}
	}

	verifyOpts, err := license.VerifyOptionsFromEnv(license.VerifyOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain-code: license config: %v\n", err)
		os.Exit(1)
	}
	if _, err := license.CheckSidecar("brain-code", verifyOpts); err != nil {
		fmt.Fprintf(os.Stderr, "brain-code: license: %v\n", err)
		os.Exit(1)
	}

	handler := newCodeHandler()
	var runErr error
	if listen != "" {
		runErr = sidecar.ListenAndServe(listen, handler)
	} else {
		runErr = sidecar.Run(handler)
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "brain-code: %v\n", runErr)
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
	reg.Register(toolguard.WrapFilePolicy(tool.WrapSandbox(tool.NewEditFileTool("code"), bounds.Sandbox), bounds.FilePolicy))
	reg.Register(toolguard.WrapDeletePolicy(tool.WrapSandbox(tool.NewDeleteFileTool("code"), bounds.Sandbox), bounds.FilePolicy))
	reg.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(tool.NewListFilesTool("code"), bounds.Sandbox), bounds.FilePolicy))
	reg.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(tool.NewSearchTool("code"), bounds.Sandbox), bounds.FilePolicy))

	sh := tool.NewShellExecTool("code", bounds.Sandbox)
	if bounds.CommandSandbox != nil {
		sh.SetCommandSandbox(bounds.CommandSandbox)
	}
	reg.Register(toolguard.WrapCommandPolicy(tool.WrapSandbox(sh, bounds.Sandbox), bounds.CommandSandbox, bounds.SandboxConfig, bounds.FilePolicy))
	reg.Register(tool.NewNoteTool("code"))

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-code: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindCode))...)
	}
	return reg, nil
}
