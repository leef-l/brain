package persistence

import (
	"context"
	"encoding/json"
	"time"
)

// Checkpoint is the turn-level resume anchor defined in
// 26-持久化与恢复.md §7.
//
// Every successful turn writes a Checkpoint row so that Resume
// (26 §7) can rebuild the LLM context, cost counters and budget state
// exactly as they were at the end of the last committed turn. The
// three *Ref fields point at CAS entries so that large message /
// system-prompt / tool-schema payloads are deduplicated across turns.
type Checkpoint struct {
	// RunID is the snowflake id of the BrainKernel run.
	RunID int64

	// TurnIndex is the zero-based index of the committed turn.
	TurnIndex int

	// BrainID identifies the brain instance that owns the run.
	BrainID string

	// State is the run's BrainRunState at the end of the turn
	// (see 26 §7 state machine).
	State string

	// MessagesRef points at the CAS payload holding the L3 message
	// history for the turn. See 26 §7.
	MessagesRef Ref

	// SystemRef points at the CAS payload holding the L1+L2 system
	// prompt composition for the turn. See 26 §7.
	SystemRef Ref

	// ToolsRef points at the CAS payload holding the tool schema set
	// effective at the turn. See 26 §7.
	ToolsRef Ref

	// CostSnapshot is the cumulative cost ledger at turn end, as
	// opaque JSON so schema evolution does not break old rows.
	CostSnapshot json.RawMessage

	// TokenSnapshot is the cumulative token counter at turn end.
	TokenSnapshot json.RawMessage

	// BudgetRemain is the remaining budget envelope at turn end.
	BudgetRemain json.RawMessage

	// TraceParent is the W3C traceparent string propagated across
	// Resume to keep distributed traces stitched (26 §7).
	TraceParent string

	// TurnUUID is the idempotency key of the turn, used by Resume to
	// refuse duplicate replays (26 §7 idempotency rules).
	TurnUUID string

	// ResumeAttempts counts how many times the checkpoint has been
	// used by Resume. Incremented atomically via MarkResumeAttempt.
	ResumeAttempts int

	// UpdatedAt is the timestamp of the most recent Save.
	UpdatedAt time.Time
}

// RunCheckpointStore is the v1 interface for run-level resume anchors
// defined in 26-持久化与恢复.md §7. Save MUST be strictly idempotent on
// (RunID, TurnIndex, TurnUUID) so the same turn can be retried without
// side effects.
type RunCheckpointStore interface {
	// Save upserts the checkpoint for (cp.RunID, cp.TurnIndex). See
	// 26 §7 and the idempotency table in 26 §7.
	Save(ctx context.Context, cp *Checkpoint) error

	// Get returns the latest checkpoint for runID, or a not-found
	// BrainError if none exists. See 26 §7.
	Get(ctx context.Context, runID int64) (*Checkpoint, error)

	// MarkResumeAttempt atomically increments ResumeAttempts on the
	// latest checkpoint for runID. See 26 §7.
	MarkResumeAttempt(ctx context.Context, runID int64) error
}
