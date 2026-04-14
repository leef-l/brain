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

// MarketView is the narrow data contract consumed by strategies and the
// signal aggregator. The concrete data source can be a snapshot, adapter, or
// test double.
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
}

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
