package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/persistence"
)

// PersistentStore adapts the shared persistence signal trace backend to the
// quant-layer audit contract.
type PersistentStore struct {
	backend persistence.SignalTraceStore
}

func NewPersistentStore(backend persistence.SignalTraceStore) *PersistentStore {
	if backend == nil {
		return nil
	}
	return &PersistentStore{backend: backend}
}

func (s *PersistentStore) Save(ctx context.Context, trace *SignalTrace) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("persistent signal trace store is not configured")
	}
	return s.backend.Save(ctx, toPersistentTrace(trace))
}

func (s *PersistentStore) Query(ctx context.Context, filter QueryFilter) ([]SignalTrace, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("persistent signal trace store is not configured")
	}
	rows, err := s.backend.Query(ctx, persistence.SignalTraceFilter{
		Symbol: filter.Symbol,
		Limit:  filter.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SignalTrace, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromPersistentTrace(row))
	}
	return out, nil
}

func toPersistentTrace(trace *SignalTrace) *persistence.SignalTrace {
	if trace == nil {
		return nil
	}
	return &persistence.SignalTrace{
		TraceID:           trace.TraceID,
		Symbol:            trace.Symbol,
		SnapshotSeq:       trace.SnapshotSeq,
		Outcome:           trace.Outcome,
		Price:             trace.Price,
		Direction:         trace.Direction,
		Confidence:        trace.Confidence,
		DominantStrategy:  trace.DominantStrategy,
		GlobalRiskAllowed: trace.GlobalRiskAllowed,
		GlobalRiskReason:  trace.GlobalRiskReason,
		ReviewRequested:   trace.ReviewRequested,
		ReviewApproved:    trace.ReviewApproved,
		ReviewReason:      trace.ReviewReason,
		ReviewSizeFactor:  trace.ReviewSizeFactor,
		RejectedStage:     trace.RejectedStage,
		Reason:            trace.Reason,
		DraftCandidates:   append(json.RawMessage(nil), trace.DraftCandidates...),
		AccountResults:    append(json.RawMessage(nil), trace.AccountResults...),
		SignalsJSON:       append(json.RawMessage(nil), trace.PlanJSON...),
		CreatedAt:         unixMilliToTime(trace.CreatedAt),
		UpdatedAt:         unixMilliToTime(trace.UpdatedAt),
	}
}

func fromPersistentTrace(row *persistence.SignalTrace) SignalTrace {
	if row == nil {
		return SignalTrace{}
	}
	return SignalTrace{
		TraceID:           row.TraceID,
		Symbol:            row.Symbol,
		SnapshotSeq:       row.SnapshotSeq,
		Price:             row.Price,
		Direction:         row.Direction,
		Outcome:           row.Outcome,
		Confidence:        row.Confidence,
		DominantStrategy:  row.DominantStrategy,
		GlobalRiskAllowed: row.GlobalRiskAllowed,
		GlobalRiskReason:  row.GlobalRiskReason,
		RejectedStage:     row.RejectedStage,
		Reason:            row.Reason,
		ReviewRequested:   row.ReviewRequested,
		ReviewApproved:    row.ReviewApproved,
		ReviewReason:      row.ReviewReason,
		ReviewSizeFactor:  row.ReviewSizeFactor,
		DraftCandidates:   append(json.RawMessage(nil), row.DraftCandidates...),
		AccountResults:    append(json.RawMessage(nil), row.AccountResults...),
		PlanJSON:          append(json.RawMessage(nil), row.SignalsJSON...),
		CreatedAt:         row.CreatedAt.UTC().UnixMilli(),
		UpdatedAt:         row.UpdatedAt.UTC().UnixMilli(),
	}
}

func unixMilliToTime(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}
