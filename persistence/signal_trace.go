package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SignalTrace is the persisted audit record for Quant Brain decisions.
//
// The struct is intentionally small and storage-facing only. Higher layers
// may project richer in-memory audit models onto this record, but the
// persistence boundary keeps the fields that are needed for recovery,
// trace lookup, and regression replay.
type SignalTrace struct {
	ID                int64           `json:"id,omitempty"`
	TraceID           string          `json:"trace_id"`
	Symbol            string          `json:"symbol"`
	SnapshotSeq       uint64          `json:"snapshot_seq"`
	Outcome           string          `json:"outcome,omitempty"`
	Price             float64         `json:"price,omitempty"`
	Direction         string          `json:"direction,omitempty"`
	Confidence        float64         `json:"confidence,omitempty"`
	DominantStrategy  string          `json:"dominant_strategy,omitempty"`
	GlobalRiskAllowed bool            `json:"global_risk_allowed,omitempty"`
	GlobalRiskReason  string          `json:"global_risk_reason,omitempty"`
	ReviewRequested   bool            `json:"review_requested,omitempty"`
	ReviewApproved    bool            `json:"review_approved,omitempty"`
	ReviewReason      string          `json:"review_reason,omitempty"`
	ReviewSizeFactor  float64         `json:"review_size_factor,omitempty"`
	RejectedStage     string          `json:"rejected_stage,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	DraftCandidates   json.RawMessage `json:"draft_candidates,omitempty"`
	AccountResults    json.RawMessage `json:"account_results,omitempty"`
	SignalsJSON       json.RawMessage `json:"signals_json,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// SignalTraceFilter keeps trace lookup intentionally narrow so callers do
// not depend on backend-specific query features at the persistence boundary.
type SignalTraceFilter struct {
	Symbol string
	Limit  int
}

// SignalTraceStore persists SignalTrace records.
//
// Save MUST be idempotent on TraceID when the caller replays the same trace.
// Query returns the newest rows first and only supports the narrow filter
// needed by the quant regression skeleton.
type SignalTraceStore interface {
	Save(ctx context.Context, trace *SignalTrace) error
	Get(ctx context.Context, traceID string) (*SignalTrace, error)
	Query(ctx context.Context, filter SignalTraceFilter) ([]*SignalTrace, error)
}

func cloneSignalTrace(trace *SignalTrace) *SignalTrace {
	if trace == nil {
		return nil
	}
	out := *trace
	if len(trace.DraftCandidates) > 0 {
		out.DraftCandidates = append(json.RawMessage(nil), trace.DraftCandidates...)
	}
	if len(trace.AccountResults) > 0 {
		out.AccountResults = append(json.RawMessage(nil), trace.AccountResults...)
	}
	if len(trace.SignalsJSON) > 0 {
		out.SignalsJSON = append(json.RawMessage(nil), trace.SignalsJSON...)
	}
	return &out
}

func nextSignalTraceID(seq int64) string {
	return fmt.Sprintf("trace-%06d", seq)
}
