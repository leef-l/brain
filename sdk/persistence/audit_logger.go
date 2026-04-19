package persistence

import (
	"context"
	"encoding/json"
	"time"
)

// AuditEvent represents a single audit trail entry.
type AuditEvent struct {
	ID          int64           `json:"id"`
	EventID     string          `json:"event_id"`
	ExecutionID string          `json:"execution_id"`
	EventType   string          `json:"event_type"`
	Actor       string          `json:"actor"`
	Timestamp   time.Time       `json:"timestamp"`
	Data        json.RawMessage `json:"data,omitempty"`
	StatusCode  string          `json:"status_code"`
	Details     string          `json:"details,omitempty"`
}

// AuditFilter specifies query criteria for audit events.
type AuditFilter struct {
	ExecutionID string
	EventType   string
	Actor       string
	Since       time.Time
	Until       time.Time
	Limit       int
}

// AuditLogger persists and queries audit trail events.
type AuditLogger interface {
	Log(ctx context.Context, ev *AuditEvent) error
	Query(ctx context.Context, filter AuditFilter) ([]*AuditEvent, error)
	Purge(ctx context.Context, olderThanDays int) (int64, error)
}
