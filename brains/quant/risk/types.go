package risk

import (
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// Action describes the intent of the order relative to existing exposure.
type Action string

const (
	ActionOpen    Action = "open"
	ActionClose   Action = "close"
	ActionReduce  Action = "reduce"
	ActionReverse Action = "reverse"
)

// Position is a minimal open-position snapshot.
type Position struct {
	Symbol     string
	Direction  strategy.Direction
	Quantity   float64
	Notional   float64
	EntryPrice float64
	MarkPrice  float64
	Leverage   int
}

// OrderRequest is the minimal order-level risk input.
type OrderRequest struct {
	Symbol        string
	Action        Action
	Direction     strategy.Direction
	EntryPrice    float64
	StopLoss      float64
	Quantity      float64
	Notional      float64
	Leverage      int
	ATR           float64
	AccountEquity float64

	// AnomalyScore is optional. When provided, Guard.CheckOrder will also
	// evaluate anomaly rules via AnomalyGuard.
	AnomalyScore *strategy.AnomalyScore
}

// PortfolioSnapshot captures the aggregate exposure state.
type PortfolioSnapshot struct {
	Equity               float64
	Positions            []Position
	RealizedLossTodayPct float64
	CorrelatedGroups     map[string][]string
}

// CircuitSnapshot captures the extreme-condition metrics used by the breaker.
type CircuitSnapshot struct {
	VolatilityPercentile  float64
	BtcMove15mPct         float64
	ExecutorFailureStreak int
	MemoryGB              float64
	OpenInterestDrop5mPct float64
}

// Decision is the unified result for risk checks.
type Decision struct {
	Allowed             bool
	Layer               string
	Reason              string
	Action              string
	PauseFor            time.Duration
	RequiresLiquidation bool
}
