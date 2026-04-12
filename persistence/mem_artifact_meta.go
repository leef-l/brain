package persistence

import (
	"context"
	"fmt"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// MemArtifactMetaStore is the in-process implementation of
// ArtifactMetaStore from 26-持久化与恢复.md §6.3. Put / IncRefCount /
// DecRefCount are all serialised under a single mutex so refcount moves
// are atomic with respect to GC queries — the correctness rule from
// §6.6 is that a Get that observes refCount==0 MUST NOT race against a
// concurrent IncRefCount that would have bumped it back to 1.
//
// Stdlib-only per brain骨架实施计划.md §4.6.
type MemArtifactMetaStore struct {
	mu      sync.RWMutex
	rows    map[Ref]*ArtifactMeta
	nowFunc func() time.Time
}

// NewMemArtifactMetaStore builds a MemArtifactMetaStore with a
// deterministic clock for tests. A nil clock defaults to UTC now.
func NewMemArtifactMetaStore(now func() time.Time) *MemArtifactMetaStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemArtifactMetaStore{
		rows:    make(map[Ref]*ArtifactMeta),
		nowFunc: now,
	}
}

// Put upserts the metadata row for meta.Ref. If the row already exists
// the non-zero fields on the incoming record win, RefCount is carried
// over from the existing row so Put is a safe no-op on the hot path —
// callers that want to bump refcount MUST go through IncRefCount.
//
// This split matches the §8.3 idempotency table: ArtifactStore.Put is
// naturally idempotent, so the refcount bump is the byte backend's
// responsibility, not the meta store's.
func (s *MemArtifactMetaStore) Put(ctx context.Context, meta *ArtifactMeta) error {
	if meta == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemArtifactMetaStore.Put: meta is nil"),
		)
	}
	if meta.Ref == "" {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemArtifactMetaStore.Put: Ref is empty"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowFunc()
	existing, ok := s.rows[meta.Ref]
	if !ok {
		row := *meta
		if row.CreatedAt.IsZero() {
			row.CreatedAt = now
		}
		row.UpdatedAt = now
		// Clone RunID/TurnIndex pointers so callers cannot mutate our copy.
		if meta.RunID != nil {
			v := *meta.RunID
			row.RunID = &v
		}
		if meta.TurnIndex != nil {
			v := *meta.TurnIndex
			row.TurnIndex = &v
		}
		row.Tags = copyTags(meta.Tags)
		if row.RefCount < 0 {
			row.RefCount = 0
		}
		s.rows[meta.Ref] = &row
		return nil
	}

	// Upsert path — merge non-zero metadata fields, preserve RefCount.
	if meta.MimeType != "" {
		existing.MimeType = meta.MimeType
	}
	if meta.SizeBytes != 0 {
		existing.SizeBytes = meta.SizeBytes
	}
	if meta.Caption != "" {
		existing.Caption = meta.Caption
	}
	if meta.RunID != nil {
		v := *meta.RunID
		existing.RunID = &v
	}
	if meta.TurnIndex != nil {
		v := *meta.TurnIndex
		existing.TurnIndex = &v
	}
	if meta.Tags != nil {
		existing.Tags = copyTags(meta.Tags)
	}
	existing.UpdatedAt = now
	return nil
}

// Get returns a deep copy of the metadata row.
func (s *MemArtifactMetaStore) Get(ctx context.Context, ref Ref) (*ArtifactMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	row, ok := s.rows[ref]
	if !ok {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemArtifactMetaStore.Get: ref %q not found", ref)),
		)
	}
	return cloneMeta(row), nil
}

// IncRefCount atomically increases RefCount by 1 and refreshes UpdatedAt.
func (s *MemArtifactMetaStore) IncRefCount(ctx context.Context, ref Ref) error {
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := s.rows[ref]
	if !ok {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemArtifactMetaStore.IncRefCount: ref %q not found", ref)),
		)
	}
	row.RefCount++
	row.UpdatedAt = s.nowFunc()
	return nil
}

// DecRefCount atomically decreases RefCount by 1. The row is NOT deleted
// when the counter reaches 0; the background GC from §6.6 is responsible
// for eventual cleanup after the grace window elapses.
//
// RefCount is clamped at 0: a Dec against a zero-count row returns
// CodeInvariantViolated so the caller's bookkeeping bug is surfaced
// immediately instead of silently turning the counter negative.
func (s *MemArtifactMetaStore) DecRefCount(ctx context.Context, ref Ref) error {
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := s.rows[ref]
	if !ok {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemArtifactMetaStore.DecRefCount: ref %q not found", ref)),
		)
	}
	if row.RefCount <= 0 {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf("MemArtifactMetaStore.DecRefCount: ref %q already at zero", ref)),
		)
	}
	row.RefCount--
	row.UpdatedAt = s.nowFunc()
	return nil
}

// Exists is a non-interface convenience used by the compliance tests to
// probe whether a ref is currently tracked without taking a snapshot.
func (s *MemArtifactMetaStore) Exists(ref Ref) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.rows[ref]
	return ok
}

func cloneMeta(m *ArtifactMeta) *ArtifactMeta {
	if m == nil {
		return nil
	}
	out := *m
	if m.RunID != nil {
		v := *m.RunID
		out.RunID = &v
	}
	if m.TurnIndex != nil {
		v := *m.TurnIndex
		out.TurnIndex = &v
	}
	out.Tags = copyTags(m.Tags)
	return &out
}

func copyTags(tags map[string]string) map[string]string {
	if tags == nil {
		return nil
	}
	out := make(map[string]string, len(tags))
	for k, v := range tags {
		out[k] = v
	}
	return out
}
