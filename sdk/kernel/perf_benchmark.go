// perf_benchmark.go — 性能基准框架（Performance Benchmark）
//
// MACCS Wave 6 生产级硬化：延迟追踪、吞吐量统计、基准报告。
// PerfCollector 收集运行时性能指标，生成包含百分位数和标准差的统计报告。
package kernel

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// PerfMetric — 性能指标
// ---------------------------------------------------------------------------

// PerfMetric 表示单次性能采样。Name 可为 llm_call / tool_exec / brain_delegate / phase_duration 等。
type PerfMetric struct {
	Name      string            `json:"name"`
	Value     time.Duration     `json:"value"`
	Tags      map[string]string `json:"tags,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// LatencyStats — 延迟统计
// ---------------------------------------------------------------------------

// LatencyStats 汇总某一指标的延迟分布，包含百分位数和标准差。
type LatencyStats struct {
	Name   string        `json:"name"`
	Count  int           `json:"count"`
	Min    time.Duration `json:"min"`
	Max    time.Duration `json:"max"`
	Avg    time.Duration `json:"avg"`
	P50    time.Duration `json:"p50"`
	P95    time.Duration `json:"p95"`
	P99    time.Duration `json:"p99"`
	StdDev time.Duration `json:"std_dev"`
}

// ---------------------------------------------------------------------------
// ThroughputStats — 吞吐量统计
// ---------------------------------------------------------------------------

// ThroughputStats 描述某一指标在时间窗口内的吞吐量。
type ThroughputStats struct {
	Name         string  `json:"name"`
	TotalOps     int     `json:"total_ops"`
	WindowSec    float64 `json:"window_sec"`
	OpsPerSecond float64 `json:"ops_per_second"`
}

// ---------------------------------------------------------------------------
// BenchmarkReport — 基准报告
// ---------------------------------------------------------------------------

// BenchmarkReport 是一次完整的性能基准报告。
type BenchmarkReport struct {
	ReportID    string            `json:"report_id"`
	Latencies   []LatencyStats    `json:"latencies"`
	Throughputs []ThroughputStats `json:"throughputs"`
	StartedAt   time.Time         `json:"started_at"`
	EndedAt     time.Time         `json:"ended_at"`
	Duration    time.Duration     `json:"duration"`
	Summary     string            `json:"summary"`
}

// ---------------------------------------------------------------------------
// PerfTimer — 计时器
// ---------------------------------------------------------------------------

// PerfTimer 提供 Start/Stop 计时，Stop 时自动将结果 Record 到 PerfCollector。
type PerfTimer struct {
	collector *PerfCollector
	name      string
	tags      map[string]string
	startTime time.Time
}

// Stop 停止计时并自动 Record 到关联的 PerfCollector，返回经过的时长。
func (t *PerfTimer) Stop() time.Duration {
	elapsed := time.Since(t.startTime)
	t.collector.Record(t.name, elapsed, t.tags)
	return elapsed
}

// ---------------------------------------------------------------------------
// PerfCollector — 性能收集器
// ---------------------------------------------------------------------------

// PerfCollector 收集运行时性能指标，线程安全。
type PerfCollector struct {
	mu        sync.RWMutex
	metrics   map[string][]PerfMetric
	startTime time.Time
}

// NewPerfCollector 创建并返回一个新的 PerfCollector。
func NewPerfCollector() *PerfCollector {
	return &PerfCollector{
		metrics:   make(map[string][]PerfMetric),
		startTime: time.Now(),
	}
}

// Record 记录一个性能指标。
func (pc *PerfCollector) Record(name string, value time.Duration, tags map[string]string) {
	m := PerfMetric{
		Name:      name,
		Value:     value,
		Tags:      tags,
		Timestamp: time.Now(),
	}
	pc.mu.Lock()
	pc.metrics[name] = append(pc.metrics[name], m)
	pc.mu.Unlock()
}

// StartTimer 开始计时，返回 PerfTimer；调用 PerfTimer.Stop() 时自动 Record。
func (pc *PerfCollector) StartTimer(name string, tags map[string]string) *PerfTimer {
	return &PerfTimer{
		collector: pc,
		name:      name,
		tags:      tags,
		startTime: time.Now(),
	}
}

// GetLatencyStats 计算指定指标的延迟统计（含百分位数和标准差）。
// 若无数据返回 nil。
func (pc *PerfCollector) GetLatencyStats(name string) *LatencyStats {
	pc.mu.RLock()
	raw, ok := pc.metrics[name]
	if !ok || len(raw) == 0 {
		pc.mu.RUnlock()
		return nil
	}
	// 复制一份，避免长时间持锁
	values := make([]time.Duration, len(raw))
	for i, m := range raw {
		values[i] = m.Value
	}
	pc.mu.RUnlock()

	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })

	n := len(values)
	var total time.Duration
	for _, v := range values {
		total += v
	}
	avg := total / time.Duration(n)

	return &LatencyStats{
		Name:   name,
		Count:  n,
		Min:    values[0],
		Max:    values[n-1],
		Avg:    avg,
		P50:    percentile(values, 0.50),
		P95:    percentile(values, 0.95),
		P99:    percentile(values, 0.99),
		StdDev: stdDev(values, avg),
	}
}

// GetThroughput 计算指定指标从首次采样到末次采样的吞吐量。
// 若无数据返回 nil。
func (pc *PerfCollector) GetThroughput(name string) *ThroughputStats {
	pc.mu.RLock()
	raw, ok := pc.metrics[name]
	if !ok || len(raw) == 0 {
		pc.mu.RUnlock()
		return nil
	}
	n := len(raw)
	earliest := raw[0].Timestamp
	latest := raw[n-1].Timestamp
	for _, m := range raw {
		if m.Timestamp.Before(earliest) {
			earliest = m.Timestamp
		}
		if m.Timestamp.After(latest) {
			latest = m.Timestamp
		}
	}
	pc.mu.RUnlock()

	window := latest.Sub(earliest).Seconds()
	if window <= 0 {
		window = 1 // 避免除零；单点采样视为 1 秒窗口
	}

	return &ThroughputStats{
		Name:         name,
		TotalOps:     n,
		WindowSec:    window,
		OpsPerSecond: float64(n) / window,
	}
}

// GenerateReport 生成完整基准报告，覆盖所有已收集的指标名称。
func (pc *PerfCollector) GenerateReport() *BenchmarkReport {
	pc.mu.RLock()
	names := make([]string, 0, len(pc.metrics))
	for name := range pc.metrics {
		names = append(names, name)
	}
	pc.mu.RUnlock()

	sort.Strings(names)

	now := time.Now()
	report := &BenchmarkReport{
		ReportID:  fmt.Sprintf("bench-%d", now.UnixNano()),
		StartedAt: pc.startTime,
		EndedAt:   now,
		Duration:  now.Sub(pc.startTime),
	}

	totalMetrics := 0
	for _, name := range names {
		if ls := pc.GetLatencyStats(name); ls != nil {
			report.Latencies = append(report.Latencies, *ls)
			totalMetrics += ls.Count
		}
		if ts := pc.GetThroughput(name); ts != nil {
			report.Throughputs = append(report.Throughputs, *ts)
		}
	}

	report.Summary = fmt.Sprintf("%d metrics across %d categories in %s",
		totalMetrics, len(names), report.Duration.Round(time.Millisecond))

	return report
}

// Reset 重置收集器，清空所有指标。
func (pc *PerfCollector) Reset() {
	pc.mu.Lock()
	pc.metrics = make(map[string][]PerfMetric)
	pc.startTime = time.Now()
	pc.mu.Unlock()
}

// MetricCount 返回所有指标的总采样数。
func (pc *PerfCollector) MetricCount() int {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	total := 0
	for _, ms := range pc.metrics {
		total += len(ms)
	}
	return total
}

// ---------------------------------------------------------------------------
// 内部辅助：百分位数与标准差
// ---------------------------------------------------------------------------

// percentile 在已排序切片中计算第 p 百分位数（p 范围 0.0~1.0）。
func percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	idx := p * float64(n-1)
	lower := int(idx)
	upper := lower + 1
	if upper >= n {
		return sorted[n-1]
	}
	frac := idx - float64(lower)
	return sorted[lower] + time.Duration(frac*float64(sorted[upper]-sorted[lower]))
}

// stdDev 计算标准差。
func stdDev(values []time.Duration, avg time.Duration) time.Duration {
	if len(values) < 2 {
		return 0
	}
	var sumSq float64
	for _, v := range values {
		diff := float64(v - avg)
		sumSq += diff * diff
	}
	variance := sumSq / float64(len(values))
	return time.Duration(math.Sqrt(variance))
}
