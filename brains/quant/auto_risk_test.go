package quant

import (
	"testing"
)

func TestAutoScale_50USDT(t *testing.T) {
	ac := AutoRiskConfig{
		Enabled:      true,
		Level:        "aggressive",
		MinOrderUSDT: 5.0,
	}
	r := ac.AutoScale(50, RiskConfig{})

	// 50 USDT: min_fraction = 5/50 = 10%
	if r.Sizer.MinFraction < 0.09 || r.Sizer.MinFraction > 0.11 {
		t.Errorf("min_fraction for 50 USDT: got %.4f, want ~0.10", r.Sizer.MinFraction)
	}
	// Max concurrent: should be ≤ 2 for micro account
	if r.Guard.MaxConcurrentPositions > 2 {
		t.Errorf("max_concurrent for 50 USDT: got %d, want ≤ 2", r.Guard.MaxConcurrentPositions)
	}
	// Min fraction × max_concurrent should not exceed total exposure
	totalPossible := r.Sizer.MinFraction * float64(r.Guard.MaxConcurrentPositions) * 100
	if totalPossible > r.Guard.MaxTotalExposurePct {
		t.Errorf("min positions exceed exposure: %.1f%% > %.1f%%", totalPossible, r.Guard.MaxTotalExposurePct)
	}
	t.Logf("50 USDT aggressive: min=%.4f max=%.4f concurrent=%d kelly=%.2f",
		r.Sizer.MinFraction, r.Sizer.MaxFraction, r.Guard.MaxConcurrentPositions, r.Sizer.ScaleFraction)
}

func TestAutoScale_1000USDT(t *testing.T) {
	ac := AutoRiskConfig{
		Enabled:      true,
		Level:        "moderate",
		MinOrderUSDT: 5.0,
	}
	r := ac.AutoScale(1000, RiskConfig{})

	// 1000 USDT: min_fraction = max(5/1000, 0.002) = 0.5%
	if r.Sizer.MinFraction < 0.004 || r.Sizer.MinFraction > 0.006 {
		t.Errorf("min_fraction for 1000 USDT: got %.4f, want ~0.005", r.Sizer.MinFraction)
	}
	// 1000 USDT moderate: min_fraction=0.5%, 8 positions × 0.5% = 4% — well within 40% exposure.
	// Concurrency cap: ≤ 3 only applies to < 1000 USDT. At exactly 1000, full profile is used.
	if r.Guard.MaxConcurrentPositions > 8 {
		t.Errorf("max_concurrent for 1000 USDT moderate: got %d, want ≤ 8", r.Guard.MaxConcurrentPositions)
	}
	t.Logf("1000 USDT moderate: min=%.4f max=%.4f concurrent=%d",
		r.Sizer.MinFraction, r.Sizer.MaxFraction, r.Guard.MaxConcurrentPositions)
}

func TestAutoScale_10000USDT(t *testing.T) {
	ac := AutoRiskConfig{
		Enabled:      true,
		Level:        "aggressive",
		MinOrderUSDT: 5.0,
	}
	r := ac.AutoScale(10000, RiskConfig{})

	// 10000 USDT: min_fraction = max(5/10000, 0.002) = 0.2%
	if r.Sizer.MinFraction < 0.001 || r.Sizer.MinFraction > 0.003 {
		t.Errorf("min_fraction for 10000 USDT: got %.4f, want ~0.002", r.Sizer.MinFraction)
	}
	// aggressive with 10000 should get full 15 concurrent
	if r.Guard.MaxConcurrentPositions != 15 {
		t.Errorf("max_concurrent for 10000 aggressive: got %d, want 15", r.Guard.MaxConcurrentPositions)
	}
	t.Logf("10000 USDT aggressive: min=%.4f max=%.4f concurrent=%d kelly=%.2f",
		r.Sizer.MinFraction, r.Sizer.MaxFraction, r.Guard.MaxConcurrentPositions, r.Sizer.ScaleFraction)
}

func TestAutoScale_100000USDT(t *testing.T) {
	ac := AutoRiskConfig{
		Enabled:      true,
		Level:        "conservative",
		MinOrderUSDT: 5.0,
	}
	r := ac.AutoScale(100000, RiskConfig{})

	// min_fraction = 0.002 (floor)
	if r.Sizer.MinFraction != 0.002 {
		t.Errorf("min_fraction for 100000: got %.4f, want 0.002", r.Sizer.MinFraction)
	}
	// Conservative large account: up to 4 concurrent (3 * 1.5)
	if r.Guard.MaxConcurrentPositions < 3 || r.Guard.MaxConcurrentPositions > 5 {
		t.Errorf("max_concurrent for 100000 conservative: got %d, want 3-5", r.Guard.MaxConcurrentPositions)
	}
	t.Logf("100000 USDT conservative: min=%.4f max=%.4f concurrent=%d",
		r.Sizer.MinFraction, r.Sizer.MaxFraction, r.Guard.MaxConcurrentPositions)
}

func TestAutoScale_ManualCeiling(t *testing.T) {
	ac := AutoRiskConfig{
		Enabled:      true,
		Level:        "aggressive",
		MinOrderUSDT: 5.0,
	}
	manual := RiskConfig{
		Guard: GuardConfig{
			MaxConcurrentPositions: 5, // ceiling: even aggressive can't exceed 5
			MaxTotalExposurePct:    50,
			MaxLeverage:            10,
			MinStopDistanceATR:     0.3,
			MaxStopDistancePct:     10,
		},
	}
	r := ac.AutoScale(10000, manual)

	if r.Guard.MaxConcurrentPositions > 5 {
		t.Errorf("ceiling violated: got %d, want ≤ 5", r.Guard.MaxConcurrentPositions)
	}
	if r.Guard.MaxTotalExposurePct > 50 {
		t.Errorf("exposure ceiling violated: got %.1f, want ≤ 50", r.Guard.MaxTotalExposurePct)
	}
	// Manual MaxLeverage/stop distances should pass through
	if r.Guard.MaxLeverage != 10 {
		t.Errorf("MaxLeverage not passed through: got %d, want 10", r.Guard.MaxLeverage)
	}
	if r.Guard.MinStopDistanceATR != 0.3 {
		t.Errorf("MinStopDistanceATR not passed through: got %.2f, want 0.3", r.Guard.MinStopDistanceATR)
	}
	t.Logf("10000 aggressive with ceiling: concurrent=%d exposure=%.0f",
		r.Guard.MaxConcurrentPositions, r.Guard.MaxTotalExposurePct)
}

func TestAutoScale_Disabled(t *testing.T) {
	ac := AutoRiskConfig{Enabled: false}
	manual := RiskConfig{
		Guard: GuardConfig{MaxConcurrentPositions: 99},
		Sizer: SizerConfig{MinFraction: 0.123},
	}
	r := ac.AutoScale(10000, manual)
	if r.Guard.MaxConcurrentPositions != 99 || r.Sizer.MinFraction != 0.123 {
		t.Errorf("disabled auto_risk should return manual config unchanged")
	}
}
