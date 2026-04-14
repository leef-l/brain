package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseInstrumentsResponse(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantIDs []string
		wantErr bool
	}{
		{
			name: "normal response with 3 instruments",
			body: `{"code":"0","data":[{"instId":"BTC-USDT-SWAP"},{"instId":"ETH-USDT-SWAP"},{"instId":"SOL-USDT-SWAP"}]}`,
			wantIDs: []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP", "SOL-USDT-SWAP"},
		},
		{
			name:    "error code",
			body:    `{"code":"50001","data":[],"msg":"invalid request"}`,
			wantErr: true,
		},
		{
			name:    "empty data",
			body:    `{"code":"0","data":[]}`,
			wantIDs: []string{},
		},
		{
			name:    "malformed JSON",
			body:    `{not json}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := parseInstrumentsResponse([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(ids) != len(tt.wantIDs) {
				t.Fatalf("got %d ids, want %d", len(ids), len(tt.wantIDs))
			}
			for i, id := range ids {
				if id != tt.wantIDs[i] {
					t.Errorf("ids[%d] = %q, want %q", i, id, tt.wantIDs[i])
				}
			}
		})
	}
}

func TestFetchInstrumentsHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/public/instruments" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("instType") != "SWAP" {
			t.Errorf("missing instType=SWAP query param")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"0","data":[{"instId":"BTC-USDT-SWAP"},{"instId":"ETH-USDT-SWAP"}]}`))
	}))
	defer srv.Close()

	p := NewOKXSwapProvider("test", OKXSwapConfig{
		RESTURL: srv.URL,
	})
	p.httpClient = srv.Client()

	ids, err := p.fetchInstruments(context.Background())
	if err != nil {
		t.Fatalf("fetchInstruments error: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d ids, want 2", len(ids))
	}
	if ids[0] != "BTC-USDT-SWAP" || ids[1] != "ETH-USDT-SWAP" {
		t.Errorf("unexpected ids: %v", ids)
	}
}

func TestBuildSubscribeArgs(t *testing.T) {
	args := buildSubscribeArgs("candle1m", []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP"})
	if len(args) != 2 {
		t.Fatalf("got %d args, want 2", len(args))
	}
	if args[0]["channel"] != "candle1m" {
		t.Errorf("channel = %q", args[0]["channel"])
	}
	if args[1]["instId"] != "ETH-USDT-SWAP" {
		t.Errorf("instId = %q", args[1]["instId"])
	}
}

func TestNewOKXSwapProviderDefaults(t *testing.T) {
	p := NewOKXSwapProvider("test-provider", OKXSwapConfig{})
	if p.Name() != "test-provider" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.config.PingInterval != 25*time.Second {
		t.Errorf("PingInterval = %v", p.config.PingInterval)
	}
	if len(p.config.ReconnectDelay) != 6 {
		t.Errorf("ReconnectDelay len = %d", len(p.config.ReconnectDelay))
	}
	h := p.Health()
	if h.Status != "init" {
		t.Errorf("initial status = %q", h.Status)
	}
}

func TestSubscribeSink(t *testing.T) {
	p := NewOKXSwapProvider("test", OKXSwapConfig{})
	var called bool
	sink := sinkFunc(func(ev DataEvent) { called = true })
	if err := p.Subscribe(sink); err != nil {
		t.Fatal(err)
	}
	// Verify sink is stored (we can't easily trigger OnEvent without a real conn,
	// but we can at least verify Subscribe doesn't error).
	_ = called
}

// sinkFunc adapts a plain function to the DataSink interface.
type sinkFunc func(DataEvent)

func (f sinkFunc) OnEvent(ev DataEvent) { f(ev) }
