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
    account_id  VARCHAR(64)  NOT NULL DEFAULT '',
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
    mae         DOUBLE PRECISION NOT NULL DEFAULT 0,
    mfe         DOUBLE PRECISION NOT NULL DEFAULT 0,
    leverage    INTEGER          NOT NULL DEFAULT 0,
    stop_loss   DOUBLE PRECISION NOT NULL DEFAULT 0,
    take_profit DOUBLE PRECISION NOT NULL DEFAULT 0,
    atr         DOUBLE PRECISION NOT NULL DEFAULT 0,
    confidence  DOUBLE PRECISION NOT NULL DEFAULT 0,
    strategy    VARCHAR(32)      NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_trade_records_symbol  ON trade_records (symbol, direction);
CREATE INDEX IF NOT EXISTS idx_trade_records_unit    ON trade_records (unit_id);
CREATE INDEX IF NOT EXISTS idx_trade_records_exit    ON trade_records (exit_time DESC);
CREATE INDEX IF NOT EXISTS idx_trade_records_account ON trade_records (account_id);

-- Add columns if table already exists (idempotent).
DO $$ BEGIN
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS mae DOUBLE PRECISION NOT NULL DEFAULT 0;
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS mfe DOUBLE PRECISION NOT NULL DEFAULT 0;
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS leverage INTEGER NOT NULL DEFAULT 0;
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS stop_loss DOUBLE PRECISION NOT NULL DEFAULT 0;
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS take_profit DOUBLE PRECISION NOT NULL DEFAULT 0;
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS atr DOUBLE PRECISION NOT NULL DEFAULT 0;
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS confidence DOUBLE PRECISION NOT NULL DEFAULT 0;
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS strategy VARCHAR(32) NOT NULL DEFAULT '';
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS orig_stop_loss DOUBLE PRECISION NOT NULL DEFAULT 0;
EXCEPTION WHEN OTHERS THEN NULL;
END $$;
`

func (s *PGStore) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, tradeMigrationSQL)
	return err
}

// Save persists a trade record (upsert — allows updating exit fields).
func (s *PGStore) Save(ctx context.Context, record TradeRecord) error {
	const q = `
		INSERT INTO trade_records
			(id, account_id, unit_id, symbol, direction, entry_price, exit_price,
			 quantity, pnl, pnl_pct, entry_time, exit_time, reason, mae, mfe,
			 leverage, stop_loss, take_profit, atr, confidence, strategy,
			 orig_stop_loss)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		ON CONFLICT (id) DO UPDATE SET
			exit_price = EXCLUDED.exit_price,
			pnl        = EXCLUDED.pnl,
			pnl_pct    = EXCLUDED.pnl_pct,
			exit_time  = EXCLUDED.exit_time,
			reason     = EXCLUDED.reason,
			mae        = GREATEST(trade_records.mae, EXCLUDED.mae),
			mfe        = GREATEST(trade_records.mfe, EXCLUDED.mfe)`

	exitTime := nilTime(record.ExitTime)
	// If OrigStopLoss not set, default to StopLoss (backwards compat).
	origSL := record.OrigStopLoss
	if origSL == 0 {
		origSL = record.StopLoss
	}
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, q,
		record.ID,
		record.AccountID,
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
		record.MAE,
		record.MFE,
		record.Leverage,
		record.StopLoss,
		record.TakeProfit,
		record.ATR,
		record.Confidence,
		record.Strategy,
		origSL,
	)
	return err
}

// Update modifies an existing trade record's exit fields.
func (s *PGStore) Update(ctx context.Context, id string, update TradeUpdate) error {
	const q = `UPDATE trade_records
		SET exit_price = $2, pnl = $3, pnl_pct = $4, exit_time = $5, reason = $6,
		    mae = GREATEST(mae, $7), mfe = GREATEST(mfe, $8)
		WHERE id = $1`
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()
	tag, err := s.pool.Exec(ctx, q, id, update.ExitPrice, update.PnL, update.PnLPct, nilTime(update.ExitTime), update.Reason, update.MAE, update.MFE)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("trade %q not found", id)
	}
	return nil
}

// UpdateSLTP updates the stop-loss and take-profit on an open trade record.
// Used by the trailing stop mechanism.
func (s *PGStore) UpdateSLTP(ctx context.Context, id string, sl, tp float64) error {
	const q = `UPDATE trade_records SET stop_loss = $2, take_profit = $3 WHERE id = $1`
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, q, id, sl, tp)
	return err
}

// UpdateMAEMFE atomically updates MAE/MFE if new values are larger.
func (s *PGStore) UpdateMAEMFE(ctx context.Context, id string, mae, mfe float64) error {
	const q = `UPDATE trade_records
		SET mae = GREATEST(mae, $2), mfe = GREATEST(mfe, $3)
		WHERE id = $1`
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, q, id, mae, mfe)
	return err
}

// Query returns trade records matching the filter.
func (s *PGStore) Query(f Filter) []TradeRecord {
	q := `SELECT id, COALESCE(account_id, ''), unit_id, symbol, direction, entry_price, exit_price,
	             quantity, pnl, pnl_pct, entry_time, COALESCE(exit_time, '0001-01-01'), reason,
	             COALESCE(mae, 0), COALESCE(mfe, 0),
	             COALESCE(leverage, 0), COALESCE(stop_loss, 0), COALESCE(take_profit, 0),
	             COALESCE(atr, 0), COALESCE(confidence, 0), COALESCE(strategy, ''),
	             COALESCE(orig_stop_loss, 0)
	      FROM trade_records WHERE 1=1`
	args := []any{}
	idx := 1

	if f.AccountID != "" {
		q += fmt.Sprintf(" AND account_id=$%d", idx)
		args = append(args, f.AccountID)
		idx++
	}
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
	if f.OpenOnly {
		q += " AND exit_price = 0"
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
			&r.ID, &r.AccountID, &r.UnitID, &r.Symbol, &dir,
			&r.EntryPrice, &r.ExitPrice, &r.Quantity,
			&r.PnL, &r.PnLPct, &r.EntryTime, &r.ExitTime, &r.Reason,
			&r.MAE, &r.MFE,
			&r.Leverage, &r.StopLoss, &r.TakeProfit,
			&r.ATR, &r.Confidence, &r.Strategy,
			&r.OrigStopLoss,
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
		SELECT id, COALESCE(account_id, ''), unit_id, symbol, direction, entry_price, exit_price,
		       quantity, pnl, pnl_pct, entry_time, COALESCE(exit_time, '0001-01-01'), reason,
		       COALESCE(mae, 0), COALESCE(mfe, 0),
		       COALESCE(leverage, 0), COALESCE(stop_loss, 0), COALESCE(take_profit, 0),
		       COALESCE(atr, 0), COALESCE(confidence, 0), COALESCE(strategy, ''),
		       COALESCE(orig_stop_loss, 0)
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
			&r.ID, &r.AccountID, &r.UnitID, &r.Symbol, &dir,
			&r.EntryPrice, &r.ExitPrice, &r.Quantity,
			&r.PnL, &r.PnLPct, &r.EntryTime, &r.ExitTime, &r.Reason,
			&r.MAE, &r.MFE,
			&r.Leverage, &r.StopLoss, &r.TakeProfit,
			&r.ATR, &r.Confidence, &r.Strategy,
			&r.OrigStopLoss,
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
