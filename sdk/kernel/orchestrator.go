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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/diaglog"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/flow"
	"github.com/leef-l/brain/sdk/kernel/mcpadapter"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
)

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

	// Args are optional extra argv entries passed to the sidecar process.
	Args []string `json:"args,omitempty"`

	// Env are optional KEY=VALUE pairs appended to the sidecar environment.
	Env []string `json:"env,omitempty"`

	// Model is the LLM model ID to use for this brain via LLMProxy.
	// An empty string means the brain does not use LLM proxying.
	Model string `json:"model,omitempty"`

	// AutoStart launches the sidecar immediately on Orchestrator creation
	// rather than lazily on first delegation.
	AutoStart bool `json:"auto_start,omitempty"`

	// MinApprovalLevel 是此 brain 的 manifest 最小审批等级。
	// 当非空时，SemanticApprover 会将其与工具显式声明的等级取最大值。
	MinApprovalLevel string `json:"min_approval_level,omitempty"`
}

// OrchestratorConfig configures the Orchestrator. When Brains is non-empty,
// only those brains are probed — the built-in kind list is not used. When
// Brains is empty, the Orchestrator falls back to probing agent.BuiltinKinds()
// via the BinResolver for backward compatibility.
type OrchestratorConfig struct {
	Brains     []BrainRegistration          `json:"brains,omitempty"`
	MCPServers []mcpadapter.MCPServerConfig `json:"mcp_servers,omitempty"`
}

// Orchestrator manages specialist brain lifecycle and subtask delegation.
type Orchestrator struct {
	runner      BrainRunner
	llmProxy    *LLMProxy
	binResolver func(kind agent.Kind) (string, error)
	toolCalls   SpecialistToolCallAuthorizer

	// contextEngine 是可选的上下文装配引擎。当非 nil 时，Delegate 在
	// 发送消息给下游 brain 之前会调用 Assemble() 装配和压缩上下文。
	contextEngine ContextEngine

	// capMatcher 是可选的能力匹配器。当非 nil 且 TargetKind 为空时，
	// Delegate 会通过三阶段匹配算法自动选择最佳 brain。
	capMatcher *CapabilityMatcher

	// learner 是可选的自适应学习引擎。当非 nil 时，
	// Delegate 在任务完成后记录结果用于 L1 能力画像更新。
	learner *LearningEngine

	// approver 是可选的语义审批器。当非 nil 时，HandleSpecialistCallToolFrom
	// 优先使用它代替 toolCalls（静态白名单），实现基于操作语义的授权决策。
	approver SemanticApprover

	// pool 是可选的共享进程池。当非 nil 时，getOrStartSidecar 会
	// 委托给 pool.GetBrain，使多个 Run 共享同一个全局池。
	pool BrainPool

	// leaseManager 是可选的租约管理器。当非 nil 时，Delegate 在获取
	// sidecar 之前会调用 AcquireSet 获取目标 brain 的租约，完成后释放。
	leaseManager LeaseManager

	// mcpBrainPool 是可选的 MCP brain 池。当非 nil 时，getOrStartSidecar
	// 会 fallback 到 mcpBrainPool.GetBrain，使 MCP 服务器可以像普通
	// sidecar 一样被 CallTool 调用。
	mcpBrainPool *mcpadapter.MCPBrainPool

	// available records which sidecar binaries exist on disk.
	available map[agent.Kind]bool

	// registrations stores BrainRegistration for config-driven brains.
	registrations map[agent.Kind]*BrainRegistration

	// reverseHandlersRegistered tracks which RPC sessions have already had
	// reverse-RPC handlers installed. BrainPool may return the same live
	// sidecar process on repeated delegation, so registration must be
	// per-session and idempotent.
	reverseHandlersRegistered map[protocol.BidirRPC]struct{}

	// humanTakeoverHandler 由上层(cmd/brain)注入,收到 sidecar 反向
	// RPC 的求助请求时转发给真正的协调器。
	humanTakeoverHandler HumanTakeoverHandler

	// brainProgressHandler 由 cmd/brain 注入,把 sidecar 的 brain/progress
	// 事件(tool_start / tool_end 等)转给 chat 的 progressCh,实现流式
	// 透传子任务进度。
	brainProgressHandler BrainProgressHandler

	// EventBus 用于将 sidecar 的 brain/progress 事件实时发布到统一事件总线，
	// 供 HTTP SSE 客户端订阅。非 nil 时与 brainProgressHandler 同时生效。
	EventBus events.EventBus

	// streamPipes 用于 Workflow streaming edge 的跨进程流式数据传输。
	// 当 sidecar 通过 brain/stream/write 通知发送 chunk 时，写入此 registry。
	// ExecuteWorkflow 会将 WorkflowEngine 的 pipe registry 注入此处，使 sidecar
	// 和 host 共享同一组 pipe。
	streamPipes *flow.PipeRegistry
	streamMu    sync.Mutex

	mu sync.Mutex
}

// Learner returns the attached LearningEngine, or nil if none was configured.
func (o *Orchestrator) Learner() *LearningEngine {
	return o.learner
}

// RegisterMCPTools discovers and registers all MCP brain tools into the given
// registry. It is a no-op if no MCPBrainPool is configured.
func (o *Orchestrator) RegisterMCPTools(ctx context.Context, registry tool.Registry) error {
	if o.mcpBrainPool == nil {
		return nil
	}
	return o.mcpBrainPool.RegisterAllTools(ctx, registry)
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
		runner:                    runner,
		llmProxy:                  llmProxy,
		binResolver:               binResolver,
		toolCalls:                 DefaultSpecialistToolCallAuthorizer(),
		available:                 make(map[agent.Kind]bool),
		registrations:             make(map[agent.Kind]*BrainRegistration),
		reverseHandlersRegistered: make(map[protocol.BidirRPC]struct{}),
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

	// Initialize MCP brain pool from config.
	if len(cfg.MCPServers) > 0 {
		o.mcpBrainPool = mcpadapter.NewMCPBrainPool(cfg.MCPServers)
	}

	// Sync Model fields from registrations into LLMProxy.ModelForKind
	// so that the LLM proxy knows which model each brain should use.
	o.syncLLMModels()
	o.initSemanticApprover()

	return o
}

// OrchestratorOption 是 Orchestrator 的可选配置函数。
type OrchestratorOption func(*Orchestrator)

// WithContextEngine 设置可选的上下文装配引擎。
// 当设置后，Delegate 在发送消息给下游 brain 之前会调用 Assemble() 装配上下文。
func WithContextEngine(ce ContextEngine) OrchestratorOption {
	return func(o *Orchestrator) {
		o.contextEngine = ce
	}
}

// WithCapabilityMatcher 设置可选的能力匹配器。
// 当 DelegateRequest.TargetKind 为空时，用匹配算法自动选择最佳 brain。
func WithCapabilityMatcher(cm *CapabilityMatcher) OrchestratorOption {
	return func(o *Orchestrator) {
		o.capMatcher = cm
	}
}

// WithLearningEngine 设置可选的自适应学习引擎。
// Delegate 完成后会记录结果，用于 L1 能力画像的 EWMA 更新。
func WithLearningEngine(le *LearningEngine) OrchestratorOption {
	return func(o *Orchestrator) {
		o.learner = le
	}
}

// WithSemanticApprover 设置语义审批器，替代 SpecialistToolCallAuthorizer 的静态白名单。
// 当设置后，HandleSpecialistCallToolFrom 优先使用语义审批而非静态规则。
func WithSemanticApprover(sa SemanticApprover) OrchestratorOption {
	return func(o *Orchestrator) {
		o.approver = sa
	}
}

// WithLeaseManager 设置可选的租约管理器。
// 当设置后，Delegate 在获取 sidecar 之前会尝试获取目标 brain 的租约，
// 在 delegate 完成后（无论成功/失败）通过 defer 释放租约。
func WithLeaseManager(lm LeaseManager) OrchestratorOption {
	return func(o *Orchestrator) {
		o.leaseManager = lm
	}
}

// WithMCPBrainPool 设置可选的 MCP brain 池。
// 当设置后，getOrStartSidecar 会在 ProcessBrainPool 失败后 fallback 到
// MCPBrainPool，使 MCP 服务器可以像普通 sidecar 一样被调度。
func WithMCPBrainPool(pool *mcpadapter.MCPBrainPool) OrchestratorOption {
	return func(o *Orchestrator) {
		o.mcpBrainPool = pool
	}
}

// NewOrchestratorWithPool 创建一个使用外部 BrainPool 的 Orchestrator。
// 当 pool 非 nil 时，getOrStartSidecar 会委托给 pool.GetBrain，
// 使多个 Orchestrator / Run 共享同一个全局进程池。
// 这是 v3 架构过渡期的推荐构造函数。
func NewOrchestratorWithPool(pool BrainPool, runner BrainRunner, llmProxy *LLMProxy, binResolver func(agent.Kind) (string, error), cfg OrchestratorConfig, opts ...OrchestratorOption) *Orchestrator {
	o := NewOrchestratorWithConfig(runner, llmProxy, binResolver, cfg)
	o.pool = pool
	o.syncFromPoolCatalog()
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// initSemanticApprover 若 approver 是 *DefaultSemanticApprover 且未设置
// ManifestMinLevel，则从 registrations 中自动构建查询回调。
func (o *Orchestrator) initSemanticApprover() {
	if o.approver == nil {
		return
	}
	dsa, ok := o.approver.(*DefaultSemanticApprover)
	if !ok {
		return
	}
	if dsa.ManifestMinLevel != nil {
		return // 已被外部显式设置
	}
	dsa.ManifestMinLevel = func(targetKind agent.Kind) ApprovalClass {
		o.mu.Lock()
		reg := o.registrations[targetKind]
		o.mu.Unlock()
		if reg != nil && reg.MinApprovalLevel != "" {
			return ApprovalClass(reg.MinApprovalLevel)
		}
		return ""
	}
}

func (o *Orchestrator) syncFromPoolCatalog() {
	if o.pool == nil {
		return
	}
	if regs, ok := o.pool.(brainPoolRegistrationCatalog); ok && len(o.registrations) == 0 {
		for _, reg := range regs.Registrations() {
			regCopy := reg
			o.registrations[reg.Kind] = &regCopy
		}
	}
	if catalog, ok := o.pool.(brainPoolCatalog); ok {
		for _, kind := range catalog.AvailableKinds() {
			o.available[kind] = true
		}
	}
	o.syncLLMModels()
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

// AutoStartBrains launches all sidecars that have AutoStart=true in their
// registration. This should be called after Orchestrator creation with a
// long-lived context (e.g. the serve context). Errors are logged to stderr
// but do not prevent other brains from starting.
func (o *Orchestrator) AutoStartBrains(ctx context.Context) {
	if o.pool == nil && o.mcpBrainPool == nil {
		fmt.Fprintf(os.Stderr, "orchestrator: AutoStartBrains skipped (no pool)\n")
		return
	}
	if o.pool != nil {
		o.pool.AutoStart(ctx)
	}
	if o.mcpBrainPool != nil {
		o.mcpBrainPool.AutoStart(ctx)
	}
}

// StartBrain explicitly starts a sidecar for the given kind. Returns an
// error if the kind is not available or the sidecar fails to start.
func (o *Orchestrator) StartBrain(ctx context.Context, kind agent.Kind) error {
	if o.pool == nil {
		return fmt.Errorf("brain %q: no pool available", kind)
	}
	if !o.available[kind] {
		return fmt.Errorf("brain %q not available (no sidecar binary found)", kind)
	}
	_, err := o.pool.GetBrain(ctx, kind)
	return err
}

// StopBrain stops a running sidecar for the given kind. No-op if not running.
func (o *Orchestrator) StopBrain(ctx context.Context, kind agent.Kind) error {
	if o.pool == nil {
		return nil
	}
	// BrainPool 接口的 RemoveBrain 由具体实现提供（ProcessBrainPool）。
	// 这里用类型断言调用，如果 pool 不支持则忽略。
	type brainRemover interface {
		RemoveBrain(kind agent.Kind)
	}
	if rm, ok := o.pool.(brainRemover); ok {
		rm.RemoveBrain(kind)
	}
	return nil
}

// BrainStatus describes the state of a specialist brain.
type BrainStatus struct {
	Kind        agent.Kind `json:"kind"`
	Running     bool       `json:"running"`
	Binary      string     `json:"binary,omitempty"`
	AutoStart   bool       `json:"auto_start,omitempty"`
	Description string     `json:"description,omitempty"`
	Version     string     `json:"version,omitempty"`
	Model       string     `json:"model,omitempty"`
	MinApprovalLevel string `json:"min_approval_level,omitempty"`
	Instances   int        `json:"instances,omitempty"` // 存活实例数（v3 多实例负载均衡）
}

// ListBrains returns the status of all available specialist brains.
func (o *Orchestrator) ListBrains() []BrainStatus {
	var out []BrainStatus
	if o.pool != nil {
		for _, bs := range o.pool.Status() {
			out = append(out, bs)
		}
	}
	if o.mcpBrainPool != nil {
		for _, bs := range o.mcpBrainPool.Status() {
			out = append(out, BrainStatus{
				Kind:    bs.Kind,
				Running: bs.Running,
				Binary:  bs.Binary,
			})
		}
	}
	if len(out) > 0 {
		return out
	}
	// 无 pool 时返回静态可用性列表（不含运行状态）。
	var list []BrainStatus
	for kind := range o.available {
		status := BrainStatus{Kind: kind}
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

// CanDelegate reports whether a specialist binary or MCP server exists for the given kind.
func (o *Orchestrator) CanDelegate(kind agent.Kind) bool {
	if o.available[kind] {
		return true
	}
	if o.mcpBrainPool != nil {
		for _, k := range o.mcpBrainPool.AvailableKinds() {
			if k == kind {
				return true
			}
		}
	}
	return false
}

// AvailableKinds returns the set of specialist kinds that have sidecar
// binaries on disk or MCP servers configured.
// Filters out meta-brains (central, easymvp) that should not be directly
// delegated to by the task execution orchestrator.
func (o *Orchestrator) AvailableKinds() []agent.Kind {
	kinds := make([]agent.Kind, 0, len(o.available))
	for k := range o.available {
		if k == agent.KindCentral || k == agent.KindEasyMVP {
			continue
		}
		kinds = append(kinds, k)
	}
	if o.mcpBrainPool != nil {
		for _, k := range o.mcpBrainPool.AvailableKinds() {
			if o.available[k] || k == agent.KindCentral || k == agent.KindEasyMVP {
				continue
			}
			kinds = append(kinds, k)
		}
	}
	return kinds
}

// DelegateBatch 并行派发多个无依赖的子任务到不同的 specialist brain。
// 这是实现"多方审核同时执行"的核心 API —— Central 可以同时向 code、verifier、
// browser 下发独立子任务，并在所有 brain 完成后统一收集结果。
//
// 注意：如果请求之间存在依赖关系（如 verifier 必须等待 code 完成后才能审核），
// 应使用 ExecuteWorkflow（DAG 分层执行）而非 DelegateBatch。
func (o *Orchestrator) DelegateBatch(ctx context.Context, batch *DelegateBatchRequest) (*DelegateBatchResult, error) {
	if batch == nil || len(batch.Requests) == 0 {
		return &DelegateBatchResult{}, nil
	}

	start := time.Now()
	diaglog.Info("delegate_batch", "batch start",
		"count", len(batch.Requests),
	)

	// 为所有请求生成统一的 TraceID（如果调用方未提供）。
	traceID := ""
	for _, req := range batch.Requests {
		if req.TraceID != "" {
			traceID = req.TraceID
			break
		}
	}
	if traceID == "" {
		traceID = genTraceID()
	}

	// 为每个请求注入统一的 trace 上下文和独立的 span ID。
	for i, req := range batch.Requests {
		if req.TraceID == "" {
			req.TraceID = traceID
		}
		if req.SpanID == "" {
			req.SpanID = fmt.Sprintf("span-%s-%d", traceID, i)
		}
	}

	results := make([]*DelegateResult, len(batch.Requests))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, req := range batch.Requests {
		wg.Add(1)
		go func(idx int, r *DelegateRequest) {
			defer wg.Done()
			res, err := o.Delegate(ctx, r)
			mu.Lock()
			results[idx] = res
			mu.Unlock()
			if err != nil {
				diaglog.Error("delegate_batch", "item failed",
					"idx", idx,
					"task_id", r.TaskID,
					"target_kind", r.TargetKind,
					"err", err,
				)
			}
		}(i, req)
	}

	wg.Wait()

	completed := 0
	failed := 0
	for _, res := range results {
		if res != nil && res.Status == "completed" {
			completed++
		} else {
			failed++
		}
	}

	diaglog.Info("delegate_batch", "batch finished",
		"count", len(batch.Requests),
		"completed", completed,
		"failed", failed,
		"duration", time.Since(start),
	)

	return &DelegateBatchResult{
		Results:        results,
		CompletedCount: completed,
		FailedCount:    failed,
	}, nil
}

// genTraceID 生成一个简短的分布式追踪 ID。
func genTraceID() string {
	b := make([]byte, 8)
	if _, err := randRead(b); err != nil {
		return fmt.Sprintf("trace-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("trace-%x", b)
}

// randRead 是 crypto/rand.Read 的抽象，便于测试注入。
var randRead = func(b []byte) (int, error) {
	// 使用 time.Now().UnixNano() 的低 8 字节做伪随机，避免 import crypto/rand
	// 在简单场景下的开销。生产环境可替换为 crypto/rand.Read。
	n := time.Now().UnixNano()
	for i := 0; i < len(b) && i < 8; i++ {
		b[i] = byte(n >> (i * 8))
	}
	return len(b), nil
}

// Delegate handles a subtask.delegate request: starts the specialist sidecar
// (if not already running), registers LLM proxy handlers, sends brain/execute,
// waits for completion, and returns the result.
//
// If the sidecar crashes during execution, Delegate automatically removes it
// from the pool, restarts it, and retries once.
func (o *Orchestrator) Delegate(ctx context.Context, req *DelegateRequest) (*DelegateResult, error) {
	start := time.Now()
	diaglog.Info("delegate", "delegate start",
		"task_id", req.TaskID,
		"target_kind", req.TargetKind,
		"instruction_len", len(req.Instruction),
	)

	// 如果 TargetKind 为空且有 capMatcher，自动选择最佳 brain。
	if req.TargetKind == "" && o.capMatcher != nil {
		resolved := o.resolveTargetKind(req)
		if resolved == "" {
			return &DelegateResult{
				TaskID: req.TaskID,
				Status: "rejected",
				Error:  "no brain matches the required capabilities",
				Usage:  SubtaskUsage{Duration: time.Since(start)},
			}, nil
		}
		req.TargetKind = resolved
	}

	// Check availability.
	if !o.CanDelegate(req.TargetKind) {
		return &DelegateResult{
			TaskID: req.TaskID,
			Status: "rejected",
			Error:  fmt.Sprintf("no sidecar binary available for %s; handle locally", req.TargetKind),
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, nil
	}

	// Acquire lease for target brain before delegation.
	var leases []Lease
	if o.leaseManager != nil {
		leaseReqs := []LeaseRequest{
			{
				Capability:  "brain-delegate",
				ResourceKey: string(req.TargetKind),
				AccessMode:  AccessExclusiveSession,
				Scope:       ScopeTask,
				HolderID:    req.TaskID,
			},
		}
		acquired, acquireErr := o.leaseManager.AcquireSet(ctx, leaseReqs)
		if acquireErr != nil {
			errMsg := fmt.Sprintf("brain %s is currently leased by another task", req.TargetKind)
			if errors.Is(acquireErr, ErrAcquireTimeout) {
				errMsg = fmt.Sprintf("brain %s is currently leased by another task (acquire timeout)", req.TargetKind)
			}
			return &DelegateResult{
				TaskID: req.TaskID,
				Status: "rejected",
				Error:  errMsg,
				Usage:  SubtaskUsage{Duration: time.Since(start)},
			}, nil
		}
		leases = acquired
		defer o.leaseManager.ReleaseAll(leases)
	}

	// ─── Adaptive Timeout ─────────────────────────────────────────────────
	// 如果调用方未指定超时，且学习引擎可用，基于历史 EWMA 延迟动态估算超时。
	budgetTimeout := time.Duration(0)
	if req.Budget != nil && req.Budget.Timeout > 0 {
		budgetTimeout = req.Budget.Timeout
	} else if o.learner != nil {
		taskType := req.TaskType
		if taskType == "" {
			taskType = "delegation"
		}
		budgetTimeout = o.learner.EstimateTimeout(req.TargetKind, taskType)
		if budgetTimeout > 0 {
			diaglog.Info("delegate", "adaptive timeout applied",
				"task_id", req.TaskID,
				"target_kind", req.TargetKind,
				"timeout", budgetTimeout,
			)
		}
	}

	attemptCtx := ctx
	attemptCancel := func() {}
	if budgetTimeout > 0 {
		attemptCtx, attemptCancel = context.WithTimeout(ctx, budgetTimeout)
	}

	// Try with automatic retry on crash.
	result, err := o.delegateOnce(attemptCtx, req, start)
	attemptCancel()
	if err == nil {
		logMsg := "delegate ok"
		if result.Status != "completed" {
			logMsg = "delegate finished"
		}
		diaglog.Info("delegate", logMsg,
			"task_id", req.TaskID,
			"target_kind", req.TargetKind,
			"status", result.Status,
			"duration", time.Since(start),
		)
		o.recordDelegateOutcome(req, result)
		o.sendBrainLearn(ctx, req, result)
		return result, nil
	}

	// First attempt failed — check if the parent context is done (not retryable).
	if ctx.Err() != nil {
		return result, err
	}

	// Remove crashed sidecar from pool and retry once with a fresh timeout.
	fmt.Fprintf(os.Stderr, "orchestrator: %s sidecar failed, retrying: %s\n", req.TargetKind, result.Error)
	diaglog.Warn("delegate", "delegate retrying",
		"task_id", req.TaskID,
		"target_kind", req.TargetKind,
		"err", result.Error,
		"retry", true,
	)
	o.poolRemoveBrain(req.TargetKind)

	retryCtx := ctx
	retryCancel := func() {}
	if budgetTimeout > 0 {
		retryCtx, retryCancel = context.WithTimeout(ctx, budgetTimeout)
	}
	defer retryCancel()

	retryResult, retryErr := o.delegateOnce(retryCtx, req, start)
	if retryErr != nil || retryResult.Status == "failed" {
		fmt.Fprintf(os.Stderr, "orchestrator: %s sidecar retry failed, marking degraded\n", req.TargetKind)
	}
	diaglog.Info("delegate", "delegate retry finished",
		"task_id", req.TaskID,
		"target_kind", req.TargetKind,
		"status", retryResult.Status,
		"retry_err", retryErr,
		"duration", time.Since(start),
	)
	o.recordDelegateOutcome(req, retryResult)
	o.sendBrainLearn(ctx, req, retryResult)
	return retryResult, retryErr
}

// delegateOnce performs a single delegation attempt.
func (o *Orchestrator) delegateOnce(ctx context.Context, req *DelegateRequest, start time.Time) (*DelegateResult, error) {
	// Get or start sidecar.
	ag, err := o.getOrStartSidecar(ctx, req.TargetKind)
	if err != nil {
		return &DelegateResult{
			TaskID: req.TaskID,
			Status: "failed",
			Error:  fmt.Sprintf("start sidecar %s: %v", req.TargetKind, err),
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, err
	}
	// 释放负载计数，让负载均衡策略知道该实例可用容量增加。
	defer func() {
		if releaser, ok := o.pool.(interface{ ReleaseAgent(agent.Agent) }); ok {
			releaser.ReleaseAgent(ag)
		}
	}()

	// Get the RPC session.
	rpcAgent, ok := ag.(agent.RPCAgent)
	if !ok {
		return &DelegateResult{
			TaskID: req.TaskID,
			Status: "failed",
			Error:  "agent does not implement RPCAgent",
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, fmt.Errorf("agent does not implement RPCAgent")
	}

	rpc, ok := rpcAgent.RPC().(protocol.BidirRPC)
	if !ok {
		return &DelegateResult{
			TaskID: req.TaskID,
			Status: "failed",
			Error:  "agent RPC is not BidirRPC",
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, fmt.Errorf("agent RPC is not BidirRPC")
	}

	// 如果配置了 ContextEngine，在发送给下游 brain 之前装配上下文。
	// 将 instruction 包装为 user 消息，通过 Assemble() 进行 token 预算控制和压缩。
	var assembledContext json.RawMessage
	if o.contextEngine != nil {
		messages := []llm.Message{{
			Role:    "user",
			Content: []llm.ContentBlock{{Type: "text", Text: req.Instruction}},
		}}
		// 如果请求中已有 context，将其作为 system 消息前置
		if req.Context != nil {
			messages = append([]llm.Message{{
				Role:    "system",
				Content: []llm.ContentBlock{{Type: "text", Text: string(req.Context)}},
			}}, messages...)
		}
		tokenBudget := 0
		if req.Budget != nil {
			// 用 MaxTurns * 4000 作为粗略 token 预算估算
			tokenBudget = req.Budget.MaxTurns * 4000
		}
		assembled, assembleErr := o.contextEngine.Assemble(ctx, AssembleRequest{
			RunID:       req.TaskID,
			BrainKind:   req.TargetKind,
			TaskType:    "delegation",
			Messages:    messages,
			TokenBudget: tokenBudget,
			ProjectID:   req.ProjectID,
		})
		if assembleErr != nil {
			fmt.Fprintf(os.Stderr, "orchestrator: context assemble warning: %v\n", assembleErr)
			// 装配失败不阻断，降级为原始上下文
		} else if len(assembled) > 0 {
			assembledContext, _ = json.Marshal(assembled)
		}
	}

	// Build brain/execute payload.
	payload := map[string]interface{}{
		"task_id":     req.TaskID,
		"instruction": req.Instruction,
	}
	if assembledContext != nil {
		payload["context"] = assembledContext
	} else if req.Context != nil {
		payload["context"] = req.Context
	}
	if req.Budget != nil {
		payload["budget"] = req.Budget
	}
	if req.Execution != nil {
		payload["execution"] = req.Execution
	}
	if req.Subtask != nil {
		payload["subtask"] = req.Subtask
	}
	if req.PipeID != "" {
		payload["pipe_id"] = req.PipeID
	}
	// ─── Distributed Tracing & Project Memory ─────────────────────────────
	if req.TraceID != "" {
		payload["trace_id"] = req.TraceID
	}
	if req.SpanID != "" {
		payload["span_id"] = req.SpanID
	}
	if req.ProjectID != "" {
		payload["project_id"] = req.ProjectID
	}
	if req.ParentSpanID != "" {
		payload["parent_span_id"] = req.ParentSpanID
	}

	// Send brain/execute and wait for result.
	var execResult json.RawMessage
	rpcErr := rpc.Call(ctx, "brain/execute", payload, &execResult)

	// Task #18: subtask 完成(成功或失败)后清掉 (central→target) 的 shared 桶,
	// 防止下一次 delegate 继承本次的跨脑消息。只对默认 engine 生效;第三方
	// engine 自己负责边界切断。
	if ce, ok := o.contextEngine.(*DefaultContextEngine); ok && ce != nil {
		ce.ClearShared(agent.Kind("central"), req.TargetKind)
	}

	if rpcErr != nil {
		diaglog.Error("delegate", "rpc call failed",
			"task_id", req.TaskID,
			"target_kind", req.TargetKind,
			"err", rpcErr,
		)
		return &DelegateResult{
			TaskID: req.TaskID,
			Status: "failed",
			Error:  fmt.Sprintf("brain/execute: %v", rpcErr),
			Usage:  SubtaskUsage{Duration: time.Since(start)},
		}, rpcErr
	}
	diaglog.Info("delegate", "rpc call ok",
		"task_id", req.TaskID,
		"target_kind", req.TargetKind,
		"output_bytes", len(execResult),
	)

	status, execErrMsg := normalizeExecuteResult(execResult)
	return &DelegateResult{
		TaskID: req.TaskID,
		Status: status,
		Output: execResult,
		Error:  execErrMsg,
		Usage:  SubtaskUsage{Duration: time.Since(start)},
	}, nil
}

func normalizeExecuteResult(execResult json.RawMessage) (status, errMsg string) {
	status = "completed"
	if len(execResult) == 0 {
		return status, ""
	}
	var result struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(execResult, &result); err != nil {
		return status, ""
	}
	switch result.Status {
	case "completed", "failed", "canceled":
		status = result.Status
	default:
		status = "completed"
	}
	return status, result.Error
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
	defer func() {
		if releaser, ok := o.pool.(interface{ ReleaseAgent(agent.Agent) }); ok {
			releaser.ReleaseAgent(ag)
		}
	}()

	// MCP agent path: forward via Adapter.Invoke.
	if mcpAgent, ok := ag.(*mcpadapter.MCPAgent); ok {
		adapter := mcpAgent.Adapter()
		mcpToolName := strings.TrimPrefix(req.ToolName, adapter.ToolPrefix)
		res, err := adapter.Invoke(ctx, mcpToolName, req.Arguments)
		if err != nil {
			return nil, fmt.Errorf("mcp invoke %s: %w", req.ToolName, err)
		}
		return &protocol.ToolCallResult{
			Output:  res.Output,
			IsError: res.IsError,
		}, nil
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

// poolClaimsKind reports whether the ProcessBrainPool explicitly claims a kind.
func (o *Orchestrator) poolClaimsKind(kind agent.Kind) bool {
	if o.pool == nil {
		return false
	}
	if catalog, ok := o.pool.(brainPoolCatalog); ok {
		for _, k := range catalog.AvailableKinds() {
			if k == kind {
				return true
			}
		}
		return false
	}
	if av, ok := o.pool.(interface{ Available(agent.Kind) bool }); ok {
		return av.Available(kind)
	}
	return true
}

// getOrStartSidecar returns an existing running sidecar or starts a new one.
// Delegates first to the ProcessBrainPool, then falls back to MCPBrainPool.
// Returns an error if no pool is set and no MCP brain matches.
func (o *Orchestrator) getOrStartSidecar(ctx context.Context, kind agent.Kind) (agent.Agent, error) {
	if o.pool == nil && o.mcpBrainPool == nil {
		return nil, fmt.Errorf("no BrainPool available; cannot start sidecar %s", kind)
	}

	if o.pool != nil && o.poolClaimsKind(kind) {
		ag, err := o.pool.GetBrain(ctx, kind)
		if err == nil {
			// Register LLM proxy and reverse RPC handlers on the sidecar's session.
			if rpcAgent, ok := ag.(agent.RPCAgent); ok {
				if rpc, ok := rpcAgent.RPC().(protocol.BidirRPC); ok {
					o.registerReverseHandlers(rpc, kind)
				}
			}
			return ag, nil
		}
		// Pool claims this kind but failed to start — don't fallback to MCP.
		return nil, err
	}

	if o.mcpBrainPool != nil {
		for _, k := range o.mcpBrainPool.AvailableKinds() {
			if k == kind {
				return o.mcpBrainPool.GetBrain(ctx, kind)
			}
		}
	}

	return nil, fmt.Errorf("brain %q not available", kind)
}

func (o *Orchestrator) registerReverseHandlers(rpc protocol.BidirRPC, callerKind agent.Kind) {
	if rpc == nil {
		return
	}
	o.mu.Lock()
	if o.reverseHandlersRegistered == nil {
		o.reverseHandlersRegistered = make(map[protocol.BidirRPC]struct{})
	}
	if _, ok := o.reverseHandlersRegistered[rpc]; ok {
		o.mu.Unlock()
		return
	}
	o.reverseHandlersRegistered[rpc] = struct{}{}
	o.mu.Unlock()

	if o.llmProxy != nil {
		o.llmProxy.RegisterHandlers(rpc, callerKind)
	}

	// Defensive: another Orchestrator instance may have already registered
	// handlers on this shared sidecar RPC session. Use HandlerExists to avoid
	// duplicate-registration panic.
	registerHandlerIfMissing := func(method string, handler protocol.HandlerFunc) {
		if !rpc.HandlerExists(method) {
			rpc.Handle(method, handler)
		}
	}

	registerHandlerIfMissing(protocol.MethodSubtaskDelegate, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if callerKind != agent.KindCentral {
			return nil, fmt.Errorf("subtask.delegate is only allowed from central")
		}
		return o.HandleSubtaskDelegate()(ctx, params)
	})
	registerHandlerIfMissing(protocol.MethodSpecialistCallTool, o.HandleSpecialistCallToolFrom(callerKind))

	// L0 学习指标上报：sidecar 调用 brain/metrics 将聚合指标喂给 LearningEngine。
	registerHandlerIfMissing(protocol.MethodBrainMetrics, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		if o.learner == nil {
			return map[string]string{"status": "ignored"}, nil
		}
		var metrics BrainMetrics
		if err := json.Unmarshal(params, &metrics); err != nil {
			return nil, fmt.Errorf("unmarshal BrainMetrics: %w", err)
		}
		// 如果 sidecar 没有填 BrainKind，用 callerKind 兜底。
		if metrics.BrainKind == "" {
			metrics.BrainKind = callerKind
		}
		o.learner.IngestBrainMetrics(metrics)
		return map[string]string{"status": "ok"}, nil
	})

	// 人工求助反向 RPC:sidecar 的 human.request_takeover 工具通过
	// HumanTakeoverBridge 把请求送到这里,我们转给上层注入的 handler
	// (cmd/brain 在 serve/chat 启动时注入,指向真正的协调器),阻塞等
	// /resume 或 /abort。未注入时返回 aborted。
	registerHandlerIfMissing(protocol.MethodHumanRequestTakeover, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		handler := o.humanTakeoverHandler
		if handler == nil {
			return map[string]string{
				"outcome": "aborted",
				"note":    "no human takeover handler registered in host",
			}, nil
		}
		return handler(ctx, string(callerKind), params)
	})

	// 专家大脑的细粒度进度事件(tool_start / tool_end / turn / content / llm_*)。
	// 转给上层注入的 handler,让 chat REPL 流式打印 subtask 进度；
	// 同时转发到 EventBus，供 HTTP SSE 客户端实时订阅。
	registerHandlerIfMissing(protocol.MethodBrainProgress, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		h := o.brainProgressHandler
		if h != nil {
			h(ctx, string(callerKind), params)
		}
		if o.EventBus != nil {
			var ev struct {
				Kind        string `json:"kind"`
				ExecutionID string `json:"execution_id,omitempty"`
				ToolName    string `json:"tool_name,omitempty"`
				Message     string `json:"message,omitempty"`
				Detail      string `json:"detail,omitempty"`
				OK          bool   `json:"ok,omitempty"`
			}
			if err := json.Unmarshal(params, &ev); err == nil && ev.ExecutionID != "" {
				// Map sidecar progress kinds to unified event types.
				evType := "agent.progress"
				switch ev.Kind {
				case "tool_start":
					evType = "agent.tool_start"
				case "tool_end":
					evType = "agent.tool_end"
				case "turn":
					evType = "agent.turn"
				case "content", "llm_delta":
					evType = "llm.content_delta"
				case "llm_start":
					evType = "llm.message_start"
				case "llm_end":
					evType = "llm.message_end"
				case "tool_call_delta":
					evType = "llm.tool_call_delta"
				}
				data, _ := json.Marshal(map[string]interface{}{
					"tool_name": ev.ToolName,
					"message":   ev.Message,
					"detail":    ev.Detail,
					"ok":        ev.OK,
				})
				o.EventBus.Publish(ctx, events.Event{
					ExecutionID: ev.ExecutionID,
					Type:        evType,
					Data:        data,
				})
			}
		}
		return map[string]string{"ok": "1"}, nil
	})

	// Workflow streaming edge 的跨进程流式数据写入。
	// sidecar 在 brain/execute 执行过程中,通过此方法将中间输出实时写入 PipeRegistry。
	registerHandlerIfMissing(protocol.MethodBrainStreamWrite, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var req struct {
			PipeID string `json:"pipe_id"`
			Chunk  string `json:"chunk"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return map[string]string{"ok": "0", "error": err.Error()}, nil
		}
		data, err := base64.StdEncoding.DecodeString(req.Chunk)
		if err != nil {
			return map[string]string{"ok": "0", "error": "decode chunk: " + err.Error()}, nil
		}
		if err := o.streamPipes.Write(ctx, req.PipeID, data); err != nil {
			return map[string]string{"ok": "0", "error": "write pipe: " + err.Error()}, nil
		}
		return map[string]string{"ok": "1"}, nil
	})
}

// BrainProgressHandler 由 cmd/brain 层注入,收到 sidecar 反向 RPC 的
// brain/progress 事件后把它转给 chat 的 progressCh。
type BrainProgressHandler func(ctx context.Context, callerKind string, params json.RawMessage)

// SetBrainProgressHandler 注入 handler。nil 清空,等价于丢弃事件。
func (o *Orchestrator) SetBrainProgressHandler(h BrainProgressHandler) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.brainProgressHandler = h
}

// HumanTakeoverHandler 由上层(cmd/brain)实现,收到 sidecar 反向 RPC 后
// 调真正的协调器并返回 {outcome, note}。避免 kernel 直接依赖 sdk/tool。
type HumanTakeoverHandler func(ctx context.Context, callerKind string, params json.RawMessage) (interface{}, error)

// RegisterStreamPipe 注册一个 streaming edge 的 pipe，供跨进程流式传输使用。
func (o *Orchestrator) RegisterStreamPipe(edgeID string, pipe *flow.PipeBackend) {
	o.streamMu.Lock()
	defer o.streamMu.Unlock()
	if o.streamPipes == nil {
		o.streamPipes = flow.NewPipeRegistry()
	}
	o.streamPipes.SetPipe(edgeID, pipe)
}

// UnregisterStreamPipe 注销 streaming edge 的 pipe。
func (o *Orchestrator) UnregisterStreamPipe(edgeID string) {
	o.streamMu.Lock()
	defer o.streamMu.Unlock()
	if o.streamPipes != nil {
		_ = o.streamPipes.Close(edgeID)
	}
}

// SetHumanTakeoverHandler 注入 handler。多次调用取最后一次。nil 清空。
func (o *Orchestrator) SetHumanTakeoverHandler(h HumanTakeoverHandler) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.humanTakeoverHandler = h
}

// poolRemoveBrain removes a sidecar from the pool (if pool supports it).
func (o *Orchestrator) poolRemoveBrain(kind agent.Kind) {
	if o.pool == nil {
		return
	}
	type brainRemover interface {
		RemoveBrain(kind agent.Kind)
	}
	if rm, ok := o.pool.(brainRemover); ok {
		rm.RemoveBrain(kind)
	}
	// 清理旧 RPC session 的反向 handler 注册记录，
	// 避免已移除的 sidecar 的 RPC 对象泄漏。
	// 重新启动的 sidecar 会在 registerReverseHandlers 中重新注册。
	o.reverseHandlersRegistered = make(map[protocol.BidirRPC]struct{})
}

// CollectMetrics 主动从所有运行中的 sidecar 拉取 brain/metrics 指标。
func (o *Orchestrator) CollectMetrics(ctx context.Context) {
	if o.learner == nil || o.pool == nil {
		return
	}
	for _, kind := range o.AvailableKinds() {
		ag, err := o.pool.GetBrain(ctx, kind)
		if err != nil {
			continue
		}
		rpcAgent, ok := ag.(agent.RPCAgent)
		if !ok {
			continue
		}
		bidir, ok := rpcAgent.RPC().(protocol.BidirRPC)
		if !ok {
			continue
		}
		var metrics BrainMetrics
		if err := bidir.Call(ctx, "brain/metrics", nil, &metrics); err != nil {
			continue
		}
		if metrics.BrainKind == "" {
			metrics.BrainKind = kind
		}
		o.learner.IngestBrainMetrics(metrics)
	}
}

// Shutdown gracefully stops all running specialist sidecars and MCP connections.
func (o *Orchestrator) Shutdown(ctx context.Context) error {
	var lastErr error
	if o.pool != nil {
		if err := o.pool.Shutdown(ctx); err != nil {
			lastErr = err
		}
	}
	if o.mcpBrainPool != nil {
		if err := o.mcpBrainPool.Shutdown(ctx); err != nil {
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
		if !o.CanDelegate(k) {
			missing = append(missing, string(k))
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("NOTE: The following specialist brains are NOT available (no sidecar binary found): %v. "+
		"You must handle tasks for these roles yourself.", missing)
}

// resolveTargetKind 通过 CapabilityMatcher（+可选的 LearningEngine）自动选择最佳 brain。
func (o *Orchestrator) resolveTargetKind(req *DelegateRequest) agent.Kind {
	matchReq := MatchRequest{
		Required:  req.RequiredCaps,
		Preferred: req.PreferredCaps,
	}
	candidates := o.capMatcher.Match(matchReq)
	if len(candidates) == 0 {
		return ""
	}

	// 只有一个候选或没有 learner，直接取 cap 匹配最高分。
	if len(candidates) == 1 || o.learner == nil {
		return candidates[0].BrainKind
	}

	// 用 learner 排名加权：capScore * 0.7 + learnScore * 0.3
	taskType := req.TaskType
	if taskType == "" {
		taskType = "delegation"
	}
	rankings := o.learner.RankBrains(taskType, WeightPolicy{})
	rankMap := make(map[agent.Kind]float64, len(rankings))
	for _, r := range rankings {
		rankMap[r.BrainKind] = r.Score
	}

	bestKind := candidates[0].BrainKind
	bestScore := 0.0
	for _, c := range candidates {
		learnScore := rankMap[c.BrainKind]
		combined := c.CombinedScore*0.7 + learnScore*0.3
		if combined > bestScore {
			bestScore = combined
			bestKind = c.BrainKind
		}
	}
	return bestKind
}

// recordDelegateOutcome 将委派结果反馈给 LearningEngine (L1 EWMA 更新)。
func (o *Orchestrator) recordDelegateOutcome(req *DelegateRequest, result *DelegateResult) {
	if o.learner == nil || result == nil {
		return
	}
	taskType := req.TaskType
	if taskType == "" {
		taskType = "delegation"
	}

	accuracy := 0.0
	stability := 0.0
	if result.Status == "completed" {
		accuracy = 1.0
		stability = 1.0
	}

	speed := 0.0
	if result.Usage.Duration > 0 && result.Usage.Duration < 30*time.Second {
		speed = 1.0 - float64(result.Usage.Duration)/float64(30*time.Second)
		if speed < 0 {
			speed = 0
		}
	}

	cost := 1.0
	if result.Usage.CostUSD > 0 {
		cost = 1.0 / (1.0 + result.Usage.CostUSD*10)
	}

	o.learner.RecordDelegateResult(req.TargetKind, taskType, accuracy, speed, cost, stability)

	// L1-Latency: 记录实际延迟用于自适应超时估算。
	o.learner.RecordDelegateLatency(req.TargetKind, taskType, result.Usage.Duration)

	// L2: 记录单步序列（单次委派作为一个 step，由调用方聚合多步序列）
	o.learner.RecordSequence(TaskSequenceRecord{
		SequenceID: req.TaskID,
		TotalScore: accuracy,
		RecordedAt: time.Now(),
		Steps: []TaskStep{{
			BrainKind: req.TargetKind,
			TaskType:  taskType,
			Duration:  result.Usage.Duration,
			Score:     accuracy,
		}},
	})
}

// sendBrainLearn 异步通知 sidecar 本次执行结果，激活 sidecar 侧的 L0 学习。
func (o *Orchestrator) sendBrainLearn(ctx context.Context, req *DelegateRequest, result *DelegateResult) {
	if result == nil {
		return
	}
	ag, err := o.getOrStartSidecar(ctx, req.TargetKind)
	if err != nil {
		return
	}
	rpcAgent, ok := ag.(agent.RPCAgent)
	if !ok {
		return
	}
	rpc, ok := rpcAgent.RPC().(protocol.BidirRPC)
	if !ok {
		return
	}
	payload := map[string]interface{}{
		"task_id":   req.TaskID,
		"task_type": req.TaskType,
		"success":   result.Status == "completed",
		"duration":  result.Usage.Duration.Seconds(),
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "orchestrator: learn goroutine panic: %v\n", r)
			}
		}()
		learnCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = rpc.Call(learnCtx, "brain/learn", payload, nil)
	}()
}

// HandleSubtaskDelegate returns a protocol.HandlerFunc that can be registered
// on a central brain's RPC session to handle subtask.delegate requests.
func (o *Orchestrator) HandleSubtaskDelegate() protocol.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var req DelegateRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("unmarshal DelegateRequest: %w", err)
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
			sa := o.approver
			authorizer := o.toolCalls
			o.mu.Unlock()

			if sa != nil {
				decision := sa.Approve(ctx, ApprovalRequest{
					CallerKind: callerKind,
					TargetKind: req.TargetKind,
					ToolName:   req.ToolName,
					Mode:       "delegate",
				})
				if !decision.Granted {
					return nil, fmt.Errorf("semantic approval denied: %s", decision.Reason)
				}
			} else if authorizer != nil {
				if err := authorizer.AuthorizeSpecialistToolCall(ctx, callerKind, req.TargetKind, req.ToolName); err != nil {
					return nil, err
				}
			}
		}
		return o.CallTool(ctx, &req)
	}
}

// ---------------------------------------------------------------------------
// WorkflowEngine integration — DAG execution via Orchestrator
// ---------------------------------------------------------------------------

// WorkflowNodeReporter 是 Workflow 执行过程中的可选事件回调。
// 调用方（serve/chat/run）可注入自己的 reporter 来发送 SSE/进度事件。
type WorkflowNodeReporter func(eventType string, nodeID string, status string, output string, errMsg string)

// ExecuteWorkflow 用 WorkflowEngine 执行一个 DAG 工作流。
// 每个节点映射为一次 Delegate 调用，目标 brain 由 node.BrainID 指定，
// 为空时通过 capMatcher 自动选择。
//
// reporter 可选，用于实时报告节点状态（started/completed/failed）。
func (o *Orchestrator) ExecuteWorkflow(ctx context.Context, wf *Workflow, reporter WorkflowNodeReporter) (*WorkflowResult, error) {
	store := flow.NewMemStore()
	executor := o.makeWorkflowNodeExecutor(ctx, reporter)
	engine := NewWorkflowEngine(store, executor)

	// Streaming edge 跨进程共享：若 Orchestrator 已持有 streamPipes，
	// 将其注入 WorkflowEngine，使 sidecar brain/stream/write 通知
	// 直接写入 WorkflowEngine 内部消费的同一组 pipe。
	if o.streamPipes != nil {
		engine.SetPipeRegistry(o.streamPipes)
	}

	// L2: 如果 learner 存在，使用 RecommendOrder 优化无依赖节点的执行顺序
	if o.learner != nil && len(wf.Nodes) > 1 {
		nodeMap := make(map[string]WorkflowNode, len(wf.Nodes))
		for _, n := range wf.Nodes {
			nodeMap[n.ID] = n
		}
		var steps []TaskStep
		for _, n := range wf.Nodes {
			steps = append(steps, TaskStep{
				BrainKind: agent.Kind(n.BrainID),
				TaskType:  n.TaskType,
			})
		}
		recommended := o.learner.RecommendOrder(steps)
		if len(recommended) == len(steps) {
			// 按推荐顺序重新排列 wf.Nodes（保持依赖关系不变）
			order := make(map[string]int, len(recommended))
			for i, s := range recommended {
				// 找到匹配的节点 ID
				for _, n := range wf.Nodes {
					if n.BrainID == string(s.BrainKind) && n.TaskType == s.TaskType {
						if _, ok := order[n.ID]; !ok {
							order[n.ID] = i
							break
						}
					}
				}
			}
			sort.SliceStable(wf.Nodes, func(i, j int) bool {
				ii, oki := order[wf.Nodes[i].ID]
				ji, okj := order[wf.Nodes[j].ID]
				if oki && okj {
					return ii < ji
				}
				return false
			})
		}
	}

	result, err := engine.Execute(ctx, wf)
	if err != nil {
		return result, err
	}

	// L2: 记录完整 Workflow DAG 执行序列
	if o.learner != nil && result != nil {
		var steps []TaskStep
		var totalScore float64
		for nodeID, nodeResult := range result.Nodes {
			// 查找对应节点的 BrainID 和 TaskType
			var brainID, taskType string
			for _, n := range wf.Nodes {
				if n.ID == nodeID {
					brainID = n.BrainID
					taskType = n.TaskType
					break
				}
			}
			score := 0.0
			if nodeResult.State == StateCompleted {
				score = 1.0
			}
			duration := nodeResult.EndedAt.Sub(nodeResult.StartedAt)
			steps = append(steps, TaskStep{
				BrainKind: agent.Kind(brainID),
				TaskType:  taskType,
				Duration:  duration,
				Score:     score,
			})
			totalScore += score
		}
		if len(steps) > 1 {
			avgScore := totalScore / float64(len(steps))
			o.learner.RecordSequence(TaskSequenceRecord{
				SequenceID: wf.ID,
				TotalScore: avgScore,
				RecordedAt: time.Now(),
				Steps:      steps,
			})
		}
	}

	return result, nil
}

// makeWorkflowNodeExecutor 构造 WorkflowEngine 所需的 NodeExecutor。
// 内部将 WorkflowNode 转为 DelegateRequest，调用 Orchestrator.Delegate 执行。
func (o *Orchestrator) makeWorkflowNodeExecutor(ctx context.Context, reporter WorkflowNodeReporter) NodeExecutor {
	return func(execCtx context.Context, node WorkflowNode, input string) (string, error) {
		if reporter != nil {
			reporter("workflow.node.started", node.ID, "running", "", "")
		}

		req := &DelegateRequest{
			TaskID:        node.ID,
			TargetKind:    agent.Kind(node.BrainID),
			Instruction:   buildWorkflowNodePrompt(node, input),
			RequiredCaps:  node.RequiredCaps,
			PreferredCaps: node.PreferredCaps,
			TaskType:      node.TaskType,
			PipeID:        node.PipeID,
		}

		result, err := o.Delegate(execCtx, req)
		if err != nil {
			if reporter != nil {
				reporter("workflow.node.failed", node.ID, "failed", "", err.Error())
			}
			return "", err
		}

		if result.Status != "completed" {
			errMsg := result.Error
			if errMsg == "" {
				errMsg = fmt.Sprintf("task %s", result.Status)
			}
			if reporter != nil {
				reporter("workflow.node.failed", node.ID, result.Status, "", errMsg)
			}
			return "", fmt.Errorf("node %s: %s", node.ID, errMsg)
		}

		output := string(result.Output)
		if reporter != nil {
			reporter("workflow.node.completed", node.ID, "completed", output, "")
		}
		return output, nil
	}
}

// buildWorkflowNodePrompt 把上游输入和当前节点 prompt 合并。
// 如果上游有输出，作为上下文前置。
func buildWorkflowNodePrompt(node WorkflowNode, upstreamInput string) string {
	if upstreamInput == "" {
		return node.Prompt
	}
	return fmt.Sprintf("Previous results:\n%s\n\nTask: %s", upstreamInput, node.Prompt)
}
