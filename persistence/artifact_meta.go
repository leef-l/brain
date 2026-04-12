// Package persistence implements the persistence and recovery layer defined
// in 26-持久化与恢复.md.
//
// The package groups seven concerns into a single Go package so that the
// dual-track (SQLite WAL for standalone / MySQL for cluster) backends can
// share a single set of v1-frozen interfaces:
//
//   - PlanStore           — BrainPlan + BrainPlanDelta snapshot / journal
//     (26 §5)
//   - ArtifactStore       — Content-addressable storage (CAS) byte backend
//     (26 §6)
//   - ArtifactMetaStore   — CAS metadata, refcount and GC bookkeeping
//     (26 §6.3)
//   - RunCheckpointStore  — Turn-level checkpoint used by Resume (26 §7)
//   - UsageLedger         — Token / cost accounting with idempotency
//     (26 §8)
//   - ResumeCoordinator   — Run recovery protocol (26 §7)
//   - CAS key helpers     — Sha256Hex / ComputeKey / ParseRef (26 §6.2)
//
// The v1 contract is frozen: schemas, on-disk layout, CAS key algorithm and
// Resume protocol MUST NOT be broken; only additive changes are allowed.
//
// Implementations in this package:
//
//   - cas.go — Sha256Hex / ComputeKey / ParseRef (pure stdlib helpers)
//   - mem_plan_store.go — in-process PlanStore with optimistic locking
//   - mem_artifact_meta.go — in-process ArtifactMetaStore with atomic refcount
//   - mem_artifact_store.go — in-process ArtifactStore (CAS dedup via meta)
//   - fs_artifact_store.go — local filesystem CAS layout from §6.4
//   - mem_run_checkpoint.go — RunCheckpointStore with turn_uuid idempotency
//   - mem_usage_ledger.go — UsageLedger with IdempotencyKey dedup
//   - mem_resume.go — ResumeCoordinator composed over the checkpoint store
//
// All implementations are stdlib-only per brain骨架实施计划.md §4.6 — the
// cluster-tier SQLite WAL / MySQL backends land in a separate package once
// the external driver dependency is introduced (decision logged in the
// skeleton plan §8).
package persistence

import (
	"context"
	"time"
)

// ArtifactMeta is the CAS metadata record defined in 26-持久化与恢复.md §6.3.
//
// Each row corresponds to exactly one CAS Ref and tracks refcount, MIME
// type, size and optional run/turn provenance so that the background GC
// can identify orphaned content per 26 §6.6.
type ArtifactMeta struct {
	// Ref is the CAS key, formatted as "sha256/<64 lowercase hex>"
	// per 26 §6.2.
	Ref Ref

	// MimeType is the content's sniffed or declared MIME type.
	MimeType string

	// SizeBytes is the byte length of the stored content.
	SizeBytes int64

	// RunID optionally binds the artifact to the run that produced it.
	// nil means the artifact is shared / pre-seeded.
	RunID *int64

	// TurnIndex optionally binds the artifact to a specific turn inside
	// RunID. nil when RunID is nil.
	TurnIndex *int

	// Caption is a human-readable short description used by tracing UIs.
	Caption string

	// RefCount is the number of live references. GC removes the content
	// only when RefCount drops to 0 and the grace window has elapsed
	// (26 §6.6).
	RefCount int64

	// Tags is a free-form label map used by observability and GC policies.
	Tags map[string]string

	// CreatedAt is the first Put timestamp.
	CreatedAt time.Time

	// UpdatedAt is the timestamp of the most recent refcount mutation.
	UpdatedAt time.Time
}

// ArtifactMetaStore is the v1 interface for CAS metadata defined in
// 26-持久化与恢复.md §6.3. Implementations MUST be safe for concurrent use
// and MUST make IncRefCount / DecRefCount atomic relative to GC.
type ArtifactMetaStore interface {
	// Put upserts the metadata row for meta.Ref. See 26 §6.3.
	Put(ctx context.Context, meta *ArtifactMeta) error

	// Get returns the metadata row for ref, or a not-found BrainError if
	// the row does not exist. See 26 §6.3.
	Get(ctx context.Context, ref Ref) (*ArtifactMeta, error)

	// IncRefCount atomically increases RefCount by 1. See 26 §6.3.
	IncRefCount(ctx context.Context, ref Ref) error

	// DecRefCount atomically decreases RefCount by 1; the row becomes GC
	// eligible once the count reaches 0. See 26 §6.3 and §6.6.
	DecRefCount(ctx context.Context, ref Ref) error
}
