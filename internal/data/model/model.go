package model

import "github.com/leef-l/brain/internal/quantcontracts"

const (
	BrainKindData = string(quantcontracts.KindData)

	ToolGetSnapshot      = quantcontracts.ToolDataGetSnapshot
	ToolGetFeatureVector = quantcontracts.ToolDataGetFeatureVector
	ToolGetCandles       = quantcontracts.ToolDataGetCandles
	ToolGetSimilar       = quantcontracts.ToolDataGetSimilarPatterns
	ToolProviderHealth   = quantcontracts.ToolDataProviderHealth
	ToolValidationStats  = "data.validation_stats"
	ToolReplayStart      = "data.replay_start"
	ToolReplayStop       = "data.replay_stop"
)

const (
	ProviderStateStarting = "starting"
	ProviderStateSyncing  = "syncing"
	ProviderStateActive   = "active"
	ProviderStateDegraded = "degraded"
	ProviderStateStopped  = "stopped"
)

const (
	HealthStateHealthy  = "healthy"
	HealthStateDegraded = "degraded"
	HealthStateStopped  = "stopped"
)

const (
	ValidationAccept = "accept"
	ValidationReject = "reject"
	ValidationSkip   = "skip"
)

type MarketEvent struct {
	Provider  string  `json:"provider"`
	Topic     string  `json:"topic"`
	Symbol    string  `json:"symbol"`
	Kind      string  `json:"kind,omitempty"`
	Sequence  uint64  `json:"sequence,omitempty"`
	Timestamp int64   `json:"timestamp,omitempty"`
	Price     float64 `json:"price,omitempty"`
	Volume    float64 `json:"volume,omitempty"`
	Digest    uint64  `json:"digest,omitempty"`
	Payload   []byte  `json:"payload,omitempty"`
}

type Candle struct {
	Timestamp int64   `json:"timestamp"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
	Volume    float64 `json:"volume"`
}

type MarketSnapshot struct {
	Provider       string              `json:"provider"`
	Topic          string              `json:"topic"`
	Symbol         string              `json:"symbol"`
	Kind           string              `json:"kind,omitempty"`
	SourceSeq      uint64              `json:"source_seq,omitempty"`
	WriteSeq       uint64              `json:"write_seq,omitempty"`
	Timestamp      int64               `json:"timestamp,omitempty"`
	Price          float64             `json:"price,omitempty"`
	Volume         float64             `json:"volume,omitempty"`
	FeatureVector  []float64           `json:"feature_vector,omitempty"`
	Candles        map[string][]Candle `json:"candles,omitempty"`
	ProviderState  string              `json:"provider_state,omitempty"`
	Validation     string              `json:"validation,omitempty"`
	ValidationNote string              `json:"validation_note,omitempty"`
	SourceDigest   uint64              `json:"source_digest,omitempty"`
}

type ValidationResult struct {
	Accepted bool   `json:"accepted"`
	Action   string `json:"action"`
	Reason   string `json:"reason,omitempty"`
	Stage    string `json:"stage,omitempty"`
	Key      string `json:"key,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
}

type ValidationStats struct {
	Accepted uint64 `json:"accepted"`
	Rejected uint64 `json:"rejected"`
	Skipped  uint64 `json:"skipped"`
}

type ProviderHealth struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	Detail       string `json:"detail,omitempty"`
	LastSequence uint64 `json:"last_sequence,omitempty"`
	LagMs        int64  `json:"lag_ms,omitempty"`
}

type Health struct {
	State           string           `json:"state"`
	Message         string           `json:"message,omitempty"`
	LatestSequence  uint64           `json:"latest_sequence,omitempty"`
	ActiveProviders int              `json:"active_providers,omitempty"`
	KnownProviders  int              `json:"known_providers,omitempty"`
	ValidationStats ValidationStats  `json:"validation_stats,omitempty"`
	ProviderHealths []ProviderHealth `json:"provider_healths,omitempty"`
}

type SnapshotQuery struct {
	Symbol    string `json:"symbol"`
	Timeframe string `json:"timeframe,omitempty"`
}

type FeatureVectorQuery struct {
	Symbol string `json:"symbol"`
}

type CandleQuery struct {
	Symbol    string `json:"symbol"`
	Timeframe string `json:"timeframe"`
}

type IngestRequest struct {
	Provider  string  `json:"provider"`
	Topic     string  `json:"topic"`
	Symbol    string  `json:"symbol"`
	Kind      string  `json:"kind,omitempty"`
	Sequence  uint64  `json:"sequence,omitempty"`
	Timestamp int64   `json:"timestamp,omitempty"`
	Price     float64 `json:"price,omitempty"`
	Volume    float64 `json:"volume,omitempty"`
	Digest    uint64  `json:"digest,omitempty"`
	Payload   []byte  `json:"payload,omitempty"`
}
