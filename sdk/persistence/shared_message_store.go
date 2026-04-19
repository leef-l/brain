package persistence

import (
	"context"
	"encoding/json"
	"time"
)

// SharedMessage is a persisted cross-brain context transfer record.
type SharedMessage struct {
	ID        int64           `json:"id"`
	FromBrain string          `json:"from_brain"`
	ToBrain   string          `json:"to_brain"`
	Messages  json.RawMessage `json:"messages"`
	Count     int             `json:"count"`
	CreatedAt time.Time       `json:"created_at"`
}

// SharedMessageStore persists cross-brain context transfers.
type SharedMessageStore interface {
	Save(ctx context.Context, msg *SharedMessage) error
	ListByBrains(ctx context.Context, from, to string, limit int) ([]*SharedMessage, error)
	ListRecent(ctx context.Context, limit int) ([]*SharedMessage, error)
}
