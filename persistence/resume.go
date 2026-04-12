package persistence

import "context"

// ResumeCoordinator drives the run recovery protocol defined in
// 26-持久化与恢复.md §7.
//
// A ResumeCoordinator is typically composed from a RunCheckpointStore,
// an ArtifactStore and an ArtifactMetaStore so it can rebuild the L3
// messages / L1+L2 system prompt / tool-schema context from CAS
// references before handing control back to the BrainKernel runner.
// Implementations MUST honour the idempotency contract in 26 §7 so
// replays do not double-spend budget via the UsageLedger.
type ResumeCoordinator interface {
	// Resume loads the latest Checkpoint for runID, bumps the resume
	// attempt counter, and returns the rehydrated anchor. See 26 §7.
	Resume(ctx context.Context, runID int64) (*Checkpoint, error)

	// CanResume reports whether runID has a viable checkpoint and
	// has not exceeded the max resume attempts defined in 26 §7.
	CanResume(ctx context.Context, runID int64) (bool, error)
}
