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

// BrainRegistration describes a specialist brain that the Orchestrator can
// delegate to. When provided via OrchestratorConfig.Brains, the Orchestrator
// becomes fully configuration-driven — no brain kinds are hard-coded.
type BrainRegistration struct {
	// Kind is the brain role identifier (e.g. "code", "quant", "data").
	Kind agent.Kind `json:"kind"`

	// Binary is an explicit path to the sidecar binary. When non-empty it
	// takes precedence over the BinResolver. This allows third-party brains
	// to be registered purely through configuration.
	Binary string `json:"binary,omitempty"`

	// Model is the LLM model ID to use for this brain via LLMProxy.
	// An empty string means the brain does not use LLM proxying.
	Model string `json:"model,omitempty"`

	// AutoStart launches the sidecar immediately on Orchestrator creation
	// rather than lazily on first delegation.
	AutoStart bool `json:"auto_start,omitempty"`
}

// OrchestratorConfig configures the Orchestrator. When Brains is non-empty,
// only those brains are probed — the built-in kind list is not used. When
// Brains is empty, the Orchestrator falls back to probing agent.BuiltinKinds()
// via the BinResolver for backward compatibility.
type OrchestratorConfig struct {
	Brains []BrainRegistration `json:"brains,omitempty"`
}

// Orchestrator manages specialist brain lifecycle and subtask delegation.
type Orchestrator struct {
	runner      BrainRunner
	llmProxy    *LLMProxy
	binResolver func(kind agent.Kind) (string, error)
	toolCalls   SpecialistToolCallAuthorizer

	// available records which sidecar binaries exist on disk.
	available map[agent.Kind]bool

	// registrations stores BrainRegistration for config-driven brains.
	registrations map[agent.Kind]*BrainRegistration

	mu     sync.Mutex
	active map[agent.Kind]agent.Agent // running sidecar pool (reused)
}

// NewOrchestrator creates an Orchestrator. It probes the filesystem for
// available sidecar binaries and records which kinds can be delegated.
//
// For backward compatibility, when no OrchestratorConfig is provided the
// Orchestrator probes agent.BuiltinKinds() via binResolver.
func NewOrchestrator(runner BrainRunner, llmProxy *LLMProxy, binResolver func(agent.Kind) (string, error)) *Orchestrator {
	return NewOrchestratorWithConfig(runner, llmProxy, binResolver, OrchestratorConfig{})
}

// NewOrchestratorWithConfig creates a configuration-driven Orchestrator.
// When cfg.Brains is non-empty, only those brains are registered — the
// built-in kind list is ignored. This is the recommended constructor for
// hot-pluggable brain management.
func NewOrchestratorWithConfig(runner BrainRunner, llmProxy *LLMProxy, binResolver func(agent.Kind) (string, error), cfg OrchestratorConfig) *Orchestrator {
	o := &Orchestrator{
		runner:        runner,
		llmProxy:      llmProxy,
		binResolver:   binResolver,
		toolCalls:     DefaultSpecialistToolCallAuthorizer(),
		available:     make(map[agent.Kind]bool),
		registrations: make(map[agent.Kind]*BrainRegistration),
		active:        make(map[agent.Kind]agent.Agent),
	}

	if len(cfg.Brains) > 0 {
		// Configuration-driven: only probe configured brains.
		for i := range cfg.Brains {
			reg := &cfg.Brains[i]
			o.registrations[reg.Kind] = reg
			o.probeRegistration(reg, binResolver)
		}
	} else {
		// Backward-compatible: probe all built-in kinds via binResolver.
		for _, kind := range agent.BuiltinKinds() {
			o.probeBinResolver(kind, binResolver)
		}
	}

	// Sync Model fields from registrations into LLMProxy.ModelForKind
	// so that the LLM proxy knows which model each brain should use.
	o.syncLLMModels()

	return o
}

// probeRegistration checks if a configured brain's binary exists on disk.
func (o *Orchestrator) probeRegistration(reg *BrainRegistration, binResolver func(agent.Kind) (string, error)) {
	// 1. Explicit binary path from config takes precedence.
	if reg.Binary != "" {
		if _, err := os.Stat(reg.Binary); err == nil {
			o.available[reg.Kind] = true
			return
		}
	}
	// 2. Fall back to binResolver.
	o.probeBinResolver(reg.Kind, binResolver)
}

// probeBinResolver probes a single kind through the bin resolver.
func (o *Orchestrator) probeBinResolver(kind agent.Kind, binResolver func(agent.Kind) (string, error)) {
	if binResolver == nil {
		return
	}
	path, err := binResolver(kind)
	if err != nil {
		return
	}
	if _, statErr := os.Stat(path); statErr == nil {
		o.available[kind] = true
	}
}

// syncLLMModels propagates Model fields from all registrations into
// LLMProxy.ModelForKind. Called after initial setup and after Register.
func (o *Orchestrator) syncLLMModels() {
	if o.llmProxy == nil {
		return
	}
	if o.llmProxy.ModelForKind == nil {
		o.llmProxy.ModelForKind = make(map[agent.Kind]string)
	}
	for kind, reg := range o.registrations {
		if reg.Model != "" {
			o.llmProxy.ModelForKind[kind] = reg.Model
		}
	}
}

// StartBrain explicitly starts a sidecar for the given kind. Returns an
// error if the kind is not available or the sidecar fails to start.
func (o *Orchestrator) StartBrain(ctx context.Context, kind agent.Kind) error {
	if !o.available[kind] {
		return fmt.Errorf("brain %q not available (no sidecar binary found)", kind)
	}
	_, err := o.getOrStartSidecar(ctx, kind)
	return err
}

// StopBrain stops a running sidecar for the given kind. No-op if not running.
func (o *Orchestrator) StopBrain(ctx context.Context, kind agent.Kind) error {
	o.mu.Lock()
	ag, ok := o.active[kind]
	if ok {
		delete(o.active, kind)
	}
	o.mu.Unlock()
	if !ok {
		return nil
	}
	ag.Shutdown(ctx)
	return o.runner.Stop(ctx, kind)
}

// BrainStatus describes the state of a specialist brain.
type BrainStatus struct {
	Kind    agent.Kind `json:"kind"`
	Running bool       `json:"running"`
	Binary  string     `json:"binary,omitempty"`
}

// ListBrains returns the status of all available specialist brains.
func (o *Orchestrator) ListBrains() []BrainStatus {
	o.mu.Lock()
	defer o.mu.Unlock()
	var list []BrainStatus
	for kind := range o.available {
		status := BrainStatus{
			Kind:    kind,
			Running: o.isAlive(o.active[kind]),
		}
		if o.binResolver != nil {
			if path, err := o.binResolver(kind); err == nil {
				status.Binary = path
			}
		}
		list = append(list, status)
	}
	return list
}

// Register dynamically adds a brain registration at runtime. If a sidecar
// binary is found (via reg.Binary or binResolver), the kind becomes
// immediately available for delegation. This enables hot-plugging new
// brains without restarting the Kernel.
func (o *Orchestrator) Register(reg BrainRegistration) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.registrations[reg.Kind] = &reg
	o.probeRegistration(&reg, o.binResolver)
	o.syncLLMModels()
	return o.available[reg.Kind]
}

// Unregister removes a brain kind from the Orchestrator. If a sidecar of
// that kind is currently running, it is NOT stopped — call Shutdown or
// removeSidecar first if you want to stop it.
func (o *Orchestrator) Unregister(kind agent.Kind) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.registrations, kind)
	delete(o.available, kind)
}

// Registration returns the BrainRegistration for a kind, or nil if not
// registered via config.
func (o *Orchestrator) Registration(kind agent.Kind) *BrainRegistration {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.registrations[kind]
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

	// Start a new sidecar. If a registration exists with an explicit binary
	// path, pass it to the runner so it does not need a separate binResolver
	// lookup.
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
//
// When brains are configured via OrchestratorConfig, only configured kinds are
// checked. Otherwise the built-in kind list is used.
func (o *Orchestrator) DegradationNotice() string {
	o.mu.Lock()
	var checkKinds []agent.Kind
	if len(o.registrations) > 0 {
		for k := range o.registrations {
			checkKinds = append(checkKinds, k)
		}
	} else {
		checkKinds = agent.BuiltinKinds()
	}
	o.mu.Unlock()

	var missing []string
	for _, k := range checkKinds {
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
