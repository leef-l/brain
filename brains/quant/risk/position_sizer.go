package risk

import (
	"errors"
	"math"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// SizeRequest is the input for the conservative Kelly position calculator.
type SizeRequest struct {
	AccountEquity float64
	Signal        strategy.Signal
	WinRate       float64
	AvgWin        float64
	AvgLoss       float64
}

// SizeResult is the output of the position sizer.
type SizeResult struct {
	RiskFraction  float64
	KellyFraction float64
	Notional      float64
	Quantity      float64
}

// PositionSizer computes a conservative Kelly-based allocation.
type PositionSizer struct {
	MinFraction   float64
	MaxFraction   float64
	ScaleFraction float64
}

func DefaultPositionSizer() PositionSizer {
	return PositionSizer{
		MinFraction:   0.005,
		MaxFraction:   0.05,
		ScaleFraction: 0.25,
	}
}

func (s PositionSizer) Size(req SizeRequest) (SizeResult, error) {
	if s.MinFraction <= 0 {
		s.MinFraction = 0.005
	}
	if s.MaxFraction <= 0 {
		s.MaxFraction = 0.05
	}
	if s.ScaleFraction <= 0 {
		s.ScaleFraction = 0.25
	}
	if req.AccountEquity <= 0 {
		return SizeResult{}, errors.New("account equity must be positive")
	}
	if req.Signal.Entry <= 0 {
		return SizeResult{}, errors.New("signal entry price must be positive")
	}

	kelly := 0.0
	if req.WinRate > 0 && req.AvgWin > 0 && req.AvgLoss > 0 {
		edge := req.WinRate - (1-req.WinRate)/(req.AvgWin/req.AvgLoss)
		if !math.IsNaN(edge) && !math.IsInf(edge, 0) {
			kelly = edge
		}
	}
	quarterKelly := kelly * s.ScaleFraction
	if quarterKelly <= 0 {
		quarterKelly = s.MinFraction
	}
	fraction := clamp(quarterKelly, s.MinFraction, s.MaxFraction)
	notional := req.AccountEquity * fraction
	quantity := notional / req.Signal.Entry
	return SizeResult{
		RiskFraction:  fraction,
		KellyFraction: kelly,
		Notional:      notional,
		Quantity:      quantity,
	}, nil
}

func clamp(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
