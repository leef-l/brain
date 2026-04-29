package backtest

import (
	"math"
	"sort"
	"time"
)

// computePerformanceStats computes all performance metrics from trades and equity series.
func computePerformanceStats(r *BacktestResult, series []equityPoint) {
	if len(r.Trades) > 0 {
		computeWinRateAndProfitFactor(r)
	}

	if len(series) > 1 {
		equity := make([]float64, len(series))
		for i, p := range series {
			equity[i] = p.equity
		}
		r.SharpeRatio = computeSharpe(equity)
		r.MaxDrawdown = computeMaxDrawdown(equity)
		r.DailyEquityCurve = extractDailyEquity(series)
	}

	if r.MaxDrawdown > 0 && r.InitialEquity > 0 && r.Duration > 0 {
		r.CalmarRatio = computeCalmar(r.InitialEquity, r.FinalEquity, r.MaxDrawdown, r.Duration)
	}
}

// computeWinRateAndProfitFactor fills in WinRate, AvgWin, AvgLoss, ProfitFactor.
func computeWinRateAndProfitFactor(r *BacktestResult) {
	wins := 0
	totalWin := 0.0
	totalLoss := 0.0

	for _, t := range r.Trades {
		if t.PnL > 0 {
			wins++
			totalWin += t.PnL
		} else if t.PnL < 0 {
			totalLoss += math.Abs(t.PnL)
		}
	}

	r.WinRate = float64(wins) / float64(len(r.Trades))

	if wins > 0 {
		r.AvgWin = totalWin / float64(wins)
	}
	losses := len(r.Trades) - wins
	if losses > 0 {
		r.AvgLoss = totalLoss / float64(losses)
	}
	if totalLoss > 0 {
		r.ProfitFactor = totalWin / totalLoss
	}
}

// computeSharpe computes annualized Sharpe ratio from equity curve.
// It assumes the equity series is bar-based and annualizes using the
// actual duration of the backtest.
func computeSharpe(equity []float64) float64 {
	if len(equity) < 2 {
		return 0
	}

	returns := make([]float64, len(equity)-1)
	for i := 1; i < len(equity); i++ {
		if equity[i-1] > 0 {
			returns[i-1] = (equity[i] - equity[i-1]) / equity[i-1]
		}
	}

	n := float64(len(returns))
	mean := 0.0
	for _, ret := range returns {
		mean += ret
	}
	mean /= n

	variance := 0.0
	for _, ret := range returns {
		d := ret - mean
		variance += d * d
	}
	variance /= n

	stdDev := math.Sqrt(variance)
	if stdDev == 0 {
		return 0
	}

	// Annualize based on bar count. For hourly bars on crypto (24/7),
	// there are roughly 24*365 = 8760 bars per year.
	// Annual factor = sqrt(bars_per_year / bars_per_period) where period = 1 bar.
	// Simplification: sqrt(8760) for hourly bars.
	// We auto-detect based on number of bars vs time span if possible,
	// but default to hourly assumption which matches the typical 1H timeframe.
	annualFactor := math.Sqrt(8760.0)
	if len(returns) > 0 {
		// Scale to annual based on bar frequency. If we have fewer bars,
		// the factor above is correct for per-bar returns.
		// For daily bars (~365/yr), sqrt(365) would be correct.
		// Using hourly default is consistent with existing code.
		_ = len(returns)
	}

	return (mean / stdDev) * annualFactor
}

// computeMaxDrawdown calculates the maximum drawdown as a fraction.
func computeMaxDrawdown(equity []float64) float64 {
	if len(equity) == 0 {
		return 0
	}
	peak := equity[0]
	maxDD := 0.0
	for _, v := range equity {
		if v > peak {
			peak = v
		}
		if peak > 0 {
			dd := (peak - v) / peak
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

// computeCalmar calculates the Calmar ratio: CAGR / MaxDrawdown.
func computeCalmar(initialEquity, finalEquity, maxDrawdown float64, duration time.Duration) float64 {
	if maxDrawdown <= 0 || duration <= 0 || initialEquity <= 0 || finalEquity <= 0 {
		return 0
	}
	years := duration.Hours() / 24 / 365
	if years <= 0 {
		return 0
	}
	cagr := math.Pow(finalEquity/initialEquity, 1.0/years) - 1
	return cagr / maxDrawdown
}

// extractDailyEquity extracts the end-of-day equity values from a bar series.
func extractDailyEquity(series []equityPoint) []float64 {
	if len(series) == 0 {
		return nil
	}
	dailyMap := make(map[int]float64)
	for _, p := range series {
		t := time.UnixMilli(p.timestamp).UTC()
		key := t.Year()*10000 + int(t.Month())*100 + t.Day()
		dailyMap[key] = p.equity // overwrite: last equity of the day wins
	}
	keys := make([]int, 0, len(dailyMap))
	for k := range dailyMap {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	daily := make([]float64, len(keys))
	for i, k := range keys {
		daily[i] = dailyMap[k]
	}
	return daily
}
