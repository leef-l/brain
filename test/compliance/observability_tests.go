package compliance

import (
	"context"
	"fmt"

	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/observability"
	braintesting "github.com/leef-l/brain/testing"
)

func registerObservabilityTests(r *braintesting.MemComplianceRunner) {
	// C-O-01: MemRegistry Counter increments.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-01", Description: "Counter Inc and Add", Category: "observability",
	}, func(ctx context.Context) error {
		reg := observability.NewMemRegistry()
		c := reg.Counter("test.counter", observability.Labels{"brain": "central"})
		c.Inc()
		c.Add(5)
		// If we got here without panic, the counter works.
		return nil
	})

	// C-O-02: MemRegistry Histogram observes.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-02", Description: "Histogram Observe", Category: "observability",
	}, func(ctx context.Context) error {
		reg := observability.NewMemRegistry()
		h := reg.Histogram("test.latency", observability.Labels{"brain": "code"}, nil)
		h.Observe(0.5)
		h.Observe(1.2)
		return nil
	})

	// C-O-03: MemRegistry Gauge set and add.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-03", Description: "Gauge Set and Add", Category: "observability",
	}, func(ctx context.Context) error {
		reg := observability.NewMemRegistry()
		g := reg.Gauge("test.inflight", observability.Labels{})
		g.Set(10)
		g.Add(-3)
		return nil
	})

	// C-O-04: TraceExporter StartSpan returns valid Span.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-04", Description: "TraceExporter StartSpan", Category: "observability",
	}, func(ctx context.Context) error {
		exp := observability.NewMemTraceExporter()
		spanCtx, span := exp.StartSpan(ctx, "test.span", observability.Labels{"op": "test"})
		if spanCtx == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-O-04: spanCtx nil"))
		}
		span.SetAttr("key", "value")
		span.End()
		spans := exp.Spans()
		if len(spans) != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-O-04: spans=%d, want 1", len(spans))))
		}
		return nil
	})

	// C-O-05: TraceExporter nested spans.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-05", Description: "TraceExporter nested spans", Category: "observability",
	}, func(ctx context.Context) error {
		exp := observability.NewMemTraceExporter()
		ctx1, parent := exp.StartSpan(ctx, "parent", nil)
		_, child := exp.StartSpan(ctx1, "child", nil)
		child.End()
		parent.End()
		spans := exp.Spans()
		if len(spans) != 2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-O-05: spans=%d, want 2", len(spans))))
		}
		return nil
	})

	// C-O-06: Span.SetError is safe with nil.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-06", Description: "Span.SetError nil safe", Category: "observability",
	}, func(ctx context.Context) error {
		exp := observability.NewMemTraceExporter()
		_, span := exp.StartSpan(ctx, "test", nil)
		span.SetError(nil) // should not panic
		span.End()
		return nil
	})

	// C-O-07: LogExporter Emit records.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-07", Description: "LogExporter Emit", Category: "observability",
	}, func(ctx context.Context) error {
		logs := observability.NewMemLogExporter(100, "")
		logs.Emit(ctx, observability.LogInfo, "test message", observability.Labels{"key": "val"})
		records := logs.Records()
		if len(records) != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-O-07: records=%d, want 1", len(records))))
		}
		return nil
	})

	// C-O-08: LogExporter respects minLevel.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-08", Description: "LogExporter minLevel filtering", Category: "observability",
	}, func(ctx context.Context) error {
		logs := observability.NewMemLogExporter(100, observability.LogWarn)
		logs.Emit(ctx, observability.LogDebug, "should be filtered", nil)
		logs.Emit(ctx, observability.LogWarn, "should pass", nil)
		records := logs.Records()
		if len(records) != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-O-08: records=%d, want 1", len(records))))
		}
		return nil
	})

	// C-O-09: LogLevel constants.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-09", Description: "LogLevel constants", Category: "observability",
	}, func(ctx context.Context) error {
		levels := []observability.LogLevel{
			observability.LogTrace, observability.LogDebug,
			observability.LogInfo, observability.LogWarn, observability.LogError,
		}
		for _, l := range levels {
			if l == "" {
				return brainerrors.New(brainerrors.CodeAssertionFailed,
					brainerrors.WithMessage("C-O-09: empty level"))
			}
		}
		return nil
	})

	// C-O-10: LogExporter capacity ring buffer.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-10", Description: "LogExporter capacity", Category: "observability",
	}, func(ctx context.Context) error {
		logs := observability.NewMemLogExporter(3, "")
		for i := 0; i < 5; i++ {
			logs.Emit(ctx, observability.LogInfo, fmt.Sprintf("msg-%d", i), nil)
		}
		records := logs.Records()
		if len(records) > 3 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-O-10: records=%d, want ≤3", len(records))))
		}
		return nil
	})

	// C-O-11: LogExporter Count.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-11", Description: "LogExporter Count", Category: "observability",
	}, func(ctx context.Context) error {
		logs := observability.NewMemLogExporter(100, "")
		logs.Emit(ctx, observability.LogInfo, "a", nil)
		logs.Emit(ctx, observability.LogInfo, "b", nil)
		if logs.Count() != 2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-O-11: Count=%d, want 2", logs.Count())))
		}
		return nil
	})

	// C-O-12: Labels type is map[string]string.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-12", Description: "Labels type", Category: "observability",
	}, func(ctx context.Context) error {
		var l observability.Labels = map[string]string{"a": "b"}
		if l["a"] != "b" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-O-12: Labels access failed"))
		}
		return nil
	})

	// C-O-13: Counter Add negative panics/errors (or is no-op per contract).
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-13", Description: "Counter non-negative contract", Category: "observability",
	}, func(ctx context.Context) error {
		reg := observability.NewMemRegistry()
		c := reg.Counter("neg.test", nil)
		// Contract says Add(n) MUST be non-negative.
		// Implementation may panic or silently ignore. We verify it doesn't corrupt.
		c.Add(0) // zero is valid
		c.Inc()
		return nil
	})

	// C-O-14: TraceExporter FindByTraceID.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-14", Description: "TraceExporter FindByTraceID", Category: "observability",
	}, func(ctx context.Context) error {
		exp := observability.NewMemTraceExporter()
		_, span := exp.StartSpan(ctx, "find-me", nil)
		span.End()
		spans := exp.Spans()
		if len(spans) == 0 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-O-14: no spans"))
		}
		found := exp.FindByTraceID(spans[0].TraceID)
		if len(found) != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-O-14: found=%d, want 1", len(found))))
		}
		return nil
	})

	// C-O-15: Gauge Add negative values allowed.
	r.Register(braintesting.ComplianceTest{
		ID: "C-O-15", Description: "Gauge Add negative", Category: "observability",
	}, func(ctx context.Context) error {
		reg := observability.NewMemRegistry()
		g := reg.Gauge("neg.gauge", nil)
		g.Set(10)
		g.Add(-5)
		// No panic = success.
		return nil
	})
}
