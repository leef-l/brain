package persistence

import (
	"context"
	"fmt"

	brainerrors "github.com/leef-l/brain/errors"
)

// FileSignalTraceStore wraps FileStore to implement SignalTraceStore.
type FileSignalTraceStore struct{ f *FileStore }

// SignalTraceStore returns a SignalTraceStore backed by this FileStore.
func (f *FileStore) SignalTraceStore() SignalTraceStore {
	return &FileSignalTraceStore{f: f}
}

func (s *FileSignalTraceStore) Save(ctx context.Context, trace *SignalTrace) error {
	if trace == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("FileSignalTraceStore.Save: trace is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	now := s.f.nowFunc()
	row := cloneSignalTrace(trace)
	if row.TraceID == "" {
		s.f.nextID++
		row.TraceID = nextSignalTraceID(s.f.nextID)
		if row.ID == 0 {
			row.ID = s.f.nextID
		}
	}
	if row.ID == 0 {
		s.f.nextID++
		row.ID = s.f.nextID
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	row.UpdatedAt = now

	for i, existing := range s.f.db.SignalTraces {
		if existing.TraceID == row.TraceID {
			if row.ID == 0 {
				row.ID = existing.ID
			}
			if row.CreatedAt.IsZero() {
				row.CreatedAt = existing.CreatedAt
			}
			s.f.db.SignalTraces[i] = row
			return s.f.flush()
		}
	}

	s.f.db.SignalTraces = append(s.f.db.SignalTraces, row)
	return s.f.flush()
}

func (s *FileSignalTraceStore) Get(ctx context.Context, traceID string) (*SignalTrace, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	for _, trace := range s.f.db.SignalTraces {
		if trace.TraceID == traceID {
			return cloneSignalTrace(trace), nil
		}
	}
	return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
		brainerrors.WithMessage(fmt.Sprintf("signal trace %q not found", traceID)),
	)
}

func (s *FileSignalTraceStore) Query(ctx context.Context, filter SignalTraceFilter) ([]*SignalTrace, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	var out []*SignalTrace
	for i := len(s.f.db.SignalTraces) - 1; i >= 0; i-- {
		row := s.f.db.SignalTraces[i]
		if filter.Symbol != "" && row.Symbol != filter.Symbol {
			continue
		}
		out = append(out, cloneSignalTrace(row))
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}
