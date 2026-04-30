package kernel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// MetricType 表示可观测指标的类型。
type MetricType string

const (
	MetricCounter   MetricType = "counter"
	MetricGauge     MetricType = "gauge"
	MetricHistogram MetricType = "histogram"
)

// ObservMetric 表示一条可观测指标数据点。
type ObservMetric struct {
	Name      string            `json:"name"`
	Type      MetricType        `json:"type"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// LogEntry 表示一条结构化日志记录。
type LogEntry struct {
	Level     string                 `json:"level"`     // debug/info/warn/error
	Message   string                 `json:"message"`
	Component string                 `json:"component"` // 来源组件
	Fields    map[string]interface{} `json:"fields,omitempty"`
	TraceID   string                 `json:"trace_id,omitempty"`
	SpanID    string                 `json:"span_id,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// SpanEvent 表示 span 生命周期内的一个事件。
type SpanEvent struct {
	Name      string            `json:"name"`
	Timestamp time.Time         `json:"timestamp"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// TraceSpan 表示一个分布式追踪 span。
type TraceSpan struct {
	TraceID   string            `json:"trace_id"`
	SpanID    string            `json:"span_id"`
	ParentID  string            `json:"parent_id,omitempty"`
	Operation string            `json:"operation"`
	Component string            `json:"component"`
	StartTime time.Time         `json:"start_time"`
	EndTime   *time.Time        `json:"end_time,omitempty"`
	Duration  time.Duration     `json:"duration,omitempty"`
	Status    string            `json:"status"` // ok/error
	Tags      map[string]string `json:"tags,omitempty"`
	Events    []SpanEvent       `json:"events,omitempty"`
}

// ObservabilityProvider 定义可观测数据的后端接口，可替换为 Prometheus、OTLP 等。
type ObservabilityProvider interface {
	EmitMetric(metric ObservMetric)
	EmitLog(entry LogEntry)
	EmitSpan(span TraceSpan)
	Flush(ctx context.Context) error
}

const defaultBufferMax = 10000

// ObservabilityHub 聚合指标、日志和 span，分发到已注册的 provider。
type ObservabilityHub struct {
	mu        sync.RWMutex
	providers []ObservabilityProvider
	metrics   []ObservMetric
	logs      []LogEntry
	spans     []TraceSpan
	bufferMax int
}

// NewObservabilityHub 创建观测中枢。
func NewObservabilityHub() *ObservabilityHub {
	return &ObservabilityHub{bufferMax: defaultBufferMax}
}

// AddProvider 注册一个观测后端。
func (h *ObservabilityHub) AddProvider(provider ObservabilityProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.providers = append(h.providers, provider)
}

// Counter 记录一个计数器指标。
func (h *ObservabilityHub) Counter(name string, value float64, labels map[string]string) {
	h.emitMetric(name, MetricCounter, value, labels)
}

// Gauge 记录一个量表指标。
func (h *ObservabilityHub) Gauge(name string, value float64, labels map[string]string) {
	h.emitMetric(name, MetricGauge, value, labels)
}

// Histogram 记录一个直方图指标。
func (h *ObservabilityHub) Histogram(name string, value float64, labels map[string]string) {
	h.emitMetric(name, MetricHistogram, value, labels)
}

func (h *ObservabilityHub) emitMetric(name string, typ MetricType, value float64, labels map[string]string) {
	m := ObservMetric{
		Name: name, Type: typ, Value: value,
		Labels: labels, Timestamp: time.Now(),
	}
	h.mu.Lock()
	if len(h.metrics) < h.bufferMax {
		h.metrics = append(h.metrics, m)
	}
	providers := make([]ObservabilityProvider, len(h.providers))
	copy(providers, h.providers)
	h.mu.Unlock()
	for _, p := range providers {
		p.EmitMetric(m)
	}
}

// Log 记录一条结构化日志。
func (h *ObservabilityHub) Log(level, component, message string, fields map[string]interface{}) {
	entry := LogEntry{
		Level: level, Message: message, Component: component,
		Fields: fields, Timestamp: time.Now(),
	}
	h.mu.Lock()
	if len(h.logs) < h.bufferMax {
		h.logs = append(h.logs, entry)
	}
	providers := make([]ObservabilityProvider, len(h.providers))
	copy(providers, h.providers)
	h.mu.Unlock()
	for _, p := range providers {
		p.EmitLog(entry)
	}
}

// StartSpan 开始一个新的追踪 span。
func (h *ObservabilityHub) StartSpan(traceID, operation, component string) *TraceSpan {
	return &TraceSpan{
		TraceID: traceID, SpanID: randomID(),
		Operation: operation, Component: component,
		StartTime: time.Now(), Status: "ok",
	}
}

// EndSpan 结束一个 span 并将其写入缓冲和 provider。
func (h *ObservabilityHub) EndSpan(span *TraceSpan, status string) {
	now := time.Now()
	span.EndTime = &now
	span.Duration = now.Sub(span.StartTime)
	span.Status = status
	h.mu.Lock()
	if len(h.spans) < h.bufferMax {
		h.spans = append(h.spans, *span)
	}
	providers := make([]ObservabilityProvider, len(h.providers))
	copy(providers, h.providers)
	h.mu.Unlock()
	for _, p := range providers {
		p.EmitSpan(*span)
	}
}

// ChildSpan 在给定父 span 下创建子 span，继承 traceID 和 component。
func (h *ObservabilityHub) ChildSpan(parent *TraceSpan, operation string) *TraceSpan {
	return &TraceSpan{
		TraceID: parent.TraceID, SpanID: randomID(),
		ParentID: parent.SpanID, Operation: operation,
		Component: parent.Component, StartTime: time.Now(),
		Status: "ok",
	}
}

// Flush 将所有缓冲数据刷新到已注册的 provider。
func (h *ObservabilityHub) Flush(ctx context.Context) error {
	h.mu.RLock()
	providers := make([]ObservabilityProvider, len(h.providers))
	copy(providers, h.providers)
	h.mu.RUnlock()
	for _, p := range providers {
		if err := p.Flush(ctx); err != nil {
			return err
		}
	}
	return nil
}

// GetMetrics 返回缓冲中的所有指标快照。
func (h *ObservabilityHub) GetMetrics() []ObservMetric {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]ObservMetric, len(h.metrics))
	copy(out, h.metrics)
	return out
}

// GetLogs 按级别过滤日志。若 level 为空则返回全部。
func (h *ObservabilityHub) GetLogs(level string) []LogEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if level == "" {
		out := make([]LogEntry, len(h.logs))
		copy(out, h.logs)
		return out
	}
	var out []LogEntry
	for _, e := range h.logs {
		if e.Level == level {
			out = append(out, e)
		}
	}
	return out
}

// GetSpans 返回指定 traceID 的所有 span。若 traceID 为空则返回全部。
func (h *ObservabilityHub) GetSpans(traceID string) []TraceSpan {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if traceID == "" {
		out := make([]TraceSpan, len(h.spans))
		copy(out, h.spans)
		return out
	}
	var out []TraceSpan
	for _, s := range h.spans {
		if s.TraceID == traceID {
			out = append(out, s)
		}
	}
	return out
}

// MemObservabilityProvider 将所有观测数据存储在内存中，用于开发和测试。
type MemObservabilityProvider struct {
	mu      sync.Mutex
	Metrics []ObservMetric
	Logs    []LogEntry
	Spans   []TraceSpan
}

// NewMemObservabilityProvider 创建内存观测后端。
func NewMemObservabilityProvider() *MemObservabilityProvider {
	return &MemObservabilityProvider{}
}

// EmitMetric 存储一条指标。
func (m *MemObservabilityProvider) EmitMetric(metric ObservMetric) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Metrics = append(m.Metrics, metric)
}

// EmitLog 存储一条日志。
func (m *MemObservabilityProvider) EmitLog(entry LogEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Logs = append(m.Logs, entry)
}

// EmitSpan 存储一个 span。
func (m *MemObservabilityProvider) EmitSpan(span TraceSpan) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Spans = append(m.Spans, span)
}

// Flush 内存后端无需刷新，直接返回 nil。
func (m *MemObservabilityProvider) Flush(_ context.Context) error {
	return nil
}

// randomID 生成 8 字节 hex ID（16 字符）。
func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
