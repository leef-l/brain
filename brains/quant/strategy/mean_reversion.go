package strategy

import (
	"fmt"
	"math"
	"time"
)

// MeanReversionParams holds tunable parameters for MeanReversion.
type MeanReversionParams struct {
	BBOversold       float64 `json:"bb_oversold" yaml:"bb_oversold"`               // BB 超卖线 (default 0.15)
	BBOverbought     float64 `json:"bb_overbought" yaml:"bb_overbought"`           // BB 超买线 (default 0.85)
	MaxVolumeRatio   float64 `json:"max_volume_ratio" yaml:"max_volume_ratio"`     // 最大量比 (default 1.2)
	BaseConfidence   float64 `json:"base_confidence" yaml:"base_confidence"`       // 基础置信度 (default 0.45)
	SignalBoost      float64 `json:"signal_boost" yaml:"signal_boost"`             // 方向确认加分 (default 0.25)
	SecondaryBBLong  float64 `json:"secondary_bb_long" yaml:"secondary_bb_long"`   // 二级BB超卖阈值 (default 0.35)
	SecondaryRSILong float64 `json:"secondary_rsi_long" yaml:"secondary_rsi_long"` // 二级RSI超卖阈值 (default 0.40)
	SecondaryBBShort float64 `json:"secondary_bb_short" yaml:"secondary_bb_short"` // 二级BB超买阈值 (default 0.65)
	SecondaryRSIShort float64 `json:"secondary_rsi_short" yaml:"secondary_rsi_short"` // 二级RSI超买阈值 (default 0.60)
	HTFADXThreshold  float64 `json:"htf_adx_threshold" yaml:"htf_adx_threshold"`  // 高TF ADX强趋势阈值 (default 0.30)
	HTFBoost         float64 `json:"htf_boost" yaml:"htf_boost"`                  // 高TF强趋势加成 (default 1.15)
	CalmBoost        float64 `json:"calm_boost" yaml:"calm_boost"`                // 低波回调加成 (default 1.10)
	FundingBoost     float64 `json:"funding_boost" yaml:"funding_boost"`           // 资金费率对齐加成 (default 1.10)
	TPMultiplier     float64 `json:"tp_multiplier" yaml:"tp_multiplier"`           // TP放大系数（顺势更大TP）(default 1.20)
	SLFallbackPct    float64 `json:"sl_fallback_pct" yaml:"sl_fallback_pct"`       // ATR无效时止损回退 (default 0.005)
}

func DefaultMeanReversionParams() MeanReversionParams {
	return MeanReversionParams{
		BBOversold:        0.20,
		BBOverbought:      0.80,
		MaxVolumeRatio:    1.5,
		BaseConfidence:    0.45,
		SignalBoost:       0.30,
		SecondaryBBLong:   0.40,
		SecondaryRSILong:  0.45,
		SecondaryBBShort:  0.60,
		SecondaryRSIShort: 0.55,
		HTFADXThreshold:   0.30,
		HTFBoost:          1.15,
		CalmBoost:         1.10,
		FundingBoost:      1.10,
		TPMultiplier:      1.20,
		SLFallbackPct:     0.005,
	}
}

type MeanReversion struct {
	Params MeanReversionParams
}

func NewMeanReversion() Strategy { return MeanReversion{Params: DefaultMeanReversionParams()} }

func NewMeanReversionWithParams(p MeanReversionParams) Strategy {
	d := DefaultMeanReversionParams()
	if p.BBOversold <= 0 { p.BBOversold = d.BBOversold }
	if p.BBOverbought <= 0 { p.BBOverbought = d.BBOverbought }
	if p.MaxVolumeRatio <= 0 { p.MaxVolumeRatio = d.MaxVolumeRatio }
	if p.BaseConfidence <= 0 { p.BaseConfidence = d.BaseConfidence }
	if p.SignalBoost <= 0 { p.SignalBoost = d.SignalBoost }
	if p.SecondaryBBLong <= 0 { p.SecondaryBBLong = d.SecondaryBBLong }
	if p.SecondaryRSILong <= 0 { p.SecondaryRSILong = d.SecondaryRSILong }
	if p.SecondaryBBShort <= 0 { p.SecondaryBBShort = d.SecondaryBBShort }
	if p.SecondaryRSIShort <= 0 { p.SecondaryRSIShort = d.SecondaryRSIShort }
	if p.HTFADXThreshold <= 0 { p.HTFADXThreshold = d.HTFADXThreshold }
	if p.HTFBoost <= 0 { p.HTFBoost = d.HTFBoost }
	if p.CalmBoost <= 0 { p.CalmBoost = d.CalmBoost }
	if p.FundingBoost <= 0 { p.FundingBoost = d.FundingBoost }
	if p.TPMultiplier <= 0 { p.TPMultiplier = d.TPMultiplier }
	if p.SLFallbackPct <= 0 { p.SLFallbackPct = d.SLFallbackPct }
	return MeanReversion{Params: p}
}

func (MeanReversion) Name() string { return "MeanReversion" }

func (MeanReversion) Timeframes() []string { return []string{"1m", "5m", "15m", "1H", "4H"} }

func (m MeanReversion) Compute(view MarketView) Signal {
	if view.HasFeatureView() {
		return m.computeFromFeatures(view)
	}
	return m.computeLegacy(view)
}

// computeFromFeatures implements trend-aligned pullback entry:
// 1. Read higher TF EMA to determine the dominant trend direction
// 2. Only trade WITH the trend — never against it
// 3. Wait for price to pull back to BB band (oversold in uptrend / overbought in downtrend)
// 4. Enter when the pullback reaches a good level, with trend as tailwind
func (m MeanReversion) computeFromFeatures(view MarketView) Signal {
	f := view.Feature()
	tf := view.Timeframe()

	bbPos := f.BBPosition(tf)     // [0,1]: 0=at lower, 0.5=at mid, 1=at upper
	rsiVal := f.RSI(tf)           // [0,1] normalized
	adxVal := f.ADX(tf)           // [0,1] normalized
	volRatio := f.VolumeRatio(tf) // current vol / SMA vol

	if f.CurrentPrice() <= 0 {
		return Signal{Strategy: "MeanReversion", Direction: DirectionHold, Reason: "feature vector not ready", Timestamp: time.Now().UTC()}
	}

	// BB/RSI zero means the indicator hasn't warmed up yet.
	bbReady := bbPos != 0 || rsiVal != 0
	if !bbReady {
		return Signal{Strategy: "MeanReversion", Direction: DirectionHold, Reason: "indicators warming up", Timestamp: time.Now().UTC()}
	}

	// ── Step 1: Determine higher-TF trend direction ──
	// Use one level up from current TF for trend context.
	htf := higherTF(tf)
	htfEma9 := f.EMADeviation(htf, 9)
	htfEma21 := f.EMADeviation(htf, 21)

	trendUp := htfEma9 > 0 && htfEma21 > 0
	trendDown := htfEma9 < 0 && htfEma21 < 0

	noTrend := !trendUp && !trendDown

	// ── Step 2: Wait for pullback to the right zone ──
	// When HTF trend is clear, use secondary (relaxed) thresholds.
	// When no HTF trend, require primary (strict) BB extremes only.
	var long, short bool
	if trendUp {
		long = (bbPos < m.Params.BBOversold || (bbPos < m.Params.SecondaryBBLong && rsiVal < m.Params.SecondaryRSILong)) && volRatio <= m.Params.MaxVolumeRatio
	} else if trendDown {
		short = (bbPos > m.Params.BBOverbought || (bbPos > m.Params.SecondaryBBShort && rsiVal > m.Params.SecondaryRSIShort)) && volRatio <= m.Params.MaxVolumeRatio
	} else {
		long = bbPos < m.Params.BBOversold && rsiVal < m.Params.SecondaryRSILong && volRatio <= m.Params.MaxVolumeRatio
		short = bbPos > m.Params.BBOverbought && rsiVal > m.Params.SecondaryRSIShort && volRatio <= m.Params.MaxVolumeRatio
	}

	if !long && !short {
		pullbackInfo := fmt.Sprintf("bb=%.2f rsi=%.2f", bbPos, rsiVal)
		var label string
		switch {
		case trendUp:
			label = "uptrend but no pullback yet"
		case trendDown:
			label = "downtrend but no rally yet"
		default:
			label = "no trend and no BB extreme"
		}
		return Signal{Strategy: "MeanReversion", Direction: DirectionHold, Confidence: 0.12,
			Reason: fmt.Sprintf("%s (%s)", label, pullbackInfo), Timestamp: time.Now().UTC()}
	}

	// ── Step 3: Score confidence ──
	confidence := m.Params.BaseConfidence
	direction := DirectionHold
	reason := ""

	if long {
		direction = DirectionLong
		confidence += m.Params.SignalBoost
		if noTrend {
			confidence *= 0.75
			reason = fmt.Sprintf("no-trend BB extreme long: bb=%.2f rsi=%.2f adx=%.2f",
				bbPos, rsiVal, adxVal)
		} else {
			reason = fmt.Sprintf("uptrend pullback: bb=%.2f rsi=%.2f adx=%.2f %s_ema9=%.4f",
				bbPos, rsiVal, adxVal, htf, htfEma9)
		}
	} else {
		direction = DirectionShort
		confidence += m.Params.SignalBoost
		if noTrend {
			confidence *= 0.75
			reason = fmt.Sprintf("no-trend BB extreme short: bb=%.2f rsi=%.2f adx=%.2f",
				bbPos, rsiVal, adxVal)
		} else {
			reason = fmt.Sprintf("downtrend rally: bb=%.2f rsi=%.2f adx=%.2f %s_ema9=%.4f",
				bbPos, rsiVal, adxVal, htf, htfEma9)
		}
	}

	// Stronger trend (higher ADX on higher TF) → more confidence
	htfAdx := f.ADX(htf)
	if htfAdx > m.Params.HTFADXThreshold {
		confidence *= m.Params.HTFBoost
		reason += "; strong htf trend"
	}

	// Current TF low ADX (ranging locally) is good — means the pullback is calm, not panic
	if adxVal < 0.25 {
		confidence *= m.Params.CalmBoost
		reason += "; calm pullback"
	}

	// Funding rate alignment
	if fr := f.FundingRate(); direction == DirectionShort && fr > 0.0005 {
		confidence *= m.Params.FundingBoost
		reason += "; crowded longs support short"
	} else if direction == DirectionLong && fr < -0.0005 {
		confidence *= m.Params.FundingBoost
		reason += "; negative funding supports long"
	}

	confidence = clamp(confidence, 0, 1)
	priceNow := f.CurrentPrice()
	slATR := bestATRRatio(f, tf)
	slMult, tpMult := SLTPMultipliers(tf)
	stopDistance := slATR * priceNow * slMult
	if stopDistance <= 0 {
		stopDistance = math.Abs(priceNow) * m.Params.SLFallbackPct
	}
	takeDistance := slATR * priceNow * tpMult * m.Params.TPMultiplier

	signal := Signal{
		Strategy:   "MeanReversion",
		Direction:  direction,
		Confidence: confidence,
		Entry:      priceNow,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	if long {
		signal.StopLoss = priceNow - stopDistance
		signal.TakeProfit = priceNow + takeDistance
	} else {
		signal.StopLoss = priceNow + stopDistance
		signal.TakeProfit = priceNow - takeDistance
	}
	return signal
}

// computeLegacy is the original candle-based computation for backtest mode.
func (MeanReversion) computeLegacy(view MarketView) Signal {
	candles := view.Candles(view.Timeframe())
	if len(candles) < 25 {
		return Signal{Strategy: "MeanReversion", Direction: DirectionHold, Reason: "insufficient candles", Timestamp: time.Now().UTC()}
	}

	prices := closes(candles)
	vols := volumes(candles)
	mid, upper, lower, width := bollinger(prices, 20, 2.5)
	rsiValue := rsi(prices, 14)
	adxValue, _, _ := adx(candles, 14)
	volMA := sma(vols, 20)
	closePrice := last(prices)
	entry := view.CurrentPrice()
	if entry <= 0 {
		entry = closePrice
	}

	long := (closePrice < lower || (closePrice < mid && rsiValue < 35)) && last(vols) <= volMA*1.2
	short := (closePrice > upper || (closePrice > mid && rsiValue > 65)) && last(vols) <= volMA*1.2
	if !long && !short {
		return Signal{
			Strategy:   "MeanReversion",
			Direction:  DirectionHold,
			Confidence: 0.12,
			Reason:     "mean reversion setup absent",
			Timestamp:  time.Now().UTC(),
		}
	}

	confidence := 0.40
	reason := ""
	if long {
		confidence += 0.25
		reason = fmt.Sprintf("price below lower band, rsi=%.2f, adx=%.2f", rsiValue, adxValue)
	} else {
		confidence += 0.25
		reason = fmt.Sprintf("price above upper band, rsi=%.2f, adx=%.2f", rsiValue, adxValue)
	}

	stopDistance := width * 0.5
	if stopDistance <= 0 {
		stopDistance = math.Abs(entry) * 0.01
	}
	signal := Signal{
		Strategy:   "MeanReversion",
		Confidence: clamp(confidence, 0, 1),
		Entry:      entry,
		Reason:     reason,
		Timestamp:  time.Now().UTC(),
	}
	if long {
		signal.Direction = DirectionLong
		signal.StopLoss = entry - stopDistance
		signal.TakeProfit = mid
	} else {
		signal.Direction = DirectionShort
		signal.StopLoss = entry + stopDistance
		signal.TakeProfit = mid
	}
	return signal
}
