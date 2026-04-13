// Command brain-verifier is the VerifierBrain sidecar binary.
//
// VerifierBrain is a read-only specialist brain that independently verifies
// work produced by other brains. It runs its own Agent Loop with only
// read-only tools (read_file, run_tests, check_output).
// See 02-BrainKernel设计.md §3 (decision 2: verifier is independent).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/executionpolicy"
	"github.com/leef-l/brain/license"
	"github.com/leef-l/brain/sidecar"
	"github.com/leef-l/brain/tool"
	"github.com/leef-l/brain/toolguard"
	"github.com/leef-l/brain/toolpolicy"
)

type verifierHandler struct {
	registry tool.Registry
	caller   sidecar.KernelCaller
}

func newVerifierHandler() *verifierHandler {
	var reg tool.Registry = tool.NewMemRegistry()
	reg.Register(tool.NewVerifierReadFileTool())
	reg.Register(tool.NewRunTestsTool())
	reg.Register(tool.NewCheckOutputTool())

	// Browser action tool — delegates to Browser Brain via Kernel.
	reg.Register(tool.NewBrowserActionTool())

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-verifier: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindVerifier))...)
	}

	return &verifierHandler{registry: reg}
}

func (h *verifierHandler) Kind() agent.Kind { return agent.KindVerifier }
func (h *verifierHandler) Version() string  { return brain.SDKVersion }
func (h *verifierHandler) Tools() []string  { return sidecar.RegistryToolNames(h.registry) }
func (h *verifierHandler) ToolSchemas() []tool.Schema {
	return sidecar.RegistryToolSchemas(h.registry)
}

// SetKernelCaller implements sidecar.RichBrainHandler.
func (h *verifierHandler) SetKernelCaller(caller sidecar.KernelCaller) {
	h.caller = caller
}

func (h *verifierHandler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		return h.handleToolsCall(ctx, params)
	case "brain/execute", "brain/verify":
		return h.handleVerify(ctx, params)
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

// handleVerify runs the Verifier Brain's Agent Loop for verification.
func (h *verifierHandler) handleVerify(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("parse request: %v", err),
		}, nil
	}

	if h.caller == nil {
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: "verifier brain ready (no LLM proxy available)",
			Turns:   0,
		}, nil
	}

	systemPrompt := "You are an independent verifier brain. Your job is to verify work " +
		"produced by other brains. You have READ-ONLY access to the codebase.\n\n" +
		"Your tools:\n" +
		"- verifier.read_file: Read file contents (read-only)\n" +
		"- verifier.run_tests: Run test suites\n" +
		"- verifier.check_output: Check command output against expectations\n" +
		"- verifier.browser_action: Request browser operations for UI verification\n" +
		"  (opens pages, clicks, types, takes screenshots via Browser Brain)\n\n" +
		"IMPORTANT:\n" +
		"- You CANNOT modify files — you are read-only\n" +
		"- You MUST be independent — judge the work objectively\n" +
		"- You MUST provide a clear verdict: PASS or FAIL\n" +
		"- Include specific reasons for your verdict\n" +
		"- If tests fail, report which tests and why\n" +
		"- For UI verification, use browser_action to take screenshots and " +
		"interact with the UI, then analyze the results\n" +
		"- browser_action invokes Browser Brain tools via the Kernel without " +
		"running Browser Brain's Agent Loop — you do not control the browser directly\n\n" +
		"Your response MUST end with one of:\n" +
		"  VERDICT: PASS — [reason]\n" +
		"  VERDICT: FAIL — [reason]"

	maxTurns := 8
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

func (h *verifierHandler) handleToolsCall(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return sidecar.DispatchToolCall(ctx, params, h.registry, h.buildRegistry)
}

func main() {
	if _, err := license.CheckSidecar("brain-verifier", license.VerifyOptions{}); err != nil {
		fmt.Fprintf(os.Stderr, "brain-verifier: license: %v\n", err)
		os.Exit(1)
	}
	if err := sidecar.Run(newVerifierHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "brain-verifier: %v\n", err)
		os.Exit(1)
	}
}

func (h *verifierHandler) buildRegistry(spec *executionpolicy.ExecutionSpec) (tool.Registry, error) {
	bounds, err := toolguard.NewBoundaries(spec)
	if err != nil {
		return nil, err
	}

	var reg tool.Registry = tool.NewMemRegistry()
	reg.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(tool.NewVerifierReadFileTool(), bounds.Sandbox), bounds.FilePolicy))

	rt := tool.NewRunTestsTool()
	rt.SetSandbox(bounds.Sandbox)
	if bounds.CommandSandbox != nil {
		rt.SetCommandSandbox(bounds.CommandSandbox)
	}
	reg.Register(toolguard.WrapCommandPolicy(tool.WrapSandbox(rt, bounds.Sandbox), bounds.CommandSandbox, bounds.SandboxConfig, bounds.FilePolicy))
	reg.Register(tool.NewCheckOutputTool())

	bat := tool.NewBrowserActionTool()
	bat.SetKernelCaller(h.caller)
	bat.SetExecutionSpec(spec)
	reg.Register(bat)

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-verifier: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindVerifier))...)
	}
	return reg, nil
}
