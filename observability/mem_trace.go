package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// ctxKey is a private context key type for trace span storage.
type ctxKey struct{}

// MemSpan represents a single span in the trace tree per 24-可观测性.md §5.
// Spans are identified by a unique SpanID and linked to their parent via ParentID.
// A root span has an empty ParentID and a newly-generated TraceID.
type MemSpan struct {
	TraceID   string    // Root span ID, 16-byte hex. Propagated to all children.
	SpanID    string    // This span's ID, 8-byte hex.
	ParentID  string    // Parent span ID, empty for root spans.
	Name      string    // Human-readable span name.
	Attrs     Labels    // Immutable attributes (deep copy at creation).
	StartedAt time.Time // Span start time.
	EndedAt   time.Time // Span end time (filled by End()).
	Error     string    // Error message (filled by SetError()).
	ended     bool      // Private flag to prevent double-End().
}

// MemTraceExporter is the in-memory trace exporter per 24-可观测性.md §5.
// It maintains an append-only log of spans and reconstructs parent/child
// relationships via context propagation. All operations are thread-safe.
type MemTraceExporter struct {
	mu    sync.Mutex
	spans []*MemSpan
}

// NewMemTraceExporter creates a new in-memory trace exporter.
// See 24-可观测性.md §5.
func NewMemTraceExporter() *MemTraceExporter {
	return &MemTraceExporter{
		spans: make([]*MemSpan, 0),
	}
}

// StartSpan opens a new span named `name` as a child of whatever span is
// already active in ctx (or as a root span if none). The attrs bag is
// applied to the span immediately and MUST follow the attribute conventions
// in 24-可观测性.md §B. The caller MUST invoke the returned Span's End method
// exactly once, typically via `defer span.End()`.
//
// See 24-可观测性.md §5.1 for the layered hierarchy (run → turn → llm/tool → sidecar).
func (e *MemTraceExporter) StartSpan(ctx context.Context, name string, attrs Labels) (context.Context, Span) {
	// Extract parent span from context if present
	var traceID, parentID string
	if parent, ok := ctx.Value(ctxKey{}).(*MemSpan); ok && parent != nil {
		traceID = parent.TraceID
		parentID = parent.SpanID
	}

	// Generate IDs for new span
	if traceID == "" {
		// Root span: generate new trace ID (16-byte hex = 128 bits)
		traceID = generateID(16)
	}
	spanID := generateID(8)

	// Deep copy attrs to prevent caller mutations
	attrsCopy := make(Labels)
	for k, v := range attrs {
		attrsCopy[k] = v
	}

	span := &MemSpan{
		TraceID:   traceID,
		SpanID:    spanID,
		ParentID:  parentID,
		Name:      name,
		Attrs:     attrsCopy,
		StartedAt: time.Now(),
	}

	// Append to exporter's span log
	e.mu.Lock()
	e.spans = append(e.spans, span)
	e.mu.Unlock()

	// Return context with this span as the active span
	newCtx := context.WithValue(ctx, ctxKey{}, span)
	return newCtx, span
}

// SetAttr attaches a single attribute to the span. Keys MUST be drawn from
// the attribute conventions in 24-可观测性.md §B to keep cardinality inside
// the budget defined in §4.4.
func (s *MemSpan) SetAttr(key, value string) {
	s.Attrs[key] = value
}

// SetError marks the span as failed and attaches error metadata per
// 24-可观测性.md §B.4. Passing a nil error is a no-op.
func (s *MemSpan) SetError(err error) {
	if err != nil {
		s.Error = err.Error()
	}
}

// End closes the span and records the end time. Calling End more than once
// on the same Span is a programming error and may be ignored or panic.
func (s *MemSpan) End() {
	if !s.ended {
		s.EndedAt = time.Now()
		s.ended = true
	}
}

// Spans returns a deep copy of all recorded spans in the trace tree, in
// append order. This is safe for concurrent access and modifications.
func (e *MemTraceExporter) Spans() []*MemSpan {
	e.mu.Lock()
	defer e.mu.Unlock()

	result := make([]*MemSpan, 0, len(e.spans))
	for _, s := range e.spans {
		// Deep copy span with deep-copied attrs
		spanCopy := &MemSpan{
			TraceID:   s.TraceID,
			SpanID:    s.SpanID,
			ParentID:  s.ParentID,
			Name:      s.Name,
			StartedAt: s.StartedAt,
			EndedAt:   s.EndedAt,
			Error:     s.Error,
			ended:     s.ended,
			Attrs:     make(Labels),
		}
		for k, v := range s.Attrs {
			spanCopy.Attrs[k] = v
		}
		result = append(result, spanCopy)
	}

	return result
}

// FindByTraceID returns all spans belonging to the given trace ID, in
// chronological order. Returns nil if no spans match. The returned slice
// is a deep copy.
func (e *MemTraceExporter) FindByTraceID(traceID string) []*MemSpan {
	e.mu.Lock()
	defer e.mu.Unlock()

	var result []*MemSpan
	for _, s := range e.spans {
		if s.TraceID == traceID {
			// Deep copy span with deep-copied attrs
			spanCopy := &MemSpan{
				TraceID:   s.TraceID,
				SpanID:    s.SpanID,
				ParentID:  s.ParentID,
				Name:      s.Name,
				StartedAt: s.StartedAt,
				EndedAt:   s.EndedAt,
				Error:     s.Error,
				ended:     s.ended,
				Attrs:     make(Labels),
			}
			for k, v := range s.Attrs {
				spanCopy.Attrs[k] = v
			}
			result = append(result, spanCopy)
		}
	}

	return result
}

// generateID generates a random hex string of the specified number of bytes.
// Used for trace IDs (16 bytes) and span IDs (8 bytes).
func generateID(numBytes int) string {
	buf := make([]byte, numBytes)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
