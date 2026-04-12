// Package observability defines the metrics, traces, and logs contracts
// that every BrainKernel component MUST emit, as specified in
// 24-可观测性.md.
//
// The three signals (Metrics / Traces / Logs) are modelled as independent
// interfaces so that the Kernel, Runners, built-in brains, and third-party
// brains can be wired to different exporters (OTel, stdout, test doubles)
// without touching call sites. See 24 §2.1 for the three-signal design and
// §3 for the planned OpenTelemetry backend.
//
// This package has zero external dependencies — it only imports the
// standard library — and does not import any other brain subpackage. The
// concrete OTel-backed implementations will be added in a later wave; for
// now every method body is an `unimplemented:` panic so that grep can
// locate all TODO sites (see brain骨架实施计划.md §4.2).
package observability

import "context"

// LogExporter is the structured log sink defined in 24-可观测性.md §6.1.
//
// Implementations MUST forward every Emit call to a structured backend
// (OTel logs, stdout JSON, test buffer, ...). Attributes MUST be rendered
// as key/value fields rather than being interpolated into the message so
// that downstream pipelines can index and redact them per 24 §6.4.
type LogExporter interface {
	// Emit records a single structured log entry at the given level. The
	// ctx parameter carries trace correlation (trace_id / span_id) per
	// 24-可观测性.md §3.3 and MUST be propagated to the backend when
	// available.
	Emit(ctx context.Context, level LogLevel, msg string, attrs Labels)
}

// LogLevel is the five-value severity taxonomy defined in
// 24-可观测性.md §6.2. Implementations MUST classify every log entry into
// exactly one of the constants below; custom levels are forbidden so that
// alert routing and sampling policies stay uniform across components.
type LogLevel string

const (
	// LogTrace is the most verbose level, reserved for step-by-step
	// internal tracing of a component. Disabled in production by default.
	// See 24-可观测性.md §6.2.
	LogTrace LogLevel = "trace"

	// LogDebug captures developer-oriented diagnostic detail that is only
	// useful while reproducing an issue. See 24-可观测性.md §6.2.
	LogDebug LogLevel = "debug"

	// LogInfo records normal lifecycle events (run started, turn
	// completed, tool executed). This is the default production level.
	// See 24-可观测性.md §6.2.
	LogInfo LogLevel = "info"

	// LogWarn signals a recoverable anomaly that did not fail the
	// operation but deserves attention (retry fired, degraded path
	// taken). See 24-可观测性.md §6.2.
	LogWarn LogLevel = "warn"

	// LogError marks an operation-level failure that MUST be visible in
	// alerts and error budgets. See 24-可观测性.md §6.2.
	LogError LogLevel = "error"
)
