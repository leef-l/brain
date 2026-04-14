// Package provider defines core data types and interfaces for the Data Brain.
package provider

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Candle represents a single OHLCV candlestick bar.
type Candle struct {
	InstID    string
	Bar       string  // "1m", "5m", "15m", "1H", "4H"
	Timestamp int64   // milliseconds
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	VolumeCcy float64 // volume in coin denomination
}

// Trade represents a single fill / tick.
type Trade struct {
	InstID    string
	TradeID   string
	Price     float64
	Size      float64
	Side      string // "buy" / "sell"
	Timestamp int64
}

// PriceLevel is one price/size pair in an order book.
type PriceLevel struct {
	Price float64
	Size  float64
}

// OrderBook is a 5-level depth snapshot.
type OrderBook struct {
	InstID    string
	Bids      [5]PriceLevel
	Asks      [5]PriceLevel
	Timestamp int64
}

// FundingRate holds a single funding-rate snapshot.
type FundingRate struct {
	InstID      string
	Rate        float64
	NextFunding int64
	Timestamp   int64
}

// EventPriority classifies event urgency.
type EventPriority uint8

const (
	// PriorityRealtime means the event must be delivered immediately.
	PriorityRealtime EventPriority = 1
	// PriorityNearRT means the event is near-real-time and may be buffered.
	PriorityNearRT EventPriority = 2
)

// DataEvent is the unified envelope delivered to consumers.
type DataEvent struct {
	Provider  string
	Symbol    string
	Topic     string
	Timestamp int64
	LocalTS   int64
	Priority  EventPriority
	Payload   any
}

// DataSink is the consumer callback interface.
type DataSink interface {
	OnEvent(event DataEvent)
}

// ProviderHealth reports the current health of a DataProvider.
type ProviderHealth struct {
	Status     string
	Latency    time.Duration
	LastEvent  time.Time
	ErrorCount int64
}

// DataProvider is the abstraction every market-data source must implement.
type DataProvider interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Health() ProviderHealth
	Subscribe(sink DataSink) error
}

// ProviderFactory creates a DataProvider from a generic config map.
type ProviderFactory func(cfg map[string]any) (DataProvider, error)

// ProviderRegistry is a thread-safe registry of provider factories.
type ProviderRegistry struct {
	mu        sync.RWMutex
	factories map[string]ProviderFactory
}

// NewProviderRegistry creates an empty registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		factories: make(map[string]ProviderFactory),
	}
}

// Register adds a factory under the given name.
func (r *ProviderRegistry) Register(name string, factory ProviderFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Create instantiates a provider by looking up the named factory.
func (r *ProviderRegistry) Create(name string, cfg map[string]any) (DataProvider, error) {
	r.mu.RLock()
	f, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", name)
	}
	return f(cfg)
}
