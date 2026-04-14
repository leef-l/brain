package persistence

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// MemArtifactStore is the in-process ArtifactStore from
// 26-持久化与恢复.md §6. It holds raw content bytes keyed by Ref and
// delegates metadata / refcount bookkeeping to a MemArtifactMetaStore so
// the two halves stay in sync under a single mutex per key.
//
// Put is naturally idempotent per §8.3: a second call with byte-identical
// content MUST be a byte-backend no-op AND MUST bump the metadata
// refcount so the background GC's eventual cleanup can count outstanding
// references. The bump happens transactionally with the existence check
// to avoid the C-P-03 race where 1000 concurrent Puts might under-count.
type MemArtifactStore struct {
	mu      sync.Mutex
	bytesBy map[Ref][]byte
	meta    *MemArtifactMetaStore
	nowFunc func() time.Time
}

// NewMemArtifactStore composes the byte backend over the given meta
// store. Passing a nil meta store is a programmer bug — the ArtifactStore
// interface contract in §6.3 requires both halves to exist.
func NewMemArtifactStore(meta *MemArtifactMetaStore, now func() time.Time) *MemArtifactStore {
	if meta == nil {
		panic("persistence.NewMemArtifactStore: meta store is required")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemArtifactStore{
		bytesBy: make(map[Ref][]byte),
		meta:    meta,
		nowFunc: now,
	}
}

// Put stores artifact.Content under its CAS Ref. The flow matches §6.4 /
// §8 idempotency rules:
//
//  1. Compute the CAS Ref from the raw content bytes.
//  2. Under the store mutex, check if the Ref is already present.
//  3. If present → bump refcount and return — bytes are unchanged.
//  4. If absent → write bytes, then Put a fresh meta row with RefCount=1.
//
// Step 4 is the only path that ever touches the byte map, so concurrent
// Put of the same content always collapses to exactly one backend write,
// satisfying the C-P-03 dedup test.
func (s *MemArtifactStore) Put(ctx context.Context, runID int64, artifact Artifact) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return "", wrapCtxErr(err)
	}

	ref := ComputeKey(artifact.Content)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.bytesBy[ref]; exists {
		// Already stored — bump refcount under the same lock so the
		// counter stays strictly monotone even under parallel writers.
		if err := s.meta.IncRefCount(ctx, ref); err != nil {
			return "", err
		}
		return ref, nil
	}

	// Fresh insert — copy bytes so the caller can mutate the slice.
	stored := make([]byte, len(artifact.Content))
	copy(stored, artifact.Content)
	s.bytesBy[ref] = stored

	now := s.nowFunc()
	meta := &ArtifactMeta{
		Ref:       ref,
		MimeType:  detectMimeType(artifact),
		SizeBytes: int64(len(stored)),
		Caption:   artifact.Caption,
		RefCount:  1,
		Tags:      map[string]string{"kind": artifact.Kind},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if runID != 0 {
		rid := runID
		meta.RunID = &rid
	}
	if err := s.meta.Put(ctx, meta); err != nil {
		// Roll back the byte write so the next Put can re-attempt.
		delete(s.bytesBy, ref)
		return "", err
	}
	return ref, nil
}

// Get returns a ReadCloser over a defensive copy of the stored bytes.
// The copy shields callers from later Put/Delete touching the same Ref.
func (s *MemArtifactStore) Get(ctx context.Context, ref Ref) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, ok := s.bytesBy[ref]
	if !ok {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemArtifactStore.Get: ref %q not found", ref)),
		)
	}
	buf := make([]byte, len(raw))
	copy(buf, raw)
	return io.NopCloser(bytes.NewReader(buf)), nil
}

// Exists reports whether the byte backend currently holds ref. It does
// NOT consult the meta store on purpose: §6.3 allows meta to linger after
// the bytes are GC'd (soft delete window), so only the byte map is the
// source of truth for "is the content still fetchable".
func (s *MemArtifactStore) Exists(ctx context.Context, ref Ref) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, wrapCtxErr(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.bytesBy[ref]
	return ok, nil
}

// detectMimeType maps a free-form Artifact.Kind to a best-effort MIME
// string. Unknown kinds fall back to application/octet-stream so the
// metadata row always has a non-empty MimeType.
func detectMimeType(a Artifact) string {
	switch a.Kind {
	case "text", "log", "stdout", "stderr", "patch":
		return "text/plain; charset=utf-8"
	case "json":
		return "application/json"
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	default:
		return "application/octet-stream"
	}
}
