package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// MemPlanStore is the in-process implementation of PlanStore from
// 26-持久化与恢复.md §5. It stores BrainPlan snapshots plus an append-only
// BrainPlanDelta journal under a single RWMutex so Update can atomically
// (a) verify the optimistic-lock version, (b) append the delta, and
// (c) refresh the snapshot — matching the transaction boundary MUST rule
// in §4.4 / §9.1.
//
// MemPlanStore is safe for concurrent use. It is intentionally stdlib-only
// per brain骨架实施计划.md §4.6: the cluster-tier SQLite WAL / MySQL backend
// lives in a separate package once the driver dependency is introduced,
// but the semantic contract (version conflict, archived → write rejected,
// delta ordering) is enforced here so the compliance test matrix can run
// without a DB.
type MemPlanStore struct {
	mu      sync.RWMutex
	plans   map[int64]*memPlanEntry
	idSeq   atomic.Int64
	nowFunc func() time.Time
}

// memPlanEntry bundles a BrainPlan snapshot with its full delta history.
// The delta slice is append-only; ListByRun / Get clone the snapshot
// before returning so callers cannot mutate internal state.
type memPlanEntry struct {
	plan   *BrainPlan
	deltas []*BrainPlanDelta
}

// NewMemPlanStore builds a MemPlanStore with a deterministic clock for
// tests. Passing nil defaults to time.Now in UTC.
func NewMemPlanStore(now func() time.Time) *MemPlanStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemPlanStore{
		plans:   make(map[int64]*memPlanEntry),
		nowFunc: now,
	}
}

// Create inserts a new BrainPlan at version 1. The caller MAY supply
// plan.ID to keep external snowflake ids stable; a zero ID triggers the
// internal monotonic sequence. Version is always clamped to 1 because
// the v1 protocol requires brand-new plans to start fresh per §5.2.
func (s *MemPlanStore) Create(ctx context.Context, plan *BrainPlan) (int64, error) {
	if plan == nil {
		return 0, brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemPlanStore.Create: plan is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return 0, wrapCtxErr(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := plan.ID
	if id == 0 {
		id = s.idSeq.Add(1)
	} else if _, exists := s.plans[id]; exists {
		return 0, brainerrors.New(brainerrors.CodeDBUniqueViolation,
			brainerrors.WithMessage(fmt.Sprintf("MemPlanStore.Create: plan id %d already exists", id)),
		)
	}

	now := s.nowFunc()
	state := plan.CurrentState
	if len(state) == 0 {
		state = json.RawMessage("{}")
	}
	snapshot := &BrainPlan{
		ID:           id,
		RunID:        plan.RunID,
		BrainID:      plan.BrainID,
		Version:      1,
		CurrentState: append(json.RawMessage(nil), state...),
		Archived:     false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.plans[id] = &memPlanEntry{plan: snapshot}
	return id, nil
}

// Get returns a defensive copy of the snapshot for id so mutations on
// the returned struct cannot reach back into the store.
func (s *MemPlanStore) Get(ctx context.Context, id int64) (*BrainPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.plans[id]
	if !ok {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemPlanStore.Get: plan id %d not found", id)),
		)
	}
	return clonePlan(entry.plan), nil
}

// Update atomically appends delta to the journal and advances the
// snapshot version. The transaction contract from 26 §4.4 / §9.1 is
// upheld by holding the write lock for the full operation:
//
//  1. Reject archived plans with ClassUserFault.
//  2. Enforce optimistic locking: delta.Version MUST equal
//     plan.Version + 1, otherwise return CodeDBDeadlock so callers can
//     re-read and retry per §5.3.
//  3. Append the delta to the journal.
//  4. Refresh CurrentState (if delta carries a non-empty payload).
//  5. Stamp UpdatedAt.
//
// On any failure the in-memory state is untouched, matching SQLite /
// MySQL rollback semantics.
func (s *MemPlanStore) Update(ctx context.Context, id int64, delta *BrainPlanDelta) error {
	if delta == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemPlanStore.Update: delta is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.plans[id]
	if !ok {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemPlanStore.Update: plan id %d not found", id)),
		)
	}
	if entry.plan.Archived {
		return brainerrors.New(brainerrors.CodeWorkflowPrecondition,
			brainerrors.WithMessage(fmt.Sprintf("MemPlanStore.Update: plan %d is archived", id)),
		)
	}

	wantVersion := entry.plan.Version + 1
	if delta.Version != wantVersion {
		return brainerrors.New(brainerrors.CodeDBDeadlock,
			brainerrors.WithMessage(fmt.Sprintf(
				"MemPlanStore.Update: optimistic-lock mismatch plan=%d have=%d delta=%d",
				id, entry.plan.Version, delta.Version,
			)),
		)
	}

	// Commit — append delta, advance snapshot, stamp UpdatedAt.
	now := s.nowFunc()
	committedDelta := &BrainPlanDelta{
		ID:        s.idSeq.Add(1),
		PlanID:    id,
		Version:   wantVersion,
		OpType:    delta.OpType,
		Payload:   append(json.RawMessage(nil), delta.Payload...),
		Actor:     delta.Actor,
		CreatedAt: now,
	}
	if delta.ID != 0 {
		committedDelta.ID = delta.ID
	}
	entry.deltas = append(entry.deltas, committedDelta)
	entry.plan.Version = wantVersion
	if len(delta.Payload) > 0 {
		entry.plan.CurrentState = append(json.RawMessage(nil), delta.Payload...)
	}
	entry.plan.UpdatedAt = now
	return nil
}

// ListByRun returns a snapshot slice of every plan owned by runID.
// The returned order is deterministic: ascending plan ID.
func (s *MemPlanStore) ListByRun(ctx context.Context, runID int64) ([]*BrainPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*BrainPlan, 0)
	for _, entry := range s.plans {
		if entry.plan.RunID == runID {
			out = append(out, clonePlan(entry.plan))
		}
	}
	// Deterministic order without importing sort — small n, insertion sort.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ID > out[j].ID; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out, nil
}

// Archive marks id read-only. Subsequent Update calls MUST return a
// ClassUserFault BrainError per §5.
func (s *MemPlanStore) Archive(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.plans[id]
	if !ok {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("MemPlanStore.Archive: plan id %d not found", id)),
		)
	}
	entry.plan.Archived = true
	entry.plan.UpdatedAt = s.nowFunc()
	return nil
}

// DeltasFor returns a copy of the delta journal for id. Exposed as a
// package-local helper for the compliance tests that need to inspect
// the journal directly; not part of the PlanStore interface.
func (s *MemPlanStore) DeltasFor(id int64) []*BrainPlanDelta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.plans[id]
	if !ok {
		return nil
	}
	out := make([]*BrainPlanDelta, len(entry.deltas))
	for i, d := range entry.deltas {
		copy := *d
		if len(d.Payload) > 0 {
			copy.Payload = append(json.RawMessage(nil), d.Payload...)
		}
		out[i] = &copy
	}
	return out
}

func clonePlan(p *BrainPlan) *BrainPlan {
	if p == nil {
		return nil
	}
	out := *p
	if len(p.CurrentState) > 0 {
		out.CurrentState = append(json.RawMessage(nil), p.CurrentState...)
	}
	return &out
}

// wrapCtxErr converts a context cancellation / deadline into a BrainError
// so callers get a consistent typed error.
func wrapCtxErr(err error) *brainerrors.BrainError {
	return brainerrors.Wrap(err, brainerrors.CodeDeadlineExceeded,
		brainerrors.WithMessage("persistence operation aborted by context"),
	)
}
