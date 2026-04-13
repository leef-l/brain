// Package observability — otlp_exporter.go provides pluggable OTLP-style
// exporters that wrap the existing TraceExporter, LogExporter, and Registry
// interfaces with protocol-level serialization and batching.
//
// These exporters are wire-protocol-agnostic stubs that accept a Sender
// callback — the actual HTTP/gRPC transport is injected by the user's
// OTel SDK wiring. This keeps the brain package stdlib-only while
// supporting full OTel interop. See 24-可观测性.md §3.
package observability

import (
	"context"
	"sync"
	"time"
)

// ── OTLP Trace Exporter ─────────────────────────────────────────────────

// SpanData is the exported representation of a finished span, suitable
// for serialization to OTLP/JSON or any other wire format.
type SpanData struct {
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Name       string            `json:"name"`
	StartTime  time.Time         `json:"start_time"`
	EndTime    time.Time         `json:"end_time"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	StatusCode string            `json:"status_code"` // "OK", "ERROR", "UNSET"
	StatusMsg  string            `json:"status_msg,omitempty"`
}

// SpanSender is the callback that transports finished spans to a backend
// (e.g. OTel Collector, Jaeger, stdout). Implementations MUST be safe for
// concurrent calls.
type SpanSender func(ctx context.Context, batch []SpanData) error

// OTLPTraceExporter wraps the MemTraceExporter with batched export
// capability. Spans are collected in-memory and flushed via the SpanSender
// on demand or when the batch size threshold is reached.
type OTLPTraceExporter struct {
	mu        sync.Mutex
	inner     *MemTraceExporter
	sender    SpanSender
	batchSize int
	pending   []SpanData
}

// OTLPTraceOption configures the OTLP trace exporter.
type OTLPTraceOption func(*OTLPTraceExporter)

// WithTraceBatchSize sets the number of spans buffered before auto-flush.
// Default is 64.
func WithTraceBatchSize(n int) OTLPTraceOption {
	return func(e *OTLPTraceExporter) {
		if n > 0 {
			e.batchSize = n
		}
	}
}

// NewOTLPTraceExporter creates a trace exporter that buffers spans and
// flushes them via sender. Pass nil sender for a no-op exporter (useful
// for testing the batching logic).
func NewOTLPTraceExporter(sender SpanSender, opts ...OTLPTraceOption) *OTLPTraceExporter {
	e := &OTLPTraceExporter{
		inner:     NewMemTraceExporter(),
		sender:    sender,
		batchSize: 64,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// StartSpan delegates to the inner MemTraceExporter and records the span
// for later export.
func (e *OTLPTraceExporter) StartSpan(ctx context.Context, name string, attrs Labels) (context.Context, Span) {
	newCtx, span := e.inner.StartSpan(ctx, name, attrs)
	return newCtx, &otlpSpanWrapper{inner: span, exporter: e}
}

// Flush exports all pending spans via the sender. Safe for concurrent use.
func (e *OTLPTraceExporter) Flush(ctx context.Context) error {
	e.mu.Lock()
	if len(e.pending) == 0 {
		e.mu.Unlock()
		return nil
	}
	batch := e.pending
	e.pending = nil
	e.mu.Unlock()

	if e.sender == nil {
		return nil
	}
	return e.sender(ctx, batch)
}

// Pending returns the number of unflushed spans. Test helper.
func (e *OTLPTraceExporter) Pending() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pending)
}

func (e *OTLPTraceExporter) enqueue(sd SpanData) {
	e.mu.Lock()
	e.pending = append(e.pending, sd)
	shouldFlush := len(e.pending) >= e.batchSize
	e.mu.Unlock()

	if shouldFlush && e.sender != nil {
		_ = e.Flush(context.Background())
	}
}

// otlpSpanWrapper wraps a MemSpan and enqueues SpanData on End().
type otlpSpanWrapper struct {
	inner    Span
	exporter *OTLPTraceExporter
	mu       sync.Mutex
	ended    bool
	errMsg   string
	attrs    map[string]string
}

func (w *otlpSpanWrapper) SetAttr(key, value string) {
	w.mu.Lock()
	if w.attrs == nil {
		w.attrs = make(map[string]string)
	}
	w.attrs[key] = value
	w.mu.Unlock()
	w.inner.SetAttr(key, value)
}

func (w *otlpSpanWrapper) SetError(err error) {
	if err != nil {
		w.mu.Lock()
		w.errMsg = err.Error()
		w.mu.Unlock()
	}
	w.inner.SetError(err)
}

func (w *otlpSpanWrapper) End() {
	w.mu.Lock()
	if w.ended {
		w.mu.Unlock()
		return
	}
	w.ended = true
	errMsg := w.errMsg
	attrs := make(map[string]string, len(w.attrs))
	for k, v := range w.attrs {
		attrs[k] = v
	}
	w.mu.Unlock()

	w.inner.End()

	// Build SpanData from the inner span snapshot.
	spans := w.exporter.inner.Spans()
	if len(spans) > 0 {
		last := spans[len(spans)-1]
		status := "OK"
		if errMsg != "" {
			status = "ERROR"
		}
		sd := SpanData{
			TraceID:    last.TraceID,
			SpanID:     last.SpanID,
			ParentID:   last.ParentID,
			Name:       last.Name,
			StartTime:  last.StartedAt,
			EndTime:    last.EndedAt,
			Attrs:      mergeAttrs(last.Attrs, attrs),
			StatusCode: status,
			StatusMsg:  errMsg,
		}
		w.exporter.enqueue(sd)
	}
}

func mergeAttrs(base, extra Labels) map[string]string {
	result := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
}

// ── OTLP Log Exporter ───────────────────────────────────────────────────

// LogData is the exported representation of a log record.
type LogData struct {
	Timestamp time.Time         `json:"timestamp"`
	Level     LogLevel          `json:"level"`
	Message   string            `json:"message"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

// LogSender transports log batches to a backend.
type LogSender func(ctx context.Context, batch []LogData) error

// OTLPLogExporter wraps MemLogExporter with batched export and optional
// log sanitization.
type OTLPLogExporter struct {
	mu        sync.Mutex
	inner     *MemLogExporter
	sender    LogSender
	sanitizer LogSanitizer
	batchSize int
	pending   []LogData
}

// OTLPLogOption configures the OTLP log exporter.
type OTLPLogOption func(*OTLPLogExporter)

// WithLogBatchSize sets the log batch threshold.
func WithLogBatchSize(n int) OTLPLogOption {
	return func(e *OTLPLogExporter) {
		if n > 0 {
			e.batchSize = n
		}
	}
}

// WithLogSanitizer installs a sanitizer that redacts sensitive data before
// export. See 24-可观测性.md §6.4.
func WithLogSanitizer(s LogSanitizer) OTLPLogOption {
	return func(e *OTLPLogExporter) {
		e.sanitizer = s
	}
}

// NewOTLPLogExporter creates a log exporter with batching and optional
// sanitization.
func NewOTLPLogExporter(sender LogSender, capacity int, minLevel LogLevel, opts ...OTLPLogOption) *OTLPLogExporter {
	e := &OTLPLogExporter{
		inner:     NewMemLogExporter(capacity, minLevel),
		sender:    sender,
		batchSize: 64,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Emit records a log entry, applies sanitization, and buffers for export.
func (e *OTLPLogExporter) Emit(ctx context.Context, level LogLevel, msg string, attrs Labels) {
	// Apply sanitization before storing or exporting.
	if e.sanitizer != nil {
		msg = e.sanitizer.SanitizeMessage(msg)
		attrs = e.sanitizer.SanitizeAttrs(attrs)
	}

	e.inner.Emit(ctx, level, msg, attrs)

	ld := LogData{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Message:   msg,
		Attrs:     cloneLabels(attrs),
	}

	e.mu.Lock()
	e.pending = append(e.pending, ld)
	shouldFlush := len(e.pending) >= e.batchSize
	e.mu.Unlock()

	if shouldFlush && e.sender != nil {
		_ = e.Flush(ctx)
	}
}

// Flush exports all pending logs via the sender.
func (e *OTLPLogExporter) Flush(ctx context.Context) error {
	e.mu.Lock()
	if len(e.pending) == 0 {
		e.mu.Unlock()
		return nil
	}
	batch := e.pending
	e.pending = nil
	e.mu.Unlock()

	if e.sender == nil {
		return nil
	}
	return e.sender(ctx, batch)
}

// Pending returns the number of unflushed log records. Test helper.
func (e *OTLPLogExporter) Pending() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pending)
}

// Records delegates to the inner MemLogExporter for test inspection.
func (e *OTLPLogExporter) Records() []LogRecord {
	return e.inner.Records()
}

func cloneLabels(l Labels) map[string]string {
	if l == nil {
		return nil
	}
	cp := make(map[string]string, len(l))
	for k, v := range l {
		cp[k] = v
	}
	return cp
}

// ── OTLP Metrics Exporter ───────────────────────────────────────────────

// MetricData is a single exported metric snapshot.
type MetricData struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"` // "counter", "histogram", "gauge"
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value,omitempty"`   // counter/gauge current value
	Values []float64         `json:"values,omitempty"`  // histogram observations
	Bucket []float64         `json:"buckets,omitempty"` // histogram bucket boundaries
}

// MetricSender transports metric snapshots to a backend.
type MetricSender func(ctx context.Context, batch []MetricData) error

// OTLPMetricsExporter wraps MemRegistry with periodic export capability.
type OTLPMetricsExporter struct {
	inner  *MemRegistry
	sender MetricSender
}

// NewOTLPMetricsExporter creates a metrics exporter backed by MemRegistry.
func NewOTLPMetricsExporter(sender MetricSender) *OTLPMetricsExporter {
	return &OTLPMetricsExporter{
		inner:  NewMemRegistry(),
		sender: sender,
	}
}

// Counter delegates to inner MemRegistry.
func (e *OTLPMetricsExporter) Counter(name string, labels Labels) Counter {
	return e.inner.Counter(name, labels)
}

// Histogram delegates to inner MemRegistry.
func (e *OTLPMetricsExporter) Histogram(name string, labels Labels, buckets []float64) Histogram {
	return e.inner.Histogram(name, labels, buckets)
}

// Gauge delegates to inner MemRegistry.
func (e *OTLPMetricsExporter) Gauge(name string, labels Labels) Gauge {
	return e.inner.Gauge(name, labels)
}

// Flush exports a snapshot of all registered metrics via the sender.
func (e *OTLPMetricsExporter) Flush(ctx context.Context) error {
	if e.sender == nil {
		return nil
	}
	snapshot := e.inner.Snapshot()
	if len(snapshot) == 0 {
		return nil
	}

	batch := make([]MetricData, 0, len(snapshot))
	for key, val := range snapshot {
		// Snapshot keys are "type|name|labels" with float64 values.
		md := MetricData{
			Name:  key,
			Type:  "metric",
			Value: val,
		}
		batch = append(batch, md)
	}
	return e.sender(ctx, batch)
}

// ── Interface assertions ────────────────────────────────────────────────

var (
	_ TraceExporter = (*OTLPTraceExporter)(nil)
	_ LogExporter   = (*OTLPLogExporter)(nil)
	_ Registry      = (*OTLPMetricsExporter)(nil)
)
