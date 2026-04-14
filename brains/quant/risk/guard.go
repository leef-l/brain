package risk

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// Guard enforces the three-layer risk rules from the design doc.
type Guard struct {
	MaxSinglePositionPct   float64
	MaxLeverage            int
	MinStopDistanceATR     float64
	MaxStopDistancePct     float64
	MaxConcurrentPositions int
	MaxTotalExposurePct    float64
	MaxSameDirectionPct    float64
	StopNewTradesLossPct   float64
	LiquidateAllLossPct    float64
	VolatilityPause        time.Duration
	BtcMovePause           time.Duration
	ExecutorFailurePause   time.Duration
	MemoryAlertThresholdGB float64
	LiquidationPause       time.Duration
}

func DefaultGuard() Guard {
	return Guard{
		MaxSinglePositionPct:   5,
		MaxLeverage:            20,
		MinStopDistanceATR:     1,
		MaxStopDistancePct:     10,
		MaxConcurrentPositions: 5,
		MaxTotalExposurePct:    30,
		MaxSameDirectionPct:    20,
		StopNewTradesLossPct:   3,
		LiquidateAllLossPct:    5,
		VolatilityPause:        time.Hour,
		BtcMovePause:           30 * time.Minute,
		ExecutorFailurePause:   10 * time.Minute,
		MemoryAlertThresholdGB: 40,
		LiquidationPause:       15 * time.Minute,
	}
}

func (g Guard) CheckOrder(req OrderRequest, portfolio PortfolioSnapshot) Decision {
	if req.Leverage > 0 && req.Leverage > g.MaxLeverage {
		return deny("layer-1", "leverage above limit", false)
	}
	if portfolio.Equity > 0 && req.Notional/portfolio.Equity*100 > g.MaxSinglePositionPct {
		return deny("layer-1", "single position above 5% of equity", false)
	}
	if req.StopLoss == 0 || req.EntryPrice == 0 {
		return deny("layer-1", "stop loss and entry price are required", false)
	}

	stopDistance := math.Abs(req.EntryPrice - req.StopLoss)
	if req.ATR > 0 && stopDistance < req.ATR*g.MinStopDistanceATR {
		return deny("layer-1", "stop distance below ATR floor", false)
	}
	if stopDistance/req.EntryPrice*100 > g.MaxStopDistancePct {
		return deny("layer-1", "stop distance too wide", false)
	}

	for _, position := range portfolio.Positions {
		if position.Symbol != req.Symbol {
			continue
		}
		if req.Action == ActionOpen {
			if position.Direction == req.Direction {
				return deny("layer-1", "same symbol already open", false)
			}
			return deny("layer-1", "opposite position must be closed first", false)
		}
		if req.Action == ActionReverse && position.Direction == req.Direction {
			return deny("layer-1", "reverse requires opposite existing position", false)
		}
	}

	return allow("layer-1")
}

func (g Guard) CheckPortfolio(req OrderRequest, portfolio PortfolioSnapshot) Decision {
	if portfolio.Equity <= 0 {
		return deny("layer-2", "equity must be positive", false)
	}
	if len(portfolio.Positions) >= g.MaxConcurrentPositions {
		return deny("layer-2", "too many concurrent positions", false)
	}

	totalExposure := 0.0
	longExposure := 0.0
	shortExposure := 0.0
	openSymbols := make(map[string]strategy.Direction, len(portfolio.Positions))
	for _, position := range portfolio.Positions {
		totalExposure += position.Notional
		openSymbols[position.Symbol] = position.Direction
		switch position.Direction {
		case strategy.DirectionLong:
			longExposure += position.Notional
		case strategy.DirectionShort:
			shortExposure += position.Notional
		}
	}

	if req.Action == ActionOpen || req.Action == ActionReverse {
		totalExposure += req.Notional
		switch req.Direction {
		case strategy.DirectionLong:
			longExposure += req.Notional
		case strategy.DirectionShort:
			shortExposure += req.Notional
		}
	}

	if totalExposure/portfolio.Equity*100 > g.MaxTotalExposurePct {
		return deny("layer-2", "total exposure above 30% of equity", false)
	}
	if math.Max(longExposure, shortExposure)/portfolio.Equity*100 > g.MaxSameDirectionPct {
		return deny("layer-2", "same direction exposure above 20% of equity", false)
	}
	if portfolio.RealizedLossTodayPct > g.LiquidateAllLossPct {
		return Decision{
			Allowed:             false,
			Layer:               "layer-2",
			Reason:              "daily loss above liquidation threshold",
			Action:              "liquidate",
			RequiresLiquidation: true,
		}
	}
	if portfolio.RealizedLossTodayPct > g.StopNewTradesLossPct && (req.Action == ActionOpen || req.Action == ActionReverse) {
		return deny("layer-2", "daily loss above stop-new-trades threshold", false)
	}

	for group, members := range portfolio.CorrelatedGroups {
		if !contains(members, req.Symbol) {
			continue
		}
		for _, member := range members {
			if member == req.Symbol {
				continue
			}
			if dir, ok := openSymbols[member]; ok && dir == req.Direction {
				return deny("layer-2", fmt.Sprintf("correlated symbol %s already held in same direction", member), false)
			}
		}
		_ = group
	}

	return allow("layer-2")
}

func (g Guard) CheckCircuitBreaker(circuit CircuitSnapshot) Decision {
	switch {
	case circuit.VolatilityPercentile > 99:
		return pause("layer-3", "market volatility above 99th percentile", g.VolatilityPause)
	case circuit.BtcMove15mPct >= 5 || circuit.BtcMove15mPct <= -5:
		return pause("layer-3", "btc moved more than 5% in 15m", g.BtcMovePause)
	case circuit.ExecutorFailureStreak >= 3:
		return pause("layer-3", "executor failed 3 times in a row", g.ExecutorFailurePause)
	case circuit.MemoryGB >= g.MemoryAlertThresholdGB:
		return Decision{
			Allowed:  false,
			Layer:    "layer-3",
			Reason:   "memory usage above alert threshold",
			Action:   "alert",
			PauseFor: 0,
		}
	case circuit.OpenInterestDrop5mPct >= 5:
		return pause("layer-3", "open interest dropped more than 5% in 5m", g.LiquidationPause)
	default:
		return allow("layer-3")
	}
}

func (g Guard) Evaluate(req OrderRequest, portfolio PortfolioSnapshot, circuit CircuitSnapshot) Decision {
	if decision := g.CheckOrder(req, portfolio); !decision.Allowed {
		return decision
	}
	if decision := g.CheckPortfolio(req, portfolio); !decision.Allowed {
		return decision
	}
	if decision := g.CheckCircuitBreaker(circuit); !decision.Allowed {
		return decision
	}
	return allow("pass")
}

func allow(layer string) Decision {
	return Decision{Allowed: true, Layer: layer, Action: "allow"}
}

func deny(layer, reason string, liquidate bool) Decision {
	return Decision{Allowed: false, Layer: layer, Reason: reason, Action: "reject", RequiresLiquidation: liquidate}
}

func pause(layer, reason string, d time.Duration) Decision {
	return Decision{Allowed: false, Layer: layer, Reason: reason, Action: "pause", PauseFor: d}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}
