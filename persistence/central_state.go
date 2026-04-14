package persistence

import (
	"context"
	"encoding/json"
	"time"
)

// CentralState captures the storage-facing Central brain runtime state.
// It keeps the persistence boundary decoupled from the in-memory service
// packages so persistence drivers do not import internal/central types.
type CentralState struct {
	Control   CentralControlState `json:"control"`
	Review    CentralReviewState  `json:"review"`
	UpdatedAt time.Time           `json:"updated_at"`
}

type CentralControlState struct {
	TradingPaused        bool              `json:"trading_paused"`
	PauseReason          string            `json:"pause_reason,omitempty"`
	PausedInstruments    map[string]string `json:"paused_instruments,omitempty"`
	LastAction           string            `json:"last_action,omitempty"`
	LastReason           string            `json:"last_reason,omitempty"`
	LastConfigScope      string            `json:"last_config_scope,omitempty"`
	LastConfigPatch      json.RawMessage   `json:"last_config_patch,omitempty"`
	LastReviewOutcome    string            `json:"last_review_outcome,omitempty"`
	LastReviewReason     string            `json:"last_review_reason,omitempty"`
	LastReviewSizeFactor float64           `json:"last_review_size_factor,omitempty"`
	UpdatedAtMS          int64             `json:"updated_at_ms,omitempty"`
}

type CentralReviewState struct {
	LastRequest  json.RawMessage `json:"last_request,omitempty"`
	LastDecision json.RawMessage `json:"last_decision,omitempty"`
	LastRunAtMS  int64           `json:"last_run_at_ms,omitempty"`
}

// CentralStateStore persists the latest Central brain control/review state.
type CentralStateStore interface {
	Save(ctx context.Context, state *CentralState) error
	Get(ctx context.Context) (*CentralState, error)
}

func cloneCentralState(state *CentralState) *CentralState {
	if state == nil {
		return nil
	}
	out := *state
	if len(state.Control.PausedInstruments) > 0 {
		out.Control.PausedInstruments = make(map[string]string, len(state.Control.PausedInstruments))
		for symbol, reason := range state.Control.PausedInstruments {
			out.Control.PausedInstruments[symbol] = reason
		}
	}
	if len(state.Control.LastConfigPatch) > 0 {
		out.Control.LastConfigPatch = append(json.RawMessage(nil), state.Control.LastConfigPatch...)
	}
	if len(state.Review.LastRequest) > 0 {
		out.Review.LastRequest = append(json.RawMessage(nil), state.Review.LastRequest...)
	}
	if len(state.Review.LastDecision) > 0 {
		out.Review.LastDecision = append(json.RawMessage(nil), state.Review.LastDecision...)
	}
	return &out
}
