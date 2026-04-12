package observability

import (
	"sort"
	"strings"
	"sync"
)

// MemCounter is the in-memory implementation of Counter per
// 24-可观测性.md §4.2. All operations are thread-safe via atomic.
type MemCounter struct {
	mu    sync.Mutex
	value float64
}

// Inc adds one to the counter. See 24-可观测性.md §4.2.
func (c *MemCounter) Inc() {
	c.Add(1)
}

// Add increments the counter by n, which MUST be non-negative.
// See 24-可观测性.md §4.2.
func (c *MemCounter) Add(n float64) {
	c.mu.Lock()
	c.value += n
	c.mu.Unlock()
}

// value returns the current counter value (for testing).
func (c *MemCounter) currentValue() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

// MemHistogram is the in-memory implementation of Histogram per
// 24-可观测性.md §4.2. It records raw observations and computes
// bucket counts. All operations are thread-safe.
type MemHistogram struct {
	mu      sync.Mutex
	values  []float64
	buckets []float64
}

// Observe records a single sample in the histogram. See
// 24-可观测性.md §4.2.
func (h *MemHistogram) Observe(v float64) {
	h.mu.Lock()
	h.values = append(h.values, v)
	h.mu.Unlock()
}

// currentValues returns a copy of all observed values (for testing).
func (h *MemHistogram) currentValues() []float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]float64, len(h.values))
	copy(result, h.values)
	return result
}

// MemGauge is the in-memory implementation of Gauge per
// 24-可观测性.md §4.2. It tracks a single instantaneous value
// that can go up and down. All operations are thread-safe.
type MemGauge struct {
	mu    sync.Mutex
	value float64
}

// Set replaces the gauge's current value with v. See
// 24-可观测性.md §4.2.
func (g *MemGauge) Set(v float64) {
	g.mu.Lock()
	g.value = v
	g.mu.Unlock()
}

// Add adjusts the gauge by n, which MAY be negative. See
// 24-可观测性.md §4.2.
func (g *MemGauge) Add(n float64) {
	g.mu.Lock()
	g.value += n
	g.mu.Unlock()
}

// currentValue returns the current gauge value (for testing).
func (g *MemGauge) currentValue() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.value
}

// MemRegistry is the in-memory implementation of Registry per
// 24-可观测性.md §4. It manages counters, histograms, and gauges
// by (name, labels) key. All methods are thread-safe.
//
// See 24-可观测性.md §4 for the metric design principles and
// cardinality budget constraints.
type MemRegistry struct {
	mu         sync.RWMutex
	counters   map[string]*MemCounter
	histograms map[string]*MemHistogram
	gauges     map[string]*MemGauge
}

// NewMemRegistry creates a new in-memory metric registry. The returned
// registry is safe to share across goroutines. See 24-可观测性.md §4.
func NewMemRegistry() *MemRegistry {
	return &MemRegistry{
		counters:    make(map[string]*MemCounter),
		histograms:  make(map[string]*MemHistogram),
		gauges:      make(map[string]*MemGauge),
	}
}

// Counter returns (or creates) a monotonically-increasing counter identified
// by name and labels. See 24-可观测性.md §4.2 for the required metric families.
func (r *MemRegistry) Counter(name string, labels Labels) Counter {
	key := metricKey(name, labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[key]; ok {
		return c
	}
	c := &MemCounter{}
	r.counters[key] = c
	return c
}

// Histogram returns (or creates) a histogram instrument with the given
// explicit bucket boundaries (in the metric's natural unit). Passing nil
// buckets means "use the implementation default". See 24-可观测性.md §4.2.
func (r *MemRegistry) Histogram(name string, labels Labels, buckets []float64) Histogram {
	key := metricKey(name, labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.histograms[key]; ok {
		return h
	}
	h := &MemHistogram{buckets: buckets}
	r.histograms[key] = h
	return h
}

// Gauge returns (or creates) a bidirectional gauge instrument for values
// that can go up and down (queue depth, in-flight runs, ...). See
// 24-可观测性.md §4.2.
func (r *MemRegistry) Gauge(name string, labels Labels) Gauge {
	key := metricKey(name, labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[key]; ok {
		return g
	}
	g := &MemGauge{}
	r.gauges[key] = g
	return g
}

// CounterValue returns the current value of a counter identified by name
// and labels. Returns 0 if the counter does not exist.
func (r *MemRegistry) CounterValue(name string, labels Labels) float64 {
	key := metricKey(name, labels)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if c, ok := r.counters[key]; ok {
		return c.currentValue()
	}
	return 0
}

// HistogramValues returns all observed values for a histogram identified
// by name and labels. Returns nil if the histogram does not exist. The
// returned slice is a deep copy.
func (r *MemRegistry) HistogramValues(name string, labels Labels) []float64 {
	key := metricKey(name, labels)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if h, ok := r.histograms[key]; ok {
		return h.currentValues()
	}
	return nil
}

// GaugeValue returns the current value of a gauge identified by name and
// labels. Returns 0 if the gauge does not exist.
func (r *MemRegistry) GaugeValue(name string, labels Labels) float64 {
	key := metricKey(name, labels)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if g, ok := r.gauges[key]; ok {
		return g.currentValue()
	}
	return 0
}

// Snapshot returns a flat map of all metrics in the registry. Keys are
// formatted as "metric_type|metric_name|sorted_labels". Values are
// current measurements. This is a deep copy safe for concurrent modification.
func (r *MemRegistry) Snapshot() map[string]float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]float64)

	for key, c := range r.counters {
		result["counter|"+key] = c.currentValue()
	}

	for key, h := range r.histograms {
		// For histograms, report the count of observations
		values := h.currentValues()
		result["histogram|"+key+"|count"] = float64(len(values))
	}

	for key, g := range r.gauges {
		result["gauge|"+key] = g.currentValue()
	}

	return result
}

// metricKey returns a stable key from (name, labels) by sorting labels
// alphabetically. Format: "metric_name|key1=val1,key2=val2".
func metricKey(name string, labels Labels) string {
	if len(labels) == 0 {
		return name
	}

	// Sort labels by key for stable key generation
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString(name)
	sb.WriteString("|")

	for i, k := range keys {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(labels[k])
	}

	return sb.String()
}
