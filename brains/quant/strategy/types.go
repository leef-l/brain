package strategy

import "time"

// Direction is the normalized trading direction used by strategy and risk.
type Direction string

const (
	DirectionFlat  Direction = "flat"
	DirectionHold  Direction = "hold"
	DirectionLong  Direction = "long"
	DirectionShort Direction = "short"
)

// Candle is the minimal market bar used by the simplified strategies.
type Candle struct {
	Timestamp int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

// ── MarketView v2 ────────────────────────────────────────────────

// MarketView is the narrow data contract consumed by strategies and the
// signal aggregator. The concrete data source can be a snapshot, adapter,
// or test double.
//
// v2 adds Feature() and HasFeatureView() for structured access to the
// 192-dim feature vector. Strategies should prefer FeatureView when
// available (live trading) and fall back to Candles-based computation
// when it's not (backtesting).
type MarketView interface {
	Symbol() string
	Timeframe() string
	Candles(timeframe string) []Candle
	CurrentPrice() float64
	FeatureVector() []float64
	FundingRate() float64
	OrderBookImbalance() float64
	TradeFlowToxicity() float64
	BigBuyRatio() float64
	TradeDensityRatio() float64
	SimilarityWinRate() float64

	// v2: structured feature vector access
	Feature() FeatureView
	HasFeatureView() bool
}

// ── FeatureView — structured read of the 192-dim vector ──────────

// FeatureView provides O(1) index-based access to the 192-dim feature
// vector produced by the data brain. Strategies read pre-computed
// indicators through this interface instead of re-computing from candles.
type FeatureView interface {
	// Price features [0:60]
	EMADeviation(tf string, period int) float64 // period: 9/21/55
	EMACross(tf string) float64                 // EMA9 - EMA21
	RSI(tf string) float64                      // [0, 1]
	MACDHistogram(tf string) float64
	BBPosition(tf string) float64 // [0, 1]
	ATRRatio(tf string) float64
	PriceChange(tf string, bars int) float64 // bars: 5/20
	Volatility(tf string, bars int) float64  // bars: 5/20
	ADX(tf string) float64                   // [0, 1]

	// Volume features [60:100]
	VolumeRatio(tf string) float64
	OBVSlope(tf string) float64
	VolumePriceCorr(tf string) float64
	VolumeBreakout(tf string) bool

	// Microstructure [100:130]
	OrderBookImbalance() float64
	Spread() float64
	TradeFlowToxicity() float64
	BigBuyRatio() float64
	BigSellRatio() float64
	TradeDensityRatio() float64
	BuySellRatio() float64
	FundingRate() float64

	// Momentum [130:160]
	Momentum(tf string, bars int) float64 // bars: 1/3/10
	VolatilityRatio(tf string) float64    // vol5/vol20

	// Cross-asset [160:176]
	BTCExcessReturn() float64
	BTCMomentum() float64
	ETHMomentum() float64
	BTCCorrelation() float64
	ETHCorrelation() float64

	// ML-enhanced / rule-fallback [176:192]
	MarketRegime() MarketRegimeProb
	VolPrediction() VolPrediction
	AnomalyScore() AnomalyScore

	// Meta
	MLReady() bool
	Symbol() string
	CurrentPrice() float64
	RawVector() []float64 // full 192-dim
}

// MarketRegimeProb holds the 4 market state probabilities from [176:179].
type MarketRegimeProb struct {
	Trend    float64 // [176]
	Range    float64 // [177]
	Breakout float64 // [178]
	Panic    float64 // [179]
}

// Dominant returns the regime with the highest probability.
func (m MarketRegimeProb) Dominant() string {
	max := m.Trend
	name := "trend"
	if m.Range > max {
		max = m.Range
		name = "range"
	}
	if m.Breakout > max {
		max = m.Breakout
		name = "breakout"
	}
	if m.Panic > max {
		max = m.Panic
		name = "panic"
	}
	_ = max
	return name
}

// VolPrediction holds volatility predictions from [180:183].
type VolPrediction struct {
	Vol1H         float64 // [180]
	Vol4H         float64 // [181]
	VolPercentile float64 // [182]
	VolDirection  float64 // [183]
}

// AnomalyScore holds anomaly scores from [184:187].
type AnomalyScore struct {
	Price     float64 // [184]
	Volume    float64 // [185]
	OrderBook float64 // [186]
	Combined  float64 // [187]
}

// ── Snapshot — in-memory MarketView for tests and backtest ───────

// Snapshot is a lightweight in-memory implementation of MarketView.
type Snapshot struct {
	SymbolValue             string
	TimeframeValue          string
	CandlesByTimeframe      map[string][]Candle
	CurrentPriceValue       float64
	FeatureVectorValue      []float64
	FundingRateValue        float64
	OrderBookImbalanceValue float64
	TradeFlowToxicityValue  float64
	BigBuyRatioValue        float64
	TradeDensityRatioValue  float64
	SimilarityWinRateValue  float64
}

func (s Snapshot) Symbol() string { return s.SymbolValue }
func (s Snapshot) Timeframe() string {
	if s.TimeframeValue != "" {
		return s.TimeframeValue
	}
	return "1H"
}

func (s Snapshot) Candles(timeframe string) []Candle {
	if timeframe == "" {
		timeframe = s.Timeframe()
	}
	if s.CandlesByTimeframe == nil {
		return nil
	}
	return append([]Candle(nil), s.CandlesByTimeframe[timeframe]...)
}

func (s Snapshot) CurrentPrice() float64 { return s.CurrentPriceValue }
func (s Snapshot) FeatureVector() []float64 {
	return append([]float64(nil), s.FeatureVectorValue...)
}
func (s Snapshot) FundingRate() float64        { return s.FundingRateValue }
func (s Snapshot) OrderBookImbalance() float64 { return s.OrderBookImbalanceValue }
func (s Snapshot) TradeFlowToxicity() float64  { return s.TradeFlowToxicityValue }
func (s Snapshot) BigBuyRatio() float64        { return s.BigBuyRatioValue }
func (s Snapshot) TradeDensityRatio() float64  { return s.TradeDensityRatioValue }
func (s Snapshot) SimilarityWinRate() float64  { return s.SimilarityWinRateValue }

// v2: Snapshot does not carry a FeatureView (backtest mode).
func (s Snapshot) Feature() FeatureView  { return nil }
func (s Snapshot) HasFeatureView() bool  { return false }

// Signal is the simplified strategy output consumed by the aggregator and
// risk engine.
type Signal struct {
	Strategy   string
	Direction  Direction
	Confidence float64
	Entry      float64
	StopLoss   float64
	TakeProfit float64
	Reason     string
	Timestamp  time.Time
}

// Strategy is the common interface implemented by the four simplified
// strategies.
type Strategy interface {
	Name() string
	Timeframes() []string
	Compute(view MarketView) Signal
}
