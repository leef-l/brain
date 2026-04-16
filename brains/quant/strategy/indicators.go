package strategy

import "math"

func closes(candles []Candle) []float64 {
	out := make([]float64, 0, len(candles))
	for _, c := range candles {
		out = append(out, c.Close)
	}
	return out
}

func highs(candles []Candle) []float64 {
	out := make([]float64, 0, len(candles))
	for _, c := range candles {
		out = append(out, c.High)
	}
	return out
}

func lows(candles []Candle) []float64 {
	out := make([]float64, 0, len(candles))
	for _, c := range candles {
		out = append(out, c.Low)
	}
	return out
}

func volumes(candles []Candle) []float64 {
	out := make([]float64, 0, len(candles))
	for _, c := range candles {
		out = append(out, c.Volume)
	}
	return out
}

func last(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[len(v)-1]
}

func previous(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	return v[len(v)-2]
}

func sma(values []float64, period int) float64 {
	if period <= 0 || len(values) < period {
		return 0
	}
	sum := 0.0
	for _, v := range values[len(values)-period:] {
		sum += v
	}
	return sum / float64(period)
}

func stddev(values []float64, period int) float64 {
	if period <= 1 || len(values) < period {
		return 0
	}
	mean := sma(values, period)
	sum := 0.0
	for _, v := range values[len(values)-period:] {
		d := v - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(period))
}

func ema(values []float64, period int) float64 {
	if period <= 0 || len(values) == 0 {
		return 0
	}
	if len(values) < period {
		return sma(values, len(values))
	}
	k := 2.0 / float64(period+1)
	acc := sma(values[:period], period)
	for _, v := range values[period:] {
		acc = v*k + acc*(1-k)
	}
	return acc
}

func atr(candles []Candle, period int) float64 {
	if period <= 0 || len(candles) < period+1 {
		return 0
	}
	trs := make([]float64, 0, len(candles)-1)
	for i := 1; i < len(candles); i++ {
		cur := candles[i]
		prev := candles[i-1]
		tr := math.Max(cur.High-cur.Low, math.Max(math.Abs(cur.High-prev.Close), math.Abs(cur.Low-prev.Close)))
		trs = append(trs, tr)
	}
	return ema(trs, period)
}

func rsi(values []float64, period int) float64 {
	if period <= 0 || len(values) < period+1 {
		return 0
	}
	gain := 0.0
	loss := 0.0
	for i := len(values) - period; i < len(values); i++ {
		delta := values[i] - values[i-1]
		if delta >= 0 {
			gain += delta
		} else {
			loss -= delta
		}
	}
	if gain == 0 && loss == 0 {
		return 50
	}
	if loss == 0 {
		return 100
	}
	rs := gain / loss
	return 100 - 100/(1+rs)
}

func macdHistogram(values []float64) float64 {
	if len(values) < 35 {
		return 0
	}
	fast := ema(values, 12)
	slow := ema(values, 26)
	macd := fast - slow
	macdSeries := make([]float64, 0, len(values))
	for i := range values {
		if i < 26 {
			continue
		}
		macdSeries = append(macdSeries, ema(values[:i+1], 12)-ema(values[:i+1], 26))
	}
	if len(macdSeries) < 9 {
		return 0
	}
	signal := ema(macdSeries, 9)
	return macd - signal
}

func macdHistogramPrev(values []float64) float64 {
	if len(values) < 36 {
		return 0
	}
	return macdHistogram(values[:len(values)-1])
}

func bollinger(values []float64, period int, k float64) (mid, upper, lower, width float64) {
	mid = sma(values, period)
	if mid == 0 {
		return
	}
	sd := stddev(values, period)
	upper = mid + k*sd
	lower = mid - k*sd
	width = upper - lower
	return
}

func adx(candles []Candle, period int) (adxValue, diPlus, diMinus float64) {
	if period <= 0 || len(candles) < period+1 {
		return 0, 0, 0
	}
	trs := make([]float64, 0, len(candles)-1)
	pdm := make([]float64, 0, len(candles)-1)
	mdm := make([]float64, 0, len(candles)-1)
	for i := 1; i < len(candles); i++ {
		cur := candles[i]
		prev := candles[i-1]
		upMove := cur.High - prev.High
		downMove := prev.Low - cur.Low
		var plusDM, minusDM float64
		if upMove > downMove && upMove > 0 {
			plusDM = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM = downMove
		}
		tr := math.Max(cur.High-cur.Low, math.Max(math.Abs(cur.High-prev.Close), math.Abs(cur.Low-prev.Close)))
		trs = append(trs, tr)
		pdm = append(pdm, plusDM)
		mdm = append(mdm, minusDM)
	}
	atrValue := ema(trs, period)
	if atrValue == 0 {
		return 0, 0, 0
	}
	diPlus = 100 * ema(pdm, period) / atrValue
	diMinus = 100 * ema(mdm, period) / atrValue
	if diPlus+diMinus == 0 {
		return 0, diPlus, diMinus
	}
	dxSeries := make([]float64, 0, len(trs))
	for i := range trs {
		p := ema(pdm[:i+1], min(period, i+1))
		m := ema(mdm[:i+1], min(period, i+1))
		t := ema(trs[:i+1], min(period, i+1))
		if t == 0 || p+m == 0 {
			continue
		}
		diP := 100 * p / t
		diM := 100 * m / t
		dxSeries = append(dxSeries, 100*math.Abs(diP-diM)/(diP+diM))
	}
	adxValue = ema(dxSeries, period)
	return adxValue, diPlus, diMinus
}

func obv(candles []Candle) float64 {
	if len(candles) < 2 {
		return 0
	}
	acc := 0.0
	for i := 1; i < len(candles); i++ {
		switch {
		case candles[i].Close > candles[i-1].Close:
			acc += candles[i].Volume
		case candles[i].Close < candles[i-1].Close:
			acc -= candles[i].Volume
		}
	}
	return acc
}

func obvSlope(candles []Candle) float64 {
	if len(candles) < 6 {
		return 0
	}
	split := len(candles) / 2
	first := obv(candles[:split])
	second := obv(candles[split:])
	return second - first
}

func highestHigh(candles []Candle, period int) float64 {
	if period <= 0 || len(candles) < period {
		return 0
	}
	max := candles[len(candles)-period].High
	for _, c := range candles[len(candles)-period:] {
		if c.High > max {
			max = c.High
		}
	}
	return max
}

func lowestLow(candles []Candle, period int) float64 {
	if period <= 0 || len(candles) < period {
		return 0
	}
	min := candles[len(candles)-period].Low
	for _, c := range candles[len(candles)-period:] {
		if c.Low < min {
			min = c.Low
		}
	}
	return min
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clamp(value, minValue, maxValue float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

// bestATRRatio returns the ATR ratio for stop-loss/take-profit distance.
// Uses the strategy's own TF ATR directly, with one-level-up fallback.
func bestATRRatio(f FeatureView, strategyTF string) float64 {
	if atr := f.ATRRatio(strategyTF); atr > 0 {
		return atr
	}
	// Fallback: one level up.
	htf := higherTF(strategyTF)
	if htf != strategyTF {
		if atr := f.ATRRatio(htf); atr > 0 {
			return atr
		}
	}
	return 0
}

// tfLadder defines the timeframe hierarchy from shortest to longest.
var tfLadder = []string{"1m", "5m", "15m", "1H", "4H", "1D"}

// higherTF returns the next higher timeframe in the ladder.
func higherTF(tf string) string {
	for i, t := range tfLadder {
		if t == tf && i+1 < len(tfLadder) {
			return tfLadder[i+1]
		}
	}
	return tf
}

// SLTPMultipliers returns (stopMult, takeMult) scaled by timeframe.
// Shorter TFs use tighter SL/TP for fast scalping; longer TFs use wider.
func SLTPMultipliers(tf string) (stop, take float64) {
	switch tf {
	case "1m":
		return 1.5, 3.5 // 盈亏比 2.3x: 止损放宽防噪音扫损, TP 加大拉高期望
	case "5m":
		return 1.2, 3.0 // 盈亏比 2.5x
	case "15m":
		return 1.5, 3.5 // 盈亏比 2.3x
	case "1H":
		return 1.5, 3.5 // 盈亏比 2.3x
	case "4H":
		return 2.0, 5.0 // 盈亏比 2.5x: 大周期趋势跟踪，让利润跑
	default:
		return 1.5, 3.0
	}
}
