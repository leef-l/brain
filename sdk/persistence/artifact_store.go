package persistence

import (
	"context"
	"io"
)

// Ref is a CAS key formatted as "sha256/<64 lowercase hex>", defined in
// 26-持久化与恢复.md §6.2. New hash algorithms MUST be introduced as new
// prefixes (e.g. "blake3/") and MUST NOT reuse the "sha256/" namespace.
type Ref string

// Artifact is a content-addressable payload defined in 26-持久化与恢复.md §6.
//
// The CAS guarantees that two Put calls with byte-identical Content
// produce the same Ref (dedup), so Kind and Caption are metadata hints
// that get copied into ArtifactMeta rather than participating in the hash.
type Artifact struct {
	// Kind is a short, free-form tag describing what the bytes are
	// (e.g. "screenshot", "stdout", "patch"). Not part of the CAS key.
	Kind string

	// Content is the raw bytes that the CAS key is computed from.
	Content []byte

	// Caption is a human-readable description shown by tracing UIs.
	// Not part of the CAS key.
	Caption string
}

// ArtifactStore is the v1 byte-level CAS interface defined in
// 26-持久化与恢复.md §6. The dual-track implementation is SQLite WAL +
// ~/.easymvp/artifacts for standalone (26 §6.4) and MySQL + S3/MinIO for
// cluster (26 §6.5). Both tracks MUST produce identical Refs for the same
// content per 26 §3 (CAS key algorithm parity).
type ArtifactStore interface {
	// Put stores artifact.Content and returns its CAS Ref. Put MUST be
	// naturally idempotent: a second Put with the same content is a
	// no-op on the byte backend and bumps refcount in the metadata store
	// (26 §7 idempotency rules, §6.3).
	Put(ctx context.Context, runID int64, artifact Artifact) (Ref, error)

	// Get opens a reader over the raw bytes referenced by ref. The
	// caller MUST Close the reader. See 26 §6.
	Get(ctx context.Context, ref Ref) (io.ReadCloser, error)

	// Exists returns true if ref is currently resolvable in the byte
	// backend. See 26 §6.
	Exists(ctx context.Context, ref Ref) (bool, error)
}
