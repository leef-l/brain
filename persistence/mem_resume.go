package persistence

import (
	"context"
	"fmt"

	brainerrors "github.com/leef-l/brain/errors"
)

// MaxResumeAttempts is the per-Run cap from 26-持久化与恢复.md §7.7
// ("resume_attempts 上限 3 次"). Once a run's checkpoint has been marked
// this many times the ResumeCoordinator refuses further attempts, the
// kernel layer surfaces a resume_failed audit event, and the run is
// parked in paused for a human decision.
const MaxResumeAttempts = 3

// MemResumeCoordinator is the in-process ResumeCoordinator from
// 26-持久化与恢复.md §7. It composes a RunCheckpointStore (and nothing
// else) because the minimal resume contract is:
//
//  1. Read the latest checkpoint for runID.
//  2. If resume_attempts has already reached MaxResumeAttempts, refuse.
//  3. Otherwise bump the attempt counter and return the checkpoint so
//     the caller can rehydrate the run.
//
// Rehydrating the L3 messages / system / tools CAS payloads is the
// caller's job (the kernel runner owns the ArtifactStore); the
// ResumeCoordinator only guarantees a consistent checkpoint read and
// attempt accounting.
//
// Stdlib-only per brain骨架实施计划.md §4.6.
type MemResumeCoordinator struct {
	checkpoints CheckpointReader
}

// CheckpointReader is the subset of RunCheckpointStore the coordinator
// needs. Exposing the narrow dependency makes the coordinator trivially
// testable with a fake.
type CheckpointReader interface {
	Get(ctx context.Context, runID int64) (*Checkpoint, error)
	MarkResumeAttempt(ctx context.Context, runID int64) error
}

// NewMemResumeCoordinator builds a MemResumeCoordinator over the given
// checkpoint reader. Panics if store is nil — the coordinator cannot
// function without a backing store and a nil argument is a programmer
// bug per brain骨架实施计划.md §4.5.
func NewMemResumeCoordinator(store CheckpointReader) *MemResumeCoordinator {
	if store == nil {
		panic("persistence.NewMemResumeCoordinator: store is required")
	}
	return &MemResumeCoordinator{checkpoints: store}
}

// Resume reads the latest checkpoint for runID, verifies the attempt
// cap, bumps the counter, and returns the rehydrated anchor.
//
// When the cap has already been reached Resume returns a BrainError
// tagged with CodeInvariantViolated so the kernel watchdog surfaces it
// as a protocol / policy issue and the alert pipeline fires per §7.7.
// Returning a typed BrainError also means the kernel does not need to
// parse string messages to decide whether to enter the "park in paused"
// path.
func (c *MemResumeCoordinator) Resume(ctx context.Context, runID int64) (*Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}

	cp, err := c.checkpoints.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if cp.ResumeAttempts >= MaxResumeAttempts {
		return nil, brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"MemResumeCoordinator.Resume: run %d exceeded resume attempts (%d ≥ %d)",
				runID, cp.ResumeAttempts, MaxResumeAttempts,
			)),
		)
	}
	if err := c.checkpoints.MarkResumeAttempt(ctx, runID); err != nil {
		return nil, err
	}
	// Re-read so the returned struct reflects the post-increment state.
	return c.checkpoints.Get(ctx, runID)
}

// CanResume reports whether runID currently has a viable checkpoint and
// has not yet exceeded the attempt cap. Unlike Resume it does NOT mutate
// state — callers use it for the pre-flight check in §7.4 step 2.
//
// Returning (false, nil) when the checkpoint is missing mirrors the
// §7.4 decision tree: a missing checkpoint is not an error, it simply
// means the run is not resumable (it never checkpointed).
func (c *MemResumeCoordinator) CanResume(ctx context.Context, runID int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, wrapCtxErr(err)
	}
	cp, err := c.checkpoints.Get(ctx, runID)
	if err != nil {
		if be, ok := err.(*brainerrors.BrainError); ok && be.ErrorCode == brainerrors.CodeRecordNotFound {
			return false, nil
		}
		return false, err
	}
	if cp.ResumeAttempts >= MaxResumeAttempts {
		return false, nil
	}
	switch cp.State {
	case "Completed", "Failed", "Cancelled":
		return false, nil
	}
	return true, nil
}
