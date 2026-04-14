package persistence

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// MemSignalTraceStore is the in-process SignalTraceStore used by tests and
// by the zero-dependency file driver bundle. The store upserts by TraceID so
// replaying the same signal trace is safe.
type MemSignalTraceStore struct {
	mu      sync.RWMutex
	rows    []*SignalTrace
	byID    map[string]int
	idSeq   int64
	nowFunc func() time.Time
}

// NewMemSignalTraceStore builds a MemSignalTraceStore. A nil clock defaults
// to UTC now.
func NewMemSignalTraceStore(now func() time.Time) *MemSignalTraceStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemSignalTraceStore{
		rows:    make([]*SignalTrace, 0),
		byID:    make(map[string]int),
		nowFunc: now,
	}
}

// Save upserts the trace by TraceID. Empty TraceIDs are auto-generated so the
// store can still be used in lightweight fixtures.
func (s *MemSignalTraceStore) Save(ctx context.Context, trace *SignalTrace) error {
	if trace == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemSignalTraceStore.Save: trace is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowFunc()
	if trace.TraceID == "" {
		s.idSeq++
		trace.TraceID = nextSignalTraceID(s.idSeq)
		if trace.ID == 0 {
			trace.ID = s.idSeq
		}
	}
	if idx, ok := s.byID[trace.TraceID]; ok {
		existing := s.rows[idx]
		updated := cloneSignalTrace(trace)
		if updated.ID == 0 {
			updated.ID = existing.ID
		}
		if updated.CreatedAt.IsZero() {
			updated.CreatedAt = existing.CreatedAt
		}
		if updated.CreatedAt.IsZero() {
			updated.CreatedAt = now
		}
		updated.UpdatedAt = now
		s.rows[idx] = updated
		return nil
	}

	stored := cloneSignalTrace(trace)
	if stored.ID == 0 {
		s.idSeq++
		stored.ID = s.idSeq
	}
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = now
	}
	stored.UpdatedAt = now
	s.byID[stored.TraceID] = len(s.rows)
	s.rows = append(s.rows, stored)
	return nil
}

// Get returns a defensive copy of the trace with traceID.
func (s *MemSignalTraceStore) Get(ctx context.Context, traceID string) (*SignalTrace, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx, ok := s.byID[traceID]
	if !ok {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemSignalTraceStore.Get: trace %q not found", traceID)),
		)
	}
	return cloneSignalTrace(s.rows[idx]), nil
}

// Query returns newest-first signal traces matching the optional symbol
// filter. The returned rows are defensive copies.
func (s *MemSignalTraceStore) Query(ctx context.Context, filter SignalTraceFilter) ([]*SignalTrace, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := filter.Limit
	if limit <= 0 || limit > len(s.rows) {
		limit = len(s.rows)
	}

	out := make([]*SignalTrace, 0, limit)
	for i := len(s.rows) - 1; i >= 0 && len(out) < limit; i-- {
		row := s.rows[i]
		if filter.Symbol != "" && !strings.EqualFold(row.Symbol, filter.Symbol) {
			continue
		}
		out = append(out, cloneSignalTrace(row))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
