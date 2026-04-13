package observability

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ── OTLPTraceExporter tests ─────────────────────────────────────────────

func TestOTLPTraceExporterStartSpanAndFlush(t *testing.T) {
	var mu sync.Mutex
	var exported []SpanData

	sender := func(ctx context.Context, batch []SpanData) error {
		mu.Lock()
		exported = append(exported, batch...)
		mu.Unlock()
		return nil
	}

	e := NewOTLPTraceExporter(sender)
	ctx := context.Background()

	ctx2, span := e.StartSpan(ctx, "test-span", Labels{"foo": "bar"})
	_ = ctx2
	span.End()

	if e.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", e.Pending())
	}

	if err := e.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(exported) != 1 {
		t.Fatalf("exported = %d, want 1", len(exported))
	}
	if exported[0].Name != "test-span" {
		t.Errorf("Name = %q, want %q", exported[0].Name, "test-span")
	}
	if exported[0].StatusCode != "OK" {
		t.Errorf("StatusCode = %q, want %q", exported[0].StatusCode, "OK")
	}
}

func TestOTLPTraceExporterErrorSpan(t *testing.T) {
	var exported []SpanData
	sender := func(ctx context.Context, batch []SpanData) error {
		exported = append(exported, batch...)
		return nil
	}

	e := NewOTLPTraceExporter(sender)
	_, span := e.StartSpan(context.Background(), "err-span", nil)
	span.SetError(errors.New("boom"))
	span.End()

	_ = e.Flush(context.Background())
	if len(exported) != 1 {
		t.Fatalf("exported = %d, want 1", len(exported))
	}
	if exported[0].StatusCode != "ERROR" {
		t.Errorf("StatusCode = %q, want %q", exported[0].StatusCode, "ERROR")
	}
	if exported[0].StatusMsg != "boom" {
		t.Errorf("StatusMsg = %q, want %q", exported[0].StatusMsg, "boom")
	}
}

func TestOTLPTraceExporterAutoFlush(t *testing.T) {
	var mu sync.Mutex
	var flushed int

	sender := func(ctx context.Context, batch []SpanData) error {
		mu.Lock()
		flushed += len(batch)
		mu.Unlock()
		return nil
	}

	e := NewOTLPTraceExporter(sender, WithTraceBatchSize(3))

	for i := 0; i < 3; i++ {
		_, span := e.StartSpan(context.Background(), "span", nil)
		span.End()
	}

	mu.Lock()
	defer mu.Unlock()
	if flushed < 3 {
		t.Errorf("auto-flush: flushed = %d, want >= 3", flushed)
	}
}

func TestOTLPTraceExporterNilSender(t *testing.T) {
	e := NewOTLPTraceExporter(nil)
	_, span := e.StartSpan(context.Background(), "noop", nil)
	span.End()

	if err := e.Flush(context.Background()); err != nil {
		t.Fatalf("Flush with nil sender: %v", err)
	}
}

func TestOTLPTraceExporterFlushEmpty(t *testing.T) {
	called := false
	sender := func(ctx context.Context, batch []SpanData) error {
		called = true
		return nil
	}
	e := NewOTLPTraceExporter(sender)
	if err := e.Flush(context.Background()); err != nil {
		t.Fatalf("Flush empty: %v", err)
	}
	if called {
		t.Error("sender should not be called for empty flush")
	}
}

// ── OTLPLogExporter tests ───────────────────────────────────────────────

func TestOTLPLogExporterEmitAndFlush(t *testing.T) {
	var exported []LogData
	sender := func(ctx context.Context, batch []LogData) error {
		exported = append(exported, batch...)
		return nil
	}

	e := NewOTLPLogExporter(sender, 128, LogInfo)
	e.Emit(context.Background(), LogInfo, "hello", Labels{"key": "val"})

	if e.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", e.Pending())
	}

	if err := e.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(exported) != 1 {
		t.Fatalf("exported = %d, want 1", len(exported))
	}
	if exported[0].Message != "hello" {
		t.Errorf("Message = %q, want %q", exported[0].Message, "hello")
	}
}

func TestOTLPLogExporterWithSanitizer(t *testing.T) {
	var exported []LogData
	sender := func(ctx context.Context, batch []LogData) error {
		exported = append(exported, batch...)
		return nil
	}

	sanitizer := NewPatternSanitizer()
	e := NewOTLPLogExporter(sender, 128, LogInfo, WithLogSanitizer(sanitizer))

	e.Emit(context.Background(), LogInfo, "key is sk-ant-abcdefghijklmnopqrstuvwxyz", Labels{
		"api_key": "secret-value",
		"safe":    "visible",
	})

	_ = e.Flush(context.Background())
	if len(exported) != 1 {
		t.Fatalf("exported = %d, want 1", len(exported))
	}
	if exported[0].Attrs["api_key"] != "[REDACTED]" {
		t.Errorf("api_key not redacted: %q", exported[0].Attrs["api_key"])
	}
	if exported[0].Attrs["safe"] != "visible" {
		t.Errorf("safe attr changed: %q", exported[0].Attrs["safe"])
	}
	if exported[0].Message == "key is sk-ant-abcdefghijklmnopqrstuvwxyz" {
		t.Error("message should be sanitized but was not")
	}
}

func TestOTLPLogExporterRecords(t *testing.T) {
	e := NewOTLPLogExporter(nil, 128, LogInfo)
	e.Emit(context.Background(), LogInfo, "test", nil)

	recs := e.Records()
	if len(recs) != 1 {
		t.Fatalf("Records = %d, want 1", len(recs))
	}
}

// ── OTLPMetricsExporter tests ───────────────────────────────────────────

func TestOTLPMetricsExporterCounterAndFlush(t *testing.T) {
	var exported []MetricData
	sender := func(ctx context.Context, batch []MetricData) error {
		exported = append(exported, batch...)
		return nil
	}

	e := NewOTLPMetricsExporter(sender)
	c := e.Counter("test_counter", Labels{"env": "test"})
	c.Inc()
	c.Add(4)

	if err := e.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(exported) == 0 {
		t.Fatal("expected at least 1 metric")
	}
}

func TestOTLPMetricsExporterGaugeAndHistogram(t *testing.T) {
	e := NewOTLPMetricsExporter(nil)
	g := e.Gauge("test_gauge", nil)
	g.Set(42)
	g.Add(-2)

	h := e.Histogram("test_hist", nil, []float64{10, 50, 100})
	h.Observe(25)

	// Just verify no panics — nil sender means flush is a no-op.
	if err := e.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

// ── PatternSanitizer tests ──────────────────────────────────────────────

func TestPatternSanitizerMessage(t *testing.T) {
	s := NewPatternSanitizer()

	cases := []struct {
		name  string
		input string
		want  string // substring that should NOT appear
	}{
		{"anthropic key", "using sk-ant-abcdefghijklmnopqrstuvwxyz", "sk-ant-"},
		{"openai key", "key: sk-1234567890abcdefghijklmn", "sk-123456"},
		{"bearer token", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9", "eyJhbGci"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := s.SanitizeMessage(tc.input)
			if result == tc.input {
				t.Errorf("message was not sanitized: %q", result)
			}
		})
	}
}

func TestPatternSanitizerAttrs(t *testing.T) {
	s := NewPatternSanitizer()

	attrs := Labels{
		"api_key":       "sk-ant-secret123456789012345678",
		"authorization": "Bearer token123",
		"password":      "hunter2",
		"safe_field":    "visible value",
		"x-custom-token": "should-redact",
	}

	result := s.SanitizeAttrs(attrs)

	if result["api_key"] != "[REDACTED]" {
		t.Errorf("api_key = %q, want [REDACTED]", result["api_key"])
	}
	if result["authorization"] != "[REDACTED]" {
		t.Errorf("authorization = %q, want [REDACTED]", result["authorization"])
	}
	if result["password"] != "[REDACTED]" {
		t.Errorf("password = %q, want [REDACTED]", result["password"])
	}
	if result["safe_field"] != "visible value" {
		t.Errorf("safe_field = %q, want %q", result["safe_field"], "visible value")
	}
	if result["x-custom-token"] != "[REDACTED]" {
		t.Errorf("x-custom-token = %q, want [REDACTED]", result["x-custom-token"])
	}

	// Original must not be mutated.
	if attrs["api_key"] == "[REDACTED]" {
		t.Error("original attrs were mutated")
	}
}

func TestPatternSanitizerNilAttrs(t *testing.T) {
	s := NewPatternSanitizer()
	result := s.SanitizeAttrs(nil)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestPatternSanitizerExtraKeys(t *testing.T) {
	s := NewPatternSanitizer(WithExtraSensitiveKeys("custom_secret"))

	attrs := Labels{"custom_secret": "value"}
	result := s.SanitizeAttrs(attrs)
	if result["custom_secret"] != "[REDACTED]" {
		t.Errorf("custom_secret = %q, want [REDACTED]", result["custom_secret"])
	}
}
