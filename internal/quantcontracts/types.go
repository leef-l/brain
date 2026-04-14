package quantcontracts

type Direction string

const (
	DirectionLong  Direction = "long"
	DirectionShort Direction = "short"
	DirectionFlat  Direction = "flat"
)

type SnapshotQuality struct {
	ProviderState string   `json:"provider_state,omitempty"`
	Stale         bool     `json:"stale,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

type MarketSnapshot struct {
	Version         string          `json:"version"`
	Sequence        uint64          `json:"sequence"`
	Provider        string          `json:"provider"`
	Topic           string          `json:"topic,omitempty"`
	Symbol          string          `json:"symbol"`
	TimestampMillis int64           `json:"timestamp_millis"`
	Bid             float64         `json:"bid,omitempty"`
	Ask             float64         `json:"ask,omitempty"`
	Last            float64         `json:"last,omitempty"`
	Mark            float64         `json:"mark,omitempty"`
	Index           float64         `json:"index,omitempty"`
	Volume24h       float64         `json:"volume_24h,omitempty"`
	FundingRate     float64         `json:"funding_rate,omitempty"`
	OpenInterest    float64         `json:"open_interest,omitempty"`
	ContextFlags    uint64          `json:"context_flags,omitempty"`
	FeatureVector   []float64       `json:"feature_vector,omitempty"`
	Quality         SnapshotQuality `json:"quality,omitempty"`
}

type DispatchCandidate struct {
	AccountID        string    `json:"account_id"`
	Symbol           string    `json:"symbol"`
	Direction        Direction `json:"direction"`
	ProposedQty      float64   `json:"proposed_qty"`
	ProposedNotional float64   `json:"proposed_notional"`
	WeightFactor     float64   `json:"weight_factor,omitempty"`
	Allowed          bool      `json:"allowed"`
	RiskReason       string    `json:"risk_reason,omitempty"`
}

type ReviewDecision struct {
	Approved         bool     `json:"approved"`
	Reason           string   `json:"reason,omitempty"`
	ReasonCode       string   `json:"reason_code,omitempty"`
	SizeFactor       float64  `json:"size_factor,omitempty"`
	Actions          []string `json:"actions,omitempty"`
	ReviewedAtMillis int64    `json:"reviewed_at_millis,omitempty"`
}

type DispatchPlan struct {
	TraceID    string              `json:"trace_id"`
	Symbol     string              `json:"symbol"`
	Direction  Direction           `json:"direction"`
	Snapshot   *MarketSnapshot     `json:"snapshot,omitempty"`
	Candidates []DispatchCandidate `json:"candidates,omitempty"`
	Review     *ReviewDecision     `json:"review,omitempty"`
}

type ReviewTrace struct {
	Requested  bool     `json:"requested"`
	Approved   bool     `json:"approved"`
	Reason     string   `json:"reason,omitempty"`
	ReasonCode string   `json:"reason_code,omitempty"`
	SizeFactor float64  `json:"size_factor,omitempty"`
	Actions    []string `json:"actions,omitempty"`
}

type SignalTrace struct {
	TraceID         string              `json:"trace_id"`
	Symbol          string              `json:"symbol"`
	Direction       Direction           `json:"direction"`
	SnapshotSeq     uint64              `json:"snapshot_seq,omitempty"`
	DraftCandidates []DispatchCandidate `json:"draft_candidates,omitempty"`
	Review          *ReviewTrace        `json:"review,omitempty"`
	RejectedStage   string              `json:"rejected_stage,omitempty"`
	Outcome         string              `json:"outcome,omitempty"`
	CreatedAtMillis int64               `json:"created_at_millis,omitempty"`
}

type DataAlert struct {
	Level  string `json:"level"`
	Type   string `json:"type"`
	Symbol string `json:"symbol,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type AccountError struct {
	AccountID  string `json:"account_id"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	Recovering bool   `json:"recovering,omitempty"`
}

type MacroEvent struct {
	Provider         string `json:"provider,omitempty"`
	EventType        string `json:"event_type"`
	Summary          string `json:"summary,omitempty"`
	Severity         string `json:"severity,omitempty"`
	OccurredAtMillis int64  `json:"occurred_at_millis,omitempty"`
}

type SnapshotQuery struct {
	Symbol string `json:"symbol"`
}

type SnapshotQueryResult struct {
	Snapshot *MarketSnapshot `json:"snapshot,omitempty"`
}

type FeatureVectorQuery struct {
	Symbol string `json:"symbol"`
}

type FeatureVectorResult struct {
	Symbol   string    `json:"symbol"`
	Sequence uint64    `json:"sequence,omitempty"`
	Vector   []float64 `json:"vector,omitempty"`
}

type CandleQuery struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	Limit    int    `json:"limit,omitempty"`
}

type Candle struct {
	OpenTimeMillis int64   `json:"open_time_millis"`
	Open           float64 `json:"open"`
	High           float64 `json:"high"`
	Low            float64 `json:"low"`
	Close          float64 `json:"close"`
	Volume         float64 `json:"volume,omitempty"`
}

type CandleQueryResult struct {
	Symbol   string   `json:"symbol"`
	Interval string   `json:"interval"`
	Candles  []Candle `json:"candles,omitempty"`
}

type SimilarPatternsQuery struct {
	Symbol string    `json:"symbol"`
	Vector []float64 `json:"vector,omitempty"`
	TopK   int       `json:"top_k,omitempty"`
}

type SimilarPattern struct {
	PatternID string  `json:"pattern_id"`
	Score     float64 `json:"score"`
}

type SimilarPatternsResult struct {
	Symbol   string           `json:"symbol"`
	Patterns []SimilarPattern `json:"patterns,omitempty"`
}

type ProviderHealth struct {
	Provider    string `json:"provider"`
	Status      string `json:"status"`
	LatencyMS   int64  `json:"latency_ms,omitempty"`
	LastEventMS int64  `json:"last_event_ms,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type ProviderHealthResult struct {
	Providers []ProviderHealth `json:"providers,omitempty"`
}

type ReviewTradeRequest struct {
	TraceID    string              `json:"trace_id"`
	Symbol     string              `json:"symbol"`
	Direction  Direction           `json:"direction"`
	Snapshot   *MarketSnapshot     `json:"snapshot,omitempty"`
	Candidates []DispatchCandidate `json:"candidates,omitempty"`
	Reason     string              `json:"reason,omitempty"`
}

type PauseTradingRequest struct {
	Reason string `json:"reason,omitempty"`
}

type PauseInstrumentRequest struct {
	Symbol string `json:"symbol"`
	Reason string `json:"reason,omitempty"`
}

type ResumeInstrumentRequest struct {
	Symbol string `json:"symbol"`
}

type ForceCloseRequest struct {
	AccountID string `json:"account_id,omitempty"`
	Symbol    string `json:"symbol"`
	Reason    string `json:"reason,omitempty"`
}
