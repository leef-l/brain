// Package shared provides the ThinBrain abstraction — a reusable, generic
// sidecar brain implementation that eliminates the ~150 lines of boilerplate
// duplicated in every specialist brain (code, verifier, fault, desktop).
//
// A ThinBrain wraps: tool registry, HandleMethod dispatch, brain/execute Agent
// Loop, brain/metrics export, brain/learn feedback, and license bootstrap.
// Each concrete brain only needs to declare its tools + systemPrompt.
package shared

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolguard"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

// ResultHook is an optional post-processor for brain/execute results.
// VerifierBrain uses it to inject VERDICT: PASS/FAIL parsing.
type ResultHook func(*sidecar.ExecuteResult)

// ExecuteHandler is an optional custom handler for brain/execute.
// If set, it replaces the default Agent Loop execution.
// BrowserBrain uses it to implement the perception-first architecture.
type ExecuteHandler func(ctx context.Context, params json.RawMessage) (interface{}, error)

// ThinBrain is the generic sidecar brain implementation.
type ThinBrain struct {
	kind            agent.Kind
	version         string
	registry        tool.Registry
	caller          sidecar.KernelCaller
	learner         kernel.BrainLearner

	systemPrompt    string
	defaultMaxTurns int
	taskTypeLabel   string

	registryBuilder sidecar.RegistryBuilder
	resultHook      ResultHook
}

// NewThinBrain creates a generic ThinBrain.
//
// Parameters:
//   - kind: brain role identifier (e.g. agent.KindCode)
//   - registry: base tool registry (used for static tools/list)
//   - systemPrompt: the system prompt injected into brain/execute Agent Loop
//   - defaultMaxTurns: default budget for brain/execute (overridden by request)
func NewThinBrain(kind agent.Kind, registry tool.Registry, systemPrompt string, defaultMaxTurns int) *ThinBrain {
	if defaultMaxTurns <= 0 {
		defaultMaxTurns = 10
	}
	return &ThinBrain{
		kind:            kind,
		version:         "1.0.0",
		registry:        registry,
		learner:         kernel.NewDefaultBrainLearner(kind),
		systemPrompt:    systemPrompt,
		defaultMaxTurns: defaultMaxTurns,
		taskTypeLabel:   fmt.Sprintf("%s.execute", kind),
	}
}

// WithLearner replaces the default BrainLearner with a domain-specialized
// implementation. Call this before RunBrain if the brain has custom L0
// learning logic.
func (tb *ThinBrain) WithLearner(l kernel.BrainLearner) *ThinBrain {
	tb.learner = l
	return tb
}

// WithRegistryBuilder sets the per-request registry builder used when
// brain/execute or tools/call carries an ExecutionSpec.
func (tb *ThinBrain) WithRegistryBuilder(builder sidecar.RegistryBuilder) *ThinBrain {
	tb.registryBuilder = builder
	return tb
}

// WithResultHook sets a post-processor for brain/execute results.
func (tb *ThinBrain) WithResultHook(hook ResultHook) *ThinBrain {
	tb.resultHook = hook
	return tb
}

// WithTaskTypeLabel overrides the default task-type label used in learning
// records (default is "{kind}.execute").
func (tb *ThinBrain) WithTaskTypeLabel(label string) *ThinBrain {
	tb.taskTypeLabel = label
	return tb
}

// ---------------------------------------------------------------------------
// sidecar.BrainHandler implementation
// ---------------------------------------------------------------------------

// Kind returns the brain role.
func (tb *ThinBrain) Kind() agent.Kind { return tb.kind }

// Version returns the brain sidecar version.
func (tb *ThinBrain) Version() string { return tb.version }

// Tools returns the list of supported tool names.
func (tb *ThinBrain) Tools() []string { return sidecar.RegistryToolNames(tb.registry) }

// ToolSchemas returns full tool schemas.
func (tb *ThinBrain) ToolSchemas() []tool.Schema {
	return sidecar.RegistryToolSchemas(tb.registry)
}

// ---------------------------------------------------------------------------
// sidecar.RichBrainHandler implementation
// ---------------------------------------------------------------------------

// SetKernelCaller receives the reverse-RPC channel from the sidecar runtime.
func (tb *ThinBrain) SetKernelCaller(caller sidecar.KernelCaller) {
	tb.caller = caller
	sidecar.SetProgressContext(caller, string(tb.kind))
}

// ---------------------------------------------------------------------------
// RPC method dispatch
// ---------------------------------------------------------------------------

// HandleMethod dispatches brain-specific RPC methods.
func (tb *ThinBrain) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		return tb.handleToolsCall(ctx, params)
	case "brain/execute":
		return tb.handleExecute(ctx, params)
	case "brain/metrics":
		return tb.learner.ExportMetrics(), nil
	case "brain/learn":
		return nil, tb.handleLearn(ctx, params)
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

// ---------------------------------------------------------------------------
// Internal handlers
// ---------------------------------------------------------------------------

func (tb *ThinBrain) handleLearn(ctx context.Context, params json.RawMessage) error {
	var req struct {
		TaskType string  `json:"task_type"`
		Success  bool    `json:"success"`
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return err
	}
	return tb.learner.RecordOutcome(ctx, kernel.TaskOutcome{
		TaskType: req.TaskType,
		Success:  req.Success,
		Duration: time.Duration(req.Duration * float64(time.Second)),
	})
}

func (tb *ThinBrain) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("parse request: %v", err),
		}, nil
	}

	if tb.caller == nil {
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: fmt.Sprintf("%s brain ready (no LLM proxy available)", tb.kind),
			Turns:   0,
		}, nil
	}

	budget := req.Budget
	if budget == nil {
		budget = &sidecar.ExecuteBudget{MaxTurns: tb.defaultMaxTurns}
	} else if budget.MaxTurns <= 0 {
		budget.MaxTurns = tb.defaultMaxTurns
	}

	var registry tool.Registry = tb.registry
	if req.Execution != nil && tb.registryBuilder != nil {
		var err error
		registry, err = tb.registryBuilder(req.Execution)
		if err != nil {
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  err.Error(),
			}, nil
		}
	}

	tb.injectKernelCaller(registry)

	start := time.Now()

	var observer loop.ToolObserver = sidecar.StderrToolObserver{}
	if req.PipeID != "" {
		observer = &sidecar.StreamingToolObserver{PipeID: req.PipeID, Base: sidecar.StderrToolObserver{}}
	}
	result := sidecar.RunAgentLoopFull(ctx, tb.caller, registry, tb.systemPrompt, req.Instruction, budget, req.Context, observer, req.ExecutionID)

	if tb.resultHook != nil {
		tb.resultHook(result)
	}

	tb.learner.RecordOutcome(ctx, kernel.TaskOutcome{
		TaskType:  tb.taskTypeLabel,
		Success:   result.Status == "completed",
		Duration:  time.Since(start),
		ToolCalls: result.Turns,
	})
	return result, nil
}

func (tb *ThinBrain) handleToolsCall(ctx context.Context, params json.RawMessage) (interface{}, error) {
	// 提取 tool name 用于领域学习指标分类
	var req struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(params, &req)

	result, err := sidecar.DispatchToolCall(ctx, params, tb.registry, tb.registryBuilderWithInjection)

	// 如果 learner 支持工具级记录，驱动领域特化指标更新
	if req.Name != "" {
		if recorder, ok := tb.learner.(kernel.ToolOutcomeRecorder); ok {
			success := err == nil
			if r, ok := result.(*protocol.ToolCallResult); ok {
				success = !r.IsError
			}
			recorder.RecordToolOutcome(req.Name, success)
		}
	}

	return result, err
}

// registryBuilderWithInjection wraps the user's registryBuilder and injects
// KernelCaller into any tools that accept it (e.g. BrowserActionTool).
func (tb *ThinBrain) registryBuilderWithInjection(spec *executionpolicy.ExecutionSpec) (tool.Registry, error) {
	if tb.registryBuilder == nil {
		return tb.registry, nil
	}
	reg, err := tb.registryBuilder(spec)
	if err != nil {
		return nil, err
	}
	tb.injectKernelCaller(reg)
	return reg, nil
}

// injectKernelCaller finds tools implementing KernelCallerInjector and wires
// the current reverse-RPC caller so they can delegate to the Kernel.
func (tb *ThinBrain) injectKernelCaller(registry tool.Registry) {
	if tb.caller == nil || registry == nil {
		return
	}
	for _, t := range registry.List() {
		if injector, ok := t.(interface{ SetKernelCaller(sidecar.KernelCaller) }); ok {
			injector.SetKernelCaller(tb.caller)
		}
	}
}

// RegisterWithPolicy registers a slice of tools into a fresh MemRegistry,
// then applies tool-policy filtering for the given brain kind.
func RegisterWithPolicy(kind agent.Kind, tools ...tool.Tool) tool.Registry {
	var reg tool.Registry = tool.NewMemRegistry()
	for _, t := range tools {
		reg.Register(t)
	}
	if cfg, err := toolpolicy.Load(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain-%s: load tool policy: %v\n", kind, err)
	} else {
		reg = toolpolicy.FilterRegistry(reg, cfg, toolpolicy.ToolScopesForDelegate(string(kind))...)
	}
	return reg
}

// ThinBrainMainConfig holds the configuration for running a thin brain main().
type ThinBrainMainConfig struct {
	Name                  string
	Handler               sidecar.BrainHandler
	PreRun                func() error
	LicenseResultCallback func(interface{}) // using interface{} to avoid importing license here if not needed
}

// RunThinBrainMain encapsulates the common boilerplate of every thin brain's main():
//   1. Parse --listen flag
//   2. License check
//   3. Optional PreRun hook
//   4. Run via stdio or ListenAndServe
func RunThinBrainMain(cfg ThinBrainMainConfig) error {
	listen := ""
	for i, arg := range os.Args[1:] {
		if arg == "--listen" && i+1 < len(os.Args[1:]) {
			listen = os.Args[i+2]
		}
	}

	verifyOpts, err := license.VerifyOptionsFromEnv(license.VerifyOptions{})
	if err != nil {
		return fmt.Errorf("%s: license config: %v", cfg.Name, err)
	}
	res, err := license.CheckSidecar(cfg.Name, verifyOpts)
	if err != nil {
		return fmt.Errorf("%s: license: %v", cfg.Name, err)
	}
	if cfg.LicenseResultCallback != nil {
		cfg.LicenseResultCallback(res)
	}
	if cfg.PreRun != nil {
		if err := cfg.PreRun(); err != nil {
			return err
		}
	}

	if listen != "" {
		return sidecar.ListenAndServe(listen, cfg.Handler)
	}
	return sidecar.Run(cfg.Handler)
}

// RunBrain is a convenience one-liner for standard thin brains.
// It creates a ThinBrain from the named brain's defaults and runs it.
func RunBrain(name string) error {
	return RunBrainWithLearner(name, nil)
}

// RunBrainWithLearner is like RunBrain but allows injecting a domain-specific
// BrainLearner (e.g. CodeBrainLearner, VerifierBrainLearner) instead of the
// generic DefaultBrainLearner.
func RunBrainWithLearner(name string, learner kernel.BrainLearner) error {
	var tb *ThinBrain
	switch name {
	case "code":
		reg := RegisterWithPolicy(agent.KindCode,
			tool.NewReadFileTool("code"),
			tool.NewWriteFileTool("code"),
			tool.NewEditFileTool("code"),
			tool.NewDeleteFileTool("code"),
			tool.NewListFilesTool("code"),
			tool.NewSearchTool("code"),
			tool.NewShellExecTool("code", nil),
			tool.NewNoteTool("code"),
		)
		systemPrompt := "You are a specialist code brain. Your job is to write, edit, and debug code. " +
			"You have tools for reading files, writing files, editing files (prefer code.edit_file over code.write_file for small changes to save tokens), " +
			"listing files, searching code, and executing shell commands. " +
			"Complete the task described in the user message. " +
			"Be precise and efficient. Write clean, working code.\n\n" +
			"FOR COMPLEX TASKS (3+ steps): start by calling code.note with action=add to plan your steps, " +
			"then mark each step done (action=done) as you complete them. This prevents getting lost mid-task. " +
			"For simple tasks, skip planning and just execute.\n\n" +
			"When done, summarize what you did."
		tb = NewThinBrain(agent.KindCode, reg, systemPrompt, 10).
			WithRegistryBuilder(func(spec *executionpolicy.ExecutionSpec) (tool.Registry, error) {
				bounds, err := toolguard.NewBoundaries(spec)
				if err != nil {
					return nil, err
				}
				var r tool.Registry = tool.NewMemRegistry()
				r.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(tool.NewReadFileTool("code"), bounds.Sandbox), bounds.FilePolicy))
				r.Register(toolguard.WrapFilePolicy(tool.WrapSandbox(tool.NewWriteFileTool("code"), bounds.Sandbox), bounds.FilePolicy))
				r.Register(toolguard.WrapFilePolicy(tool.WrapSandbox(tool.NewEditFileTool("code"), bounds.Sandbox), bounds.FilePolicy))
				r.Register(toolguard.WrapDeletePolicy(tool.WrapSandbox(tool.NewDeleteFileTool("code"), bounds.Sandbox), bounds.FilePolicy))
				r.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(tool.NewListFilesTool("code"), bounds.Sandbox), bounds.FilePolicy))
				r.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(tool.NewSearchTool("code"), bounds.Sandbox), bounds.FilePolicy))
				sh := tool.NewShellExecTool("code", bounds.Sandbox)
				if bounds.CommandSandbox != nil {
					sh.SetCommandSandbox(bounds.CommandSandbox)
				}
				r.Register(toolguard.WrapCommandPolicy(tool.WrapSandbox(sh, bounds.Sandbox), bounds.CommandSandbox, bounds.SandboxConfig, bounds.FilePolicy))
				r.Register(tool.NewNoteTool("code"))
				return r, nil
			})
	case "verifier":
		reg := RegisterWithPolicy(agent.KindVerifier,
			tool.NewVerifierReadFileTool(),
			tool.NewRunTestsTool(),
			tool.NewCheckOutputTool(),
			tool.NewBrowserActionTool(),
			tool.NewNoteTool("verifier"),
		)
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
		tb = NewThinBrain(agent.KindVerifier, reg, systemPrompt, 8).
			WithRegistryBuilder(func(spec *executionpolicy.ExecutionSpec) (tool.Registry, error) {
				bounds, err := toolguard.NewBoundaries(spec)
				if err != nil {
					return nil, err
				}
				var r tool.Registry = tool.NewMemRegistry()
				r.Register(toolguard.WrapReadPolicy(tool.WrapSandbox(tool.NewVerifierReadFileTool(), bounds.Sandbox), bounds.FilePolicy))
				rt := tool.NewRunTestsTool()
				rt.SetSandbox(bounds.Sandbox)
				if bounds.CommandSandbox != nil {
					rt.SetCommandSandbox(bounds.CommandSandbox)
				}
				r.Register(toolguard.WrapCommandPolicy(tool.WrapSandbox(rt, bounds.Sandbox), bounds.CommandSandbox, bounds.SandboxConfig, bounds.FilePolicy))
				r.Register(tool.NewCheckOutputTool())
				r.Register(tool.NewBrowserActionTool())
				r.Register(tool.NewNoteTool("verifier"))
				return r, nil
			})
	case "fault":
		reg := RegisterWithPolicy(agent.KindFault,
			tool.NewInjectErrorTool(),
			tool.NewInjectLatencyTool(),
			tool.NewKillProcessTool(),
			tool.NewCorruptResponseTool(),
			tool.NewNoteTool("fault"),
		)
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
		tb = NewThinBrain(agent.KindFault, reg, systemPrompt, 8).
			WithRegistryBuilder(func(spec *executionpolicy.ExecutionSpec) (tool.Registry, error) {
				bounds, err := toolguard.NewBoundaries(spec)
				if err != nil {
					return nil, err
				}
				var r tool.Registry = tool.NewMemRegistry()
				r.Register(tool.WrapSandbox(tool.NewInjectErrorTool(), bounds.Sandbox))
				r.Register(tool.WrapSandbox(tool.NewInjectLatencyTool(), bounds.Sandbox))
				r.Register(tool.WrapSandbox(tool.NewKillProcessTool(), bounds.Sandbox))
				r.Register(tool.WrapSandbox(tool.NewCorruptResponseTool(), bounds.Sandbox))
				r.Register(tool.NewNoteTool("fault"))
				return r, nil
			})
	case "browser":
		return fmt.Errorf("browser brain requires RunBrowserBrain() — use shared.RunThinBrainMain with browser handler")
	default:
		return fmt.Errorf("unknown brain: %s", name)
	}

	if learner != nil {
		tb.WithLearner(learner)
	}

	return RunThinBrainMain(ThinBrainMainConfig{
		Name:    "brain-" + name,
		Handler: tb,
	})
}


// MustRun is a convenience wrapper around sidecar.Run that panics on error.
// It is the standard entry point for thin-brain binaries:
//
//	func main() { shared.MustRun(tb) }
func MustRun(handler sidecar.BrainHandler) {
	if err := sidecar.Run(handler); err != nil {
		panic(fmt.Sprintf("sidecar run failed: %v", err))
	}
}
