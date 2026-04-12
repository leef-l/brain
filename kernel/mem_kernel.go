package kernel

import (
	"time"

	"github.com/leef-l/brain/observability"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/security"
	"github.com/leef-l/brain/tool"
)

// MemKernelOptions tunes NewMemKernel. All fields are optional; the zero
// value yields a fully-functional in-memory Kernel suitable for unit
// tests, the `brain doctor` smoke path, and the v0.1.0 reference
// executable.
type MemKernelOptions struct {
	// Now overrides the wall-clock source used by every in-memory store.
	// nil defaults to time.Now in UTC.
	Now func() time.Time

	// LogCapacity bounds the MemLogExporter ring buffer. 0 defaults to 512.
	LogCapacity int

	// MinLogLevel filters logs emitted through the exporter. Zero value
	// passes every level.
	MinLogLevel observability.LogLevel

	// ExtraTools are appended to the tool registry after the default
	// builtins. They are registered in slice order; duplicates will
	// surface as a registry error.
	ExtraTools []tool.Tool

	// BrainKind is the naming prefix for the default builtin tools
	// (echo / reject_task). Empty defaults to "central".
	BrainKind string
}

// NewMemKernel returns a fully-wired Kernel backed exclusively by the
// in-memory implementations from persistence/, observability/, tool/,
// and security/. It is the one-stop constructor used by:
//
//   - `cmd/brain run` as the reference executor in v0.1.0
//   - `cmd/brain doctor` for the PlanStore / ArtifactStore smoke probes
//   - every Kernel-level unit / compliance test in brain/testing/
//
// The function panics only on registry misuse (duplicate tool names),
// which the caller can avoid by keeping ExtraTools name-unique.
//
// See 02-BrainKernel设计.md §12.2 and 28-SDK交付规范.md §9.
func NewMemKernel(opts MemKernelOptions) *Kernel {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	brainKind := opts.BrainKind
	if brainKind == "" {
		brainKind = "central"
	}
	logCap := opts.LogCapacity
	if logCap <= 0 {
		logCap = 512
	}

	// --- persistence tier ---
	artifactMeta := persistence.NewMemArtifactMetaStore(now)
	artifactStore := persistence.NewMemArtifactStore(artifactMeta, now)
	planStore := persistence.NewMemPlanStore(now)
	checkpoint := persistence.NewMemRunCheckpointStore(now)
	usage := persistence.NewMemUsageLedger(now)
	resume := persistence.NewMemResumeCoordinator(checkpoint)

	// --- tool registry with builtin echo + reject_task ---
	registry := tool.NewMemRegistry()
	if err := registry.Register(tool.NewEchoTool(brainKind)); err != nil {
		panic("kernel.NewMemKernel: register echo: " + err.Error())
	}
	if err := registry.Register(tool.NewRejectTaskTool(brainKind, nil)); err != nil {
		panic("kernel.NewMemKernel: register reject_task: " + err.Error())
	}
	for _, t := range opts.ExtraTools {
		if err := registry.Register(t); err != nil {
			panic("kernel.NewMemKernel: register extra tool: " + err.Error())
		}
	}

	// --- security tier ---
	audit := security.NewHashChainAuditLogger()
	vault := security.NewMemVault(security.WithMemVaultAuditor(audit))

	// --- observability tier ---
	metrics := observability.NewMemRegistry()
	trace := observability.NewMemTraceExporter()
	logs := observability.NewMemLogExporter(logCap, opts.MinLogLevel)

	return NewKernel(
		WithPlanStore(planStore),
		WithArtifactStore(artifactStore),
		WithArtifactMetaStore(artifactMeta),
		WithRunCheckpointStore(checkpoint),
		WithUsageLedger(usage),
		WithResumeCoordinator(resume),
		WithToolRegistry(registry),
		WithVault(vault),
		WithAuditLogger(audit),
		WithMetrics(metrics),
		WithTraceExporter(trace),
		WithLogExporter(logs),
	)
}
