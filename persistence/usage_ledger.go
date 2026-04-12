package persistence

import (
	"context"
	"time"
)

// UsageRecord is a single token / cost accounting entry defined in
// 26-持久化与恢复.md §8.
//
// Every LLM call MUST write exactly one UsageRecord keyed by
// IdempotencyKey so retries and Resume replays do not double-count
// spend. The aggregate UsageLedger.Sum query returns a synthetic
// UsageRecord whose TurnIndex field is unused and whose counters are
// the per-run totals.
type UsageRecord struct {
	// RunID is the snowflake id of the BrainKernel run.
	RunID int64

	// TurnIndex is the zero-based index of the turn that produced the
	// call. -1 in aggregate rows returned by Sum.
	TurnIndex int

	// Provider is the LLM provider key (e.g. "anthropic", "openai").
	Provider string

	// Model is the provider-specific model id string.
	Model string

	// InputTokens is the billed input token count.
	InputTokens int64

	// OutputTokens is the billed output token count.
	OutputTokens int64

	// CacheRead is the cache-hit token count (prompt caching).
	CacheRead int64

	// CacheCreation is the cache-write token count (prompt caching).
	CacheCreation int64

	// CostUSD is the computed cost for the call in USD, rounded to
	// the provider's billing precision.
	CostUSD float64

	// IdempotencyKey is the unique key that de-duplicates retries.
	// Record MUST be a no-op when a row with the same key already
	// exists. See 26 §8.
	IdempotencyKey string

	// CreatedAt is the insertion timestamp.
	CreatedAt time.Time
}

// UsageLedger is the v1 interface for token / cost accounting defined
// in 26-持久化与恢复.md §8. Record MUST be idempotent on IdempotencyKey
// and Sum MUST return a stable aggregate so the budget circuit breaker
// can rely on it.
type UsageLedger interface {
	// Record inserts rec, or silently returns nil if a row with the
	// same IdempotencyKey already exists. See 26 §8.
	Record(ctx context.Context, rec *UsageRecord) error

	// Sum returns the aggregate token / cost counters for runID. The
	// returned UsageRecord uses TurnIndex = -1 to signal an aggregate
	// row and populates Provider / Model with empty strings unless
	// the backend knows the run used a single pair. See 26 §8.
	Sum(ctx context.Context, runID int64) (*UsageRecord, error)
}
