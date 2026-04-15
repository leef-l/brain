package backtest

import (
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// genTrendCandles creates candles with a clear uptrend.
func genTrendCandles(n int, startPrice, step float64) []Candle {
	candles := make([]Candle, n)
	for i := 0; i < n; i++ {
		p := startPrice + float64(i)*step
		candles[i] = strategy.Candle{
			Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).UnixMilli(),
			Open:      p - step*0.3,
			High:      p + step*0.8,
			Low:       p - step*0.5,
			Close:     p,
			Volume:    1000 + float64(i)*10,
		}
	}
	return candles
}

// genFlatCandles creates candles oscillating around a price.
func genFlatCandles(n int, basePrice, noise float64) []Candle {
	candles := make([]Candle, n)
	for i := 0; i < n; i++ {
		offset := noise
		if i%2 == 0 {
			offset = -noise
		}
		p := basePrice + offset
		candles[i] = strategy.Candle{
			Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).UnixMilli(),
			Open:      p - noise*0.2,
			High:      p + noise*0.5,
			Low:       p - noise*0.5,
			Close:     p,
			Volume:    500,
		}
	}
	return candles
}

func TestBacktestTrend(t *testing.T) {
	candles := genTrendCandles(200, 50000, 50)

	eng := NewEngine(Config{
		Symbol:        "BTC-USDT-SWAP",
		Timeframe:     "1H",
		InitialEquity: 10000,
		MaxLeverage:   1,
		SlippageBps:   5,
		FeeBps:        4,
		WarmupBars:    60,
	})

	report, err := eng.Run(candles)
	if err != nil {
		t.Fatalf("backtest error: %v", err)
	}

	t.Log(report.String())

	if report.Bars != 200 {
		t.Fatalf("bars = %d, want 200", report.Bars)
	}
	if report.InitialEquity != 10000 {
		t.Fatalf("initial equity = %.2f, want 10000", report.InitialEquity)
	}
	if report.Duration <= 0 {
		t.Fatal("duration should be positive")
	}
}

func TestBacktestFlat(t *testing.T) {
	candles := genFlatCandles(200, 50000, 10)

	eng := NewEngine(Config{
		Symbol:        "BTC-USDT-SWAP",
		Timeframe:     "1H",
		InitialEquity: 10000,
		WarmupBars:    60,
	})

	report, err := eng.Run(candles)
	if err != nil {
		t.Fatalf("backtest error: %v", err)
	}

	t.Log(report.String())

	// Flat market should have few or no trades
	if report.MaxDrawdown > 0.5 {
		t.Fatalf("max drawdown = %.2f%%, too high for flat market", report.MaxDrawdown*100)
	}
}

func TestBacktestTooFewCandles(t *testing.T) {
	candles := genTrendCandles(30, 50000, 50)

	eng := NewEngine(Config{Symbol: "TEST", WarmupBars: 60})
	_, err := eng.Run(candles)
	if err == nil {
		t.Fatal("expected error for too few candles")
	}
}

func TestReportString(t *testing.T) {
	r := &Report{
		Symbol:        "BTC-USDT-SWAP",
		Timeframe:     "1H",
		Bars:          100,
		Trades:        []Trade{{PnL: 100}, {PnL: -50}},
		TotalReturn:   0.05,
		WinRate:       0.5,
		ProfitFactor:  2.0,
		MaxDrawdown:   0.03,
		SharpeRatio:   1.5,
		InitialEquity: 10000,
		FinalEquity:   10500,
		Duration:      42 * time.Millisecond,
	}

	s := r.String()
	if s == "" {
		t.Fatal("report string should not be empty")
	}
	t.Log(s)
}
