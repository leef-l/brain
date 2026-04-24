// Command brain-fault is the FaultBrain sidecar binary.
//
// FaultBrain is a specialist brain for chaos engineering and fault injection.
// It can simulate failures, inject latency, kill processes, and corrupt
// command output to test system resilience.
//
// It runs its own Agent Loop internally, calling llm.complete via reverse
// RPC to the Kernel, and executing fault injection tools locally.
//
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

type faultHandler struct {
	registry tool.Registry
	caller   sidecar.KernelCaller
	learner  *kernel.DefaultBrainLearner
}

func newFaultHandler() *faultHandler {
	var reg tool.Registry = tool.NewMemRegistry()
	reg.Register(tool.NewInjectErrorTool())
	reg.Register(tool.NewInjectLatencyTool())
	reg.Register(tool.NewKillProcessTool())
	reg.Register(tool.NewCorruptResponseTool())
	reg.Register(tool.NewNoteTool("fault"))

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-fault: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindFault))...)
	}
	return &faultHandler{
		registry: reg,
		learner:  kernel.NewDefaultBrainLearner(agent.KindFault),
	}
}

func (h *faultHandler) Kind() agent.Kind { return agent.KindFault }
func (h *faultHandler) Version() string  { return brain.SDKVersion }
func (h *faultHandler) Tools() []string  { return sidecar.RegistryToolNames(h.registry) }
func (h *faultHandler) ToolSchemas() []tool.Schema {
	return sidecar.RegistryToolSchemas(h.registry)
}

// SetKernelCaller implements sidecar.RichBrainHandler.
func (h *faultHandler) SetKernelCaller(caller sidecar.KernelCaller) {
	h.caller = caller
	sidecar.SetProgressContext(caller, string(h.Kind()))
}

func (h *faultHandler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
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

func (h *faultHandler) handleLearn(ctx context.Context, params json.RawMessage) error {
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

// handleExecute runs the Fault Brain's Agent Loop for a delegated task.
func (h *faultHandler) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
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
			Summary: "fault brain ready (no LLM proxy available)",
			Turns:   0,
		}, nil
	}

	systemPrompt := `You are a specialist fault brain for chaos engineering and resilience testing.

Your job is to inject faults, simulate failures, and test how systems handle errors.
You have these tools:

- fault.inject_error: Inject errors — file corruption, env poisoning, disk full simulation
- fault.inject_latency: Add artificial latency to command execution
- fault.kill_process: Kill processes by PID or name (SIGTERM, SIGKILL, SIGSTOP, etc.)
- fault.corrupt_response: Run commands and corrupt their output (truncate, shuffle, replace, noise)

IMPORTANT SAFETY RULES:
- NEVER kill critical system processes (init, systemd, sshd, kernel threads)
- NEVER corrupt files in /etc, /boot, or other system directories
- Always create COPIES when corrupting files — never modify originals
- Prefer SIGTERM over SIGKILL (give processes a chance to clean up)
- Document what you did so the user can undo it
- disk_full is capped at 1GB to prevent actual disk exhaustion

WORKFLOW:
1. Understand what the user wants to test
2. Plan the fault injection strategy
3. Execute the faults
4. Report what was injected and expected impact
5. Suggest how to verify resilience and clean up

When done, summarize:
- What faults were injected
- Expected impact on the system
- How to clean up / revert`

	budget := req.Budget
	if budget == nil {
		budget = &sidecar.ExecuteBudget{MaxTurns: 8}
	} else if budget.MaxTurns <= 0 {
		budget.MaxTurns = 8
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
		TaskType:  "fault.execute",
		Success:   result.Status == "completed",
		Duration:  time.Since(start),
		ToolCalls: result.Turns,
	})
	return result, nil
}

func (h *faultHandler) handleToolsCall(ctx context.Context, params json.RawMessage) (interface{}, error) {
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
		fmt.Fprintf(os.Stderr, "brain-fault: license config: %v\n", err)
		os.Exit(1)
	}
	if _, err := license.CheckSidecar("brain-fault", verifyOpts); err != nil {
		fmt.Fprintf(os.Stderr, "brain-fault: license: %v\n", err)
		os.Exit(1)
	}

	handler := newFaultHandler()
	var runErr error
	if listen != "" {
		runErr = sidecar.ListenAndServe(listen, handler)
	} else {
		runErr = sidecar.Run(handler)
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "brain-fault: %v\n", runErr)
		os.Exit(1)
	}
}

func (h *faultHandler) buildRegistry(spec *executionpolicy.ExecutionSpec) (tool.Registry, error) {
	bounds, err := toolguard.NewBoundaries(spec)
	if err != nil {
		return nil, err
	}

	var reg tool.Registry = tool.NewMemRegistry()
	reg.Register(tool.WrapSandbox(tool.NewInjectErrorTool(), bounds.Sandbox))
	reg.Register(tool.WrapSandbox(tool.NewInjectLatencyTool(), bounds.Sandbox))
	reg.Register(tool.WrapSandbox(tool.NewKillProcessTool(), bounds.Sandbox))
	reg.Register(tool.WrapSandbox(tool.NewCorruptResponseTool(), bounds.Sandbox))
	reg.Register(tool.NewNoteTool("fault"))

	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-fault: load tool policy: %v\n", err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(agent.KindFault))...)
	}
	return reg, nil
}
