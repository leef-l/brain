package tracer

import (
	"context"
	"sync"
)

// MemoryStore is a thread-safe in-memory trace store.
// Suitable for development/paper trading. Use PGTraceStore for production.
type MemoryStore struct {
	mu      sync.RWMutex
	traces  []SignalTrace
	maxSize int // ring buffer size, 0 = unlimited
}

// NewMemoryStore creates an in-memory trace store.
// maxSize limits storage; 0 means unlimited.
func NewMemoryStore(maxSize int) *MemoryStore {
	return &MemoryStore{maxSize: maxSize}
}

func (s *MemoryStore) Save(_ context.Context, trace *SignalTrace) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.traces = append(s.traces, *trace)

	// Evict oldest if over limit
	if s.maxSize > 0 && len(s.traces) > s.maxSize {
		s.traces = s.traces[len(s.traces)-s.maxSize:]
	}
	return nil
}

func (s *MemoryStore) Query(_ context.Context, f TraceFilter) ([]SignalTrace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []SignalTrace
	// Iterate in reverse (newest first)
	for i := len(s.traces) - 1; i >= 0; i-- {
		t := s.traces[i]
		if f.Symbol != "" && t.Symbol != f.Symbol {
			continue
		}
		if f.Outcome != "" && t.Outcome != f.Outcome {
			continue
		}
		if !f.Since.IsZero() && t.Timestamp.Before(f.Since) {
			continue
		}
		result = append(result, t)
		if f.Limit > 0 && len(result) >= f.Limit {
			break
		}
	}
	return result, nil
}

func (s *MemoryStore) Count(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(len(s.traces)), nil
}

var _ Store = (*MemoryStore)(nil)
