package strategy

import (
	"math"
	"sync"
)

const calibratorBucketCount = 5

// calibratorBucketBounds defines the upper bounds of each confidence bucket.
var calibratorBucketBounds = []float64{0.2, 0.4, 0.6, 0.8, 1.0}

func calibratorBucketIndex(confidence float64) int {
	c := math.Abs(confidence)
	for i, bound := range calibratorBucketBounds {
		if c <= bound {
			return i
		}
	}
	return calibratorBucketCount - 1
}

// calibratorBucketStats tracks predictions and successes for one bucket.
type calibratorBucketStats struct {
	predictions int
	successes   int
}

// calibratorStrategyStats holds per-bucket and global stats for a strategy.
type calibratorStrategyStats struct {
	buckets [calibratorBucketCount]calibratorBucketStats
	total   int
	wins    int
}

// ConfidenceCalibrator calibrates raw strategy confidence by mapping it to
// the historical win rate of the corresponding confidence bucket. When a
// bucket has fewer than MinSamples observations, the global win rate is
// used as a fallback.
type ConfidenceCalibrator struct {
	mu         sync.RWMutex
	strategies map[string]*calibratorStrategyStats
	MinSamples int // minimum samples per bucket before trusting it (default 10)
}

// NewConfidenceCalibrator creates a new ConfidenceCalibrator.
func NewConfidenceCalibrator() *ConfidenceCalibrator {
	return &ConfidenceCalibrator{
		strategies: make(map[string]*calibratorStrategyStats),
		MinSamples: 10,
	}
}

func (cc *ConfidenceCalibrator) getOrCreate(strategy string) *calibratorStrategyStats {
	if s, ok := cc.strategies[strategy]; ok {
		return s
	}
	s := &calibratorStrategyStats{}
	cc.strategies[strategy] = s
	return s
}

// RecordPrediction records a single prediction outcome for a strategy.
func (cc *ConfidenceCalibrator) RecordPrediction(strategy string, confidence float64, win bool) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	s := cc.getOrCreate(strategy)
	idx := calibratorBucketIndex(confidence)
	s.buckets[idx].predictions++
	s.total++
	if win {
		s.buckets[idx].successes++
		s.wins++
	}
}

// Calibrate returns the calibrated confidence for the given strategy and raw
// confidence. If the bucket has insufficient samples, the global win rate is
// returned instead.
func (cc *ConfidenceCalibrator) Calibrate(strategy string, rawConfidence float64) float64 {
	cc.mu.RLock()
	defer cc.mu.RUnlock()

	s, ok := cc.strategies[strategy]
	if !ok || s.total == 0 {
		return rawConfidence
	}

	idx := calibratorBucketIndex(rawConfidence)
	b := s.buckets[idx]

	minSamples := cc.MinSamples
	if minSamples <= 0 {
		minSamples = 10
	}

	if b.predictions >= minSamples {
		return float64(b.successes) / float64(b.predictions)
	}

	// Fallback to global win rate.
	return float64(s.wins) / float64(s.total)
}

// GlobalWinRate returns the overall win rate for a strategy.
func (cc *ConfidenceCalibrator) GlobalWinRate(strategy string) float64 {
	cc.mu.RLock()
	defer cc.mu.RUnlock()

	s, ok := cc.strategies[strategy]
	if !ok || s.total == 0 {
		return 0
	}
	return float64(s.wins) / float64(s.total)
}
