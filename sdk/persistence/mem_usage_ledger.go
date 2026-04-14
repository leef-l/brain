package persistence

import (
	"context"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// MemUsageLedger is the in-process UsageLedger from 26-持久化与恢复.md §8.
// The ledger is an append-only log of UsageRecord entries keyed by
// IdempotencyKey; Record is a strict no-op when the same key is written
// a second time (§8.3 row "Cost.Charge: 显式 idempotency_key"). Sum
// aggregates all records for a RunID, skipping the TurnIndex field per
// §8.
//
// The idempotency guarantee is the anchor that lets Resume replay a
// crashed Run without double-charging: the replay emits the same
// IdempotencyKey as the original call, so the second Record returns
// success without touching the counters — satisfying C-P-14.
//
// Stdlib-only per brain骨架实施计划.md §4.6.
type MemUsageLedger struct {
	mu        sync.Mutex
	rows      []*UsageRecord
	byKey     map[string]int // index into rows
	nowFunc   func() time.Time
}

// NewMemUsageLedger builds a MemUsageLedger. A nil clock defaults to UTC now.
func NewMemUsageLedger(now func() time.Time) *MemUsageLedger {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemUsageLedger{
		byKey:   make(map[string]int),
		nowFunc: now,
	}
}

// Record inserts rec. If a row with the same IdempotencyKey already
// exists the call is a no-op — the rec argument is entirely ignored and
// no error is returned. This is the "naturally idempotent" flavor of
// §8.2, implemented via an explicit unique-key index so duplicate
// detection is O(1).
func (l *MemUsageLedger) Record(ctx context.Context, rec *UsageRecord) error {
	if rec == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemUsageLedger.Record: rec is nil"),
		)
	}
	if rec.IdempotencyKey == "" {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("MemUsageLedger.Record: IdempotencyKey is required"),
		)
	}
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.byKey[rec.IdempotencyKey]; exists {
		// Strict idempotency: do not overwrite anything, do not error.
		return nil
	}
	stored := *rec
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = l.nowFunc()
	}
	l.byKey[rec.IdempotencyKey] = len(l.rows)
	l.rows = append(l.rows, &stored)
	return nil
}

// Sum returns the aggregate counters for runID. The returned UsageRecord
// sets TurnIndex = -1 to signal an aggregate row per the contract in
// UsageLedger.Sum's docstring, and leaves Provider/Model empty because
// an aggregate can span multiple pairs.
func (l *MemUsageLedger) Sum(ctx context.Context, runID int64) (*UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	agg := &UsageRecord{
		RunID:     runID,
		TurnIndex: -1,
	}
	for _, row := range l.rows {
		if row.RunID != runID {
			continue
		}
		agg.InputTokens += row.InputTokens
		agg.OutputTokens += row.OutputTokens
		agg.CacheRead += row.CacheRead
		agg.CacheCreation += row.CacheCreation
		agg.CostUSD += row.CostUSD
	}
	return agg, nil
}

// Count returns the number of stored rows. Not part of the UsageLedger
// interface — used by the compliance tests to assert that duplicate
// Record calls did not create extra rows.
func (l *MemUsageLedger) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.rows)
}
