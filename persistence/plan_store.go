package persistence

import (
	"context"
	"encoding/json"
	"time"
)

// BrainPlan is the snapshot half of the BrainPlan dual-track model
// defined in 26-持久化与恢复.md §5.
//
// A BrainPlan row stores the latest materialised state of a plan; the
// per-turn journal of mutations lives in BrainPlanDelta rows. Recovery
// replays the deltas on top of the latest snapshot to rebuild the
// working plan (26 §5 + §7).
type BrainPlan struct {
	// ID is the snowflake primary key.
	ID int64

	// RunID binds the plan to the BrainKernel run that owns it.
	RunID int64

	// BrainID identifies the brain instance that authored the plan.
	BrainID string

	// Version is the monotonically increasing snapshot version that
	// matches the latest applied delta.
	Version int64

	// CurrentState is the opaque JSON snapshot of the plan body.
	CurrentState json.RawMessage

	// Archived marks the plan as read-only; archived plans MUST NOT
	// accept further Update calls (26 §5).
	Archived bool

	// CreatedAt is the insertion timestamp.
	CreatedAt time.Time

	// UpdatedAt is the timestamp of the most recent Update.
	UpdatedAt time.Time
}

// BrainPlanDelta is one row of the journal half of the dual-track model
// defined in 26-持久化与恢复.md §5.
//
// Every mutation to CurrentState is written as an append-only delta so
// the plan can be rebuilt turn-by-turn during Resume. OpType is a
// free-form tag (e.g. "patch", "replace", "append") understood by the
// plan domain layer.
type BrainPlanDelta struct {
	// ID is the snowflake primary key.
	ID int64

	// PlanID references BrainPlan.ID.
	PlanID int64

	// Version is the post-apply snapshot version this delta produces.
	Version int64

	// OpType is the mutation kind tag understood by the plan domain.
	OpType string

	// Payload is the opaque JSON body of the mutation.
	Payload json.RawMessage

	// Actor identifies who produced the delta (brain id, tool name,
	// or "human").
	Actor string

	// CreatedAt is the insertion timestamp.
	CreatedAt time.Time
}

// PlanStore is the v1 interface for the BrainPlan dual-track model
// defined in 26-持久化与恢复.md §5. Update MUST be atomic (delta insert +
// snapshot refresh happen inside a single transaction) to keep the
// snapshot and journal in lockstep.
type PlanStore interface {
	// Create inserts a new BrainPlan at version 0 and returns the
	// generated snowflake ID. See 26 §5.
	Create(ctx context.Context, plan *BrainPlan) (int64, error)

	// Get returns the plan with the given id. See 26 §5.
	Get(ctx context.Context, id int64) (*BrainPlan, error)

	// Update atomically appends delta to the journal and refreshes the
	// snapshot of the plan identified by id. See 26 §5.
	Update(ctx context.Context, id int64, delta *BrainPlanDelta) error

	// ListByRun returns every plan owned by the given run. See 26 §5.
	ListByRun(ctx context.Context, runID int64) ([]*BrainPlan, error)

	// Archive marks the plan as read-only; subsequent Update calls MUST
	// fail with a ClassUserFault-style BrainError. See 26 §5.
	Archive(ctx context.Context, id int64) error
}
