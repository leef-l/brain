// Package data contains configuration types for the Data Brain.
package data

import "time"

// Config is the top-level configuration for the Data Brain.
type Config struct {
	ActiveList ActiveListConfig
	Providers  []ProviderConfig
	Backfill   BackfillConfig
	Validation ValidationConfig
	RingBuffer RingBufferConfig
	Feature    FeatureConfig
}

// ActiveListConfig controls which instruments are actively tracked.
type ActiveListConfig struct {
	MinVolume24h   float64       // minimum 24h volume to include
	MaxInstruments int           // cap on number of instruments
	UpdateInterval time.Duration // how often to refresh the active list
	AlwaysInclude  []string      // instruments to always track regardless of volume
}

// ProviderConfig describes one data provider instance.
type ProviderConfig struct {
	Name   string
	Type   string         // e.g. "okx-swap"
	Params map[string]any // provider-specific configuration
}

// BackfillConfig controls historical data back-filling.
type BackfillConfig struct {
	Enabled    bool
	MaxDays    int           // how many days of history to fill
	RateLimit  time.Duration // minimum delay between REST calls
	BatchSize  int           // number of candles per request
	Concurrent int           // parallel backfill workers
}

// ValidationConfig sets data quality thresholds.
type ValidationConfig struct {
	MaxGapDuration time.Duration // max acceptable gap in candle series
	MaxPriceJump   float64      // max single-bar price change ratio (e.g. 0.15 = 15%)
	StaleTimeout   time.Duration // mark provider stale after this silence
}

// RingBufferConfig sizes the in-memory ring buffers.
type RingBufferConfig struct {
	CandleDepth    int // number of candles per (instID, bar) to keep
	TradeDepth     int // number of recent trades per instrument
	OrderBookDepth int // number of order-book snapshots to keep
}

// FeatureConfig controls real-time feature computation.
type FeatureConfig struct {
	Enabled  bool
	Windows  []int // rolling window sizes (number of bars)
	Interval time.Duration // feature re-computation interval
}
