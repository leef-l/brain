// Package validator provides data quality checks for incoming market events.
package validator

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/leef-l/brain/brains/data/provider"
)

// Config holds thresholds for data validation.
type Config struct {
	MaxPriceChangePct    float64 // single-tick max price change %, default 10.0
	MaxFutureTSMs        int64   // allowed future timestamp offset (ms), default 5000
	MaxStaleTSMs         int64   // allowed stale timestamp offset (ms), default 300000
	GapBackfillThreshold int     // consecutive missing candle threshold, default 3
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxPriceChangePct:    10.0,
		MaxFutureTSMs:        5000,
		MaxStaleTSMs:         300000,
		GapBackfillThreshold: 3,
	}
}

// Alert represents a data quality issue detected by the Validator.
type Alert struct {
	Level   string    // "warning", "critical"
	Type    string    // "price_spike", "gap", "stale", "future_ts", "duplicate_ts"
	Symbol  string
	Detail  string
	EventTS time.Time
}

// AlertSink is a callback that receives alerts.
type AlertSink func(alert Alert)

// nowFunc is overridable for testing.
var nowFunc = func() time.Time { return time.Now() }

// Validator checks incoming DataEvents for quality issues.
type Validator struct {
	config    Config
	lastPrice map[string]float64
	lastTS    map[string]int64
	gap       *GapDetector
	alertSink AlertSink
	mu        sync.Mutex
}

// New creates a Validator with the given config and alert sink.
func New(config Config, alertSink AlertSink) *Validator {
	v := &Validator{
		config:    config,
		lastPrice: make(map[string]float64),
		lastTS:    make(map[string]int64),
		alertSink: alertSink,
	}
	v.gap = NewGapDetector(config.GapBackfillThreshold, func(instID, bar string, from, to int64) {
		if alertSink != nil {
			alertSink(Alert{
				Level:   "warning",
				Type:    "gap",
				Symbol:  instID,
				Detail:  fmt.Sprintf("bar=%s missing candles from %d to %d", bar, from, to),
				EventTS: time.UnixMilli(to),
			})
		}
	})
	return v
}

// Validate returns true if the event is valid, false if it should be discarded.
func (v *Validator) Validate(event *provider.DataEvent) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	now := nowFunc()
	nowMs := now.UnixMilli()

	// 1. Future timestamp
	if event.Timestamp > nowMs+v.config.MaxFutureTSMs {
		v.emit(Alert{
			Level:   "warning",
			Type:    "future_ts",
			Symbol:  event.Symbol,
			Detail:  fmt.Sprintf("timestamp %d is %d ms in the future", event.Timestamp, event.Timestamp-nowMs),
			EventTS: time.UnixMilli(event.Timestamp),
		})
		return false
	}

	// 2. Stale data
	if event.Timestamp < nowMs-v.config.MaxStaleTSMs {
		v.emit(Alert{
			Level:   "warning",
			Type:    "stale",
			Symbol:  event.Symbol,
			Detail:  fmt.Sprintf("timestamp %d is %d ms stale", event.Timestamp, nowMs-event.Timestamp),
			EventTS: time.UnixMilli(event.Timestamp),
		})
		return false
	}

	// 3. Price spike
	price := extractPrice(event)
	if price > 0 {
		key := event.Symbol
		if last, ok := v.lastPrice[key]; ok && last > 0 {
			changePct := math.Abs(price-last) / last * 100
			if changePct > v.config.MaxPriceChangePct {
				v.emit(Alert{
					Level:   "critical",
					Type:    "price_spike",
					Symbol:  event.Symbol,
					Detail:  fmt.Sprintf("price changed %.2f%% (%.4f -> %.4f)", changePct, last, price),
					EventTS: time.UnixMilli(event.Timestamp),
				})
				return false
			}
		}
		v.lastPrice[key] = price
	}

	// 4. Duplicate timestamp
	tsKey := event.Symbol + "|" + event.Topic
	if lastTS, ok := v.lastTS[tsKey]; ok && lastTS == event.Timestamp {
		return false
	}
	v.lastTS[tsKey] = event.Timestamp

	// 5. Gap detection (candles only)
	v.gap.Observe(event)

	return true
}

func (v *Validator) emit(a Alert) {
	if v.alertSink != nil {
		v.alertSink(a)
	}
}

// extractPrice gets a price from the event payload.
func extractPrice(event *provider.DataEvent) float64 {
	switch p := event.Payload.(type) {
	case provider.Candle:
		return p.Close
	case *provider.Candle:
		return p.Close
	case []provider.Candle:
		if len(p) > 0 {
			return p[len(p)-1].Close
		}
	case provider.Trade:
		return p.Price
	case *provider.Trade:
		return p.Price
	case []provider.Trade:
		if len(p) > 0 {
			return p[len(p)-1].Price
		}
	}
	return 0
}
