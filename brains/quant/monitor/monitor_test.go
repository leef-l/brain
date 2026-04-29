package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/sdk/events"
)

type mockBackend struct {
	balance   exchange.Balance
	positions []exchange.Position
}

func (m *mockBackend) Name() string { return "mock" }

func (m *mockBackend) GetBalance(_ context.Context) (exchange.Balance, error) {
	return m.balance, nil
}

func (m *mockBackend) GetAllPositions(_ context.Context) ([]exchange.Position, error) {
	return m.positions, nil
}

func TestNewTradeMonitorDefaults(t *testing.T) {
	m := NewTradeMonitor(MonitorConfig{})
	if m.cfg.PnLThresholdPct != -5 {
		t.Fatalf("expected -5, got %f", m.cfg.PnLThresholdPct)
	}
	if m.cfg.ConsecutiveLosses != 3 {
		t.Fatalf("expected 3, got %d", m.cfg.ConsecutiveLosses)
	}
	if m.cfg.PriceSlipThreshold != 0.01 {
		t.Fatalf("expected 0.01, got %f", m.cfg.PriceSlipThreshold)
	}
	if m.cfg.CheckInterval != 30*time.Second {
		t.Fatalf("expected 30s, got %v", m.cfg.CheckInterval)
	}
}

func TestTradeMonitorAlertHandler(t *testing.T) {
	backend := &mockBackend{
		balance: exchange.Balance{Total: 10000},
		positions: []exchange.Position{
			{Symbol: "BTC", Side: "long", AvgPrice: 70000, MarkPrice: 66000, Quantity: 1},
		},
	}

	mon := NewTradeMonitor(MonitorConfig{PnLThresholdPct: -5})
	mon.SetBackend(backend)

	var received Alert
	mon.RegisterAlertHandler(func(a Alert) {
		received = a
	})

	mon.check(context.Background())

	if received.Type != AlertPnLThreshold {
		t.Fatalf("expected pnl_threshold alert, got %s", received.Type)
	}
}

func TestTradeMonitorConsecutiveLoss(t *testing.T) {
	backend := &mockBackend{balance: exchange.Balance{Total: 10000}}
	mon := NewTradeMonitor(MonitorConfig{ConsecutiveLosses: 2})
	mon.SetBackend(backend)

	var received Alert
	mon.RegisterAlertHandler(func(a Alert) {
		if a.Type == AlertConsecutiveLoss {
			received = a
		}
	})

	mon.RecordLoss()
	mon.RecordLoss()
	mon.check(context.Background())

	if received.Type != AlertConsecutiveLoss {
		t.Fatalf("expected consecutive_loss alert, got %s", received.Type)
	}
}

func TestTradeMonitorPriceSlip(t *testing.T) {
	backend := &mockBackend{balance: exchange.Balance{Total: 10000}}
	mon := NewTradeMonitor(MonitorConfig{PriceSlipThreshold: 0.005})
	mon.SetBackend(backend)

	var received Alert
	mon.RegisterAlertHandler(func(a Alert) {
		if a.Type == AlertPriceSlip {
			received = a
		}
	})

	mon.RecordFill("BTC", 65000, 66000)
	mon.check(context.Background())

	if received.Type != AlertPriceSlip {
		t.Fatalf("expected price_slip alert, got %s", received.Type)
	}
}

func TestTradeMonitorBalanceAnomaly(t *testing.T) {
	backend := &mockBackend{balance: exchange.Balance{Total: 10000}}
	mon := NewTradeMonitor(MonitorConfig{})
	mon.SetBackend(backend)

	var received Alert
	mon.RegisterAlertHandler(func(a Alert) {
		if a.Type == AlertBalanceAnomaly {
			received = a
		}
	})

	mon.check(context.Background()) // sets baseline
	backend.balance.Total = 8000    // 20% drop
	mon.check(context.Background())

	if received.Type != AlertBalanceAnomaly {
		t.Fatalf("expected balance_anomaly alert, got %s", received.Type)
	}
}

func TestTradeMonitorAPIErrorRate(t *testing.T) {
	backend := &mockBackend{balance: exchange.Balance{Total: 10000}}
	mon := NewTradeMonitor(MonitorConfig{})
	mon.SetBackend(backend)

	var received Alert
	mon.RegisterAlertHandler(func(a Alert) {
		if a.Type == AlertAPIErrorRate {
			received = a
		}
	})

	for i := 0; i < 15; i++ {
		mon.RecordAPICall()
	}
	for i := 0; i < 5; i++ {
		mon.RecordAPIError()
	}
	mon.check(context.Background())

	if received.Type != AlertAPIErrorRate {
		t.Fatalf("expected api_error_rate alert, got %s", received.Type)
	}
}

func TestTradeMonitorEventBus(t *testing.T) {
	bus := events.NewMemEventBus()
	backend := &mockBackend{
		balance: exchange.Balance{Total: 10000},
		positions: []exchange.Position{
			{Symbol: "BTC", Side: "long", AvgPrice: 70000, MarkPrice: 66000, Quantity: 1},
		},
	}

	mon := NewTradeMonitor(MonitorConfig{PnLThresholdPct: -5})
	mon.SetBackend(backend)
	mon.SetEventBus(bus)

	ch, cancel := bus.Subscribe(context.Background(), "")
	defer cancel()

	mon.check(context.Background())

	select {
	case ev := <-ch:
		if ev.Type != "quant.monitor.alert" {
			t.Fatalf("expected quant.monitor.alert, got %s", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestTradeMonitorStartStop(t *testing.T) {
	mon := NewTradeMonitor(MonitorConfig{CheckInterval: 100 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	mon.Start(ctx)
	time.Sleep(250 * time.Millisecond)
	cancel()
	mon.Stop()
}
