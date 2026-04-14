package active

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func mockOKXServer(tickers []okxTicker) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := okxTickerResp{
			Code: "0",
			Data: tickers,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func sampleTickers() []okxTicker {
	return []okxTicker{
		{InstID: "BTC-USDT-SWAP", Last: "50000", VolCcy24h: "500"},       // vol = 25,000,000
		{InstID: "ETH-USDT-SWAP", Last: "3000", VolCcy24h: "5000"},       // vol = 15,000,000
		{InstID: "SOL-USDT-SWAP", Last: "100", VolCcy24h: "200000"},      // vol = 20,000,000
		{InstID: "DOGE-USDT-SWAP", Last: "0.1", VolCcy24h: "50000000"},   // vol = 5,000,000
		{InstID: "XRP-USDT-SWAP", Last: "0.5", VolCcy24h: "30000000"},    // vol = 15,000,000
		{InstID: "SHIB-USDT-SWAP", Last: "0.00001", VolCcy24h: "1000000000"}, // vol = 10,000
	}
}

func TestRefreshParsesAndSorts(t *testing.T) {
	srv := mockOKXServer(sampleTickers())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.RESTURL = srv.URL
	cfg.AlwaysInclude = nil

	al := New(cfg, srv.Client())

	result, err := al.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}

	// Should be sorted descending by volume; only those >= 10M
	// BTC=25M, SOL=20M, ETH=15M, XRP=15M
	if len(result) != 4 {
		t.Fatalf("expected 4 instruments, got %d: %+v", len(result), result)
	}
	if result[0].InstID != "BTC-USDT-SWAP" {
		t.Fatalf("expected BTC first, got %s", result[0].InstID)
	}
	if result[1].InstID != "SOL-USDT-SWAP" {
		t.Fatalf("expected SOL second, got %s", result[1].InstID)
	}
}

func TestAlwaysIncludeEvenLowVolume(t *testing.T) {
	srv := mockOKXServer(sampleTickers())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.RESTURL = srv.URL
	cfg.AlwaysInclude = []string{"SHIB-USDT-SWAP"} // vol = 10,000, below threshold

	al := New(cfg, srv.Client())
	result, err := al.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}

	found := false
	for _, inst := range result {
		if inst.InstID == "SHIB-USDT-SWAP" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("AlwaysInclude instrument SHIB-USDT-SWAP missing from result")
	}
}

func TestMaxInstrumentsLimit(t *testing.T) {
	srv := mockOKXServer(sampleTickers())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.RESTURL = srv.URL
	cfg.MaxInstruments = 2
	cfg.AlwaysInclude = nil

	al := New(cfg, srv.Client())
	result, err := al.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 instruments (MaxInstruments=2), got %d", len(result))
	}
}

func TestMinVolume24hFilter(t *testing.T) {
	srv := mockOKXServer(sampleTickers())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.RESTURL = srv.URL
	cfg.MinVolume24h = 20_000_000
	cfg.AlwaysInclude = nil

	al := New(cfg, srv.Client())
	result, err := al.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}

	// Only BTC=25M, SOL=20M meet >= 20M
	if len(result) != 2 {
		t.Fatalf("expected 2 instruments with vol >= 20M, got %d: %+v", len(result), result)
	}
}

func TestIsActive(t *testing.T) {
	srv := mockOKXServer(sampleTickers())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.RESTURL = srv.URL
	cfg.AlwaysInclude = nil

	al := New(cfg, srv.Client())
	_, err := al.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}

	if !al.IsActive("BTC-USDT-SWAP") {
		t.Fatal("BTC-USDT-SWAP should be active")
	}
	if al.IsActive("SHIB-USDT-SWAP") {
		t.Fatal("SHIB-USDT-SWAP should not be active (low volume)")
	}
}

func TestConcurrentAccess(t *testing.T) {
	srv := mockOKXServer(sampleTickers())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.RESTURL = srv.URL

	al := New(cfg, srv.Client())

	var wg sync.WaitGroup

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			al.Refresh(context.Background())
		}
	}()

	// Reader goroutines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				al.IsActive("BTC-USDT-SWAP")
				al.List()
				al.Count()
				al.LastUpdate()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent access test timed out")
	}
}

func TestListReturnsCopy(t *testing.T) {
	srv := mockOKXServer(sampleTickers())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.RESTURL = srv.URL
	cfg.AlwaysInclude = nil

	al := New(cfg, srv.Client())
	al.Refresh(context.Background())

	list1 := al.List()
	list2 := al.List()

	if len(list1) != len(list2) {
		t.Fatal("List should return consistent results")
	}

	// Mutating the returned slice should not affect internal state
	if len(list1) > 0 {
		list1[0] = "MODIFIED"
		list3 := al.List()
		if list3[0] == "MODIFIED" {
			t.Fatal("List should return a copy, not a reference to internal state")
		}
	}
}

func TestLastUpdate(t *testing.T) {
	srv := mockOKXServer(sampleTickers())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.RESTURL = srv.URL

	al := New(cfg, srv.Client())

	if !al.LastUpdate().IsZero() {
		t.Fatal("LastUpdate should be zero before first refresh")
	}

	before := time.Now()
	al.Refresh(context.Background())
	after := time.Now()

	lu := al.LastUpdate()
	if lu.Before(before) || lu.After(after) {
		t.Fatalf("LastUpdate %v not between %v and %v", lu, before, after)
	}
}
