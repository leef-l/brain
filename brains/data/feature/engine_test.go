package feature

import (
	"math"
	"testing"

	"github.com/leef-l/brain/brains/data/processor"
	"github.com/leef-l/brain/brains/data/provider"
)

// helper: generate n candles with linearly increasing close price.
func genCandles(instID, bar string, n int, basePrice, step float64) []provider.Candle {
	candles := make([]provider.Candle, n)
	for i := 0; i < n; i++ {
		p := basePrice + float64(i)*step
		candles[i] = provider.Candle{
			InstID:    instID,
			Bar:       bar,
			Timestamp: int64(i) * 60000,
			Open:      p - step*0.3,
			High:      p + step*0.5,
			Low:       p - step*0.5,
			Close:     p,
			Volume:    100 + float64(i)*10,
		}
	}
	return candles
}

// feedCandles feeds candle history into the aggregator.
func feedCandles(agg *processor.CandleAggregator, instID, tf string, candles []provider.Candle) {
	for _, c := range candles {
		agg.OnCandle(instID, tf, c)
	}
}

func newTestEngine() (*Engine, *processor.CandleAggregator, *processor.OrderBookTracker, *processor.TradeFlowTracker) {
	agg := processor.NewCandleAggregator()
	ob := processor.NewOrderBookTracker()
	tf := processor.NewTradeFlowTracker(300000)
	eng := NewEngine(agg, ob, tf)
	return eng, agg, ob, tf
}

func TestComputeEmpty(t *testing.T) {
	eng, _, _, _ := newTestEngine()
	vec := eng.Compute("NONEXISTENT")
	if len(vec) != VectorDim {
		t.Fatalf("expected dim %d, got %d", VectorDim, len(vec))
	}
	for i, v := range vec {
		if v != 0 {
			t.Errorf("vec[%d] = %f, want 0", i, v)
		}
	}
}

func TestComputePriceFeatures(t *testing.T) {
	eng, agg, _, _ := newTestEngine()
	instID := "BTC-USDT-SWAP"

	// Feed 60 candles into 1m timeframe to ensure all indicators are ready
	candles := genCandles(instID, "1m", 60, 50000, 10)
	feedCandles(agg, instID, "1m", candles)

	vec := eng.Compute(instID)
	if len(vec) != VectorDim {
		t.Fatalf("expected dim %d, got %d", VectorDim, len(vec))
	}

	// Price features [0:12] should have some non-zero values
	hasNonZero := false
	for i := 0; i < 12; i++ {
		if vec[i] != 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		t.Error("expected some non-zero values in price features [0:12]")
	}

	// EMA9 should be ready after 60 candles
	if vec[0] == 0 {
		t.Error("vec[0] (EMA9 deviation) should be non-zero after 60 candles")
	}
	// RSI should be ready
	if vec[4] == 0 {
		t.Error("vec[4] (RSI) should be non-zero after 60 candles")
	}
	// RSI should be in [0,1]
	if vec[4] < 0 || vec[4] > 1 {
		t.Errorf("vec[4] (RSI normalized) = %f, want [0,1]", vec[4])
	}
}

func TestComputeMicrostructure(t *testing.T) {
	eng, _, obTracker, tfTracker := newTestEngine()
	instID := "ETH-USDT-SWAP"

	// Feed order book
	book := provider.OrderBook{
		InstID:    instID,
		Timestamp: 1000,
		Bids: [5]provider.PriceLevel{
			{Price: 3000, Size: 10},
			{Price: 2999, Size: 20},
			{Price: 2998, Size: 30},
			{Price: 2997, Size: 40},
			{Price: 2996, Size: 50},
		},
		Asks: [5]provider.PriceLevel{
			{Price: 3001, Size: 8},
			{Price: 3002, Size: 15},
			{Price: 3003, Size: 25},
			{Price: 3004, Size: 35},
			{Price: 3005, Size: 45},
		},
	}
	obTracker.Update(instID, book)

	// Feed trades
	trades := []provider.Trade{
		{InstID: instID, Price: 3000.5, Size: 5, Side: "buy", Timestamp: 1000},
		{InstID: instID, Price: 3000.3, Size: 3, Side: "sell", Timestamp: 1100},
		{InstID: instID, Price: 3000.8, Size: 50, Side: "buy", Timestamp: 1200}, // big trade
	}
	for _, tr := range trades {
		tfTracker.OnTrade(instID, tr)
	}

	vec := eng.Compute(instID)

	// Microstructure [100:130] should have non-zero values
	if vec[100] == 0 {
		t.Error("vec[100] (Imbalance) should be non-zero")
	}
	if vec[101] == 0 {
		t.Error("vec[101] (Spread) should be non-zero")
	}
	if vec[102] == 0 {
		t.Error("vec[102] (MidPrice) should be non-zero")
	}

	// Trade flow features
	if vec[110] == 0 {
		t.Error("vec[110] (Toxicity) should be non-zero")
	}
	if vec[114] == 0 {
		t.Error("vec[114] (BuySellRatio) should be non-zero")
	}
}

func TestComputeTimeSeries(t *testing.T) {
	eng, agg, _, _ := newTestEngine()
	instID := "BTC-USDT-SWAP"

	// Feed enough candles for time-series features
	candles := genCandles(instID, "1m", 30, 50000, 10)
	feedCandles(agg, instID, "1m", candles)

	vec := eng.Compute(instID)

	// Time-series features [130:136] (1m timeframe) should have some non-zero values
	hasNonZero := false
	for i := 130; i < 136; i++ {
		if vec[i] != 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		t.Error("expected some non-zero values in time-series features [130:136]")
	}
}

func TestComputeCrossAsset(t *testing.T) {
	eng, agg, _, _ := newTestEngine()

	// Feed BTC and ETH data
	btcCandles := genCandles("BTC-USDT-SWAP", "1m", 30, 50000, 10)
	feedCandles(agg, "BTC-USDT-SWAP", "1m", btcCandles)

	ethCandles := genCandles("ETH-USDT-SWAP", "1m", 30, 3000, 5)
	feedCandles(agg, "ETH-USDT-SWAP", "1m", ethCandles)

	// Feed SOL data (a non-BTC instrument to test cross-asset)
	solCandles := genCandles("SOL-USDT-SWAP", "1m", 30, 100, 1)
	feedCandles(agg, "SOL-USDT-SWAP", "1m", solCandles)

	vec := eng.Compute("SOL-USDT-SWAP")

	// Cross-asset features [160:180]
	// vec[160] = excess return vs BTC
	// vec[161] = BTC momentum
	// vec[165] = ETH momentum
	if vec[160] == 0 && vec[161] == 0 {
		t.Error("expected non-zero cross-asset features for SOL vs BTC")
	}
	if vec[165] == 0 {
		t.Error("expected non-zero ETH momentum in cross-asset features")
	}

	// BTC should NOT have cross-asset vs itself
	btcVec := eng.Compute("BTC-USDT-SWAP")
	if btcVec[160] != 0 || btcVec[161] != 0 {
		t.Error("BTC should not have excess return vs itself")
	}
}

func TestComputeArrayDim(t *testing.T) {
	eng, _, _, _ := newTestEngine()
	arr := eng.ComputeArray("TEST")
	if len(arr) != VectorDim {
		t.Fatalf("expected array length %d, got %d", VectorDim, len(arr))
	}
}

func TestSerializeDeserialize(t *testing.T) {
	original := make([]float64, VectorDim)
	for i := range original {
		original[i] = float64(i) * 0.123
	}

	data := Serialize(original)
	restored := Deserialize(data)

	if len(restored) != len(original) {
		t.Fatalf("length mismatch: %d vs %d", len(restored), len(original))
	}
	for i := range original {
		if restored[i] != original[i] {
			t.Errorf("mismatch at [%d]: %f vs %f", i, restored[i], original[i])
		}
	}
}

func TestCosineSimilarity(t *testing.T) {
	// Same vector -> similarity = 1
	a := []float64{1, 2, 3, 4, 5}
	sim := CosineSimilarity(a, a)
	if math.Abs(sim-1.0) > 1e-10 {
		t.Errorf("same vector similarity = %f, want 1.0", sim)
	}

	// Orthogonal vectors -> similarity = 0
	b := []float64{1, 0}
	c := []float64{0, 1}
	sim = CosineSimilarity(b, c)
	if math.Abs(sim) > 1e-10 {
		t.Errorf("orthogonal similarity = %f, want 0.0", sim)
	}

	// Opposite vectors -> similarity = -1
	d := []float64{1, 2, 3}
	e := []float64{-1, -2, -3}
	sim = CosineSimilarity(d, e)
	if math.Abs(sim+1.0) > 1e-10 {
		t.Errorf("opposite similarity = %f, want -1.0", sim)
	}

	// Zero vector -> similarity = 0
	z := []float64{0, 0, 0}
	sim = CosineSimilarity(a[:3], z)
	if sim != 0 {
		t.Errorf("zero vector similarity = %f, want 0.0", sim)
	}

	// Empty / mismatched -> 0
	if CosineSimilarity(nil, nil) != 0 {
		t.Error("nil similarity should be 0")
	}
	if CosineSimilarity(a, b) != 0 {
		t.Error("mismatched length similarity should be 0")
	}
}

func TestNormalize(t *testing.T) {
	vec := []float64{10, 20, 30, 40, 50}
	norm := Normalize(vec)
	if len(norm) != 5 {
		t.Fatalf("expected length 5, got %d", len(norm))
	}
	if norm[0] != 0 {
		t.Errorf("min should normalize to 0, got %f", norm[0])
	}
	if norm[4] != 1 {
		t.Errorf("max should normalize to 1, got %f", norm[4])
	}
	if math.Abs(norm[2]-0.5) > 1e-10 {
		t.Errorf("mid should normalize to 0.5, got %f", norm[2])
	}

	// All same values -> all zeros
	same := []float64{5, 5, 5}
	normSame := Normalize(same)
	for i, v := range normSame {
		if v != 0 {
			t.Errorf("same values: norm[%d] = %f, want 0", i, v)
		}
	}

	// Empty
	if Normalize(nil) != nil {
		t.Error("nil input should return nil")
	}
}
