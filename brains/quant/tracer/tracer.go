// Package tracer provides decision audit trail persistence for the quant brain.
// Every trade signal evaluation is recorded as a SignalTrace, forming a complete
// chain: market data → strategy signals → aggregation → global risk → per-account execution.
package tracer

import (
	"context"
	"time"

	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
)

// SignalTrace records one complete decision cycle for a single symbol.
type SignalTrace struct {
	TraceID   string    `json:"trace_id"`
	Timestamp time.Time `json:"timestamp"`
	Symbol    string    `json:"symbol"`

	// Data snapshot reference
	SnapshotSeq uint64    `json:"snapshot_seq"`
	Price       float64   `json:"price"`
	Features    []float64 `json:"features,omitempty"` // 192-dim vector at decision time

	// Individual strategy outputs
	Signals []strategy.Signal `json:"signals"`

	// Aggregated result
	Aggregated strategy.AggregatedSignal `json:"aggregated"`

	// Global risk decision
	GlobalRisk risk.Decision `json:"global_risk"`

	// Per-account execution results
	AccountResults []AccountTraceResult `json:"account_results,omitempty"`

	// Final outcome
	Outcome string `json:"outcome"` // "executed", "rejected_risk", "rejected_global", "needs_review", "hold"
}

// AccountTraceResult records one account's handling of a signal.
type AccountTraceResult struct {
	AccountID  string        `json:"account_id"`
	UnitID     string        `json:"unit_id"`
	RiskResult risk.Decision `json:"risk_result"`
	OrderID    string        `json:"order_id,omitempty"`
	Status     string        `json:"status"` // "filled", "rejected", "skipped"
	Quantity   float64       `json:"quantity,omitempty"`
	Latency    time.Duration `json:"latency"`
}

// TraceFilter constrains which traces to query.
type TraceFilter struct {
	Symbol  string
	Outcome string
	Since   time.Time
	Limit   int
}

// Store is the trace persistence interface.
type Store interface {
	// Save persists a signal trace.
	Save(ctx context.Context, trace *SignalTrace) error

	// Query returns traces matching the filter.
	Query(ctx context.Context, filter TraceFilter) ([]SignalTrace, error)

	// Count returns the total number of stored traces.
	Count(ctx context.Context) (int64, error)
}
