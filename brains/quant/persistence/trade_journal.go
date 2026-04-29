// Package persistence provides SQLite-backed persistence for quant-specific
// data: trade journals and backtest results.
package persistence

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/leef-l/brain/brains/quant/strategy"
)

// TradeRecord is a single completed trade persisted for analysis.
type TradeRecord struct {
	ID         string
	Symbol     string
	Direction  strategy.Direction
	EntryPrice float64
	ExitPrice  float64
	Quantity   float64
	PnL        float64
	PnLPct     float64
	EntryTime  time.Time
	ExitTime   time.Time
	Strategy   string
	Regime     string
	Metadata   map[string]any
}

// TradeFilter constrains which trades to list.
type TradeFilter struct {
	Symbol   string
	Strategy string
	Since    time.Time
	Until    time.Time
	Limit    int
}

// TradeStats holds aggregated trade statistics.
type TradeStats struct {
	TotalTrades int
	TotalPnL    float64
	WinRate     float64
}

// TradeJournal persists trade records.
type TradeJournal interface {
	RecordTrade(trade TradeRecord) error
	ListTrades(filter TradeFilter) ([]TradeRecord, error)
	GetTradeStats(symbol string, since time.Time) (TradeStats, error)
}

// SQLiteTradeJournal implements TradeJournal using SQLite.
type SQLiteTradeJournal struct {
	db *sql.DB
}

// NewSQLiteTradeJournal creates a new SQLite-backed trade journal.
func NewSQLiteTradeJournal(db *sql.DB) *SQLiteTradeJournal {
	return &SQLiteTradeJournal{db: db}
}

// RecordTrade persists a trade record. If trade.ID is empty, a new UUID is generated.
func (s *SQLiteTradeJournal) RecordTrade(trade TradeRecord) error {
	if trade.ID == "" {
		trade.ID = uuid.NewString()
	}
	meta, _ := json.Marshal(trade.Metadata)
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	entryUnix := trade.EntryTime.Unix()
	exitUnix := trade.ExitTime.Unix()
	if trade.ExitTime.IsZero() {
		exitUnix = 0
	}

	_, err := s.db.Exec(`
		INSERT INTO quant_trade_records
			(id, symbol, direction, entry_price, exit_price, quantity, pnl, pnl_pct,
			 entry_time, exit_time, strategy, regime, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			exit_price = excluded.exit_price,
			pnl        = excluded.pnl,
			pnl_pct    = excluded.pnl_pct,
			exit_time  = excluded.exit_time,
			metadata   = excluded.metadata`,
		trade.ID, trade.Symbol, string(trade.Direction), trade.EntryPrice, trade.ExitPrice,
		trade.Quantity, trade.PnL, trade.PnLPct, entryUnix, exitUnix,
		trade.Strategy, trade.Regime, string(meta),
	)
	if err != nil {
		return fmt.Errorf("trade_journal: record trade: %w", err)
	}
	return nil
}

// ListTrades returns trades matching the filter, ordered by entry_time DESC.
func (s *SQLiteTradeJournal) ListTrades(filter TradeFilter) ([]TradeRecord, error) {
	q := `SELECT id, symbol, direction, entry_price, exit_price, quantity, pnl, pnl_pct,
	             entry_time, exit_time, strategy, regime, metadata
	      FROM quant_trade_records WHERE 1=1`
	args := []any{}

	if filter.Symbol != "" {
		q += " AND symbol = ?"
		args = append(args, filter.Symbol)
	}
	if filter.Strategy != "" {
		q += " AND strategy = ?"
		args = append(args, filter.Strategy)
	}
	if !filter.Since.IsZero() {
		q += " AND entry_time >= ?"
		args = append(args, filter.Since.Unix())
	}
	if !filter.Until.IsZero() {
		q += " AND entry_time <= ?"
		args = append(args, filter.Until.Unix())
	}
	q += " ORDER BY entry_time DESC"
	if filter.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("trade_journal: list trades: %w", err)
	}
	defer rows.Close()

	var out []TradeRecord
	for rows.Next() {
		var r TradeRecord
		var dir string
		var entryUnix, exitUnix int64
		var metaStr string
		if err := rows.Scan(&r.ID, &r.Symbol, &dir, &r.EntryPrice, &r.ExitPrice,
			&r.Quantity, &r.PnL, &r.PnLPct, &entryUnix, &exitUnix,
			&r.Strategy, &r.Regime, &metaStr); err != nil {
			return nil, fmt.Errorf("trade_journal: scan trade: %w", err)
		}
		r.Direction = strategy.Direction(dir)
		r.EntryTime = time.Unix(entryUnix, 0).UTC()
		if exitUnix != 0 {
			r.ExitTime = time.Unix(exitUnix, 0).UTC()
		}
		if metaStr != "" {
			_ = json.Unmarshal([]byte(metaStr), &r.Metadata)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetTradeStats returns aggregated statistics for trades matching symbol and since.
func (s *SQLiteTradeJournal) GetTradeStats(symbol string, since time.Time) (TradeStats, error) {
	q := `SELECT COUNT(*), COALESCE(SUM(pnl), 0), SUM(CASE WHEN pnl > 0 THEN 1 ELSE 0 END) FROM quant_trade_records WHERE 1=1`
	args := []any{}

	if symbol != "" {
		q += " AND symbol = ?"
		args = append(args, symbol)
	}
	if !since.IsZero() {
		q += " AND entry_time >= ?"
		args = append(args, since.Unix())
	}

	var totalTrades, wins int
	var totalPnL sql.NullFloat64
	err := s.db.QueryRow(q, args...).Scan(&totalTrades, &totalPnL, &wins)
	if err != nil {
		return TradeStats{}, fmt.Errorf("trade_journal: get stats: %w", err)
	}

	stats := TradeStats{
		TotalTrades: totalTrades,
		TotalPnL:    totalPnL.Float64,
	}
	if totalTrades > 0 {
		stats.WinRate = float64(wins) / float64(totalTrades)
	}
	return stats, nil
}
