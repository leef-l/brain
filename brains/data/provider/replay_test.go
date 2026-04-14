package provider

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/leef-l/brain/brains/data/store"
)

// mockCandleStore implements store.CandleStore for testing.
type mockCandleStore struct {
	candles []store.Candle
}

func (m *mockCandleStore) BatchInsert(_ context.Context, _ []store.Candle) error { return nil }
func (m *mockCandleStore) Upsert(_ context.Context, _ store.Candle) error       { return nil }
func (m *mockCandleStore) LatestTimestamp(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}
func (m *mockCandleStore) DeleteBefore(_ context.Context, _ string, _ int64) error { return nil }
func (m *mockCandleStore) QueryRange(_ context.Context, instID, bar string, from, to int64) ([]store.Candle, error) {
	var result []store.Candle
	for _, c := range m.candles {
		if c.InstID == instID && c.Bar == bar && c.Timestamp >= from && c.Timestamp <= to {
			result = append(result, c)
		}
	}
	return result, nil
}

// collectSink collects all events for testing.
type collectSink struct {
	mu     sync.Mutex
	events []DataEvent
}

func (s *collectSink) OnEvent(e DataEvent) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *collectSink) Events() []DataEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]DataEvent, len(s.events))
	copy(cp, s.events)
	return cp
}

func TestReplayProvider_BasicReplay(t *testing.T) {
	now := time.Now().UnixMilli()
	ms := &mockCandleStore{
		candles: []store.Candle{
			{InstID: "BTC-USDT-SWAP", Bar: "1m", Timestamp: now - 120000, Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 10},
			{InstID: "BTC-USDT-SWAP", Bar: "1m", Timestamp: now - 60000, Open: 100.5, High: 102, Low: 100, Close: 101.5, Volume: 15},
			{InstID: "BTC-USDT-SWAP", Bar: "1m", Timestamp: now, Open: 101.5, High: 103, Low: 101, Close: 102.5, Volume: 20},
		},
	}

	p := NewReplayProvider("test-replay", ReplayConfig{
		Store:      ms,
		InstIDs:    []string{"BTC-USDT-SWAP"},
		Timeframes: []string{"1m"},
		From:       now - 200000,
		To:         now + 1000,
		Speed:      0, // as fast as possible
	})

	sink := &collectSink{}
	if err := p.Subscribe(sink); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Wait for replay to complete
	time.Sleep(200 * time.Millisecond)

	events := sink.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	for i, e := range events {
		if e.Symbol != "BTC-USDT-SWAP" {
			t.Errorf("event %d: symbol = %q, want BTC-USDT-SWAP", i, e.Symbol)
		}
		if e.Provider != "test-replay" {
			t.Errorf("event %d: provider = %q, want test-replay", i, e.Provider)
		}
		candles, ok := e.Payload.([]Candle)
		if !ok || len(candles) != 1 {
			t.Fatalf("event %d: expected []Candle with 1 element", i)
		}
	}

	// Verify order
	c0 := events[0].Payload.([]Candle)[0]
	c2 := events[2].Payload.([]Candle)[0]
	if c0.Close != 100.5 || c2.Close != 102.5 {
		t.Errorf("events not in expected order: first close=%f, last close=%f", c0.Close, c2.Close)
	}
}

func TestReplayProvider_EmptyResult(t *testing.T) {
	ms := &mockCandleStore{candles: nil}

	p := NewReplayProvider("empty-replay", ReplayConfig{
		Store:      ms,
		InstIDs:    []string{"DOGE-USDT-SWAP"},
		Timeframes: []string{"1m"},
		From:       0,
		To:         time.Now().UnixMilli(),
		Speed:      0,
	})

	sink := &collectSink{}
	_ = p.Subscribe(sink)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	if len(sink.Events()) != 0 {
		t.Errorf("expected 0 events for empty store, got %d", len(sink.Events()))
	}

	h := p.Health()
	if h.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", h.Status)
	}
}

func TestReplayProvider_StopMidReplay(t *testing.T) {
	now := time.Now().UnixMilli()
	// Create 1000 candles
	candles := make([]store.Candle, 1000)
	for i := range candles {
		candles[i] = store.Candle{
			InstID:    "BTC-USDT-SWAP",
			Bar:       "1m",
			Timestamp: now - int64(1000-i)*60000,
			Open:      100,
			High:      101,
			Low:       99,
			Close:     100,
			Volume:    10,
		}
	}
	ms := &mockCandleStore{candles: candles}

	p := NewReplayProvider("stop-test", ReplayConfig{
		Store:      ms,
		InstIDs:    []string{"BTC-USDT-SWAP"},
		Timeframes: []string{"1m"},
		From:       now - 1001*60000,
		To:         now + 1000,
		Speed:      1.0, // realtime speed — will be very slow
	})

	sink := &collectSink{}
	_ = p.Subscribe(sink)

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Let a few events through
	time.Sleep(100 * time.Millisecond)

	// Stop mid-replay
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)
	count := len(sink.Events())
	// Should have received some but not all
	if count >= 1000 {
		t.Errorf("expected partial replay after Stop, got all %d events", count)
	}
}

func TestReplayProvider_Name(t *testing.T) {
	p := NewReplayProvider("my-replay", ReplayConfig{})
	if p.Name() != "my-replay" {
		t.Errorf("Name() = %q, want my-replay", p.Name())
	}
}

func TestReplayProvider_SubscribeNil(t *testing.T) {
	p := NewReplayProvider("test", ReplayConfig{})
	// No subscribe — Start should fail
	err := p.Start(context.Background())
	if err == nil {
		t.Error("expected error when starting without subscriber")
	}
}

func TestReplayProvider_MultiTimeframe(t *testing.T) {
	now := time.Now().UnixMilli()
	ms := &mockCandleStore{
		candles: []store.Candle{
			{InstID: "BTC-USDT-SWAP", Bar: "1m", Timestamp: now - 60000, Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 10},
			{InstID: "BTC-USDT-SWAP", Bar: "5m", Timestamp: now - 300000, Open: 99, High: 102, Low: 98, Close: 101, Volume: 50},
		},
	}

	p := NewReplayProvider("multi-tf", ReplayConfig{
		Store:      ms,
		InstIDs:    []string{"BTC-USDT-SWAP"},
		Timeframes: []string{"1m", "5m"},
		From:       now - 400000,
		To:         now + 1000,
		Speed:      0,
	})

	sink := &collectSink{}
	_ = p.Subscribe(sink)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = p.Start(ctx)
	time.Sleep(200 * time.Millisecond)

	events := sink.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events (1m + 5m), got %d", len(events))
	}
}
