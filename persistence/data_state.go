package persistence

import (
	"context"
	"encoding/json"
	"time"
)

// DataState captures the storage-facing Data brain fast-path state.
// Snapshots and provider healths stay as raw JSON so persistence drivers
// remain decoupled from internal/data model types.
type DataState struct {
	Snapshots       json.RawMessage    `json:"snapshots,omitempty"`
	ProviderHealths json.RawMessage    `json:"provider_healths,omitempty"`
	Validator       DataValidatorState `json:"validator"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

type DataValidatorState struct {
	LastTS     map[string]int64  `json:"last_ts,omitempty"`
	LastDigest map[string]uint64 `json:"last_digest,omitempty"`
	Accepted   uint64            `json:"accepted,omitempty"`
	Rejected   uint64            `json:"rejected,omitempty"`
	Skipped    uint64            `json:"skipped,omitempty"`
}

// DataStateStore persists the latest Data brain fast-path state.
type DataStateStore interface {
	Save(ctx context.Context, state *DataState) error
	Get(ctx context.Context) (*DataState, error)
}

func cloneDataState(state *DataState) *DataState {
	if state == nil {
		return nil
	}
	out := *state
	if len(state.Snapshots) > 0 {
		out.Snapshots = append(json.RawMessage(nil), state.Snapshots...)
	}
	if len(state.ProviderHealths) > 0 {
		out.ProviderHealths = append(json.RawMessage(nil), state.ProviderHealths...)
	}
	if len(state.Validator.LastTS) > 0 {
		out.Validator.LastTS = make(map[string]int64, len(state.Validator.LastTS))
		for key, value := range state.Validator.LastTS {
			out.Validator.LastTS[key] = value
		}
	}
	if len(state.Validator.LastDigest) > 0 {
		out.Validator.LastDigest = make(map[string]uint64, len(state.Validator.LastDigest))
		for key, value := range state.Validator.LastDigest {
			out.Validator.LastDigest[key] = value
		}
	}
	return &out
}
