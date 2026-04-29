package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/sdk/events"
)

// AlertType categorizes monitor alerts.
type AlertType string

const (
	AlertPnLThreshold    AlertType = "pnl_threshold"
	AlertConsecutiveLoss AlertType = "consecutive_loss"
	AlertPriceSlip       AlertType = "price_slip"
	AlertAPIErrorRate    AlertType = "api_error_rate"
	AlertBalanceAnomaly  AlertType = "balance_anomaly"
)

// Alert represents a single monitor alert.
type Alert struct {
	Type      AlertType
	Symbol    string
	Message   string
	Severity  string // "warning", "critical"
	Timestamp time.Time
	Data      map[string]any
}

// AlertHandler is called when an alert fires.
type AlertHandler func(alert Alert)

// BackendReader is the interface the monitor uses to query trading state.
type BackendReader interface {
	Name() string
	GetBalance(ctx context.Context) (exchange.Balance, error)
	GetAllPositions(ctx context.Context) ([]exchange.Position, error)
}

// MonitorConfig configures trade monitoring.
type MonitorConfig struct {
	PnLThresholdPct    float64       // default -5
	ConsecutiveLosses  int           // default 3
	PriceSlipThreshold float64       // default 0.01 (1%)
	CheckInterval      time.Duration // default 30s
}

func (c *MonitorConfig) setDefaults() {
	if c.PnLThresholdPct == 0 {
		c.PnLThresholdPct = -5
	}
	if c.ConsecutiveLosses == 0 {
		c.ConsecutiveLosses = 3
	}
	if c.PriceSlipThreshold == 0 {
		c.PriceSlipThreshold = 0.01
	}
	if c.CheckInterval == 0 {
		c.CheckInterval = 30 * time.Second
	}
}

// TradeMonitor monitors live trading and triggers alerts.
type TradeMonitor struct {
	cfg      MonitorConfig
	backend  BackendReader
	eventBus events.Publisher
	mu       sync.RWMutex
	handlers []AlertHandler
	stopCh   chan struct{}
	wg       sync.WaitGroup

	// internal tracking
	lastBalance       float64
	hasLastBalance    bool
	consecutiveLosses int
	apiErrors         int
	apiCalls          int
	fills             []FillRecord
}

// FillRecord tracks an order fill for price slip monitoring.
type FillRecord struct {
	Symbol    string
	Expected  float64
	Actual    float64
	Timestamp time.Time
}

// NewTradeMonitor creates a trade monitor.
func NewTradeMonitor(cfg MonitorConfig) *TradeMonitor {
	cfg.setDefaults()
	return &TradeMonitor{
		cfg:      cfg,
		stopCh:   make(chan struct{}),
		handlers: make([]AlertHandler, 0),
		fills:    make([]FillRecord, 0, 100),
	}
}

// SetBackend sets the backend to monitor.
func (m *TradeMonitor) SetBackend(backend BackendReader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backend = backend
}

// SetEventBus sets the event bus for publishing alerts.
func (m *TradeMonitor) SetEventBus(bus events.Publisher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventBus = bus
}

// RegisterAlertHandler registers an alert callback.
func (m *TradeMonitor) RegisterAlertHandler(handler AlertHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers = append(m.handlers, handler)
}

// RecordFill records an order fill for price slip monitoring.
func (m *TradeMonitor) RecordFill(symbol string, expected, actual float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fills = append(m.fills, FillRecord{
		Symbol:    symbol,
		Expected:  expected,
		Actual:    actual,
		Timestamp: time.Now(),
	})
	if len(m.fills) > 100 {
		m.fills = m.fills[len(m.fills)-100:]
	}
}

// RecordAPIError records an API error.
func (m *TradeMonitor) RecordAPIError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apiErrors++
}

// RecordAPICall records an API call.
func (m *TradeMonitor) RecordAPICall() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apiCalls++
}

// RecordLoss records a losing trade.
func (m *TradeMonitor) RecordLoss() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.consecutiveLosses++
}

// RecordWin resets the consecutive loss counter.
func (m *TradeMonitor) RecordWin() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.consecutiveLosses = 0
}

// Start begins the monitoring loop.
func (m *TradeMonitor) Start(ctx context.Context) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.cfg.CheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.check(ctx)
			}
		}
	}()
}

// Stop stops the monitoring loop.
func (m *TradeMonitor) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

func (m *TradeMonitor) check(ctx context.Context) {
	m.mu.RLock()
	backend := m.backend
	m.mu.RUnlock()

	if backend == nil {
		return
	}

	// Check balance anomaly
	bal, err := backend.GetBalance(ctx)
	if err == nil {
		m.checkBalanceAnomaly(bal.Total)
	}

	// Check positions PnL
	positions, err := backend.GetAllPositions(ctx)
	if err == nil {
		for _, pos := range positions {
			m.checkPositionPnL(pos)
		}
	}

	// Check consecutive losses
	m.mu.RLock()
	cl := m.consecutiveLosses
	m.mu.RUnlock()
	if cl >= m.cfg.ConsecutiveLosses {
		m.fireAlert(Alert{
			Type:      AlertConsecutiveLoss,
			Message:   fmt.Sprintf("consecutive losses %d >= threshold %d", cl, m.cfg.ConsecutiveLosses),
			Severity:  "critical",
			Timestamp: time.Now(),
			Data:      map[string]any{"count": cl},
		})
	}

	// Check price slip
	m.checkPriceSlip()

	// Check API error rate
	m.checkAPIErrorRate()
}

func (m *TradeMonitor) checkBalanceAnomaly(current float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasLastBalance {
		m.lastBalance = current
		m.hasLastBalance = true
		return
	}
	if m.lastBalance <= 0 {
		m.lastBalance = current
		return
	}
	change := math.Abs(current-m.lastBalance) / m.lastBalance
	if change > 0.05 {
		m.fireAlertLocked(Alert{
			Type:      AlertBalanceAnomaly,
			Message:   fmt.Sprintf("balance changed %.2f%%", change*100),
			Severity:  "warning",
			Timestamp: time.Now(),
			Data: map[string]any{
				"last":    m.lastBalance,
				"current": current,
				"change":  change,
			},
		})
	}
	m.lastBalance = current
}

func (m *TradeMonitor) checkPositionPnL(pos exchange.Position) {
	if pos.AvgPrice == 0 {
		return
	}
	pnlPct := (pos.MarkPrice - pos.AvgPrice) / pos.AvgPrice * 100
	if pos.Side == "short" {
		pnlPct = -pnlPct
	}
	if pnlPct <= m.cfg.PnLThresholdPct {
		m.fireAlert(Alert{
			Type:      AlertPnLThreshold,
			Symbol:    pos.Symbol,
			Message:   fmt.Sprintf("%s PnL %.2f%% <= threshold %.2f%%", pos.Symbol, pnlPct, m.cfg.PnLThresholdPct),
			Severity:  "critical",
			Timestamp: time.Now(),
			Data: map[string]any{
				"symbol":  pos.Symbol,
				"pnl_pct": pnlPct,
				"side":    pos.Side,
			},
		})
	}
}

func (m *TradeMonitor) checkPriceSlip() {
	m.mu.RLock()
	fills := make([]FillRecord, len(m.fills))
	copy(fills, m.fills)
	m.mu.RUnlock()

	for _, f := range fills {
		if f.Expected == 0 {
			continue
		}
		slip := math.Abs(f.Actual-f.Expected) / f.Expected
		if slip > m.cfg.PriceSlipThreshold {
			m.fireAlert(Alert{
				Type:      AlertPriceSlip,
				Symbol:    f.Symbol,
				Message:   fmt.Sprintf("%s price slip %.2f%% > threshold %.2f%%", f.Symbol, slip*100, m.cfg.PriceSlipThreshold*100),
				Severity:  "warning",
				Timestamp: f.Timestamp,
				Data: map[string]any{
					"symbol":   f.Symbol,
					"expected": f.Expected,
					"actual":   f.Actual,
					"slip":     slip,
				},
			})
		}
	}
}

func (m *TradeMonitor) checkAPIErrorRate() {
	m.mu.RLock()
	calls := m.apiCalls
	errs := m.apiErrors
	m.mu.RUnlock()
	if calls == 0 {
		return
	}
	rate := float64(errs) / float64(calls)
	if rate > 0.2 && calls > 10 {
		m.fireAlert(Alert{
			Type:      AlertAPIErrorRate,
			Message:   fmt.Sprintf("API error rate %.1f%% (%d/%d)", rate*100, errs, calls),
			Severity:  "critical",
			Timestamp: time.Now(),
			Data: map[string]any{
				"errors": errs,
				"calls":  calls,
				"rate":   rate,
			},
		})
	}
}

func (m *TradeMonitor) fireAlert(alert Alert) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.fireAlertLocked(alert)
}

func (m *TradeMonitor) fireAlertLocked(alert Alert) {
	slog.Warn("trade monitor alert", "type", alert.Type, "message", alert.Message, "severity", alert.Severity)
	for _, h := range m.handlers {
		h(alert)
	}
	if m.eventBus != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		data, _ := json.Marshal(alert)
		m.eventBus.Publish(ctx, events.Event{
			Type:      "quant.monitor.alert",
			Timestamp: alert.Timestamp,
			Data:      data,
		})
	}
}
