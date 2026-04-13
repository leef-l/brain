package skeleton

import (
	"context"
	"sync"
	"testing"

	"github.com/leef-l/brain/observability"
)

// ---------------------------------------------------------------------------
// MemRegistry — Counter
// ---------------------------------------------------------------------------

func TestMemCounterIncAndAdd(t *testing.T) {
	reg := observability.NewMemRegistry()
	c := reg.Counter("requests_total", observability.Labels{"method": "GET"})
	c.Inc()
	c.Inc()
	c.Add(3)

	snap := reg.Snapshot()
	key := "counter|requests_total|method=GET"
	if v, ok := snap[key]; !ok || v != 5 {
		t.Errorf("counter %q = %v, want 5", key, v)
	}
}

// ---------------------------------------------------------------------------
// MemRegistry — Histogram
// ---------------------------------------------------------------------------

func TestMemHistogramObserve(t *testing.T) {
	reg := observability.NewMemRegistry()
	h := reg.Histogram("latency_ms", observability.Labels{"endpoint": "/api"}, nil)
	h.Observe(42.5)
	h.Observe(100.0)
	h.Observe(7.3)

	snap := reg.Snapshot()
	key := "histogram|latency_ms|endpoint=/api|count"
	if v, ok := snap[key]; !ok || v != 3 {
		t.Errorf("histogram %q count = %v, want 3", key, v)
	}
}

// ---------------------------------------------------------------------------
// MemRegistry — Gauge
// ---------------------------------------------------------------------------

func TestMemGaugeSetAndAdd(t *testing.T) {
	reg := observability.NewMemRegistry()
	g := reg.Gauge("queue_depth", observability.Labels{"brain": "central"})
	g.Set(10)
	g.Add(-3)

	snap := reg.Snapshot()
	key := "gauge|queue_depth|brain=central"
	if v, ok := snap[key]; !ok || v != 7 {
		t.Errorf("gauge %q = %v, want 7", key, v)
	}
}

// ---------------------------------------------------------------------------
// Counter 并发安全
// ---------------------------------------------------------------------------

func TestMemCounterConcurrency(t *testing.T) {
	reg := observability.NewMemRegistry()
	c := reg.Counter("concurrent", observability.Labels{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	snap := reg.Snapshot()
	if v := snap["counter|concurrent"]; v != 100 {
		t.Errorf("counter = %v, want 100", v)
	}
}

// ---------------------------------------------------------------------------
// MemTraceExporter — Span
// ---------------------------------------------------------------------------

func TestMemTraceSpan(t *testing.T) {
	tracer := observability.NewMemTraceExporter()
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.operation", observability.Labels{"key": "val"})
	span.SetAttr("custom", "attribute")
	span.End()

	spans := tracer.Spans()
	if len(spans) != 1 {
		t.Fatalf("spans len = %d, want 1", len(spans))
	}
	if spans[0].Name != "test.operation" {
		t.Errorf("span name = %q", spans[0].Name)
	}
}

func TestMemTraceParentChild(t *testing.T) {
	tracer := observability.NewMemTraceExporter()
	ctx := context.Background()

	parentCtx, parent := tracer.StartSpan(ctx, "parent", nil)
	_, child := tracer.StartSpan(parentCtx, "child", nil)
	child.End()
	parent.End()

	spans := tracer.Spans()
	if len(spans) != 2 {
		t.Fatalf("spans len = %d, want 2", len(spans))
	}

	// 父子应共享 TraceID
	if spans[0].TraceID != spans[1].TraceID {
		t.Error("parent and child should share TraceID")
	}
	// 子 span 的 ParentID 应等于父 span 的 SpanID
	if spans[1].ParentID != spans[0].SpanID {
		t.Error("child ParentID should match parent SpanID")
	}
}

func TestMemTraceSpanSetError(t *testing.T) {
	tracer := observability.NewMemTraceExporter()
	_, span := tracer.StartSpan(context.Background(), "err_op", nil)
	span.SetError(context.Canceled)
	span.End()

	spans := tracer.Spans()
	if spans[0].Error == "" {
		t.Error("span should have error set")
	}
}

func TestMemTraceConcurrency(t *testing.T) {
	tracer := observability.NewMemTraceExporter()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, s := tracer.StartSpan(context.Background(), "concurrent", nil)
			s.End()
		}()
	}
	wg.Wait()
	if len(tracer.Spans()) != 100 {
		t.Errorf("spans = %d, want 100", len(tracer.Spans()))
	}
}

// ---------------------------------------------------------------------------
// MemLogExporter
// ---------------------------------------------------------------------------

func TestMemLogExporterEmit(t *testing.T) {
	logger := observability.NewMemLogExporter(100, "")
	ctx := context.Background()
	logger.Emit(ctx, observability.LogInfo, "test message", observability.Labels{"key": "val"})

	logs := logger.Records()
	if len(logs) != 1 {
		t.Fatalf("logs len = %d, want 1", len(logs))
	}
	if logs[0].Message != "test message" {
		t.Errorf("message = %q", logs[0].Message)
	}
	if logs[0].Level != observability.LogInfo {
		t.Errorf("level = %q", logs[0].Level)
	}
}

func TestMemLogExporterLevelFilter(t *testing.T) {
	logger := observability.NewMemLogExporter(100, observability.LogWarn)
	ctx := context.Background()
	logger.Emit(ctx, observability.LogTrace, "trace", nil)
	logger.Emit(ctx, observability.LogDebug, "debug", nil)
	logger.Emit(ctx, observability.LogInfo, "info", nil)
	logger.Emit(ctx, observability.LogWarn, "warn", nil)
	logger.Emit(ctx, observability.LogError, "error", nil)

	logs := logger.Records()
	if len(logs) != 2 {
		t.Errorf("logs len = %d, want 2 (warn + error)", len(logs))
	}
}

func TestMemLogExporterRingBuffer(t *testing.T) {
	logger := observability.NewMemLogExporter(5, "")
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		logger.Emit(ctx, observability.LogInfo, "msg_"+intToStr(i), nil)
	}
	logs := logger.Records()
	if len(logs) != 5 {
		t.Errorf("logs len = %d, want 5 (ring buffer cap)", len(logs))
	}
}

func TestMemLogExporterConcurrency(t *testing.T) {
	logger := observability.NewMemLogExporter(512, "")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Emit(context.Background(), observability.LogInfo, "concurrent", nil)
		}()
	}
	wg.Wait()
}
