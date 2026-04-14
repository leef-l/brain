package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// SignalTrace is the audit record owned by Quant Brain.
type SignalTrace struct {
	TraceID           string          `json:"trace_id"`
	Symbol            string          `json:"symbol"`
	SnapshotSeq       uint64          `json:"snapshot_seq"`
	Direction         string          `json:"direction,omitempty"`
	Outcome           string          `json:"outcome"`
	Price             float64         `json:"price,omitempty"`
	Confidence        float64         `json:"confidence,omitempty"`
	DominantStrategy  string          `json:"dominant_strategy,omitempty"`
	GlobalRiskAllowed bool            `json:"global_risk_allowed,omitempty"`
	GlobalRiskReason  string          `json:"global_risk_reason,omitempty"`
	RejectedStage     string          `json:"rejected_stage,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	ReviewRequested   bool            `json:"review_requested,omitempty"`
	ReviewApproved    bool            `json:"review_approved,omitempty"`
	ReviewReason      string          `json:"review_reason,omitempty"`
	ReviewSizeFactor  float64         `json:"review_size_factor,omitempty"`
	DraftCandidates   json.RawMessage `json:"draft_candidates,omitempty"`
	AccountResults    json.RawMessage `json:"account_results,omitempty"`
	PlanJSON          json.RawMessage `json:"plan_json,omitempty"`
	CreatedAt         int64           `json:"created_at"`
	UpdatedAt         int64           `json:"updated_at"`
}

// QueryFilter keeps trace lookup intentionally narrow.
type QueryFilter struct {
	Symbol string
	Limit  int
}

// Store persists SignalTrace records in memory.
type Store interface {
	Save(ctx context.Context, trace *SignalTrace) error
	Query(ctx context.Context, filter QueryFilter) ([]SignalTrace, error)
}

// MemoryStore is the minimal audit backend for the Quant skeleton.
type MemoryStore struct {
	mu     sync.RWMutex
	traces []SignalTrace
	nextID uint64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (s *MemoryStore) Save(_ context.Context, trace *SignalTrace) error {
	if trace == nil {
		return fmt.Errorf("trace is nil")
	}
	now := time.Now().UTC().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	if trace.TraceID == "" {
		trace.TraceID = fmt.Sprintf("trace-%06d", s.nextID)
	}
	if trace.CreatedAt == 0 {
		trace.CreatedAt = now
	}
	trace.UpdatedAt = now
	copyTrace := *trace
	copyTrace.DraftCandidates = append(json.RawMessage(nil), trace.DraftCandidates...)
	copyTrace.AccountResults = append(json.RawMessage(nil), trace.AccountResults...)
	copyTrace.PlanJSON = append(json.RawMessage(nil), trace.PlanJSON...)
	s.traces = append(s.traces, copyTrace)
	return nil
}

func (s *MemoryStore) Query(_ context.Context, filter QueryFilter) ([]SignalTrace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := filter.Limit
	if limit <= 0 || limit > len(s.traces) {
		limit = len(s.traces)
	}
	out := make([]SignalTrace, 0, limit)
	for i := len(s.traces) - 1; i >= 0 && len(out) < limit; i-- {
		trace := s.traces[i]
		if filter.Symbol != "" && trace.Symbol != filter.Symbol {
			continue
		}
		trace.DraftCandidates = append(json.RawMessage(nil), trace.DraftCandidates...)
		trace.AccountResults = append(json.RawMessage(nil), trace.AccountResults...)
		trace.PlanJSON = append(json.RawMessage(nil), trace.PlanJSON...)
		out = append(out, trace)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}
