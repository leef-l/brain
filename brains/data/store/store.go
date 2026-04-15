// Package store defines the storage interfaces and types for the Data Brain.
// It deliberately does NOT import the provider package to avoid circular dependencies.
package store

import "context"

// Candle represents a single K-line bar. Fields mirror provider.Candle exactly,
// but the type lives in the store package to break the import cycle.
type Candle struct {
	InstID    string
	Bar       string // "1m", "5m", "15m", "1H", "4H"
	Timestamp int64  // milliseconds since epoch
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	VolumeCcy float64
}

// FeatureVector holds a serialised feature vector written by the Feature Engine.
type FeatureVector struct {
	Collection string
	InstID     string
	Timeframe  string
	Timestamp  int64
	Vector     []byte // serialised []float64
	Metadata   map[string]any
}

// BackfillProgress tracks the backfill checkpoint for a given instrument+timeframe.
type BackfillProgress struct {
	InstID    string
	Timeframe string
	LatestTS  int64 // oldest edge: how far back we've filled
	NewestTS  int64 // newest edge: the "now" when backfill first started
	BarCount  int
}

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

// CandleStore provides CRUD operations on K-line data.
type CandleStore interface {
	BatchInsert(ctx context.Context, candles []Candle) error
	Upsert(ctx context.Context, c Candle) error
	QueryRange(ctx context.Context, instID, bar string, from, to int64) ([]Candle, error)
	LatestTimestamp(ctx context.Context, instID, bar string) (int64, error)
	DeleteBefore(ctx context.Context, bar string, before int64) error
}

// VectorStore provides CRUD operations on feature vectors.
type VectorStore interface {
	Insert(ctx context.Context, vec FeatureVector) error
	QueryLatest(ctx context.Context, collection, instID, timeframe string) (*FeatureVector, error)
	DeleteVectorsBefore(ctx context.Context, collection string, before int64) error
}

// BackfillStore tracks backfill progress (checkpoint / resume).
type BackfillStore interface {
	GetProgress(ctx context.Context, instID, timeframe string) (*BackfillProgress, error)
	SaveProgress(ctx context.Context, p BackfillProgress) error
}

// AlertRecord represents a data quality alert to be persisted.
type AlertRecord struct {
	Level     string // "warning", "critical"
	AlertType string // "price_spike", "gap", "stale", "future_ts"
	Symbol    string
	Detail    string
	EventTS   int64 // milliseconds since epoch
}

// ActiveInstrumentRecord represents an instrument snapshot during a refresh.
type ActiveInstrumentRecord struct {
	InstID      string
	VolUSDT24h  float64
	Rank        int
	RefreshedAt int64 // milliseconds since epoch
}

// AlertStore persists validator alerts.
type AlertStore interface {
	InsertAlert(ctx context.Context, a AlertRecord) error
}

// ActiveInstrumentStore persists active instrument snapshots.
type ActiveInstrumentStore interface {
	InsertActiveInstruments(ctx context.Context, records []ActiveInstrumentRecord) error
}

// Store is the aggregate interface that every concrete backend must implement.
type Store interface {
	CandleStore
	VectorStore
	BackfillStore
	AlertStore
	ActiveInstrumentStore
	Migrate(ctx context.Context) error // idempotent schema creation
	Close() error
}
