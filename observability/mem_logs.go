package observability

import (
	"context"
	"sync"
	"time"
)

// LogRecord represents a single structured log entry per
// 24-可观测性.md §6.1.
type LogRecord struct {
	Timestamp time.Time
	Level     LogLevel
	Message   string
	Attrs     Labels
}

// MemLogExporter is the in-memory log exporter per 24-可观测性.md §6.1.
// It uses a ring buffer to store the N most recent log records, with
// efficient wraparound. All operations are thread-safe.
//
// Entries with LogLevel below the configured minLevel are silently dropped.
// See 24-可观测性.md §6.2 for the five-level severity taxonomy.
type MemLogExporter struct {
	mu     sync.Mutex
	cap    int
	buf    []LogRecord
	head   int
	count  int
	minLvl LogLevel
}

// NewMemLogExporter creates a new in-memory log exporter with the given
// ring buffer capacity and minimum log level. Log entries with severity
// below minLevel are silently dropped.
//
// See 24-可观测性.md §6.1 and §6.2.
func NewMemLogExporter(capacity int, minLevel LogLevel) *MemLogExporter {
	if capacity <= 0 {
		capacity = 1024
	}
	return &MemLogExporter{
		cap:   capacity,
		buf:   make([]LogRecord, capacity),
		minLvl: minLevel,
	}
}

// Emit records a single structured log entry at the given level. The ctx
// parameter carries trace correlation (trace_id / span_id) per 24-可观测性.md §3.3
// and SHOULD be propagated to the backend when available.
//
// Log entries with LogLevel below the exporter's minimum level are silently dropped.
// See 24-可观测性.md §6.
func (e *MemLogExporter) Emit(ctx context.Context, level LogLevel, msg string, attrs Labels) {
	// Apply level filtering
	if levelRank(level) < levelRank(e.minLvl) {
		return
	}

	// Deep copy attrs to prevent caller mutations
	attrsCopy := make(Labels)
	for k, v := range attrs {
		attrsCopy[k] = v
	}

	record := LogRecord{
		Timestamp: time.Now(),
		Level:     level,
		Message:   msg,
		Attrs:     attrsCopy,
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Append to ring buffer at head position
	e.buf[e.head] = record
	e.head = (e.head + 1) % e.cap

	// Track count (capped at capacity)
	if e.count < e.cap {
		e.count++
	}
}

// Records returns all log records in chronological order (oldest first).
// If fewer than cap records have been emitted, returns only the ones that
// exist. The returned slice is a deep copy, safe for concurrent access.
//
// Records are sorted by their emission order, not by timestamp.
func (e *MemLogExporter) Records() []LogRecord {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.count == 0 {
		return nil
	}

	// Copy records in chronological order from the ring buffer
	result := make([]LogRecord, 0, e.count)

	// If buffer is full, start from head (oldest record)
	// Otherwise, start from index 0
	if e.count == e.cap {
		// Buffer is full; records are at indices [head, head+1, ..., head+count-1] (mod cap)
		for i := 0; i < e.count; i++ {
			idx := (e.head + i) % e.cap
			record := e.buf[idx]
			// Deep copy attrs
			recordCopy := LogRecord{
				Timestamp: record.Timestamp,
				Level:     record.Level,
				Message:   record.Message,
				Attrs:     make(Labels),
			}
			for k, v := range record.Attrs {
				recordCopy.Attrs[k] = v
			}
			result = append(result, recordCopy)
		}
	} else {
		// Buffer not full; records are at indices [0, 1, ..., count-1]
		for i := 0; i < e.count; i++ {
			record := e.buf[i]
			// Deep copy attrs
			recordCopy := LogRecord{
				Timestamp: record.Timestamp,
				Level:     record.Level,
				Message:   record.Message,
				Attrs:     make(Labels),
			}
			for k, v := range record.Attrs {
				recordCopy.Attrs[k] = v
			}
			result = append(result, recordCopy)
		}
	}

	return result
}

// Count returns the number of log records currently stored in the exporter.
func (e *MemLogExporter) Count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}

// levelRank returns an integer rank for the log level, where trace < debug < info < warn < error.
// Higher rank = higher severity.
// Used internally for level filtering.
func levelRank(lvl LogLevel) int {
	switch lvl {
	case LogTrace:
		return 0
	case LogDebug:
		return 1
	case LogInfo:
		return 2
	case LogWarn:
		return 3
	case LogError:
		return 4
	default:
		return 2 // default to info
	}
}
