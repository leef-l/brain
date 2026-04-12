package observability

// Registry is the metric factory defined in 24-可观测性.md §4.
//
// Every component in BrainKernel MUST obtain its Counter / Histogram /
// Gauge instruments through a Registry so that naming rules (§4.1),
// cardinality budget (§4.4), and anti-patterns (§4.3) can be enforced by
// the implementation. Registries are safe to share across goroutines;
// returned instruments are expected to be cached by the implementation
// under the (name, labels) key.
type Registry interface {
	// Counter returns (or creates) a monotonically-increasing counter
	// identified by name and the fully-qualified label set. See
	// 24-可观测性.md §4.2 for the required metric families.
	Counter(name string, labels Labels) Counter

	// Histogram returns (or creates) a histogram instrument with the
	// given explicit bucket boundaries (in the metric's natural unit).
	// Passing nil buckets means "use the implementation default". See
	// 24-可观测性.md §4.2.
	Histogram(name string, labels Labels, buckets []float64) Histogram

	// Gauge returns (or creates) a bidirectional gauge instrument for
	// values that can go up and down (queue depth, in-flight runs, ...).
	// See 24-可观测性.md §4.2.
	Gauge(name string, labels Labels) Gauge
}

// Labels is the canonical key/value attribute bag attached to every
// metric, span, and log record. See 24-可观测性.md §4.4 for the
// cardinality budget that constrains which keys are allowed and
// 24-可观测性.md §B.1 for the shared attribute conventions.
type Labels map[string]string

// Counter is the monotonic counter instrument defined in
// 24-可观测性.md §4.2. Values MUST only increase; use a Gauge for
// quantities that can decrease.
type Counter interface {
	// Inc adds one to the counter. See 24-可观测性.md §4.2.
	Inc()

	// Add increments the counter by n, which MUST be non-negative. See
	// 24-可观测性.md §4.2.
	Add(n float64)
}

// Histogram is the distribution instrument defined in 24-可观测性.md §4.2.
// Histograms are used for latency, token count, payload size, and any
// other value whose distribution matters for SLO computation (§7).
type Histogram interface {
	// Observe records a single sample in the histogram. See
	// 24-可观测性.md §4.2.
	Observe(v float64)
}

// Gauge is the bidirectional instantaneous instrument defined in
// 24-可观测性.md §4.2. Gauges are used for values that can go up and down
// such as in-flight runs, queue depth, or memory usage.
type Gauge interface {
	// Set replaces the gauge's current value with v. See
	// 24-可观测性.md §4.2.
	Set(v float64)

	// Add adjusts the gauge by n, which MAY be negative. See
	// 24-可观测性.md §4.2.
	Add(n float64)
}

