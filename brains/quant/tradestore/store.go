// Package tradestore persists trade records and provides historical
// win-rate statistics for the BayesianSizer and Aggregator Oracle.
package tradestore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// TradeRecord is a completed trade persisted for analysis.
type TradeRecord struct {
	ID         string
	AccountID  string // paper-main, okx-main, etc.
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

	// Trade parameters at entry — for post-trade analysis and optimization.
	Leverage       int     // actual leverage used
	StopLoss     float64 // current SL price (may be updated by trailing stop)
	TakeProfit   float64 // current TP price (may be updated by trailing stop)
	OrigStopLoss float64 // original SL price at entry (never changes)
	ATR        float64 // ATR value at entry time (for normalizing SL/TP distances)
	Confidence float64 // aggregated signal confidence at entry
	Strategy   string  // dominant strategy name that triggered the trade

	// MAE/MFE: Maximum Adverse/Favorable Excursion during the trade's lifetime.
	// Stored as absolute price distances from entry (always >= 0).
	// Used by L1-3 SLTPOptimizer to recommend optimal ATR multipliers.
	MAE float64 // max adverse price excursion from entry
	MFE float64 // max favorable price excursion from entry
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

	// Update modifies an existing trade record identified by ID.
	// Only non-zero fields in the update are applied.
	Update(ctx context.Context, id string, update TradeUpdate) error

	// UpdateMAEMFE updates only the MAE/MFE fields if the new values are larger.
	// Called on every price tick for open trades; must be fast.
	UpdateMAEMFE(ctx context.Context, id string, mae, mfe float64) error

	// UpdateSLTP updates the stop-loss and take-profit prices on an open trade.
	// Used by the trailing stop mechanism. Only non-zero values are applied.
	UpdateSLTP(ctx context.Context, id string, sl, tp float64) error

	// Query returns trade records matching the filter.
	Query(filter Filter) []TradeRecord

	// Stats returns aggregated statistics for the filter.
	Stats(filter Filter) Stats
}

// TradeUpdate holds the fields to update on a trade record.
type TradeUpdate struct {
	ExitPrice  float64
	PnL        float64
	PnLPct     float64
	ExitTime   time.Time
	Reason     string // "stop_loss", "take_profit", "signal_exit", "manual"
	MAE        float64
	MFE        float64
	StopLoss   float64 // updated by trailing stop
	TakeProfit float64 // updated by trailing stop
}

// Filter constrains which trades to query.
type Filter struct {
	AccountID string             // empty = all accounts
	UnitID    string             // empty = all units
	Symbol    string             // empty = all symbols
	Direction strategy.Direction // empty = all directions
	Since     time.Time          // zero = no lower bound
	Limit     int                // 0 = no limit
	OpenOnly  bool               // true = only records with exit_price == 0
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

func (s *MemoryStore) Update(_ context.Context, id string, update TradeUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].ID == id {
			if update.ExitPrice != 0 {
				s.records[i].ExitPrice = update.ExitPrice
			}
			if !update.ExitTime.IsZero() {
				s.records[i].ExitTime = update.ExitTime
			}
			s.records[i].PnL = update.PnL
			s.records[i].PnLPct = update.PnLPct
			if update.Reason != "" {
				s.records[i].Reason = update.Reason
			}
			if update.MAE > s.records[i].MAE {
				s.records[i].MAE = update.MAE
			}
			if update.MFE > s.records[i].MFE {
				s.records[i].MFE = update.MFE
			}
			if update.StopLoss != 0 {
				s.records[i].StopLoss = update.StopLoss
			}
			if update.TakeProfit != 0 {
				s.records[i].TakeProfit = update.TakeProfit
			}
			return nil
		}
	}
	return fmt.Errorf("trade %q not found", id)
}

func (s *MemoryStore) UpdateMAEMFE(_ context.Context, id string, mae, mfe float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].ID == id {
			if mae > s.records[i].MAE {
				s.records[i].MAE = mae
			}
			if mfe > s.records[i].MFE {
				s.records[i].MFE = mfe
			}
			return nil
		}
	}
	return fmt.Errorf("trade %q not found", id)
}

func (s *MemoryStore) UpdateSLTP(_ context.Context, id string, sl, tp float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].ID == id {
			if sl != 0 {
				s.records[i].StopLoss = sl
			}
			if tp != 0 {
				s.records[i].TakeProfit = tp
			}
			return nil
		}
	}
	return fmt.Errorf("trade %q not found", id)
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
	if f.AccountID != "" && r.AccountID != f.AccountID {
		return false
	}
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
	if f.OpenOnly && r.ExitPrice != 0 {
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
