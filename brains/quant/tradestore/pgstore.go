package tradestore

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leef-l/brain/brains/quant/strategy"
)

// pgQueryTimeout is the default timeout for PG queries.
const pgQueryTimeout = 10 * time.Second

// PGStore implements Store backed by PostgreSQL (via pgx/v5).
// It shares the same PG instance as the data brain — just adds
// a trade_records table.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore creates a PGStore from an existing connection pool.
// Call Migrate() after creation to ensure the table exists.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// NewPGStoreFromURL creates a PGStore by dialing a new connection pool.
func NewPGStoreFromURL(ctx context.Context, connURL string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, connURL)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &PGStore{pool: pool}, nil
}

// Migrate creates the trade_records table if it doesn't exist.
const tradeMigrationSQL = `
CREATE TABLE IF NOT EXISTS trade_records (
    id          VARCHAR(128) PRIMARY KEY,
    unit_id     VARCHAR(64)  NOT NULL,
    symbol      VARCHAR(32)  NOT NULL,
    direction   VARCHAR(8)   NOT NULL,
    entry_price DOUBLE PRECISION NOT NULL DEFAULT 0,
    exit_price  DOUBLE PRECISION NOT NULL DEFAULT 0,
    quantity    DOUBLE PRECISION NOT NULL DEFAULT 0,
    pnl         DOUBLE PRECISION NOT NULL DEFAULT 0,
    pnl_pct     DOUBLE PRECISION NOT NULL DEFAULT 0,
    entry_time  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    exit_time   TIMESTAMPTZ,
    reason      VARCHAR(32)  NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_trade_records_symbol ON trade_records (symbol, direction);
CREATE INDEX IF NOT EXISTS idx_trade_records_unit   ON trade_records (unit_id);
CREATE INDEX IF NOT EXISTS idx_trade_records_exit   ON trade_records (exit_time DESC);
`

func (s *PGStore) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, tradeMigrationSQL)
	return err
}

// Save persists a trade record (upsert — allows updating exit fields).
func (s *PGStore) Save(record TradeRecord) error {
	const q = `
		INSERT INTO trade_records
			(id, unit_id, symbol, direction, entry_price, exit_price,
			 quantity, pnl, pnl_pct, entry_time, exit_time, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			exit_price = EXCLUDED.exit_price,
			pnl        = EXCLUDED.pnl,
			pnl_pct    = EXCLUDED.pnl_pct,
			exit_time  = EXCLUDED.exit_time,
			reason     = EXCLUDED.reason`

	exitTime := nilTime(record.ExitTime)
	ctx, cancel := context.WithTimeout(context.Background(), pgQueryTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, q,
		record.ID,
		record.UnitID,
		record.Symbol,
		string(record.Direction),
		record.EntryPrice,
		record.ExitPrice,
		record.Quantity,
		record.PnL,
		record.PnLPct,
		record.EntryTime,
		exitTime,
		record.Reason,
	)
	return err
}

// Query returns trade records matching the filter.
func (s *PGStore) Query(f Filter) []TradeRecord {
	q := `SELECT id, unit_id, symbol, direction, entry_price, exit_price,
	             quantity, pnl, pnl_pct, entry_time, COALESCE(exit_time, '0001-01-01'), reason
	      FROM trade_records WHERE 1=1`
	args := []any{}
	idx := 1

	if f.UnitID != "" {
		q += fmt.Sprintf(" AND unit_id=$%d", idx)
		args = append(args, f.UnitID)
		idx++
	}
	if f.Symbol != "" {
		q += fmt.Sprintf(" AND symbol=$%d", idx)
		args = append(args, f.Symbol)
		idx++
	}
	if f.Direction != "" {
		q += fmt.Sprintf(" AND direction=$%d", idx)
		args = append(args, string(f.Direction))
		idx++
	}
	if !f.Since.IsZero() {
		q += fmt.Sprintf(" AND exit_time >= $%d", idx)
		args = append(args, f.Since)
		idx++
	}

	q += " ORDER BY entry_time DESC"

	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d", idx)
		args = append(args, f.Limit)
	}

	ctx, cancel := context.WithTimeout(context.Background(), pgQueryTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		slog.Warn("pgstore query failed", "err", err)
		return nil
	}
	defer rows.Close()

	var result []TradeRecord
	for rows.Next() {
		var r TradeRecord
		var dir string
		if err := rows.Scan(
			&r.ID, &r.UnitID, &r.Symbol, &dir,
			&r.EntryPrice, &r.ExitPrice, &r.Quantity,
			&r.PnL, &r.PnLPct, &r.EntryTime, &r.ExitTime, &r.Reason,
		); err != nil {
			continue
		}
		r.Direction = strategy.Direction(dir)
		result = append(result, r)
	}
	return result
}

// Stats returns aggregated statistics for the filter.
func (s *PGStore) Stats(f Filter) Stats {
	records := s.Query(f)
	return computeStats(records)
}

// Close closes the underlying connection pool.
// Only call this if the pool was created by NewPGStoreFromURL.
// If sharing a pool, the caller manages the pool lifecycle.
func (s *PGStore) Close() {
	s.pool.Close()
}

// nilTime returns nil for zero time, or a pointer for non-zero.
func nilTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// LoadAll returns all records (used for warming up in-memory caches at startup).
func (s *PGStore) LoadAll(ctx context.Context) ([]TradeRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, unit_id, symbol, direction, entry_price, exit_price,
		       quantity, pnl, pnl_pct, entry_time, COALESCE(exit_time, '0001-01-01'), reason
		FROM trade_records ORDER BY entry_time`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TradeRecord
	for rows.Next() {
		var r TradeRecord
		var dir string
		if err := rows.Scan(
			&r.ID, &r.UnitID, &r.Symbol, &dir,
			&r.EntryPrice, &r.ExitPrice, &r.Quantity,
			&r.PnL, &r.PnLPct, &r.EntryTime, &r.ExitTime, &r.Reason,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		r.Direction = strategy.Direction(dir)
		result = append(result, r)
	}
	return result, rows.Err()
}

// Pool returns the underlying connection pool for sharing with other stores.
func (s *PGStore) Pool() *pgxpool.Pool {
	return s.pool
}

// Ensure PGStore implements Store at compile time.
var _ Store = (*PGStore)(nil)

// pgx import guard (ensure pgx is used for ErrNoRows etc.)
var _ = pgx.ErrNoRows
