// Package tradestore persists trade records and provides historical
// win-rate statistics for the BayesianSizer and Aggregator Oracle.
package tradestore

import (
	"context"
	"sync"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// TradeRecord is a completed trade persisted for analysis.
type TradeRecord struct {
	ID         string
	UnitID     string
	Symbol     string
	Direction  strategy.Direction
	EntryPrice float64
	ExitPrice  float64
	Quantity   float64
	PnL        float64
	PnLPct     float64
	EntryTime  time.Time
	ExitTime   time.Time
	Reason     string // "stop_loss", "take_profit", "signal_exit", "manual"
}

// Stats holds aggregated trade statistics.
type Stats struct {
	TotalTrades  int
	Wins         int
	Losses       int
	WinRate      float64
	AvgWin       float64
	AvgLoss      float64
	ProfitFactor float64
	TotalPnL     float64
}

// Store is the trade record persistence interface.
// MemoryStore is the default in-memory implementation.
// PGStore is the PostgreSQL-backed implementation.
type Store interface {
	// Save persists a trade record. The context allows the caller to
	// cancel or set a deadline on the write operation.
	Save(ctx context.Context, record TradeRecord) error

	// Query returns trade records matching the filter.
	Query(filter Filter) []TradeRecord

	// Stats returns aggregated statistics for the filter.
	Stats(filter Filter) Stats
}

// Filter constrains which trades to query.
type Filter struct {
	UnitID    string             // empty = all units
	Symbol    string             // empty = all symbols
	Direction strategy.Direction // empty = all directions
	Since     time.Time          // zero = no lower bound
	Limit     int                // 0 = no limit
}

// MemoryStore is a thread-safe in-memory trade store.
type MemoryStore struct {
	mu      sync.RWMutex
	records []TradeRecord
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (s *MemoryStore) Save(_ context.Context, record TradeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *MemoryStore) Query(f Filter) []TradeRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []TradeRecord
	for _, r := range s.records {
		if !matchFilter(r, f) {
			continue
		}
		result = append(result, r)
		if f.Limit > 0 && len(result) >= f.Limit {
			break
		}
	}
	return result
}

func (s *MemoryStore) Stats(f Filter) Stats {
	records := s.Query(f)
	return computeStats(records)
}

func matchFilter(r TradeRecord, f Filter) bool {
	if f.UnitID != "" && r.UnitID != f.UnitID {
		return false
	}
	if f.Symbol != "" && r.Symbol != f.Symbol {
		return false
	}
	if f.Direction != "" && r.Direction != f.Direction {
		return false
	}
	if !f.Since.IsZero() && r.ExitTime.Before(f.Since) {
		return false
	}
	return true
}

func computeStats(records []TradeRecord) Stats {
	if len(records) == 0 {
		return Stats{}
	}

	s := Stats{TotalTrades: len(records)}
	totalWin := 0.0
	totalLoss := 0.0

	for _, r := range records {
		s.TotalPnL += r.PnL
		if r.PnL > 0 {
			s.Wins++
			totalWin += r.PnL
		} else if r.PnL < 0 {
			s.Losses++
			totalLoss += -r.PnL
		}
	}

	if s.TotalTrades > 0 {
		s.WinRate = float64(s.Wins) / float64(s.TotalTrades)
	}
	if s.Wins > 0 {
		s.AvgWin = totalWin / float64(s.Wins)
	}
	if s.Losses > 0 {
		s.AvgLoss = totalLoss / float64(s.Losses)
	}
	if totalLoss > 0 {
		s.ProfitFactor = totalWin / totalLoss
	}

	return s
}
