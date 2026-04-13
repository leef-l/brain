package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leef-l/brain"
	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/cli"
	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/kernel"
	"github.com/leef-l/brain/llm"
	"github.com/leef-l/brain/loop"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/protocol"
	braintesting "github.com/leef-l/brain/testing"
	"github.com/leef-l/brain/tool"
)

func registerSDKTests(r *braintesting.MemComplianceRunner) {
	// C-SDK-01: ProtocolVersion is "1.0".
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-01", Description: "ProtocolVersion is 1.0", Category: "sdk",
	}, func(ctx context.Context) error {
		if brain.ProtocolVersion != "1.0" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-01: ProtocolVersion=%q", brain.ProtocolVersion)))
		}
		return nil
	})

	// C-SDK-02: SDKLanguage is "go".
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-02", Description: "SDKLanguage is go", Category: "sdk",
	}, func(ctx context.Context) error {
		if brain.SDKLanguage != "go" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-02: SDKLanguage=%q", brain.SDKLanguage)))
		}
		return nil
	})

	// C-SDK-03: KernelVersion follows semver format.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-03", Description: "KernelVersion semver format", Category: "sdk",
	}, func(ctx context.Context) error {
		parts := strings.SplitN(brain.KernelVersion, ".", 3)
		if len(parts) < 3 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-03: KernelVersion=%q not semver", brain.KernelVersion)))
		}
		return nil
	})

	// C-SDK-04: NewMemKernel returns non-nil Kernel.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-04", Description: "NewMemKernel non-nil", Category: "sdk",
	}, func(ctx context.Context) error {
		k := kernel.NewMemKernel(kernel.MemKernelOptions{})
		if k == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-04: NewMemKernel nil"))
		}
		return nil
	})

	// C-SDK-05: NewMemKernel wires PlanStore.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-05", Description: "NewMemKernel wires PlanStore", Category: "sdk",
	}, func(ctx context.Context) error {
		k := kernel.NewMemKernel(kernel.MemKernelOptions{})
		if k.PlanStore == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-05: PlanStore nil"))
		}
		return nil
	})

	// C-SDK-06: NewMemKernel wires ToolRegistry with builtins.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-06", Description: "NewMemKernel registers builtin tools", Category: "sdk",
	}, func(ctx context.Context) error {
		k := kernel.NewMemKernel(kernel.MemKernelOptions{BrainKind: "test"})
		if k.ToolRegistry == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-06: ToolRegistry nil"))
		}
		_, found := k.ToolRegistry.Lookup("test.echo")
		if !found {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-06: test.echo not found"))
		}
		return nil
	})

	// C-SDK-07: Tool interface — EchoTool.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-07", Description: "EchoTool implements Tool", Category: "sdk",
	}, func(ctx context.Context) error {
		echo := tool.NewEchoTool("test")
		if echo.Name() != "test.echo" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-07: Name=%q", echo.Name())))
		}
		result, err := echo.Execute(ctx, json.RawMessage(`{"message":"hello"}`))
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-07: Execute: %v", err)))
		}
		if result == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-07: result nil"))
		}
		return nil
	})

	// C-SDK-08: MockProvider Complete round-trip.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-08", Description: "MockProvider Complete round-trip", Category: "sdk",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueText("Hello!")
		resp, err := mp.Complete(ctx, &llm.ChatRequest{
			Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hi"}}}},
		})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-08: Complete: %v", err)))
		}
		if len(resp.Content) == 0 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-08: empty content"))
		}
		return nil
	})

	// C-SDK-09: MockProvider Stream round-trip.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-09", Description: "MockProvider Stream round-trip", Category: "sdk",
	}, func(ctx context.Context) error {
		mp := llm.NewMockProvider("test")
		mp.QueueText("Streamed!")
		reader, err := mp.Stream(ctx, &llm.ChatRequest{
			Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hi"}}}},
		})
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-09: Stream: %v", err)))
		}
		defer reader.Close()
		ev, err := reader.Next(ctx)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-09: Next: %v", err)))
		}
		if ev.Type == "" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-09: empty event type"))
		}
		return nil
	})

	// C-SDK-10: MemRegistry Lookup returns found/not-found.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-10", Description: "MemRegistry Lookup", Category: "sdk",
	}, func(ctx context.Context) error {
		reg := tool.NewMemRegistry()
		reg.Register(tool.NewEchoTool("test"))
		_, found := reg.Lookup("test.echo")
		if !found {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-10: test.echo not found"))
		}
		_, found = reg.Lookup("nonexistent")
		if found {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-10: nonexistent should not be found"))
		}
		return nil
	})

	// C-SDK-11: ComplianceRunner RunAll produces report.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-11", Description: "ComplianceRunner RunAll produces report", Category: "sdk",
	}, func(ctx context.Context) error {
		cr := braintesting.NewComplianceRunner().(*braintesting.MemComplianceRunner)
		cr.Register(braintesting.ComplianceTest{ID: "X-01", Description: "test", Category: "sdk"},
			func(ctx context.Context) error { return nil })
		report, err := cr.RunAll(ctx)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-11: RunAll: %v", err)))
		}
		if report.Summary.Total != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-11: Total=%d, want 1", report.Summary.Total)))
		}
		return nil
	})

	// C-SDK-12: All protocol method constants are non-empty.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-12", Description: "Protocol method constants non-empty", Category: "sdk",
	}, func(ctx context.Context) error {
		methods := []string{
			protocol.MethodInitialize, protocol.MethodShutdown,
			protocol.MethodHeartbeat, protocol.MethodLLMComplete,
			protocol.MethodLLMStream, protocol.MethodToolInvoke,
			protocol.MethodPlanCreate, protocol.MethodPlanUpdate,
			protocol.MethodArtifactPut, protocol.MethodArtifactGet,
			protocol.MethodTraceEmit, protocol.MethodAuditEmit,
		}
		for _, m := range methods {
			if m == "" {
				return brainerrors.New(brainerrors.CodeAssertionFailed,
					brainerrors.WithMessage("C-SDK-12: empty method constant"))
			}
		}
		return nil
	})

	// C-SDK-13: agent.Kind constants.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-13", Description: "agent.Kind built-in constants", Category: "sdk",
	}, func(ctx context.Context) error {
		kinds := []agent.Kind{
			agent.KindCentral, agent.KindCode,
			agent.KindBrowser, agent.KindVerifier,
		}
		for _, k := range kinds {
			if k == "" {
				return brainerrors.New(brainerrors.CodeAssertionFailed,
					brainerrors.WithMessage("C-SDK-13: empty Kind"))
			}
		}
		return nil
	})

	// C-SDK-14: FakeSidecar construction.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-14", Description: "FakeSidecar construction", Category: "sdk",
	}, func(ctx context.Context) error {
		fs := braintesting.NewFakeSidecar(nil)
		if fs == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-14: FakeSidecar nil"))
		}
		return nil
	})

	// C-SDK-15: BrainRunner interface exists (compile check via ProcessRunner).
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-15", Description: "BrainRunner interface (ProcessRunner)", Category: "sdk",
	}, func(ctx context.Context) error {
		// Compile-time check: ProcessRunner satisfies BrainRunner.
		var _ kernel.BrainRunner = (*kernel.ProcessRunner)(nil)
		return nil
	})

	// C-SDK-16: PlanStore Create/Get/Update/ListByRun/Archive all exist.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-16", Description: "PlanStore interface completeness", Category: "sdk",
	}, func(ctx context.Context) error {
		var ps persistence.PlanStore = persistence.NewMemPlanStore(nil)
		plan := &persistence.BrainPlan{BrainID: "test", Version: 1, CurrentState: json.RawMessage(`{}`)}
		id, _ := ps.Create(ctx, plan)
		_, _ = ps.Get(ctx, id)
		_ = ps.Update(ctx, id, &persistence.BrainPlanDelta{Version: 2, OpType: "replace", Payload: json.RawMessage(`{}`)})
		_, _ = ps.ListByRun(ctx, 0)
		_ = ps.Archive(ctx, id)
		return nil
	})

	// C-SDK-17: NewRun and Run lifecycle helpers.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-17", Description: "NewRun + lifecycle helpers", Category: "sdk",
	}, func(ctx context.Context) error {
		run := loop.NewRun("sdk-17", "test", loop.Budget{})
		if run.ID != "sdk-17" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-17: ID mismatch"))
		}
		if run.BrainID != "test" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-SDK-17: BrainID mismatch"))
		}
		return nil
	})

	// C-SDK-18: CLIVersion follows semver.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-18", Description: "CLIVersion semver format", Category: "sdk",
	}, func(ctx context.Context) error {
		parts := strings.SplitN(brain.CLIVersion, ".", 3)
		if len(parts) < 3 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-18: CLIVersion=%q not semver", brain.CLIVersion)))
		}
		return nil
	})

	// C-SDK-19: SDKVersion follows semver.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-19", Description: "SDKVersion semver format", Category: "sdk",
	}, func(ctx context.Context) error {
		parts := strings.SplitN(brain.SDKVersion, ".", 3)
		if len(parts) < 3 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-SDK-19: SDKVersion=%q not semver", brain.SDKVersion)))
		}
		return nil
	})

	// C-SDK-20: All 13 packages compile and are importable.
	r.Register(braintesting.ComplianceTest{
		ID: "C-SDK-20", Description: "All packages importable", Category: "sdk",
	}, func(ctx context.Context) error {
		// These imports are validated at compile time.
		// If this test compiles, all packages are importable.
		_ = brain.ProtocolVersion
		_ = agent.KindCentral
		_ = cli.ExitOK
		_ = brainerrors.CodeUnknown
		_ = kernel.NewMemKernel
		_ = llm.NewMockProvider
		_ = loop.StatePending
		_ = persistence.NewMemPlanStore
		_ = protocol.MethodInitialize
		_ = tool.NewMemRegistry
		_ = braintesting.NewComplianceRunner
		return nil
	})
}
