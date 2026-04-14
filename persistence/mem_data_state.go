package persistence

import (
	"context"
	"fmt"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// MemDataStateStore is the in-process DataStateStore used by tests and by
// the zero-dependency mem driver.
type MemDataStateStore struct {
	mu      sync.RWMutex
	state   *DataState
	nowFunc func() time.Time
}

func NewMemDataStateStore(now func() time.Time) *MemDataStateStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemDataStateStore{nowFunc: now}
}

func (s *MemDataStateStore) Save(ctx context.Context, state *DataState) error {
	if state == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemDataStateStore.Save: state is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stored := cloneDataState(state)
	if stored.UpdatedAt.IsZero() {
		stored.UpdatedAt = s.nowFunc()
	}
	s.state = stored
	return nil
}

func (s *MemDataStateStore) Get(ctx context.Context) (*DataState, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.state == nil {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("data state not found")),
		)
	}
	return cloneDataState(s.state), nil
}
