package persistence

import (
	"context"
	"fmt"

	brainerrors "github.com/leef-l/brain/errors"
)

// FileCentralStateStore wraps FileStore to implement CentralStateStore.
type FileCentralStateStore struct{ f *FileStore }

// CentralStateStore returns a CentralStateStore backed by this FileStore.
func (f *FileStore) CentralStateStore() CentralStateStore {
	return &FileCentralStateStore{f: f}
}

func (s *FileCentralStateStore) Save(ctx context.Context, state *CentralState) error {
	if state == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("FileCentralStateStore.Save: state is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	row := cloneCentralState(state)
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = s.f.nowFunc()
	}
	s.f.db.CentralState = row
	return s.f.flush()
}

func (s *FileCentralStateStore) Get(ctx context.Context) (*CentralState, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}

	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	if s.f.db.CentralState == nil {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("central state not found")),
		)
	}
	return cloneCentralState(s.f.db.CentralState), nil
}
