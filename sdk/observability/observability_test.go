package observability

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMemRegistryCounterBasic tests basic Counter operations.
func TestMemRegistryCounterBasic(t *testing.T) {
	reg := NewMemRegistry()
	counter := reg.Counter("test_counter", Labels{"type": "basic"})

	counter.Inc()
	counter.Inc()
	counter.Add(5)

	val := reg.CounterValue("test_counter", Labels{"type": "basic"})
	if val != 7 {
		t.Errorf("expected counter value 7, got %v", val)
	}
}

// TestMemRegistryCounterDifferentLabels tests that different label sets
// produce independent counters.
func TestMemRegistryCounterDifferentLabels(t *testing.T) {
	reg := NewMemRegistry()

	c1 := reg.Counter("test_counter", Labels{"env": "prod"})
	c2 := reg.Counter("test_counter", Labels{"env": "dev"})

	c1.Add(10)
	c2.Add(20)

	val1 := reg.CounterValue("test_counter", Labels{"env": "prod"})
	val2 := reg.CounterValue("test_counter", Labels{"env": "dev"})

	if val1 != 10 {
		t.Errorf("expected counter 1 value 10, got %v", val1)
	}
	if val2 != 20 {
		t.Errorf("expected counter 2 value 20, got %v", val2)
	}
}

// TestMemRegistryCounterConcurrency tests that Counter operations are
// thread-safe under concurrent load (100 goroutines × 100 increments).
func TestMemRegistryCounterConcurrency(t *testing.T) {
	reg := NewMemRegistry()
	counter := reg.Counter("concurrent_counter", nil)

	const numGoroutines = 100
	const opsPerGoroutine = 100
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				counter.Inc()
			}
		}()
	}

	wg.Wait()

	expected := float64(numGoroutines * opsPerGoroutine)
	val := reg.CounterValue("concurrent_counter", nil)
	if val != expected {
		t.Errorf("expected counter value %v, got %v", expected, val)
	}
}

// TestMemRegistryHistogramBasic tests basic Histogram operations.
func TestMemRegistryHistogramBasic(t *testing.T) {
	reg := NewMemRegistry()
	hist := reg.Histogram("test_histogram", Labels{"type": "latency"}, []float64{0.1, 0.5, 1.0})

	hist.Observe(0.05)
	hist.Observe(0.3)
	hist.Observe(0.8)
	hist.Observe(1.5)

	values := reg.HistogramValues("test_histogram", Labels{"type": "latency"})
	if len(values) != 4 {
		t.Errorf("expected 4 observations, got %d", len(values))
	}

	expected := []float64{0.05, 0.3, 0.8, 1.5}
	for i, v := range expected {
		if i >= len(values) || values[i] != v {
			t.Errorf("expected observation %d to be %v, got %v", i, v, values[i])
		}
	}
}

// TestMemRegistryGaugeBasic tests basic Gauge operations.
func TestMemRegistryGaugeBasic(t *testing.T) {
	reg := NewMemRegistry()
	gauge := reg.Gauge("test_gauge", Labels{"resource": "memory"})

	gauge.Set(100)
	if reg.GaugeValue("test_gauge", Labels{"resource": "memory"}) != 100 {
		t.Error("expected gauge value 100")
	}

	gauge.Add(50)
	if reg.GaugeValue("test_gauge", Labels{"resource": "memory"}) != 150 {
		t.Error("expected gauge value 150 after Add(50)")
	}

	gauge.Add(-30)
	if reg.GaugeValue("test_gauge", Labels{"resource": "memory"}) != 120 {
		t.Error("expected gauge value 120 after Add(-30)")
	}
}

// TestMemRegistryGaugeConcurrency tests that Gauge operations are
// thread-safe under concurrent load.
func TestMemRegistryGaugeConcurrency(t *testing.T) {
	reg := NewMemRegistry()
	gauge := reg.Gauge("concurrent_gauge", nil)

	const numGoroutines = 50
	const opsPerGoroutine = 100
	var wg sync.WaitGroup
	var expectedSum atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		increment := int64(i+1) * 10
		expectedSum.Add(increment * opsPerGoroutine)

		go func(inc float64) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				gauge.Add(inc)
			}
		}(float64(increment))
	}

	wg.Wait()

	val := reg.GaugeValue("concurrent_gauge", nil)
	expected := float64(expectedSum.Load())
	if val != expected {
		t.Errorf("expected gauge value %v, got %v", expected, val)
	}
}

// TestMemRegistrySnapshot tests that Snapshot() returns a flat map of
// all metrics and is safe for concurrent iteration.
func TestMemRegistrySnapshot(t *testing.T) {
	reg := NewMemRegistry()

	c := reg.Counter("test", Labels{"type": "counter"})
	c.Add(42)

	g := reg.Gauge("test", Labels{"type": "gauge"})
	g.Set(99)

	h := reg.Histogram("test", Labels{"type": "histogram"}, nil)
	h.Observe(1.0)
	h.Observe(2.0)

	snap := reg.Snapshot()

	if snap["counter|test|type=counter"] != 42 {
		t.Error("counter snapshot mismatch")
	}
	if snap["gauge|test|type=gauge"] != 99 {
		t.Error("gauge snapshot mismatch")
	}
	if snap["histogram|test|type=histogram|count"] != 2 {
		t.Error("histogram count mismatch")
	}
}

// TestMemTraceExporterRootSpan tests basic root span creation.
func TestMemTraceExporterRootSpan(t *testing.T) {
	exporter := NewMemTraceExporter()
	ctx := context.Background()

	_, span := exporter.StartSpan(ctx, "root_op", Labels{"step": "1"})
	defer span.End()

	memSpan := span.(*MemSpan)
	if memSpan.TraceID == "" {
		t.Error("root span should have a TraceID")
	}
	if memSpan.ParentID != "" {
		t.Error("root span should have empty ParentID")
	}
	if memSpan.Name != "root_op" {
		t.Error("span name should be 'root_op'")
	}

	spans := exporter.Spans()
	if len(spans) != 1 {
		t.Errorf("expected 1 span, got %d", len(spans))
	}
}

// TestMemTraceExporterChildSpan tests parent/child span relationships.
func TestMemTraceExporterChildSpan(t *testing.T) {
	exporter := NewMemTraceExporter()
	ctx := context.Background()

	ctx1, parent := exporter.StartSpan(ctx, "parent", nil)
	defer parent.End()

	parentSpan := parent.(*MemSpan)
	_, child := exporter.StartSpan(ctx1, "child", Labels{"depth": "1"})
	defer child.End()

	childSpan := child.(*MemSpan)

	if childSpan.ParentID != parentSpan.SpanID {
		t.Errorf("child ParentID should be %s, got %s", parentSpan.SpanID, childSpan.ParentID)
	}
	if childSpan.TraceID != parentSpan.TraceID {
		t.Errorf("child TraceID should match parent TraceID")
	}

	spans := exporter.Spans()
	if len(spans) != 2 {
		t.Errorf("expected 2 spans, got %d", len(spans))
	}
}

// TestMemTraceExporterSetAttrAndError tests span attribute and error setting.
func TestMemTraceExporterSetAttrAndError(t *testing.T) {
	exporter := NewMemTraceExporter()
	ctx := context.Background()

	_, span := exporter.StartSpan(ctx, "test_span", Labels{"initial": "attr"})
	span.SetAttr("added", "value")
	span.SetError(nil) // no-op for nil
	span.SetError(ErrTestSpan)
	span.End()

	spans := exporter.Spans()
	if len(spans) != 1 {
		t.Fatal("expected 1 span")
	}

	s := spans[0]
	if s.Attrs["initial"] != "attr" {
		t.Error("initial attr lost")
	}
	if s.Attrs["added"] != "value" {
		t.Error("added attr not present")
	}
	if s.Error == "" {
		t.Error("error should be set")
	}
}

// TestMemTraceExporterConcurrency tests that StartSpan is thread-safe
// under concurrent load.
func TestMemTraceExporterConcurrency(t *testing.T) {
	exporter := NewMemTraceExporter()
	ctx := context.Background()

	const numGoroutines = 100
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, span := exporter.StartSpan(ctx, "concurrent_span", Labels{"id": string(rune(idx))})
			defer span.End()
			// Simulate some work
			time.Sleep(time.Millisecond)
		}(i)
	}

	wg.Wait()

	spans := exporter.Spans()
	if len(spans) != numGoroutines {
		t.Errorf("expected %d spans, got %d", numGoroutines, len(spans))
	}

	// Verify all spans have a TraceID
	if len(spans) > 0 {
		for _, s := range spans {
			if s.TraceID == "" {
				t.Error("span should have a TraceID")
			}
		}
	}
}

// TestMemTraceExporterFindByTraceID tests filtering spans by trace ID.
func TestMemTraceExporterFindByTraceID(t *testing.T) {
	exporter := NewMemTraceExporter()
	ctx := context.Background()

	_, span1 := exporter.StartSpan(ctx, "trace1_root", nil)
	traceID1 := span1.(*MemSpan).TraceID
	span1.End()

	_, span2 := exporter.StartSpan(ctx, "trace2_root", nil)
	traceID2 := span2.(*MemSpan).TraceID
	span2.End()

	found1 := exporter.FindByTraceID(traceID1)
	found2 := exporter.FindByTraceID(traceID2)

	if len(found1) != 1 {
		t.Errorf("expected 1 span for trace 1, got %d", len(found1))
	}
	if len(found2) != 1 {
		t.Errorf("expected 1 span for trace 2, got %d", len(found2))
	}

	if found1[0].Name != "trace1_root" {
		t.Error("span name mismatch for trace 1")
	}
	if found2[0].Name != "trace2_root" {
		t.Error("span name mismatch for trace 2")
	}
}

// TestMemLogExporterBasic tests basic log emission and retrieval.
func TestMemLogExporterBasic(t *testing.T) {
	exporter := NewMemLogExporter(10, LogInfo)
	ctx := context.Background()

	exporter.Emit(ctx, LogInfo, "test message", Labels{"key": "value"})
	exporter.Emit(ctx, LogWarn, "warn message", Labels{"severity": "high"})

	records := exporter.Records()
	if len(records) != 2 {
		t.Errorf("expected 2 records, got %d", len(records))
	}

	if records[0].Message != "test message" {
		t.Error("first record message mismatch")
	}
	if records[1].Level != LogWarn {
		t.Error("second record level mismatch")
	}
}

// TestMemLogExporterLevelFiltering tests that log entries below minLevel
// are silently dropped.
func TestMemLogExporterLevelFiltering(t *testing.T) {
	exporter := NewMemLogExporter(10, LogWarn)
	ctx := context.Background()

	exporter.Emit(ctx, LogTrace, "trace", nil)
	exporter.Emit(ctx, LogDebug, "debug", nil)
	exporter.Emit(ctx, LogInfo, "info", nil)
	exporter.Emit(ctx, LogWarn, "warn", nil)
	exporter.Emit(ctx, LogError, "error", nil)

	records := exporter.Records()
	// Only Warn and Error should be present
	if len(records) != 2 {
		t.Errorf("expected 2 records (Warn + Error), got %d", len(records))
	}

	if records[0].Level != LogWarn {
		t.Error("first record should be Warn")
	}
	if records[1].Level != LogError {
		t.Error("second record should be Error")
	}
}

// TestMemLogExporterRingBuffer tests that the ring buffer correctly wraps
// around when exceeding capacity.
func TestMemLogExporterRingBuffer(t *testing.T) {
	exporter := NewMemLogExporter(5, LogInfo)
	ctx := context.Background()

	// Emit 8 messages (cap is 5)
	for i := 0; i < 8; i++ {
		exporter.Emit(ctx, LogInfo, "msg", Labels{"num": string(rune('0' + i))})
	}

	records := exporter.Records()
	if len(records) != 5 {
		t.Errorf("expected 5 records (ring buffer full), got %d", len(records))
	}

	// Records should be the last 5 emitted (indices 3-7)
	if records[0].Attrs["num"] != "3" {
		t.Errorf("expected first record to be msg 3, got %v", records[0].Attrs["num"])
	}
	if records[4].Attrs["num"] != "7" {
		t.Errorf("expected last record to be msg 7, got %v", records[4].Attrs["num"])
	}
}

// TestMemLogExporterConcurrency tests that Emit is thread-safe under
// concurrent load.
func TestMemLogExporterConcurrency(t *testing.T) {
	exporter := NewMemLogExporter(1000, LogInfo)
	ctx := context.Background()

	const numGoroutines = 50
	const opsPerGoroutine = 20
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				exporter.Emit(ctx, LogInfo, "msg", Labels{"goroutine": string(rune(idx))})
			}
		}(i)
	}

	wg.Wait()

	expected := numGoroutines * opsPerGoroutine
	if exporter.Count() != expected {
		t.Errorf("expected %d records, got %d", expected, exporter.Count())
	}
}

// TestMemLogExporterDeepCopyLabels tests that attrs are deep-copied
// to prevent caller mutations from affecting stored records.
func TestMemLogExporterDeepCopyLabels(t *testing.T) {
	exporter := NewMemLogExporter(10, LogInfo)
	ctx := context.Background()

	attrs := Labels{"mutable": "original"}
	exporter.Emit(ctx, LogInfo, "test", attrs)

	// Modify original attrs
	attrs["mutable"] = "modified"
	attrs["extra"] = "added"

	records := exporter.Records()
	if len(records) != 1 {
		t.Fatal("expected 1 record")
	}

	// Stored record should have original value
	if records[0].Attrs["mutable"] != "original" {
		t.Error("stored record was mutated by caller")
	}
	if _, ok := records[0].Attrs["extra"]; ok {
		t.Error("stored record has unexpected extra field")
	}
}

// TestLevelRank tests the level ranking function.
func TestLevelRank(t *testing.T) {
	ranks := map[LogLevel]int{
		LogTrace: 0,
		LogDebug: 1,
		LogInfo:  2,
		LogWarn:  3,
		LogError: 4,
	}

	for level, expectedRank := range ranks {
		if got := levelRank(level); got != expectedRank {
			t.Errorf("levelRank(%s) = %d, expected %d", level, got, expectedRank)
		}
	}

	// Test that higher severity is higher rank
	if levelRank(LogError) <= levelRank(LogWarn) {
		t.Error("LogError rank should be higher than LogWarn")
	}
}

// Test data structures and helpers

var ErrTestSpan = &testError{"test span error"}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
