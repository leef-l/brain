package strategy

import (
	"testing"
	"time"
)

type fakeView struct {
	symbol    string
	timeframe string
	candles   map[string][]Candle
	price     float64
	vector    []float64
	funding   float64
	imbalance float64
	toxicity  float64
	buyRatio  float64
	density   float64
	winRate   float64
}

func (f fakeView) Symbol() string              { return f.symbol }
func (f fakeView) Timeframe() string           { return f.timeframe }
func (f fakeView) Candles(tf string) []Candle  { return append([]Candle(nil), f.candles[tf]...) }
func (f fakeView) CurrentPrice() float64       { return f.price }
func (f fakeView) FeatureVector() []float64    { return append([]float64(nil), f.vector...) }
func (f fakeView) FundingRate() float64        { return f.funding }
func (f fakeView) OrderBookImbalance() float64 { return f.imbalance }
func (f fakeView) TradeFlowToxicity() float64  { return f.toxicity }
func (f fakeView) BigBuyRatio() float64        { return f.buyRatio }
func (f fakeView) TradeDensityRatio() float64  { return f.density }
func (f fakeView) SimilarityWinRate() float64  { return f.winRate }
func (f fakeView) Feature() FeatureView        { return nil }
func (f fakeView) HasFeatureView() bool        { return false }

func candleSeries(start, step float64, n int, volume float64) []Candle {
	out := make([]Candle, 0, n)
	for i := 0; i < n; i++ {
		close := start + float64(i)*step
		out = append(out, Candle{
			Timestamp: time.Now().Add(time.Duration(i) * time.Minute).UnixMilli(),
			Open:      close - step*0.2,
			High:      close + 0.7,
			Low:       close - 0.7,
			Close:     close,
			Volume:    volume + float64(i),
		})
	}
	return out
}

func TestTrendFollowerLong(t *testing.T) {
	view := fakeView{
		symbol:    "BTC-USDT-SWAP",
		timeframe: "1H",
		candles: map[string][]Candle{
			"1H": candleSeries(100, 1.2, 80, 100),
			"4H": candleSeries(95, 2.0, 60, 120),
		},
		price:   196,
		funding: 0.01,
		winRate: 0.58,
	}
	signal := NewTrendFollower().Compute(view)
	if signal.Direction != DirectionLong {
		t.Fatalf("direction = %s, want long", signal.Direction)
	}
	if signal.Confidence <= 0.5 {
		t.Fatalf("confidence = %.2f, want > 0.5", signal.Confidence)
	}
}

func TestMeanReversionLong(t *testing.T) {
	candles := candleSeries(200, 0, 30, 100)
	candles = append(candles, Candle{Timestamp: time.Now().UnixMilli(), Open: 140, High: 142, Low: 118, Close: 120, Volume: 25})
	candles = append(candles, Candle{Timestamp: time.Now().Add(time.Minute).UnixMilli(), Open: 120, High: 121, Low: 112, Close: 114, Volume: 20})
	view := fakeView{
		symbol:    "ETH-USDT-SWAP",
		timeframe: "15m",
		candles:   map[string][]Candle{"15m": candles},
		price:     candles[len(candles)-1].Close,
	}
	signal := NewMeanReversion().Compute(view)
	if signal.Direction != DirectionLong {
		t.Fatalf("direction = %s, want long", signal.Direction)
	}
}

func TestBreakoutMomentumLong(t *testing.T) {
	candles := candleSeries(100, 0, 24, 100)
	for i := 24; i < 31; i++ {
		close := 128 + float64(i-24)*2
		volume := 1000.0
		candles = append(candles, Candle{
			Timestamp: time.Now().Add(time.Duration(i) * time.Minute).UnixMilli(),
			Open:      close - 0.3,
			High:      close + 1.0,
			Low:       close - 0.8,
			Close:     close,
			Volume:    volume,
		})
	}
	view := fakeView{
		symbol:    "SOL-USDT-SWAP",
		timeframe: "1H",
		candles:   map[string][]Candle{"1H": candles},
		price:     candles[len(candles)-1].Close + 0.5,
	}
	signal := NewBreakoutMomentum().Compute(view)
	if signal.Direction != DirectionLong {
		t.Fatalf("direction = %s, want long", signal.Direction)
	}
}

func TestOrderFlowLong(t *testing.T) {
	view := fakeView{
		symbol:    "XRP-USDT-SWAP",
		timeframe: "tick",
		candles:   map[string][]Candle{"tick": candleSeries(1, 0.01, 12, 20)},
		price:     1.12,
		imbalance: 0.6,
		toxicity:  0.7,
		buyRatio:  0.82,
		density:   3.1,
	}
	signal := NewOrderFlow().Compute(view)
	if signal.Direction != DirectionLong {
		t.Fatalf("direction = %s, want long", signal.Direction)
	}
}

type oracleStub struct {
	winRate float64
	ok      bool
}

func (o oracleStub) HistoricalWinRate(symbol string, direction Direction, featureVector []float64) (float64, bool) {
	return o.winRate, o.ok
}

func TestAggregatorHistoryVeto(t *testing.T) {
	view := fakeView{
		symbol: "BTC-USDT-SWAP",
		vector: []float64{1, 2, 3},
	}
	agg := NewAggregator()
	agg.Oracle = oracleStub{winRate: 0.2, ok: true}
	result := agg.Aggregate(view, []Signal{{
		Strategy:   "TrendFollower",
		Direction:  DirectionLong,
		Confidence: 1,
	}}, ReviewContext{})
	if result.Direction != DirectionHold {
		t.Fatalf("direction = %s, want hold", result.Direction)
	}
}

// ── RegimeAwareAggregator tests ─────────────────────────────────

type fakeFeatureView struct {
	vec [192]float64
}

func (f *fakeFeatureView) EMADeviation(tf string, period int) float64 { return 0 }
func (f *fakeFeatureView) EMACross(tf string) float64                 { return 0 }
func (f *fakeFeatureView) RSI(tf string) float64                      { return 0 }
func (f *fakeFeatureView) MACDHistogram(tf string) float64            { return 0 }
func (f *fakeFeatureView) BBPosition(tf string) float64               { return 0 }
func (f *fakeFeatureView) ATRRatio(tf string) float64                 { return 0 }
func (f *fakeFeatureView) PriceChange(tf string, bars int) float64    { return 0 }
func (f *fakeFeatureView) Volatility(tf string, bars int) float64     { return 0 }
func (f *fakeFeatureView) ADX(tf string) float64                      { return 0 }
func (f *fakeFeatureView) VolumeRatio(tf string) float64              { return 0 }
func (f *fakeFeatureView) OBVSlope(tf string) float64                 { return 0 }
func (f *fakeFeatureView) VolumePriceCorr(tf string) float64          { return 0 }
func (f *fakeFeatureView) VolumeBreakout(tf string) bool              { return false }
func (f *fakeFeatureView) OrderBookImbalance() float64                { return 0 }
func (f *fakeFeatureView) Spread() float64                            { return 0 }
func (f *fakeFeatureView) TradeFlowToxicity() float64                 { return 0 }
func (f *fakeFeatureView) BigBuyRatio() float64                       { return 0 }
func (f *fakeFeatureView) BigSellRatio() float64                      { return 0 }
func (f *fakeFeatureView) TradeDensityRatio() float64                 { return 0 }
func (f *fakeFeatureView) BuySellRatio() float64                      { return 0 }
func (f *fakeFeatureView) FundingRate() float64                       { return 0 }
func (f *fakeFeatureView) Momentum(tf string, bars int) float64       { return 0 }
func (f *fakeFeatureView) VolatilityRatio(tf string) float64          { return 0 }
func (f *fakeFeatureView) BTCExcessReturn() float64                   { return 0 }
func (f *fakeFeatureView) BTCMomentum() float64                       { return 0 }
func (f *fakeFeatureView) ETHMomentum() float64                       { return 0 }
func (f *fakeFeatureView) BTCCorrelation() float64                    { return 0 }
func (f *fakeFeatureView) ETHCorrelation() float64                    { return 0 }
func (f *fakeFeatureView) MLReady() bool                              { return false }
func (f *fakeFeatureView) Symbol() string                             { return "TEST" }
func (f *fakeFeatureView) CurrentPrice() float64                      { return 100 }
func (f *fakeFeatureView) RawVector() []float64                       { return f.vec[:] }

func (f *fakeFeatureView) MarketRegime() MarketRegimeProb {
	return MarketRegimeProb{
		Trend:    f.vec[176],
		Range:    f.vec[177],
		Breakout: f.vec[178],
		Panic:    f.vec[179],
	}
}

func (f *fakeFeatureView) VolPrediction() VolPrediction {
	return VolPrediction{Vol1H: f.vec[180], Vol4H: f.vec[181], VolPercentile: f.vec[182], VolDirection: f.vec[183]}
}

func (f *fakeFeatureView) AnomalyScore() AnomalyScore {
	return AnomalyScore{Price: f.vec[184], Volume: f.vec[185], OrderBook: f.vec[186], Combined: f.vec[187]}
}

// featureAwareView is a fakeView with a FeatureView attached.
type featureAwareView struct {
	fakeView
	fv FeatureView
}

func (v featureAwareView) Feature() FeatureView  { return v.fv }
func (v featureAwareView) HasFeatureView() bool   { return v.fv != nil }

func TestRegimeAggregatorTrendBoost(t *testing.T) {
	var vec [192]float64
	vec[176] = 0.8 // trend dominant
	vec[177] = 0.1
	vec[178] = 0.05
	vec[179] = 0.05

	view := featureAwareView{
		fakeView: fakeView{symbol: "BTC-USDT-SWAP"},
		fv:       &fakeFeatureView{vec: vec},
	}

	signals := []Signal{
		{Strategy: "TrendFollower", Direction: DirectionLong, Confidence: 0.9},
		{Strategy: "MeanReversion", Direction: DirectionLong, Confidence: 0.6},
	}

	ra := NewRegimeAwareAggregator()
	result := ra.Aggregate(view, signals, ReviewContext{})

	// In trend regime, TrendFollower weight = 0.40 vs default 0.30
	// So the long score should be higher than with default weights
	if result.Direction != DirectionLong {
		t.Fatalf("direction = %s, want long", result.Direction)
	}
	if result.Confidence <= 0 {
		t.Fatalf("confidence = %.2f, want > 0", result.Confidence)
	}
}

func TestRegimeAggregatorPanicDamping(t *testing.T) {
	var vec [192]float64
	vec[176] = 0.05
	vec[177] = 0.05
	vec[178] = 0.05
	vec[179] = 0.85 // panic dominant

	view := featureAwareView{
		fakeView: fakeView{symbol: "BTC-USDT-SWAP"},
		fv:       &fakeFeatureView{vec: vec},
	}

	signals := []Signal{
		{Strategy: "TrendFollower", Direction: DirectionLong, Confidence: 0.95},
		{Strategy: "BreakoutMomentum", Direction: DirectionLong, Confidence: 0.9},
		{Strategy: "OrderFlow", Direction: DirectionLong, Confidence: 0.8},
	}

	ra := NewRegimeAwareAggregator()
	result := ra.Aggregate(view, signals, ReviewContext{})

	// Panic should flag NeedsReview
	if !result.NeedsReview {
		t.Fatal("panic regime should flag NeedsReview")
	}
	if result.ReviewReason == "" {
		t.Fatal("expected ReviewReason to contain panic note")
	}
}

func TestRegimeAggregatorFallbackNoFeatureView(t *testing.T) {
	// Without FeatureView, should fall back to default weights (same as Aggregator)
	view := fakeView{
		symbol: "BTC-USDT-SWAP",
	}
	signals := []Signal{
		{Strategy: "TrendFollower", Direction: DirectionLong, Confidence: 0.9},
		{Strategy: "MeanReversion", Direction: DirectionLong, Confidence: 0.8},
	}

	ra := NewRegimeAwareAggregator()
	result := ra.Aggregate(view, signals, ReviewContext{})

	agg := NewAggregator()
	baseline := agg.Aggregate(view, signals, ReviewContext{})

	if result.Direction != baseline.Direction {
		t.Fatalf("fallback direction %s != baseline %s", result.Direction, baseline.Direction)
	}
	if result.Confidence != baseline.Confidence {
		t.Fatalf("fallback confidence %.4f != baseline %.4f", result.Confidence, baseline.Confidence)
	}
}

func TestRegimeAggregatorAnomalyReview(t *testing.T) {
	var vec [192]float64
	vec[176] = 0.5 // trend
	vec[187] = 0.9 // high combined anomaly

	view := featureAwareView{
		fakeView: fakeView{symbol: "ETH-USDT-SWAP"},
		fv:       &fakeFeatureView{vec: vec},
	}

	signals := []Signal{
		{Strategy: "TrendFollower", Direction: DirectionLong, Confidence: 0.9},
	}

	ra := NewRegimeAwareAggregator()
	result := ra.Aggregate(view, signals, ReviewContext{})

	if !result.NeedsReview {
		t.Fatal("high anomaly should trigger NeedsReview")
	}
}
