package backfill

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leef-l/brain/brains/data/store"
)

// ---------------------------------------------------------------------------
// mockStore — minimal in-memory store for testing
// ---------------------------------------------------------------------------

type mockStore struct {
	candles  []store.Candle
	progress map[string]*store.BackfillProgress
}

func newMockStore() *mockStore {
	return &mockStore{progress: make(map[string]*store.BackfillProgress)}
}

func (m *mockStore) BatchInsert(_ context.Context, candles []store.Candle) error {
	m.candles = append(m.candles, candles...)
	return nil
}
func (m *mockStore) Upsert(_ context.Context, c store.Candle) error {
	m.candles = append(m.candles, c)
	return nil
}
func (m *mockStore) QueryRange(_ context.Context, _, _ string, _, _ int64) ([]store.Candle, error) {
	return m.candles, nil
}
func (m *mockStore) LatestTimestamp(_ context.Context, _, _ string) (int64, error) {
	if len(m.candles) == 0 {
		return 0, nil
	}
	return m.candles[len(m.candles)-1].Timestamp, nil
}
func (m *mockStore) DeleteBefore(_ context.Context, _ string, _ int64) error { return nil }

func (m *mockStore) Insert(_ context.Context, _ store.FeatureVector) error              { return nil }
func (m *mockStore) QueryLatest(_ context.Context, _, _, _ string) (*store.FeatureVector, error) {
	return nil, nil
}
func (m *mockStore) DeleteVectorsBefore(_ context.Context, _ string, _ int64) error { return nil }

func (m *mockStore) GetProgress(_ context.Context, instID, tf string) (*store.BackfillProgress, error) {
	p := m.progress[instID+"|"+tf]
	return p, nil
}
func (m *mockStore) SaveProgress(_ context.Context, p store.BackfillProgress) error {
	m.progress[p.InstID+"|"+p.Timeframe] = &p
	return nil
}
func (m *mockStore) InsertAlert(_ context.Context, _ store.AlertRecord) error { return nil }
func (m *mockStore) InsertActiveInstruments(_ context.Context, _ []store.ActiveInstrumentRecord) error {
	return nil
}
func (m *mockStore) Migrate(_ context.Context) error { return nil }
func (m *mockStore) Close() error                     { return nil }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// makeOKXResponse builds a JSON response body mimicking OKX API output (descending order).
func makeOKXResponse(startTS int64, count int) []byte {
	data := make([][]string, count)
	for i := 0; i < count; i++ {
		ts := startTS + int64(count-1-i)*60000 // descending
		data[i] = []string{
			fmt.Sprintf("%d", ts),
			"100.0", "110.0", "90.0", "105.0",
			"1000", "100000", "100000", "1",
		}
	}
	resp := map[string]any{"code": "0", "data": data}
	b, _ := json.Marshal(resp)
	return b
}

func TestFetchCandles_ParsesJSON(t *testing.T) {
	ts := time.Now().Add(-time.Hour).UnixMilli()
	body := makeOKXResponse(ts, 3)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	ms := newMockStore()
	bf := New(srv.Client(), ms, Config{
		RESTURL:    srv.URL,
		GoBack:     time.Hour,
		Timeframes: []string{"1m"},
		MaxBars:    100,
		RateLimit:  1000, // high limit for tests
	})

	candles, err := bf.fetchCandles(context.Background(), "BTC-USDT", "1m", ts, 100)
	if err != nil {
		t.Fatalf("fetchCandles: %v", err)
	}
	if len(candles) != 3 {
		t.Fatalf("expected 3 candles, got %d", len(candles))
	}
	// Verify ascending order after reversal.
	for i := 1; i < len(candles); i++ {
		if candles[i].Timestamp <= candles[i-1].Timestamp {
			t.Fatalf("candles not in ascending order at index %d: %d <= %d",
				i, candles[i].Timestamp, candles[i-1].Timestamp)
		}
	}
	if candles[0].Open != 100.0 || candles[0].Close != 105.0 {
		t.Fatalf("unexpected candle values: %+v", candles[0])
	}
}

func TestBackfillOne_Pagination(t *testing.T) {
	// backfillOne walks backwards from now: first call returns 5 bars (== MaxBars),
	// second call returns 2 bars (< MaxBars) → pagination stops.
	callCount := int32(0)
	nowMS := time.Now().UnixMilli()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		var body []byte
		if n == 1 {
			// page 1: 5 bars ending ~30min ago (descending from cursor)
			body = makeOKXResponse(nowMS-35*60000, 5)
		} else {
			// page 2: 2 bars further back (< MaxBars → last page)
			body = makeOKXResponse(nowMS-42*60000, 2)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	ms := newMockStore()
	bf := New(srv.Client(), ms, Config{
		RESTURL:    srv.URL,
		GoBack:     2 * time.Hour,
		Timeframes: []string{"1m"},
		MaxBars:    5,
		RateLimit:  1000,
	})

	err := bf.backfillOne(context.Background(), "BTC-USDT", "1m")
	if err != nil {
		t.Fatalf("backfillOne: %v", err)
	}
	if atomic.LoadInt32(&callCount) < 2 {
		t.Fatalf("expected >= 2 API calls for pagination, got %d", callCount)
	}
	if len(ms.candles) != 7 {
		t.Fatalf("expected 7 candles total, got %d", len(ms.candles))
	}
	// Verify progress was saved.
	prog := ms.progress["BTC-USDT|1m"]
	if prog == nil {
		t.Fatal("expected progress to be saved")
	}
	if prog.BarCount != 7 {
		t.Fatalf("expected bar_count=7, got %d", prog.BarCount)
	}
}

func TestRateLimiter_Respected(t *testing.T) {
	callCount := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		// Return empty data to stop pagination.
		w.Write([]byte(`{"code":"0","data":[]}`))
	}))
	defer srv.Close()

	ms := newMockStore()
	// Very low rate: 2 req/sec — we'll just verify it doesn't panic.
	bf := New(srv.Client(), ms, Config{
		RESTURL:    srv.URL,
		GoBack:     time.Minute,
		Timeframes: []string{"1m"},
		MaxBars:    100,
		RateLimit:  2,
	})

	err := bf.backfillOne(context.Background(), "BTC-USDT", "1m")
	if err != nil {
		t.Fatalf("backfillOne: %v", err)
	}
}

func TestParseOKXCandles_MinFields(t *testing.T) {
	// Row with exactly 7 fields should work.
	rows := [][]string{{"1000", "1.0", "2.0", "0.5", "1.5", "100", "50"}}
	candles, err := parseOKXCandles("ETH-USDT", "5m", rows)
	if err != nil {
		t.Fatalf("parseOKXCandles: %v", err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected 1 candle, got %d", len(candles))
	}
	if candles[0].InstID != "ETH-USDT" || candles[0].Bar != "5m" {
		t.Fatalf("unexpected candle: %+v", candles[0])
	}
}

func TestParseOKXCandles_TooFewFields(t *testing.T) {
	rows := [][]string{{"1000", "1.0", "2.0"}}
	_, err := parseOKXCandles("X", "1m", rows)
	if err == nil {
		t.Fatal("expected error for row with < 7 fields")
	}
}

func TestReverseCandles(t *testing.T) {
	candles := []store.Candle{
		{Timestamp: 3}, {Timestamp: 2}, {Timestamp: 1},
	}
	reverseCandles(candles)
	if candles[0].Timestamp != 1 || candles[2].Timestamp != 3 {
		t.Fatalf("reverseCandles failed: %+v", candles)
	}
}

func TestReverseCandles_Empty(t *testing.T) {
	var candles []store.Candle
	reverseCandles(candles) // should not panic
}
