package central

import (
	"context"

	"github.com/leef-l/brain/internal/central/control"
	"github.com/leef-l/brain/internal/central/review"
	"github.com/leef-l/brain/internal/central/reviewrun"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/persistence"
)

// API is the minimal service contract expected by the future brain-central
// entrypoint.
type API interface {
	ReviewTrade(ctx context.Context, req quantcontracts.ReviewTradeRequest) (quantcontracts.ReviewDecision, error)
	DataAlert(ctx context.Context, alert quantcontracts.DataAlert) (control.Result, error)
	AccountError(ctx context.Context, err quantcontracts.AccountError) (control.Result, error)
	MacroEvent(ctx context.Context, event quantcontracts.MacroEvent) (control.Result, error)
	EmergencyAction(ctx context.Context, req control.EmergencyActionRequest) (control.Result, error)
	UpdateConfig(ctx context.Context, req control.ConfigUpdateRequest) (control.Result, error)
	RunReview(ctx context.Context, req reviewrun.Request) (reviewrun.Result, error)
	State() State
	Health() Health
}

// Config keeps the central service wiring explicit and framework-free.
type Config struct {
	Review     review.Config
	Control    control.Config
	StateStore persistence.CentralStateStore
}

// State is the aggregated in-memory snapshot owned by the central service.
type State struct {
	Review  review.State  `json:"review"`
	Control control.State `json:"control"`
}

// Health is the compact readiness view for the service layer.
type Health struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
	State  State  `json:"state"`
}
