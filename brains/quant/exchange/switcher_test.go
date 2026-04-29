package exchange

import (
	"context"
	"testing"
)

func TestTradingModeSwitcherDefault(t *testing.T) {
	paper := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	sw := NewTradingModeSwitcher(ModeConfig{PaperBackend: paper})
	if sw.Current() != "paper" {
		t.Fatalf("expected default paper, got %s", sw.Current())
	}
	if sw.IsLive() {
		t.Fatal("expected not live")
	}
}

func TestTradingModeSwitcherSwitchToLiveRequiresConfirm(t *testing.T) {
	paper := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	live := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	sw := NewTradingModeSwitcher(ModeConfig{PaperBackend: paper, LiveBackend: live})

	if err := sw.Switch("live"); err == nil {
		t.Fatal("expected error when switching to live without confirmation")
	}
}

func TestTradingModeSwitcherArmAndSwitch(t *testing.T) {
	paper := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	live := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	sw := NewTradingModeSwitcher(ModeConfig{PaperBackend: paper, LiveBackend: live})
	sw.SetLiveConfirmToken("secret123")

	if err := sw.ArmLive("wrong"); err == nil {
		t.Fatal("expected error for wrong token")
	}
	if err := sw.ArmLive("secret123"); err != nil {
		t.Fatal(err)
	}
	if err := sw.Switch("live"); err != nil {
		t.Fatal(err)
	}
	if !sw.IsLive() {
		t.Fatal("expected live mode")
	}
	if sw.LiveIndicator() != "LIVE TRADING" {
		t.Fatalf("unexpected indicator: %s", sw.LiveIndicator())
	}
}

func TestTradingModeSwitcherExecute(t *testing.T) {
	paper := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	live := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	sw := NewTradingModeSwitcher(ModeConfig{PaperBackend: paper, LiveBackend: live})

	ctx := context.Background()
	resp, err := sw.Execute(ctx, OrderRequest{Symbol: "BTC-USDT-SWAP", Side: "buy", Type: "market", Quantity: 0.1, StopLoss: 60000})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "filled" {
		t.Fatalf("expected filled, got %s", resp.Status)
	}
}

func TestTradingModeSwitcherAuditLog(t *testing.T) {
	paper := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	live := NewOKXBackend(OKXBackendConfig{}, nil, nil)
	sw := NewTradingModeSwitcher(ModeConfig{PaperBackend: paper, LiveBackend: live})
	sw.Switch("paper") // no-op but should not panic

	log := sw.AuditLog()
	if len(log) != 0 {
		t.Fatalf("expected empty audit log, got %d", len(log))
	}
}

func TestTradingModeSwitcherInvalidMode(t *testing.T) {
	sw := NewTradingModeSwitcher(ModeConfig{})
	if err := sw.Switch("invalid"); err == nil {
		t.Fatal("expected error for invalid mode")
	}
}
