package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/internal/data/model"
	"github.com/leef-l/brain/internal/data/provider"
	"github.com/leef-l/brain/persistence"
)

func TestServiceIngestRejectsInvalidEvent(t *testing.T) {
	svc := New(Config{
		MonotonicTopics: map[string]bool{"trade": true},
	})

	snapshot, result, err := svc.Ingest(context.Background(), model.MarketEvent{
		Topic:  "trade",
		Symbol: "BTC-USDT-SWAP",
	})
	if err != nil {
		t.Fatalf("Ingest returned error: %v", err)
	}
	if result.Accepted {
		t.Fatalf("validation result=%+v, want reject", result)
	}
	if result.Stage != "shape" {
		t.Fatalf("stage=%q, want shape", result.Stage)
	}
	if snapshot.WriteSeq != 0 {
		t.Fatalf("write_seq=%d, want 0 on rejected event", snapshot.WriteSeq)
	}
}

func TestServiceHealthReportsProviderDegradation(t *testing.T) {
	svc := New(Config{})

	health := svc.Health(context.Background())
	if health.State != model.HealthStateDegraded {
		t.Fatalf("state=%q, want degraded", health.State)
	}
	if health.Message != "no providers registered" {
		t.Fatalf("message=%q, want no providers registered", health.Message)
	}

	if err := svc.RegisterProvider(provider.NewStaticProvider("okx").WithState(model.ProviderStateDegraded)); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	health = svc.Health(context.Background())
	if health.State != model.HealthStateDegraded {
		t.Fatalf("state=%q, want degraded", health.State)
	}
	if health.Message != "all providers degraded" {
		t.Fatalf("message=%q, want all providers degraded", health.Message)
	}
}

func TestServiceHandleToolReturnsDataAndErrors(t *testing.T) {
	svc := New(Config{DefaultTimeframe: "1m"})
	svc.StoreSnapshot(model.MarketSnapshot{
		Provider:      "fixture",
		Topic:         "trade",
		Symbol:        "BTC-USDT-SWAP",
		Timestamp:     1234567890,
		Price:         101.25,
		FeatureVector: []float64{0.1, 0.2, 0.3},
		Candles: map[string][]model.Candle{
			"1m": {{
				Timestamp: 1234567800,
				Open:      100,
				High:      102,
				Low:       99,
				Close:     101,
				Volume:    12,
			}},
		},
	})

	vectorRaw, err := svc.HandleTool(context.Background(), model.ToolGetFeatureVector, []byte(`{"symbol":"BTC-USDT-SWAP"}`))
	if err != nil {
		t.Fatalf("HandleTool feature vector: %v", err)
	}
	var vectorResp struct {
		Symbol string    `json:"symbol"`
		Vector []float64 `json:"vector"`
	}
	if err := json.Unmarshal(vectorRaw, &vectorResp); err != nil {
		t.Fatalf("feature vector unmarshal: %v", err)
	}
	if got := len(vectorResp.Vector); got != 3 {
		t.Fatalf("vector len=%d, want 3", got)
	}

	candlesRaw, err := svc.HandleTool(context.Background(), model.ToolGetCandles, []byte(`{"symbol":"BTC-USDT-SWAP","timeframe":"1m"}`))
	if err != nil {
		t.Fatalf("HandleTool candles: %v", err)
	}
	var candleResp struct {
		Timeframe string         `json:"timeframe"`
		Candles   []model.Candle `json:"candles"`
	}
	if err := json.Unmarshal(candlesRaw, &candleResp); err != nil {
		t.Fatalf("candles unmarshal: %v", err)
	}
	if candleResp.Timeframe != "1m" || len(candleResp.Candles) != 1 {
		t.Fatalf("unexpected candle response: %+v", candleResp)
	}

	notFoundRaw, err := svc.HandleTool(context.Background(), model.ToolGetSnapshot, []byte(`{"symbol":"ETH-USDT-SWAP"}`))
	if err != nil {
		t.Fatalf("HandleTool snapshot not found: %v", err)
	}
	var notFoundResp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(notFoundRaw, &notFoundResp); err != nil {
		t.Fatalf("not found unmarshal: %v", err)
	}
	if notFoundResp.OK || notFoundResp.Error != "snapshot not found" {
		t.Fatalf("unexpected not found response: %+v", notFoundResp)
	}

	if _, err := svc.HandleTool(context.Background(), model.ToolGetSnapshot, []byte(`{"symbol":`)); err == nil {
		t.Fatal("invalid JSON should return error")
	}
}

func TestServiceRestoreStateRestoresSnapshotsAndValidator(t *testing.T) {
	ctx := context.Background()
	store := persistence.NewMemDataStateStore(nil)
	cfg := Config{
		DefaultTimeframe: "1m",
		MonotonicTopics:  map[string]bool{"trade": true},
		StateStore:       store,
	}
	svc := New(cfg)
	if err := svc.RegisterProvider(provider.NewStaticProvider("okx")); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}

	event := model.MarketEvent{
		Provider:  "okx",
		Topic:     "trade",
		Symbol:    "BTC-USDT-SWAP",
		Timestamp: 1700000000000,
		Price:     101.5,
		Volume:    12,
		Payload:   []byte("tick-1"),
	}
	written, result, err := svc.Ingest(ctx, event)
	if err != nil {
		t.Fatalf("Ingest returned error: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("validation result=%+v, want accept", result)
	}

	restored := New(cfg)
	if err := restored.RestoreState(ctx); err != nil {
		t.Fatalf("RestoreState returned error: %v", err)
	}

	snapshot, ok := restored.LatestSnapshot("BTC-USDT-SWAP")
	if !ok {
		t.Fatal("expected restored snapshot")
	}
	if snapshot.WriteSeq != written.WriteSeq {
		t.Fatalf("write_seq=%d, want %d", snapshot.WriteSeq, written.WriteSeq)
	}

	dupSnapshot, dupResult, err := restored.Ingest(ctx, event)
	if err != nil {
		t.Fatalf("duplicate Ingest returned error: %v", err)
	}
	if dupResult.Accepted || dupResult.Action != model.ValidationSkip {
		t.Fatalf("duplicate validation result=%+v, want skip", dupResult)
	}
	if dupSnapshot.WriteSeq != 0 {
		t.Fatalf("duplicate snapshot write_seq=%d, want 0", dupSnapshot.WriteSeq)
	}

	health := restored.Health(ctx)
	if health.Message != "providers restored from persisted state; live feeds not attached" {
		t.Fatalf("message=%q", health.Message)
	}
}

func TestServiceDrainProvidersIngestsRegisteredEvents(t *testing.T) {
	svc := New(Config{
		DefaultTimeframe: "1m",
		MonotonicTopics:  map[string]bool{"trade": true},
	})
	if err := svc.RegisterProvider(provider.NewStaticProvider("okx",
		model.MarketEvent{
			Provider:  "okx",
			Topic:     "trade",
			Symbol:    "BTC-USDT-SWAP",
			Timestamp: 1700000000000,
			Price:     101.5,
			Payload:   []byte("tick-1"),
		},
		model.MarketEvent{
			Provider:  "okx",
			Topic:     "trade",
			Symbol:    "ETH-USDT-SWAP",
			Timestamp: 1700000001000,
			Price:     202.5,
			Payload:   []byte("tick-2"),
		},
	)); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}

	if err := svc.DrainProviders(context.Background()); err != nil {
		t.Fatalf("DrainProviders returned error: %v", err)
	}

	if snapshot, ok := svc.LatestSnapshot("ETH-USDT-SWAP"); !ok || snapshot.Price != 202.5 {
		t.Fatalf("unexpected ETH snapshot: %+v ok=%v", snapshot, ok)
	}
}
