package validator

import (
	"sync"
	"testing"
	"time"

	"github.com/leef-l/brain/brains/data/provider"
)

func fixedNow() time.Time {
	return time.UnixMilli(1_700_000_000_000)
}

func init() {
	nowFunc = fixedNow
}

func collectAlerts(t *testing.T) (AlertSink, *[]Alert) {
	t.Helper()
	var mu sync.Mutex
	var alerts []Alert
	sink := func(a Alert) {
		mu.Lock()
		defer mu.Unlock()
		alerts = append(alerts, a)
	}
	return sink, &alerts
}

func TestNormalDataPasses(t *testing.T) {
	sink, _ := collectAlerts(t)
	v := New(DefaultConfig(), sink)

	event := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "trades",
		Timestamp: fixedNow().UnixMilli() - 1000,
		Payload:   provider.Trade{Price: 50000, InstID: "BTC-USDT-SWAP"},
	}
	if !v.Validate(event) {
		t.Fatal("expected normal data to pass")
	}
}

func TestFutureTimestampRejected(t *testing.T) {
	sink, alerts := collectAlerts(t)
	v := New(DefaultConfig(), sink)

	event := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "trades",
		Timestamp: fixedNow().UnixMilli() + 10_000, // 10s in future, threshold is 5s
		Payload:   provider.Trade{Price: 50000},
	}
	if v.Validate(event) {
		t.Fatal("expected future timestamp to be rejected")
	}
	if len(*alerts) != 1 || (*alerts)[0].Type != "future_ts" {
		t.Fatalf("expected future_ts alert, got %v", *alerts)
	}
}

func TestStaleTimestampRejected(t *testing.T) {
	sink, alerts := collectAlerts(t)
	v := New(DefaultConfig(), sink)

	event := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "trades",
		Timestamp: fixedNow().UnixMilli() - 400_000, // 400s stale, threshold is 300s
		Payload:   provider.Trade{Price: 50000},
	}
	if v.Validate(event) {
		t.Fatal("expected stale timestamp to be rejected")
	}
	if len(*alerts) != 1 || (*alerts)[0].Type != "stale" {
		t.Fatalf("expected stale alert, got %v", *alerts)
	}
}

func TestPriceSpikeRejected(t *testing.T) {
	sink, alerts := collectAlerts(t)
	v := New(DefaultConfig(), sink)

	now := fixedNow().UnixMilli()

	// First event sets the baseline
	e1 := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "trades",
		Timestamp: now - 2000,
		Payload:   provider.Trade{Price: 50000},
	}
	if !v.Validate(e1) {
		t.Fatal("first event should pass")
	}

	// Second event: 15% change -> rejected
	e2 := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "trades",
		Timestamp: now - 1000,
		Payload:   provider.Trade{Price: 57500}, // 15% increase
	}
	if v.Validate(e2) {
		t.Fatal("expected price spike to be rejected")
	}
	if len(*alerts) != 1 || (*alerts)[0].Type != "price_spike" {
		t.Fatalf("expected price_spike alert, got %v", *alerts)
	}
}

func TestSmallPriceChangePasses(t *testing.T) {
	sink, _ := collectAlerts(t)
	v := New(DefaultConfig(), sink)

	now := fixedNow().UnixMilli()

	e1 := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "trades",
		Timestamp: now - 2000,
		Payload:   provider.Trade{Price: 50000},
	}
	v.Validate(e1)

	e2 := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "trades",
		Timestamp: now - 1000,
		Payload:   provider.Trade{Price: 51000}, // 2% change, under 10%
	}
	if !v.Validate(e2) {
		t.Fatal("expected small price change to pass")
	}
}

func TestDuplicateTimestampRejected(t *testing.T) {
	sink, _ := collectAlerts(t)
	v := New(DefaultConfig(), sink)

	now := fixedNow().UnixMilli()
	ts := now - 1000

	e1 := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "trades",
		Timestamp: ts,
		Payload:   provider.Trade{Price: 50000},
	}
	if !v.Validate(e1) {
		t.Fatal("first event should pass")
	}

	e2 := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "trades",
		Timestamp: ts, // same timestamp
		Payload:   provider.Trade{Price: 50000},
	}
	if v.Validate(e2) {
		t.Fatal("expected duplicate timestamp to be rejected")
	}
}

func TestAlertSinkReceivesAlerts(t *testing.T) {
	sink, alerts := collectAlerts(t)
	v := New(DefaultConfig(), sink)

	// trigger future_ts alert
	v.Validate(&provider.DataEvent{
		Symbol:    "ETH-USDT-SWAP",
		Topic:     "trades",
		Timestamp: fixedNow().UnixMilli() + 99_000,
		Payload:   provider.Trade{Price: 3000},
	})

	if len(*alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(*alerts))
	}
	a := (*alerts)[0]
	if a.Level != "warning" || a.Type != "future_ts" || a.Symbol != "ETH-USDT-SWAP" {
		t.Fatalf("unexpected alert: %+v", a)
	}
}

func TestGapDetectorTriggersCallback(t *testing.T) {
	var gapCalled bool
	var gapInstID, gapBar string

	gd := NewGapDetector(2, func(instID, bar string, from, to int64) {
		gapCalled = true
		gapInstID = instID
		gapBar = bar
	})

	base := int64(1_700_000_000_000)

	// Send candles with a big gap (skip 3 candles = 3 minutes for 1m bar)
	gd.Observe(&provider.DataEvent{
		Topic:     "candle1m",
		Timestamp: base,
		Payload: provider.Candle{
			InstID:    "BTC-USDT-SWAP",
			Bar:       "1m",
			Timestamp: base,
			Close:     50000,
		},
	})

	// Jump 4 minutes ahead (skip 3)
	gd.Observe(&provider.DataEvent{
		Topic:     "candle1m",
		Timestamp: base + 240_000,
		Payload: provider.Candle{
			InstID:    "BTC-USDT-SWAP",
			Bar:       "1m",
			Timestamp: base + 240_000,
			Close:     50100,
		},
	})

	if !gapCalled {
		t.Fatal("expected gap callback to fire")
	}
	if gapInstID != "BTC-USDT-SWAP" || gapBar != "1m" {
		t.Fatalf("unexpected gap params: instID=%s bar=%s", gapInstID, gapBar)
	}
}

func TestGapDetectorNoFalsePositive(t *testing.T) {
	var gapCalled bool
	gd := NewGapDetector(3, func(_, _ string, _, _ int64) {
		gapCalled = true
	})

	base := int64(1_700_000_000_000)

	// Sequential 1m candles, no gap
	for i := 0; i < 5; i++ {
		gd.Observe(&provider.DataEvent{
			Topic:     "candle1m",
			Timestamp: base + int64(i)*60_000,
			Payload: provider.Candle{
				InstID:    "BTC-USDT-SWAP",
				Bar:       "1m",
				Timestamp: base + int64(i)*60_000,
				Close:     50000,
			},
		})
	}

	if gapCalled {
		t.Fatal("gap callback should not fire for sequential candles")
	}
}

func TestCandlePayloadExtractsPrice(t *testing.T) {
	sink, _ := collectAlerts(t)
	v := New(DefaultConfig(), sink)

	now := fixedNow().UnixMilli()

	e := &provider.DataEvent{
		Symbol:    "BTC-USDT-SWAP",
		Topic:     "candle1m",
		Timestamp: now - 1000,
		Payload: provider.Candle{
			InstID: "BTC-USDT-SWAP",
			Bar:    "1m",
			Close:  50000,
		},
	}
	if !v.Validate(e) {
		t.Fatal("candle event should pass")
	}
}
