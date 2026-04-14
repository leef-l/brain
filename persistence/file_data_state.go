package persistence

import (
	"context"
	"fmt"

	brainerrors "github.com/leef-l/brain/errors"
)

// FileDataStateStore wraps FileStore to implement DataStateStore.
type FileDataStateStore struct{ f *FileStore }

// DataStateStore returns a DataStateStore backed by this FileStore.
func (f *FileStore) DataStateStore() DataStateStore {
	return &FileDataStateStore{f: f}
}

func (s *FileDataStateStore) Save(ctx context.Context, state *DataState) error {
	if state == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("FileDataStateStore.Save: state is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	row := cloneDataState(state)
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = s.f.nowFunc()
	}
	s.f.db.DataState = row
	return s.f.flush()
}

func (s *FileDataStateStore) Get(ctx context.Context) (*DataState, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}

	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	if s.f.db.DataState == nil {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("data state not found")),
		)
	}
	return cloneDataState(s.f.db.DataState), nil
}
