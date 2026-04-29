package adapter

import (
	"testing"
)

func TestMarketAdapterSymbolAndTimeframe(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1H")
	if ma.Symbol() != "BTC-USDT-SWAP" {
		t.Fatalf("symbol = %s, want BTC-USDT-SWAP", ma.Symbol())
	}
	if ma.Timeframe() != "1H" {
		t.Fatalf("timeframe = %s, want 1H", ma.Timeframe())
	}
}

func TestMarketAdapterDefaultTimeframe(t *testing.T) {
	ma := NewMarketAdapter("ETH-USDT-SWAP", "")
	if ma.Timeframe() != "1H" {
		t.Fatalf("timeframe = %s, want 1H", ma.Timeframe())
	}
}

func TestMarketAdapterUpdateTickUpdatesPrice(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1m")
	ma.UpdateTick(Tick{Symbol: "BTC-USDT-SWAP", Price: 50000, Volume: 1, Timestamp: 1000})
	if ma.CurrentPrice() != 50000 {
		t.Fatalf("price = %.2f, want 50000", ma.CurrentPrice())
	}
}

func TestMarketAdapterCandleAggregation(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1m")

	// Tick 1 — interval 0
	ma.UpdateTick(Tick{Price: 100, Volume: 10, Timestamp: 0})
	// Tick 2 — same interval
	ma.UpdateTick(Tick{Price: 110, Volume: 5, Timestamp: 30_000})
	// Tick 3 — next interval (60s)
	ma.UpdateTick(Tick{Price: 120, Volume: 8, Timestamp: 60_000})
	// Tick 4 — same interval
	ma.UpdateTick(Tick{Price: 115, Volume: 2, Timestamp: 90_000})

	candles := ma.Candles("1m")
	if len(candles) != 1 {
		t.Fatalf("candle count = %d, want 1", len(candles))
	}
	c := candles[0]
	if c.Open != 100 {
		t.Fatalf("open = %.2f, want 100", c.Open)
	}
	if c.High != 110 {
		t.Fatalf("high = %.2f, want 110", c.High)
	}
	if c.Low != 100 {
		t.Fatalf("low = %.2f, want 100", c.Low)
	}
	if c.Close != 110 {
		t.Fatalf("close = %.2f, want 110", c.Close)
	}
	if c.Volume != 15 {
		t.Fatalf("volume = %.2f, want 15", c.Volume)
	}
}

func TestMarketAdapterMultiTimeframe(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1m")

	// 1m boundary at 0 and 60s
	ma.UpdateTick(Tick{Price: 100, Volume: 1, Timestamp: 0})
	ma.UpdateTick(Tick{Price: 105, Volume: 1, Timestamp: 60_000})

	if len(ma.Candles("1m")) != 1 {
		t.Fatalf("1m candles = %d, want 1", len(ma.Candles("1m")))
	}
	// 5m boundary at 0 and 300s — only one candle so far
	if len(ma.Candles("5m")) != 0 {
		t.Fatalf("5m candles = %d, want 0", len(ma.Candles("5m")))
	}
}

func TestMarketAdapterFeatureVector(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1H")
	if ma.HasFeatureView() {
		t.Fatal("expected HasFeatureView = false before update")
	}

	var vec [192]float64
	vec[100] = 0.42 // OrderBookImbalance
	vec[120] = 0.01 // FundingRate
	ma.UpdateFeatureVector(vec)

	if !ma.HasFeatureView() {
		t.Fatal("expected HasFeatureView = true after update")
	}

	fv := ma.Feature()
	if fv == nil {
		t.Fatal("expected Feature() != nil")
	}
	if fv.OrderBookImbalance() != 0.42 {
		t.Fatalf("obi = %.2f, want 0.42", fv.OrderBookImbalance())
	}

	raw := ma.FeatureVector()
	if len(raw) != 192 {
		t.Fatalf("vector len = %d, want 192", len(raw))
	}
	if raw[100] != 0.42 {
		t.Fatalf("raw[100] = %.2f, want 0.42", raw[100])
	}
}

func TestMarketAdapterMicrostructureFallback(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1H")
	ma.UpdateTick(Tick{
		Price:              50000,
		FundingRate:        0.005,
		OrderBookImbalance: 0.3,
		TradeFlowToxicity:  0.6,
		BigBuyRatio:        0.7,
		BigSellRatio:       0.2,
		TradeDensityRatio:  1.5,
		Timestamp:          0,
	})

	if ma.FundingRate() != 0.005 {
		t.Fatalf("funding = %.4f, want 0.005", ma.FundingRate())
	}
	if ma.OrderBookImbalance() != 0.3 {
		t.Fatalf("obi = %.2f, want 0.3", ma.OrderBookImbalance())
	}
	if ma.TradeFlowToxicity() != 0.6 {
		t.Fatalf("toxicity = %.2f, want 0.6", ma.TradeFlowToxicity())
	}
	if ma.BigBuyRatio() != 0.7 {
		t.Fatalf("bigBuy = %.2f, want 0.7", ma.BigBuyRatio())
	}
	if ma.BigSellRatio() != 0.2 {
		t.Fatalf("bigSell = %.2f, want 0.2", ma.BigSellRatio())
	}
	if ma.TradeDensityRatio() != 1.5 {
		t.Fatalf("density = %.2f, want 1.5", ma.TradeDensityRatio())
	}
}

func TestMarketAdapterFeatureViewOverridesFallback(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1H")
	ma.UpdateTick(Tick{
		Price:              50000,
		FundingRate:        0.005,
		OrderBookImbalance: 0.3,
		Timestamp:          0,
	})

	var vec [192]float64
	vec[100] = 0.99 // overrides tick OBI
	vec[120] = 0.02 // overrides tick funding
	ma.UpdateFeatureVector(vec)

	if ma.OrderBookImbalance() != 0.99 {
		t.Fatalf("obi = %.2f, want 0.99", ma.OrderBookImbalance())
	}
	if ma.FundingRate() != 0.02 {
		t.Fatalf("funding = %.4f, want 0.02", ma.FundingRate())
	}
}

func TestMarketAdapterSimilarityWinRate(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1H")
	ma.SetSimilarityWinRate(0.65)
	if ma.SimilarityWinRate() != 0.65 {
		t.Fatalf("winRate = %.2f, want 0.65", ma.SimilarityWinRate())
	}
}

func TestMarketAdapterCandlesEmptyTimeframe(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1H")
	ma.UpdateTick(Tick{Price: 100, Volume: 1, Timestamp: 0})
	ma.UpdateTick(Tick{Price: 101, Volume: 1, Timestamp: 3_600_000})

	// When timeframe arg is empty, should use primary timeframe
	candles := ma.Candles("")
	if len(candles) != 1 {
		t.Fatalf("candles = %d, want 1", len(candles))
	}
}

func TestMarketAdapterUnknownTimeframe(t *testing.T) {
	ma := NewMarketAdapter("BTC-USDT-SWAP", "1H")
	if ma.Candles("1D") != nil {
		t.Fatal("expected nil for unknown timeframe")
	}
}
