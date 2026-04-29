// Package data contains configuration types for the Data Brain.
package data

import "time"

// Config is the top-level configuration for the Data Brain.
type Config struct {
	ActiveList ActiveListConfig  `json:"active_list" yaml:"active_list"`
	Providers  []ProviderConfig  `json:"providers" yaml:"providers"`
	Backfill   BackfillConfig    `json:"backfill" yaml:"backfill"`
	Validation     ValidationConfig     `json:"validation" yaml:"validation"`
	RingBuffer     RingBufferConfig     `json:"ring_buffer" yaml:"ring_buffer"`
	Feature        FeatureConfig        `json:"feature" yaml:"feature"`
	CircuitBreaker CircuitBreakerConfig `json:"circuit_breaker" yaml:"circuit_breaker"`
}

// ActiveListConfig controls which instruments are actively tracked.
type ActiveListConfig struct {
	MinVolume24h     float64       `json:"min_volume_24h" yaml:"min_volume_24h"`
	MaxInstruments   int           `json:"max_instruments" yaml:"max_instruments"`
	UpdateInterval   time.Duration `json:"update_interval" yaml:"update_interval"`
	AlwaysInclude    []string      `json:"always_include" yaml:"always_include"`
	RankByVolatility bool          `json:"rank_by_volatility" yaml:"rank_by_volatility"`
	MinAmplitudePct  float64       `json:"min_amplitude_pct" yaml:"min_amplitude_pct"`
}

// ProviderConfig describes one data provider instance.
type ProviderConfig struct {
	Name   string         `json:"name" yaml:"name"`
	Type   string         `json:"type" yaml:"type"`
	Params map[string]any `json:"params" yaml:"params"`
}

// BackfillConfig controls historical data back-filling.
type BackfillConfig struct {
	Enabled    bool          `json:"enabled" yaml:"enabled"`
	MaxDays    int           `json:"max_days" yaml:"max_days"`
	RateLimit  time.Duration `json:"rate_limit" yaml:"rate_limit"`
	BatchSize  int           `json:"batch_size" yaml:"batch_size"`
	Concurrent int           `json:"concurrent" yaml:"concurrent"`
}

// ValidationConfig sets data quality thresholds.
type ValidationConfig struct {
	MaxGapDuration time.Duration `json:"max_gap_duration" yaml:"max_gap_duration"`
	MaxPriceJump   float64      `json:"max_price_jump" yaml:"max_price_jump"`
	StaleTimeout   time.Duration `json:"stale_timeout" yaml:"stale_timeout"`
}

// RingBufferConfig sizes the in-memory ring buffers.
type RingBufferConfig struct {
	CandleDepth    int `json:"candle_depth" yaml:"candle_depth"`
	TradeDepth     int `json:"trade_depth" yaml:"trade_depth"`
	OrderBookDepth int `json:"order_book_depth" yaml:"order_book_depth"`
}

// CircuitBreakerConfig holds the configuration for the data brain
// circuit breaker. When validation rejection rate exceeds Threshold
// for at least Duration, the circuit breaker opens and data
// collection pauses. After Cooldown the breaker half-opens; if
// health checks pass it fully closes.
type CircuitBreakerConfig struct {
	Enabled   bool          `json:"enabled" yaml:"enabled"`
	Threshold float64       `json:"threshold" yaml:"threshold"` // rejection rate, e.g. 0.30
	Duration  time.Duration `json:"duration" yaml:"duration"`   // e.g. 60s
	Cooldown  time.Duration `json:"cooldown" yaml:"cooldown"`   // e.g. 30s
}

// FeatureConfig controls real-time feature computation.
type FeatureConfig struct {
	Enabled  bool          `json:"enabled" yaml:"enabled"`
	Windows  []int         `json:"windows" yaml:"windows"`
	Interval time.Duration `json:"interval" yaml:"interval"`
}
