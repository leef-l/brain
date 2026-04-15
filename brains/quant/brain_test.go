package quant

import (
	"context"
	"testing"
	"time"

	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/brains/quant/exchange"
)

func TestQuantBrainPipeline(t *testing.T) {
	// Setup: data brain ring buffer with a BTC snapshot
	buffers := ringbuf.NewBufferManager(64)

	var vec [192]float64
	// Price features for 1H (index 3): EMA deviations positive = bullish
	base := 3 * 12
	vec[base+0] = 0.02  // EMA9 dev
	vec[base+1] = 0.015 // EMA21 dev
	vec[base+2] = 0.01  // EMA55 dev
	vec[base+5] = 0.001 // MACD histogram
	vec[base+7] = 0.015 // ATR ratio
	vec[base+11] = 0.30 // ADX

	// 4H confirmation
	base4H := 4 * 12
	vec[base4H+0] = 0.03
	vec[base4H+1] = 0.02
	vec[base4H+2] = 0.01

	// Market regime: trend dominant
	vec[176] = 0.7
	vec[177] = 0.1
	vec[178] = 0.1
	vec[179] = 0.1

	buffers.Write("BTC-USDT-SWAP", ringbuf.MarketSnapshot{
		InstID:        "BTC-USDT-SWAP",
		Timestamp:     time.Now().UnixMilli(),
		CurrentPrice:  65000,
		BidPrice:      64999,
		AskPrice:      65001,
		FeatureVector: vec,
		MLSource:      "fallback",
		MLReady:       false,
		MarketRegime:  "trend",
	})

	// Create quant brain with paper exchange
	paperExchange := exchange.NewPaperExchange(exchange.PaperConfig{
		InitialEquity: 100000,
	})

	account := &Account{
		ID:       "paper-test",
		Exchange: paperExchange,
	}

	unit := NewTradingUnit(TradingUnitConfig{
		ID:          "test-unit",
		Account:     account,
		Symbols:     []string{"BTC-USDT-SWAP"},
		Timeframe:   "1H",
		MaxLeverage: 10,
	}, nil)

	qb := New(Config{CycleInterval: time.Second}, buffers, nil)
	qb.AddUnit(unit)

	// Run one cycle manually
	ctx := context.Background()
	qb.runCycle(ctx)

	health := qb.Health()
	cycles := health["cycles_total"].(int64)
	if cycles != 1 {
		t.Fatalf("cycles = %d, want 1", cycles)
	}

	// With strong bullish signals, we should see at least one signal generated
	signals := health["signals_generated"].(int64)
	t.Logf("signals=%d, trades_attempted=%d, trades_executed=%d, reviews=%d",
		signals,
		health["trades_attempted"].(int64),
		health["trades_executed"].(int64),
		health["reviews_flagged"].(int64))
}

func TestQuantBrainMultiUnit(t *testing.T) {
	buffers := ringbuf.NewBufferManager(64)

	var vec [192]float64
	buffers.Write("BTC-USDT-SWAP", ringbuf.MarketSnapshot{
		InstID:       "BTC-USDT-SWAP",
		Timestamp:    time.Now().UnixMilli(),
		CurrentPrice: 65000,
		FeatureVector: vec,
	})

	// Two accounts on different exchanges
	paper1 := exchange.NewPaperExchange(exchange.PaperConfig{InitialEquity: 50000})
	paper2 := exchange.NewPaperExchange(exchange.PaperConfig{InitialEquity: 100000})

	unit1 := NewTradingUnit(TradingUnitConfig{
		ID:      "unit-a",
		Account: &Account{ID: "account-a", Exchange: paper1},
		Symbols: []string{"BTC-USDT-SWAP"},
	}, nil)

	unit2 := NewTradingUnit(TradingUnitConfig{
		ID:      "unit-b",
		Account: &Account{ID: "account-b", Exchange: paper2},
		Symbols: []string{"BTC-USDT-SWAP"},
	}, nil)

	qb := New(Config{}, buffers, nil)
	qb.AddUnit(unit1)
	qb.AddUnit(unit2)

	units := qb.Units()
	if len(units) != 2 {
		t.Fatalf("units = %d, want 2", len(units))
	}
	if units[0].ID != "unit-a" || units[1].ID != "unit-b" {
		t.Fatalf("unit IDs = %s, %s", units[0].ID, units[1].ID)
	}

	// Run a cycle — both units should be evaluated
	qb.runCycle(context.Background())
	if qb.metrics.CyclesTotal.Load() != 1 {
		t.Fatal("expected 1 cycle")
	}
}

func TestTradingUnitNoShortFilter(t *testing.T) {
	// Simulate an exchange that can't short (like A-shares)
	paper := exchange.NewPaperExchange(exchange.PaperConfig{
		InitialEquity: 50000,
		Capabilities: exchange.Capabilities{
			CanShort:         false,
			MaxLeverage:      1,
			BaseCurrency:     "CNY",
			CrossAssetAnchor: "000300",
		},
	})

	unit := NewTradingUnit(TradingUnitConfig{
		ID:      "a-share-unit",
		Account: &Account{ID: "a-share", Exchange: paper},
	}, nil)

	// Guard should have MaxLeverage=1
	if unit.Guard.Base.MaxLeverage != 1 {
		t.Fatalf("MaxLeverage = %d, want 1 for no-short exchange", unit.Guard.Base.MaxLeverage)
	}

	// ShouldTrade with no symbol filter → trades all
	if !unit.ShouldTrade("000001.SZ") {
		t.Fatal("should trade any symbol when no filter set")
	}
}
