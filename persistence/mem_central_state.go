package persistence

import (
	"context"
	"fmt"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// MemCentralStateStore is the in-process CentralStateStore used by tests and
// by the zero-dependency mem driver.
type MemCentralStateStore struct {
	mu      sync.RWMutex
	state   *CentralState
	nowFunc func() time.Time
}

func NewMemCentralStateStore(now func() time.Time) *MemCentralStateStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemCentralStateStore{nowFunc: now}
}

func (s *MemCentralStateStore) Save(ctx context.Context, state *CentralState) error {
	if state == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemCentralStateStore.Save: state is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stored := cloneCentralState(state)
	if stored.UpdatedAt.IsZero() {
		stored.UpdatedAt = s.nowFunc()
	}
	s.state = stored
	return nil
}

func (s *MemCentralStateStore) Get(ctx context.Context) (*CentralState, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.state == nil {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("central state not found")),
		)
	}
	return cloneCentralState(s.state), nil
}
