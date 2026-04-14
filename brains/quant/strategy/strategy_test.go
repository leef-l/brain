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
