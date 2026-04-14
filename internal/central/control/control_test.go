package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/internal/quantcontracts"
)

func TestAutoPauseCriticalAlert(t *testing.T) {
	svc := New(Config{AutoPauseOnCriticalAlert: true})

	res, err := svc.ApplyDataAlert(context.Background(), quantcontracts.DataAlert{
		Level:  "critical",
		Type:   "feed_lag",
		Symbol: "BTC-USDT-SWAP",
	})
	if err != nil {
		t.Fatalf("ApplyDataAlert returned error: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected alert to be accepted, got %+v", res)
	}
	if got := svc.State().PausedInstruments["BTC-USDT-SWAP"]; got == "" {
		t.Fatalf("expected symbol to be paused")
	}
}

func TestPauseInstrument(t *testing.T) {
	svc := New(Config{})

	res := svc.PauseInstrument("BTC-USDT-SWAP", "manual")
	if !res.OK {
		t.Fatalf("expected pause to succeed, got %+v", res)
	}
	if got := svc.State().PausedInstruments["BTC-USDT-SWAP"]; got == "" {
		t.Fatalf("expected paused instrument to be recorded")
	}
}

func TestApplyEmergencyActionAndConfigUpdate(t *testing.T) {
	svc := New(Config{})

	emergency, err := svc.ApplyEmergencyAction(context.Background(), EmergencyActionRequest{
		Action: "pause_trading",
		Reason: "ops halt",
	})
	if err != nil {
		t.Fatalf("ApplyEmergencyAction returned error: %v", err)
	}
	if !emergency.OK || !emergency.State.TradingPaused {
		t.Fatalf("expected emergency action to pause trading, got %+v", emergency)
	}

	configResult, err := svc.ApplyConfigUpdate(context.Background(), ConfigUpdateRequest{
		Scope:  "review",
		Reason: "tighten policy",
		Patch:  json.RawMessage(`{"mode":"strict"}`),
	})
	if err != nil {
		t.Fatalf("ApplyConfigUpdate returned error: %v", err)
	}
	if !configResult.OK {
		t.Fatalf("expected config update to be recorded, got %+v", configResult)
	}
	if configResult.State.LastConfigScope != "review" {
		t.Fatalf("last config scope=%q, want review", configResult.State.LastConfigScope)
	}
	if string(configResult.State.LastConfigPatch) != `{"mode":"strict"}` {
		t.Fatalf("last config patch=%s", string(configResult.State.LastConfigPatch))
	}
}
