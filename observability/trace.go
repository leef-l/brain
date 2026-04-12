package observability

import "context"

// TraceExporter is the span factory defined in 24-可观测性.md §5.
//
// Implementations MUST create spans whose parent / child relationships
// follow the layered hierarchy documented in 24 §5.1 (run → turn →
// llm/tool → sidecar). The returned context carries the active span so
// that downstream calls can extract it for correlation per 24 §3.3.
type TraceExporter interface {
	// StartSpan opens a new span named `name` as a child of whatever
	// span is already active in ctx (or as a root span if none). The
	// attrs bag is applied to the span immediately and MUST follow the
	// attribute conventions in 24-可观测性.md §B. The caller MUST
	// invoke Span.End exactly once, typically via `defer span.End()`.
	StartSpan(ctx context.Context, name string, attrs Labels) (context.Context, Span)
}

// Span is the in-progress trace unit defined in 24-可观测性.md §5.
// A Span MUST record the required attributes listed in 24 §5.2, surface
// errors via SetError, and be closed with End exactly once. Calling any
// method after End is a programming error and implementations MAY panic.
type Span interface {
	// SetAttr attaches a single attribute to the span. Keys MUST be
	// drawn from the attribute conventions in 24-可观测性.md §B to keep
	// cardinality inside the budget defined in §4.4.
	SetAttr(key, value string)

	// SetError marks the span as failed and attaches error metadata per
	// 24-可观测性.md §B.4. Passing a nil error is a no-op.
	SetError(err error)

	// End closes the span and flushes it to the exporter. Calling End
	// more than once on the same Span is a programming error.
	End()
}
