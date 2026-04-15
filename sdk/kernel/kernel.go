// Package kernel is the top-level assembly point for the Brain SDK.
//
// A Kernel holds references to all stores, registries, providers, and exporters
// that the Brain runtime needs. User code constructs a Kernel via NewKernel
// with functional options, then hands it to the Runner/CLI layer.
//
// See 02-BrainKernel设计.md §12 (Kernel assembly) for the constitutional
// definition. Sub-specs referenced by each field are noted in the field docs.
package kernel

import (
	"github.com/leef-l/brain/sdk/observability"
	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/security"
	"github.com/leef-l/brain/sdk/tool"
)

// Kernel is the top-level Brain runtime handle.
//
// All fields are interfaces defined by the sibling sub-packages so that the
// Kernel has zero coupling to any particular implementation (SQLite vs MySQL,
// OTel vs stdout, etc.). A skeleton Kernel can be constructed with only nil
// fields — components must nil-check before dispatching work to them.
//
// Defined in 02-BrainKernel设计.md §12.1.
type Kernel struct {
	// PlanStore persists BrainPlan + deltas. See 26-持久化与恢复.md §3.
	PlanStore persistence.PlanStore

	// ArtifactStore is the CAS-backed artifact store. See 26 §4.
	ArtifactStore persistence.ArtifactStore

	// ArtifactMeta is the metadata store for artifacts (26 §4.3).
	ArtifactMeta persistence.ArtifactMetaStore

	// RunCheckpoint persists Run checkpoints for resume. See 26 §5.
	RunCheckpoint persistence.RunCheckpointStore

	// UsageLedger records token/cost usage per brain (26 §6).
	UsageLedger persistence.UsageLedger

	// Resume coordinates cross-tier resume semantics (26 §7).
	Resume persistence.ResumeCoordinator

	// ToolRegistry is the tool catalog used by brain sidecars (02 §6).
	ToolRegistry tool.Registry

	// Vault holds secrets and issues short-lived leases (23-安全模型.md §5).
	Vault security.Vault

	// AuditLogger emits audit events for Zone crossings (23 §8.4).
	AuditLogger security.AuditLogger

	// Metrics is the metrics registry (24-可观测性.md §4).
	Metrics observability.Registry

	// Trace is the trace span exporter (24 §5).
	Trace observability.TraceExporter

	// Logs is the log event exporter (24 §6).
	Logs observability.LogExporter

	// Orchestrator manages specialist brain delegation. When nil, the
	// system operates in single-brain mode (Central does everything).
	Orchestrator interface{}

	// LLMProxy handles reverse RPC LLM calls from specialist sidecars.
	// When nil, no reverse LLM proxying is available.
	LLMProxy interface{}

	// OrchestratorCfg is the configuration for the Orchestrator when it is
	// created via NewOrchestratorWithConfig. It holds brain registrations
	// for hot-pluggable sidecar management. When empty, the Orchestrator
	// falls back to probing built-in kinds.
	OrchestratorCfg *OrchestratorConfig
}

// Option configures a Kernel during construction. Options compose functionally
// so user code can write `kernel.NewKernel(kernel.WithPlanStore(s), ...)`.
//
// See 02-BrainKernel设计.md §12.2.
type Option func(*Kernel)

// NewKernel constructs a Kernel by applying the provided options in order.
// The returned Kernel is immediately usable; nil fields mean the corresponding
// capability is disabled.
//
// See 02-BrainKernel设计.md §12.2.
func NewKernel(opts ...Option) *Kernel {
	k := &Kernel{}
	for _, opt := range opts {
		if opt != nil {
			opt(k)
		}
	}
	return k
}

// WithPlanStore installs a PlanStore implementation. See 26 §3.
func WithPlanStore(s persistence.PlanStore) Option {
	return func(k *Kernel) { k.PlanStore = s }
}

// WithArtifactStore installs an ArtifactStore. See 26 §4.
func WithArtifactStore(s persistence.ArtifactStore) Option {
	return func(k *Kernel) { k.ArtifactStore = s }
}

// WithArtifactMetaStore installs an ArtifactMetaStore. See 26 §4.3.
func WithArtifactMetaStore(s persistence.ArtifactMetaStore) Option {
	return func(k *Kernel) { k.ArtifactMeta = s }
}

// WithRunCheckpointStore installs a RunCheckpointStore. See 26 §5.
func WithRunCheckpointStore(s persistence.RunCheckpointStore) Option {
	return func(k *Kernel) { k.RunCheckpoint = s }
}

// WithUsageLedger installs a UsageLedger. See 26 §6.
func WithUsageLedger(l persistence.UsageLedger) Option {
	return func(k *Kernel) { k.UsageLedger = l }
}

// WithResumeCoordinator installs a ResumeCoordinator. See 26 §7.
func WithResumeCoordinator(r persistence.ResumeCoordinator) Option {
	return func(k *Kernel) { k.Resume = r }
}

// WithToolRegistry installs a tool.Registry. See 02 §6.
func WithToolRegistry(r tool.Registry) Option {
	return func(k *Kernel) { k.ToolRegistry = r }
}

// WithVault installs a security.Vault. See 23 §5.
func WithVault(v security.Vault) Option {
	return func(k *Kernel) { k.Vault = v }
}

// WithAuditLogger installs a security.AuditLogger. See 23 §8.4.
func WithAuditLogger(a security.AuditLogger) Option {
	return func(k *Kernel) { k.AuditLogger = a }
}

// WithMetrics installs an observability.Registry. See 24 §4.
func WithMetrics(r observability.Registry) Option {
	return func(k *Kernel) { k.Metrics = r }
}

// WithTraceExporter installs an observability.TraceExporter. See 24 §5.
func WithTraceExporter(t observability.TraceExporter) Option {
	return func(k *Kernel) { k.Trace = t }
}

// WithLogExporter installs an observability.LogExporter. See 24 §6.
func WithLogExporter(l observability.LogExporter) Option {
	return func(k *Kernel) { k.Logs = l }
}

// WithPersistence installs all non-nil stores from a persistence.Stores
// bundle in one shot. This is the idiomatic way to wire a Driver's output
// into a Kernel:
//
//	stores, _ := persistence.Open("sqlite", dsn)
//	k := kernel.NewKernel(kernel.WithPersistence(stores.Stores))
func WithPersistence(s persistence.Stores) Option {
	return func(k *Kernel) {
		if s.PlanStore != nil {
			k.PlanStore = s.PlanStore
		}
		if s.ArtifactStore != nil {
			k.ArtifactStore = s.ArtifactStore
		}
		if s.ArtifactMeta != nil {
			k.ArtifactMeta = s.ArtifactMeta
		}
		if s.RunCheckpointStore != nil {
			k.RunCheckpoint = s.RunCheckpointStore
		}
		if s.UsageLedger != nil {
			k.UsageLedger = s.UsageLedger
		}
		if s.ResumeCoordinator != nil {
			k.Resume = s.ResumeCoordinator
		}
	}
}

// WithOrchestrator installs the specialist brain orchestrator.
func WithOrchestrator(o interface{}) Option {
	return func(k *Kernel) { k.Orchestrator = o }
}

// WithLLMProxy installs the reverse-RPC LLM proxy for sidecars.
func WithLLMProxy(p interface{}) Option {
	return func(k *Kernel) { k.LLMProxy = p }
}

// WithOrchestratorConfig installs an OrchestratorConfig for configuration-
// driven brain registration. When the Orchestrator is subsequently created
// via NewOrchestratorWithConfig, it will use this config to determine which
// brains to probe instead of the built-in kind list.
func WithOrchestratorConfig(cfg OrchestratorConfig) Option {
	return func(k *Kernel) { k.OrchestratorCfg = &cfg }
}
