package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// MemRunCheckpointStore is the in-process RunCheckpointStore from
// 26-持久化与恢复.md §7. It keeps exactly one row per RunID (§7 "Turn
// boundary overwrite") and enforces the turn-level idempotency contract
// from §8.3 via the TurnUUID column:
//
//  1. If the incoming Save carries a TurnUUID that is already associated
//     with the current stored checkpoint for the same RunID, the call is
//     a strict no-op — nothing changes, including UpdatedAt. This is the
//     "replay-safe" property C-P-10 exercises: a resumed Run that
//     re-emits the same turn must not mutate the store state.
//
//  2. Otherwise the row is overwritten (latest wins) with the new
//     UpdatedAt stamp.
//
// Empty TurnUUIDs are rejected with CodeInvalidParams — §7 requires the
// key to exist because Save without it cannot be idempotent.
//
// Stdlib-only per brain骨架实施计划.md §4.6.
type MemRunCheckpointStore struct {
	mu      sync.RWMutex
	rows    map[int64]*Checkpoint
	nowFunc func() time.Time
}

// NewMemRunCheckpointStore builds a MemRunCheckpointStore. A nil clock
// defaults to UTC now.
func NewMemRunCheckpointStore(now func() time.Time) *MemRunCheckpointStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemRunCheckpointStore{
		rows:    make(map[int64]*Checkpoint),
		nowFunc: now,
	}
}

// Save upserts the checkpoint for (cp.RunID, cp.TurnIndex). See the
// package-level contract on MemRunCheckpointStore for the idempotency
// rules.
func (s *MemRunCheckpointStore) Save(ctx context.Context, cp *Checkpoint) error {
	if cp == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemRunCheckpointStore.Save: checkpoint is nil"),
		)
	}
	if cp.TurnUUID == "" {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemRunCheckpointStore.Save: TurnUUID is required for idempotency"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.rows[cp.RunID]; ok && existing.TurnUUID == cp.TurnUUID {
		// §8.3 idempotency: the same turn committed twice is a no-op.
		// We deliberately do NOT bump UpdatedAt so observers can detect
		// replays purely by inspecting timestamps.
		return nil
	}

	stored := cloneCheckpoint(cp)
	stored.UpdatedAt = s.nowFunc()
	// Resume attempts are a separate counter — Save MUST NOT reset or
	// increment them; the counter lives across checkpoint turns for the
	// whole run. §7.7 ("resume_attempts 上限 3 次").
	s.rows[cp.RunID] = stored
	return nil
}

// Get returns a deep copy of the checkpoint for runID. The copy shields
// callers from later mutations.
func (s *MemRunCheckpointStore) Get(ctx context.Context, runID int64) (*Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	row, ok := s.rows[runID]
	if !ok {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemRunCheckpointStore.Get: run %d has no checkpoint", runID)),
		)
	}
	return cloneCheckpoint(row), nil
}

// MarkResumeAttempt atomically increments ResumeAttempts on the latest
// checkpoint for runID. §7.7 caps the counter at 3 in the ResumeCoordinator;
// the store itself does not enforce the cap because it has no notion of
// what "failed" means — that decision belongs to the coordinator.
func (s *MemRunCheckpointStore) MarkResumeAttempt(ctx context.Context, runID int64) error {
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := s.rows[runID]
	if !ok {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemRunCheckpointStore.MarkResumeAttempt: run %d has no checkpoint", runID)),
		)
	}
	row.ResumeAttempts++
	row.UpdatedAt = s.nowFunc()
	return nil
}

// ListResumable returns every runID that currently has a checkpoint and
// whose state is neither Completed nor Failed nor Cancelled. Exposed as a
// convenience matching §7.4 step 1.
func (s *MemRunCheckpointStore) ListResumable(ctx context.Context) ([]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]int64, 0)
	for id, row := range s.rows {
		switch row.State {
		case "Completed", "Failed", "Cancelled":
			continue
		}
		out = append(out, id)
	}
	// Deterministic order — small n insertion sort.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out, nil
}

func cloneCheckpoint(cp *Checkpoint) *Checkpoint {
	if cp == nil {
		return nil
	}
	out := *cp
	if len(cp.CostSnapshot) > 0 {
		out.CostSnapshot = append(json.RawMessage(nil), cp.CostSnapshot...)
	}
	if len(cp.TokenSnapshot) > 0 {
		out.TokenSnapshot = append(json.RawMessage(nil), cp.TokenSnapshot...)
	}
	if len(cp.BudgetRemain) > 0 {
		out.BudgetRemain = append(json.RawMessage(nil), cp.BudgetRemain...)
	}
	return &out
}
