package persistence

import (
	"context"
	"encoding/json"
	"time"
)

// RunEvent represents a lifecycle event within a run.
type RunEvent struct {
	At      time.Time       `json:"at"`
	Type    string          `json:"type"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Run represents a single execution run tracked by the system.
type Run struct {
	ID        int64           `json:"id"`
	RunKey    string          `json:"run_key"`
	BrainID   string          `json:"brain_id"`
	Prompt    string          `json:"prompt,omitempty"`
	Status    string          `json:"status"`
	Mode      string          `json:"mode,omitempty"`
	Workdir   string          `json:"workdir,omitempty"`
	TurnUUID  string          `json:"turn_uuid,omitempty"`
	PlanID    int64           `json:"plan_id,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Events    []RunEvent      `json:"events,omitempty"`
}

// RunStore persists run metadata and lifecycle events.
type RunStore interface {
	Create(ctx context.Context, run *Run) (int64, error)
	Get(ctx context.Context, id int64) (*Run, error)
	GetByKey(ctx context.Context, runKey string) (*Run, error)
	Update(ctx context.Context, id int64, mutate func(*Run)) error
	AppendEvent(ctx context.Context, id int64, ev RunEvent) error
	Finish(ctx context.Context, id int64, status string, result json.RawMessage, errText string) error
	List(ctx context.Context, limit int, status string) ([]*Run, error)
}
