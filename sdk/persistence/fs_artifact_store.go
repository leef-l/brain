package persistence

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// FSArtifactStore is the local-filesystem ArtifactStore from
// 26-持久化与恢复.md §6.4. It is the standalone-mode byte backend: all
// content lives under a configurable root directory following the frozen
// two-level sha256 layout
//
//	<root>/sha256/<ab>/<full64hex>.bin
//
// where <ab> is the first two hex chars of the digest. The two-level
// split is a MUST from §6.4 — single-directory layouts hit ext4 file
// count limits on busy sidecars.
//
// FSArtifactStore composes a MemArtifactMetaStore because §6.3 explicitly
// separates metadata from content: metadata lives in its own SQLite
// (independent process / cluster track would use MySQL). The in-memory
// meta store is the stdlib-only stand-in until the SQLite driver
// dependency is introduced, but the semantic contract (refcount +
// orphan detection) is preserved end-to-end so the compliance suite can
// exercise the full Put/Get/GC path.
//
// Stdlib-only per brain骨架实施计划.md §4.6.
type FSArtifactStore struct {
	mu      sync.Mutex
	root    string
	meta    ArtifactMetaStore
	nowFunc func() time.Time
}

// NewFSArtifactStore builds an FSArtifactStore rooted at root. The root
// directory is created on first use with mode 0o700 — artifacts can
// contain tool output that is expected to stay private to the host user,
// see 23-安全模型.md §9.
//
// A nil meta store triggers a panic — §6.3 requires the byte backend and
// metadata store to co-exist.
func NewFSArtifactStore(root string, meta ArtifactMetaStore, now func() time.Time) *FSArtifactStore {
	if meta == nil {
		panic("persistence.NewFSArtifactStore: meta store is required")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &FSArtifactStore{
		root:    root,
		meta:    meta,
		nowFunc: now,
	}
}

// Put writes artifact.Content to its CAS location following the §6.4
// atomicity recipe: tmpfile → fsync → rename → meta upsert. Rename is
// atomic on POSIX (same filesystem), so a crash between fsync and meta
// update leaves an orphan file that the GC sweeps eventually.
//
// The Put flow is:
//
//  1. Compute the Ref.
//  2. Acquire the store mutex (so parallel Puts of the same content
//     collapse to one byte write, matching C-P-03).
//  3. If the final file already exists on disk → IncRefCount and return.
//  4. Otherwise: write to a tmp file in the same shard directory,
//     Sync, rename to the final path, then Put the meta row.
//  5. If the meta Put fails, remove the file so the next Put can retry.
func (s *FSArtifactStore) Put(ctx context.Context, runID int64, artifact Artifact) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return "", wrapCtxErr(err)
	}
	ref := ComputeKey(artifact.Content)
	path, dir, err := s.pathFor(ref)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, statErr := os.Stat(path); statErr == nil {
		// Already on disk — bump refcount to match the byte-backend dedup
		// contract. §8.3 classifies ArtifactStore.Put as naturally
		// idempotent, which requires the refcount move.
		if incErr := s.meta.IncRefCount(ctx, ref); incErr != nil {
			return "", incErr
		}
		return ref, nil
	} else if !os.IsNotExist(statErr) {
		return "", brainerrors.Wrap(statErr, brainerrors.CodeAssertionFailed,
			brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Put: stat %q", path)),
		)
	}

	// Ensure shard dir exists. 0o700 keeps files tenant-isolated per
	// 23-安全模型.md §9.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", brainerrors.Wrap(err, brainerrors.CodeAssertionFailed,
			brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Put: mkdir %q", dir)),
		)
	}

	tmp, err := os.CreateTemp(dir, ".cas-*.tmp")
	if err != nil {
		return "", brainerrors.Wrap(err, brainerrors.CodeAssertionFailed,
			brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Put: create tmp in %q", dir)),
		)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(artifact.Content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", brainerrors.Wrap(err, brainerrors.CodeAssertionFailed,
			brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Put: write tmp %q", tmpPath)),
		)
	}
	// fsync is the durability MUST from §6.4 — without it, a crash
	// between write() and rename() leaves a torn file that fails its
	// own digest check on read.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", brainerrors.Wrap(err, brainerrors.CodeAssertionFailed,
			brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Put: fsync %q", tmpPath)),
		)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", brainerrors.Wrap(err, brainerrors.CodeAssertionFailed,
			brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Put: close tmp %q", tmpPath)),
		)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", brainerrors.Wrap(err, brainerrors.CodeAssertionFailed,
			brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Put: rename %q → %q", tmpPath, path)),
		)
	}

	meta := &ArtifactMeta{
		Ref:       ref,
		MimeType:  detectMimeType(artifact),
		SizeBytes: int64(len(artifact.Content)),
		Caption:   artifact.Caption,
		RefCount:  1,
		Tags:      map[string]string{"kind": artifact.Kind},
		CreatedAt: s.nowFunc(),
		UpdatedAt: s.nowFunc(),
	}
	if runID != 0 {
		rid := runID
		meta.RunID = &rid
	}
	if err := s.meta.Put(ctx, meta); err != nil {
		// Best-effort rollback of the byte write so the next Put can
		// attempt a fresh insert. §9.1 permits the content to linger as
		// an orphan if the unlink itself fails — the nightly GC will
		// reclaim it eventually.
		_ = os.Remove(path)
		return "", err
	}
	return ref, nil
}

// Get returns a ReadCloser over the on-disk bytes for ref. The caller
// MUST Close the handle when done.
func (s *FSArtifactStore) Get(ctx context.Context, ref Ref) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	path, _, err := s.pathFor(ref)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
				brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Get: ref %q not found on disk", ref)),
			)
		}
		return nil, brainerrors.Wrap(err, brainerrors.CodeAssertionFailed,
			brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Get: open %q", path)),
		)
	}
	return f, nil
}

// Exists reports whether the on-disk file for ref is currently present.
func (s *FSArtifactStore) Exists(ctx context.Context, ref Ref) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, wrapCtxErr(err)
	}
	path, _, err := s.pathFor(ref)
	if err != nil {
		return false, err
	}
	_, statErr := os.Stat(path)
	if statErr == nil {
		return true, nil
	}
	if os.IsNotExist(statErr) {
		return false, nil
	}
	return false, brainerrors.Wrap(statErr, brainerrors.CodeAssertionFailed,
		brainerrors.WithMessage(fmt.Sprintf("FSArtifactStore.Exists: stat %q", path)),
	)
}

// pathFor returns the final on-disk path for ref and the shard dir that
// contains it. It validates the ref via ParseRef so an invalid input
// surfaces as CodeInvalidParams before any filesystem syscall runs.
func (s *FSArtifactStore) pathFor(ref Ref) (path, dir string, err error) {
	algo, hexDigest, parseErr := ParseRef(string(ref))
	if parseErr != nil {
		return "", "", parseErr
	}
	shard := hexDigest[:2]
	dir = filepath.Join(s.root, algo, shard)
	path = filepath.Join(dir, hexDigest+".bin")
	return path, dir, nil
}
