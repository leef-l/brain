package persistence

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestMemCentralStateStore_SaveGet(t *testing.T) {
	store := NewMemCentralStateStore(nil)
	state := &CentralState{
		Control: CentralControlState{
			TradingPaused:     true,
			PausedInstruments: map[string]string{"BTC-USDT-SWAP": "vol spike"},
			LastConfigPatch:   json.RawMessage(`{"mode":"strict"}`),
		},
		Review: CentralReviewState{
			LastRequest:  json.RawMessage(`{"trace_id":"trace-1"}`),
			LastDecision: json.RawMessage(`{"approved":true}`),
		},
	}

	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Control.TradingPaused {
		t.Fatal("expected trading pause state")
	}
	if got.Control.PausedInstruments["BTC-USDT-SWAP"] != "vol spike" {
		t.Fatalf("paused instrument=%q", got.Control.PausedInstruments["BTC-USDT-SWAP"])
	}
	if string(got.Review.LastRequest) != `{"trace_id":"trace-1"}` {
		t.Fatalf("last request=%s", string(got.Review.LastRequest))
	}
}

func TestFileCentralStateStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.json")
	fs, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}

	state := &CentralState{
		Control: CentralControlState{
			LastAction:        "pause_trading",
			PausedInstruments: map[string]string{"ETH-USDT-SWAP": "manual"},
		},
		Review: CentralReviewState{
			LastDecision: json.RawMessage(`{"approved":false,"reason":"manual"}`),
		},
	}
	if err := fs.CentralStateStore().Save(context.Background(), state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.CentralStateStore().Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Control.LastAction != "pause_trading" {
		t.Fatalf("last action=%q", got.Control.LastAction)
	}
	if got.Control.PausedInstruments["ETH-USDT-SWAP"] != "manual" {
		t.Fatalf("paused instrument=%q", got.Control.PausedInstruments["ETH-USDT-SWAP"])
	}
	var decision struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(got.Review.LastDecision, &decision); err != nil {
		t.Fatalf("Unmarshal last decision: %v", err)
	}
	if decision.Approved || decision.Reason != "manual" {
		t.Fatalf("last decision=%+v", decision)
	}
}
