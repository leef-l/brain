package risk

import (
	"errors"
	"math"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// BayesianSizer extends PositionSizer with a Beta prior for the win rate.
// When trade samples are few (< MinSamples), it blends the observed win rate
// with a conservative prior, preventing Kelly from over-sizing on noise.
type BayesianSizer struct {
	Base PositionSizer

	// MinSamples is the threshold below which the Beta prior dominates.
	// Default: 30.
	MinSamples int

	// PriorAlpha and PriorBeta parameterize the Beta distribution prior.
	// Default: Alpha=2, Beta=2 (uniform-ish, slight center bias = 50% win).
	PriorAlpha float64
	PriorBeta  float64

	// SmallSampleFraction is used when samples == 0 (no history at all).
	// Default: 0.005 (minimum fraction).
	SmallSampleFraction float64
}

// DefaultBayesianSizer returns a BayesianSizer with conservative defaults.
func DefaultBayesianSizer() *BayesianSizer {
	return &BayesianSizer{
		Base:                DefaultPositionSizer(),
		MinSamples:          30,
		PriorAlpha:          2,
		PriorBeta:           2,
		SmallSampleFraction: 0.005,
	}
}

// BayesianSizeRequest extends SizeRequest with sample count.
type BayesianSizeRequest struct {
	AccountEquity float64
	Signal        strategy.Signal
	WinRate       float64 // observed win rate
	AvgWin        float64
	AvgLoss       float64
	Samples       int // number of historical trades
}

// Size computes position size using Bayesian-adjusted win rate.
func (bs *BayesianSizer) Size(req BayesianSizeRequest) (SizeResult, error) {
	if req.AccountEquity <= 0 {
		return SizeResult{}, errors.New("account equity must be positive")
	}
	if req.Signal.Entry <= 0 {
		return SizeResult{}, errors.New("signal entry price must be positive")
	}

	// No samples at all: use minimum fraction
	if req.Samples <= 0 {
		fraction := bs.SmallSampleFraction
		if fraction <= 0 {
			fraction = bs.Base.MinFraction
		}
		notional := req.AccountEquity * fraction
		return SizeResult{
			RiskFraction:  fraction,
			KellyFraction: 0,
			Notional:      notional,
			Quantity:      notional / req.Signal.Entry,
		}, nil
	}

	// Bayesian posterior: blend observed wins with Beta prior
	wins := float64(req.Samples) * req.WinRate
	losses := float64(req.Samples) - wins
	posteriorAlpha := bs.PriorAlpha + wins
	posteriorBeta := bs.PriorBeta + losses
	adjustedWinRate := posteriorAlpha / (posteriorAlpha + posteriorBeta)

	// When samples < MinSamples, scale down Kelly further
	sampleConfidence := 1.0
	if req.Samples < bs.MinSamples {
		sampleConfidence = float64(req.Samples) / float64(bs.MinSamples)
	}

	// Compute Kelly with adjusted win rate
	kelly := 0.0
	if req.AvgWin > 0 && req.AvgLoss > 0 {
		edge := adjustedWinRate - (1-adjustedWinRate)/(req.AvgWin/req.AvgLoss)
		if !math.IsNaN(edge) && !math.IsInf(edge, 0) {
			kelly = edge
		}
	}

	scaleFraction := bs.Base.ScaleFraction
	if scaleFraction <= 0 {
		scaleFraction = 0.25
	}
	quarterKelly := kelly * scaleFraction * sampleConfidence

	minFrac := bs.Base.MinFraction
	maxFrac := bs.Base.MaxFraction
	if minFrac <= 0 {
		minFrac = 0.005
	}
	if maxFrac <= 0 {
		maxFrac = 0.05
	}

	if quarterKelly <= 0 {
		quarterKelly = minFrac
	}
	fraction := clamp(quarterKelly, minFrac, maxFrac)
	notional := req.AccountEquity * fraction
	quantity := notional / req.Signal.Entry

	return SizeResult{
		RiskFraction:  fraction,
		KellyFraction: kelly,
		Notional:      notional,
		Quantity:      quantity,
	}, nil
}
