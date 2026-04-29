package advisor

import (
	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
)

// MarketState aggregates all market information needed by LLMAdvisor.
type MarketState struct {
	Symbol            string
	Price             float64
	Regime            string // dominant regime from MarketRegimeProb
	Signals           []strategy.Signal
	RiskStatus        RiskStatus
	PortfolioSnapshot risk.PortfolioSnapshot
}

// RiskStatus captures the current risk guard state.
type RiskStatus struct {
	Exposure     float64
	Leverage     float64
	MaxLeverage  float64
	DailyLossPct float64
	IsPaused     bool
}
