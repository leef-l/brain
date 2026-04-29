package strategy

import (
	"math"
	"sync"
	"testing"
)

func TestCalibratorEmpty(t *testing.T) {
	cc := NewConfidenceCalibrator()
	calibrated := cc.Calibrate("TrendFollower", 0.75)
	if calibrated != 0.75 {
		t.Fatalf("calibrated = %.2f, want 0.75 (fallback to raw)", calibrated)
	}
}

func TestCalibratorGlobalWinRate(t *testing.T) {
	cc := NewConfidenceCalibrator()
	cc.RecordPrediction("TrendFollower", 0.75, true)
	cc.RecordPrediction("TrendFollower", 0.80, false)
	cc.RecordPrediction("TrendFollower", 0.30, true)

	gw := cc.GlobalWinRate("TrendFollower")
	if math.Abs(gw-0.6667) > 0.01 {
		t.Fatalf("global win rate = %.4f, want ~0.6667", gw)
	}
}

func TestCalibratorBucketCalibration(t *testing.T) {
	cc := NewConfidenceCalibrator()
	cc.MinSamples = 3

	// Bucket 0.6-0.8: 3 wins out of 4 → 0.75
	cc.RecordPrediction("TrendFollower", 0.65, true)
	cc.RecordPrediction("TrendFollower", 0.70, true)
	cc.RecordPrediction("TrendFollower", 0.75, false)
	cc.RecordPrediction("TrendFollower", 0.78, true)

	calibrated := cc.Calibrate("TrendFollower", 0.70)
	if math.Abs(calibrated-0.75) > 0.001 {
		t.Fatalf("calibrated = %.4f, want 0.75", calibrated)
	}
}

func TestCalibratorBucketFallbackToGlobal(t *testing.T) {
	cc := NewConfidenceCalibrator()
	cc.MinSamples = 10

	// Bucket 0.6-0.8: only 2 samples (below threshold)
	cc.RecordPrediction("TrendFollower", 0.65, true)
	cc.RecordPrediction("TrendFollower", 0.70, false)

	// Global: 1 win / 2 total = 0.5
	calibrated := cc.Calibrate("TrendFollower", 0.70)
	if math.Abs(calibrated-0.5) > 0.001 {
		t.Fatalf("calibrated = %.4f, want 0.5 (global fallback)", calibrated)
	}
}

func TestCalibratorMultipleStrategies(t *testing.T) {
	cc := NewConfidenceCalibrator()
	cc.MinSamples = 1

	cc.RecordPrediction("TrendFollower", 0.9, true)
	cc.RecordPrediction("MeanReversion", 0.9, false)

	if cc.Calibrate("TrendFollower", 0.9) != 1.0 {
		t.Fatal("expected TrendFollower bucket win rate = 1.0")
	}
	if cc.Calibrate("MeanReversion", 0.9) != 0.0 {
		t.Fatal("expected MeanReversion bucket win rate = 0.0")
	}
}

func TestCalibratorThreadSafety(t *testing.T) {
	cc := NewConfidenceCalibrator()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(win bool) {
			defer wg.Done()
			cc.RecordPrediction("TrendFollower", 0.5, win)
		}(i%2 == 0)
	}
	wg.Wait()

	gw := cc.GlobalWinRate("TrendFollower")
	if gw < 0.4 || gw > 0.6 {
		t.Fatalf("global win rate = %.2f, expected ~0.5", gw)
	}
}

func TestCalibratorNegativeConfidence(t *testing.T) {
	cc := NewConfidenceCalibrator()
	cc.MinSamples = 1

	// Negative confidence should map to positive bucket.
	cc.RecordPrediction("TrendFollower", -0.85, true)
	calibrated := cc.Calibrate("TrendFollower", -0.85)
	if calibrated != 1.0 {
		t.Fatalf("calibrated = %.2f, want 1.0", calibrated)
	}
}

func TestCalibratorEdgeBuckets(t *testing.T) {
	cc := NewConfidenceCalibrator()
	cc.MinSamples = 1

	// Exactly 0.2 should fall into bucket 0 (0-0.2)
	cc.RecordPrediction("S1", 0.2, true)
	// Exactly 0.8 should fall into bucket 3 (0.6-0.8)
	cc.RecordPrediction("S1", 0.8, false)
	// 1.0 into bucket 4 (0.8-1.0)
	cc.RecordPrediction("S1", 1.0, true)

	if cc.Calibrate("S1", 0.2) != 1.0 {
		t.Fatalf("bucket 0 expected 1.0, got %.2f", cc.Calibrate("S1", 0.2))
	}
	if cc.Calibrate("S1", 0.8) != 0.0 {
		t.Fatalf("bucket 3 expected 0.0, got %.2f", cc.Calibrate("S1", 0.8))
	}
	if cc.Calibrate("S1", 1.0) != 1.0 {
		t.Fatalf("bucket 4 expected 1.0, got %.2f", cc.Calibrate("S1", 1.0))
	}
}
