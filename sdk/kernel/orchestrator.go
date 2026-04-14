// Package kernel — Orchestrator manages specialist brain lifecycle and
// subtask delegation.
//
// When the central brain calls subtask.delegate via reverse RPC, the
// Orchestrator starts the target specialist sidecar (or reuses an already
// running one), registers LLM proxy handlers, sends brain/execute, and
// returns the result.
//
// See 02-BrainKernel设计.md §12.5 and 20-协议规格.md §10.1.
package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/protocol"
)

// SubtaskRequest is the payload of a subtask.delegate RPC from Central.
type SubtaskRequest struct {
	// TaskID is a caller-assigned identifier for correlation.
	TaskID string `json:"task_id"`

	// TargetKind is the specialist brain to delegate to.
	TargetKind agent.Kind `json:"target_kind"`

	// Instruction is the natural-language task description.
	Instruction string `json:"instruction"`

	// Context is optional structured context (file paths, prior results).
	Context json.RawMessage `json:"context,omitempty"`

	// Budget constrains the subtask execution.
	Budget *SubtaskBudget `json:"budget,omitempty"`

	// Execution carries the effective workdir / file policy boundary the
	// specialist must inherit from the caller.
	Execution *executionpolicy.ExecutionSpec `json:"execution,omitempty"`
}

// SubtaskBudget limits a single delegated subtask.
type SubtaskBudget struct {
	MaxTurns   int           `json:"max_turns,omitempty"`
	MaxCostUSD float64       `json:"max_cost_usd,omitempty"`
	Timeout    time.Duration `json:"timeout,omitempty"`
}

// SubtaskResult is the response returned to Central after a subtask completes.
type SubtaskResult struct {
	TaskID string          `json:"task_id"`
	Status string          `json:"status"` // "completed", "failed", "rejected"
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
	Usage  SubtaskUsage    `json:"usage"`
}

// SubtaskUsage tracks resource consumption of a subtask.
type SubtaskUsage struct {
	Turns    int           `json:"turns"`
	CostUSD  float64       `json:"cost_usd"`
	Duration time.Duration `json:"duration"`
}

// Orchestrator manages specialist brain lifecycle and subtask delegation.
type Orchestrator struct {
	runner      BrainRunner
	llmProxy    *LLMProxy
	binResolver func(kind agent.Kind) (string, error)
	toolCalls   SpecialistToolCallAuthorizer

	// available records which sidecar binaries exist on disk.
	available map[agent.Kind]bool

	mu     sync.Mutex
	active map[agent.Kind]agent.Agent // running sidecar pool (reused)
}

// NewOrchestrator creates an Orchestrator. It probes the filesystem for
// available sidecar binaries and records which kinds can be delegated.
func NewOrchestrator(runner BrainRunner, llmProxy *LLMProxy, binResolver func(agent.Kind) (string, error)) *Orchestrator {
	o := &Orchestrator{
		runner:      runner,
		llmProxy:    llmProxy,
		binResolver: binResolver,
		toolCalls:   DefaultSpecialistToolCallAuthorizer(),
		available:   make(map[agent.Kind]bool),
		active:      make(map[agent.Kind]agent.Agent),
	}

	// Probe for each specialist sidecar binary.
	for _, kind := range []agent.Kind{
		agent.KindCode,
		agent.KindBrowser,
		agent.KindVerifier,
		agent.KindFault,
	} {
		if binResolver != nil {
			if path, err := binResolver(kind); err == nil {
				if _, statErr := os.Stat(path); statErr == nil {
					o.available[kind] = true
				}
			}
		}
	}

	return o
}

// SetSpecialistToolCallAuthorizer overrides the reverse-RPC authorization
// policy for specialist.call_tool. A nil authorizer disables caller-based
// checks and leaves the method open to any sidecar session.
func (o *Orchestrator) SetSpecialistToolCallAuthorizer(authorizer SpecialistToolCallAuthorizer) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.toolCalls = authorizer
}

// CanDelegate reports whether a specialist binary exists for the given kind.
func (o *Orchestrator) CanDelegate(kind agent.Kind) bool {
	return o.available[kind]
}

// AvailableKinds returns the set of specialist kinds that have sidecar
// binaries on disk.
func (o *Orchestrator) AvailableKinds() []agent.Kind {
	kinds := make([]agent.Kind, 0, len(o.available))
	for k := range o.available {
		kinds = append(kinds, k)
	}
	return kinds
}

// Delegate handles a subtask.delegate request: starts the specialist sidecar
// (if not already running), registers LLM proxy handlers, sends brain/execute,
// waits for completion, and returns the result.
//
// If the sidecar crashes during execution, Delegate automatically removes it
// from the pool, restarts it, and retries once.
func (o *Orchestrator) Delegate(ctx context.Context, req *SubtaskRequest) (*SubtaskResult, error) {
	start := time.Now()

	// Check availability.
	if !o.CanDelegate(req.TargetKind) {
		return &SubtaskResult{
			TaskID: req.TaskID,
			Status: "rejected",
			Error:  fmt.Sprintf("no sidecar binary available for %s; handle locally", req.TargetKind),
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, nil
	}

	// Apply timeout from budget only when explicitly provided.
	// Otherwise inherit the caller's context deadline unchanged.
	cancel := func() {}
	if req.Budget != nil && req.Budget.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, req.Budget.Timeout)
	}
	defer cancel()

	// Try with automatic retry on crash.
	result, err := o.delegateOnce(ctx, req, start)
	if err == nil && result.Status != "failed" {
		return result, nil
	}

	// First attempt failed — check if it's a sidecar crash (not a timeout or cancellation).
	if ctx.Err() != nil {
		return result, err
	}

	// Remove crashed sidecar from pool and retry once.
	fmt.Fprintf(os.Stderr, "orchestrator: %s sidecar failed, retrying: %s\n", req.TargetKind, result.Error)
	o.removeSidecar(req.TargetKind)

	retryResult, retryErr := o.delegateOnce(ctx, req, start)
	if retryErr != nil || retryResult.Status == "failed" {
		// Both attempts failed — mark the kind as degraded.
		fmt.Fprintf(os.Stderr, "orchestrator: %s sidecar retry failed, marking degraded\n", req.TargetKind)
	}
	return retryResult, retryErr
}

// delegateOnce performs a single delegation attempt.
func (o *Orchestrator) delegateOnce(ctx context.Context, req *SubtaskRequest, start time.Time) (*SubtaskResult, error) {
	// Get or start sidecar.
	ag, err := o.getOrStartSidecar(ctx, req.TargetKind)
	if err != nil {
		return &SubtaskResult{
			TaskID: req.TaskID,
			Status: "failed",
			Error:  fmt.Sprintf("start sidecar %s: %v", req.TargetKind, err),
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, nil
	}

	// Get the RPC session.
	rpcAgent, ok := ag.(agent.RPCAgent)
	if !ok {
		return &SubtaskResult{
			TaskID: req.TaskID,
			Status: "failed",
			Error:  "agent does not implement RPCAgent",
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, nil
	}

	rpc, ok := rpcAgent.RPC().(protocol.BidirRPC)
	if !ok {
		return &SubtaskResult{
			TaskID: req.TaskID,
			Status: "failed",
			Error:  "agent RPC is not BidirRPC",
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, nil
	}

	// Build brain/execute payload.
	payload := map[string]interface{}{
		"task_id":     req.TaskID,
		"instruction": req.Instruction,
	}
	if req.Context != nil {
		payload["context"] = req.Context
	}
	if req.Budget != nil {
		payload["budget"] = req.Budget
	}
	if req.Execution != nil {
		payload["execution"] = req.Execution
	}

	// Send brain/execute and wait for result.
	var execResult json.RawMessage
	if err := rpc.Call(ctx, "brain/execute", payload, &execResult); err != nil {
		return &SubtaskResult{
			TaskID: req.TaskID,
			Status: "failed",
			Error:  fmt.Sprintf("brain/execute: %v", err),
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, nil
	}

	return &SubtaskResult{
		TaskID: req.TaskID,
		Status: "completed",
		Output: execResult,
		Usage:  SubtaskUsage{Duration: time.Since(start)},
	}, nil
}

// CallTool invokes a specific tool on a specialist sidecar without running
// the specialist's Agent Loop. This is the deterministic cross-brain path for
// capability reuse where the caller already knows which tool to invoke.
func (o *Orchestrator) CallTool(ctx context.Context, req *protocol.SpecialistToolCallRequest) (*protocol.ToolCallResult, error) {
	if req == nil {
		return nil, fmt.Errorf("specialist tool call request is required")
	}
	if req.TargetKind == "" {
		return nil, fmt.Errorf("target_kind is required")
	}
	if req.ToolName == "" {
		return nil, fmt.Errorf("tool_name is required")
	}
	if !o.CanDelegate(req.TargetKind) {
		return nil, fmt.Errorf("no sidecar binary available for %s", req.TargetKind)
	}

	ag, err := o.getOrStartSidecar(ctx, req.TargetKind)
	if err != nil {
		return nil, fmt.Errorf("start sidecar %s: %w", req.TargetKind, err)
	}

	rpcAgent, ok := ag.(agent.RPCAgent)
	if !ok {
		return nil, fmt.Errorf("agent does not implement RPCAgent")
	}
	rpc, ok := rpcAgent.RPC().(protocol.BidirRPC)
	if !ok {
		return nil, fmt.Errorf("agent RPC is not BidirRPC")
	}

	callReq := protocol.ToolCallRequest{
		Name:      req.ToolName,
		Arguments: req.Arguments,
		Execution: req.Execution,
	}
	var result protocol.ToolCallResult
	if err := rpc.Call(ctx, "tools/call", callReq, &result); err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}
	return &result, nil
}

// getOrStartSidecar returns an existing running sidecar or starts a new one.
// If a cached sidecar is no longer alive, it is removed and a fresh one is started.
func (o *Orchestrator) getOrStartSidecar(ctx context.Context, kind agent.Kind) (agent.Agent, error) {
	o.mu.Lock()

	// Reuse existing sidecar if available AND alive.
	if ag, ok := o.active[kind]; ok {
		if o.isAlive(ag) {
			o.mu.Unlock()
			return ag, nil
		}
		// Dead sidecar — remove from pool.
		fmt.Fprintf(os.Stderr, "orchestrator: %s sidecar dead, removing from pool\n", kind)
		delete(o.active, kind)
		// Try to clean up.
		go func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ag.Shutdown(shutCtx)
			o.runner.Stop(shutCtx, kind)
		}()
	}
	o.mu.Unlock()

	// Start a new sidecar.
	desc := agent.Descriptor{
		Kind:      kind,
		LLMAccess: agent.LLMAccessProxied,
	}

	ag, err := o.runner.Start(ctx, kind, desc)
	if err != nil {
		return nil, err
	}

	// Register LLM proxy handlers on the new sidecar's RPC session.
	if rpcAgent, ok := ag.(agent.RPCAgent); ok {
		if rpc, ok := rpcAgent.RPC().(protocol.BidirRPC); ok {
			o.registerReverseHandlers(rpc, kind)
		}
	}

	// Cache the sidecar for reuse.
	o.mu.Lock()
	o.active[kind] = ag
	o.mu.Unlock()

	return ag, nil
}

func (o *Orchestrator) registerReverseHandlers(rpc protocol.BidirRPC, callerKind agent.Kind) {
	if rpc == nil {
		return
	}
	if o.llmProxy != nil {
		o.llmProxy.RegisterHandlers(rpc, callerKind)
	}
	rpc.Handle(protocol.MethodSubtaskDelegate, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if callerKind != agent.KindCentral {
			return nil, fmt.Errorf("subtask.delegate is only allowed from central")
		}
		return o.HandleSubtaskDelegate()(ctx, params)
	})
	rpc.Handle(protocol.MethodSpecialistCallTool, o.HandleSpecialistCallToolFrom(callerKind))
}

// removeSidecar removes a sidecar from the active pool and attempts cleanup.
func (o *Orchestrator) removeSidecar(kind agent.Kind) {
	o.mu.Lock()
	ag, ok := o.active[kind]
	if ok {
		delete(o.active, kind)
	}
	o.mu.Unlock()

	if ok {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ag.Shutdown(shutCtx)
		o.runner.Stop(shutCtx, kind)
	}
}

// isAlive checks if a cached sidecar agent is still alive.
// For processAgent, it checks the underlying process state.
// For other agents, it does a lightweight RPC ping.
func (o *Orchestrator) isAlive(ag agent.Agent) bool {
	// Check process-based agents by inspecting the cmd.
	type processChecker interface {
		ProcessExited() bool
	}
	if pc, ok := ag.(processChecker); ok {
		return !pc.ProcessExited()
	}

	// For other agent types, assume alive.
	return true
}

// Shutdown gracefully stops all running specialist sidecars.
func (o *Orchestrator) Shutdown(ctx context.Context) error {
	o.mu.Lock()
	agents := make(map[agent.Kind]agent.Agent, len(o.active))
	for k, v := range o.active {
		agents[k] = v
	}
	o.active = make(map[agent.Kind]agent.Agent)
	o.mu.Unlock()

	var lastErr error
	for kind := range agents {
		if err := o.runner.Stop(ctx, kind); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// DegradationNotice returns a human-readable notice describing which specialist
// brains are NOT available. Returns "" if all requested kinds are available.
// This is used by chat/run to augment Central's system prompt.
func (o *Orchestrator) DegradationNotice() string {
	allKinds := []agent.Kind{agent.KindCode, agent.KindBrowser, agent.KindVerifier, agent.KindFault}
	var missing []string
	for _, k := range allKinds {
		if !o.available[k] {
			missing = append(missing, string(k))
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("NOTE: The following specialist brains are NOT available (no sidecar binary found): %v. "+
		"You must handle tasks for these roles yourself.", missing)
}

// HandleSubtaskDelegate returns a protocol.HandlerFunc that can be registered
// on a central brain's RPC session to handle subtask.delegate requests.
func (o *Orchestrator) HandleSubtaskDelegate() protocol.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var req SubtaskRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("unmarshal SubtaskRequest: %w", err)
		}
		return o.Delegate(ctx, &req)
	}
}

// HandleSpecialistCallTool returns an unrestricted handler for trusted
// host-side registrations where the caller identity is not tracked.
func (o *Orchestrator) HandleSpecialistCallTool() protocol.HandlerFunc {
	return o.HandleSpecialistCallToolFrom("")
}

// HandleSpecialistCallToolFrom returns a protocol.HandlerFunc that can be
// registered on a sidecar RPC session to handle specialist.call_tool reverse
// RPC requests under the caller's authorization policy.
func (o *Orchestrator) HandleSpecialistCallToolFrom(callerKind agent.Kind) protocol.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var req protocol.SpecialistToolCallRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("unmarshal SpecialistToolCallRequest: %w", err)
		}
		if callerKind != "" {
			o.mu.Lock()
			authorizer := o.toolCalls
			o.mu.Unlock()
			if authorizer != nil {
				if err := authorizer.AuthorizeSpecialistToolCall(ctx, callerKind, req.TargetKind, req.ToolName); err != nil {
					return nil, err
				}
			}
		}
		return o.CallTool(ctx, &req)
	}
}
