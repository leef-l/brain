package tradestore

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leef-l/brain/brains/quant/execution"
)

// PaperPGStore persists paper exchange state (positions, open orders, equity
// snapshots) to PostgreSQL so that data survives sidecar restarts.
//
// On restart the positions are restored into MemoryState, and the first
// ProcessPriceTick recalculates unrealized PnL with live prices.
type PaperPGStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPaperPGStore creates a PaperPGStore from a shared pool.
func NewPaperPGStore(pool *pgxpool.Pool, logger *slog.Logger) *PaperPGStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &PaperPGStore{pool: pool, logger: logger}
}

// ---------- DDL ----------

const paperMigrationSQL = `
CREATE TABLE IF NOT EXISTS paper_positions (
    account_id  VARCHAR(64)  NOT NULL,
    symbol      VARCHAR(32)  NOT NULL,
    pos_side    VARCHAR(8)   NOT NULL,
    quantity    DOUBLE PRECISION NOT NULL DEFAULT 0,
    avg_price   DOUBLE PRECISION NOT NULL DEFAULT 0,
    mark_price  DOUBLE PRECISION NOT NULL DEFAULT 0,
    leverage    INT          NOT NULL DEFAULT 1,
    realized_pnl   DOUBLE PRECISION NOT NULL DEFAULT 0,
    unrealized_pnl DOUBLE PRECISION NOT NULL DEFAULT 0,
    margin      DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at  BIGINT       NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id, symbol, pos_side)
);

CREATE TABLE IF NOT EXISTS paper_orders (
    order_id    VARCHAR(128) PRIMARY KEY,
    account_id  VARCHAR(64)  NOT NULL,
    symbol      VARCHAR(32)  NOT NULL,
    side        VARCHAR(8)   NOT NULL,
    pos_side    VARCHAR(8)   NOT NULL,
    order_type  VARCHAR(16)  NOT NULL,
    quantity    VARCHAR(32)  NOT NULL DEFAULT '0',
    price       VARCHAR(32)  NOT NULL DEFAULT '',
    stop_loss   VARCHAR(32)  NOT NULL DEFAULT '',
    take_profit VARCHAR(32)  NOT NULL DEFAULT '',
    leverage    INT          NOT NULL DEFAULT 1,
    status      VARCHAR(16)  NOT NULL DEFAULT 'open',
    client_ord_id VARCHAR(128) NOT NULL DEFAULT '',
    time_in_force VARCHAR(8) NOT NULL DEFAULT '',
    created_at  BIGINT       NOT NULL DEFAULT 0,
    updated_at  BIGINT       NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_paper_orders_account ON paper_orders (account_id, status);

CREATE TABLE IF NOT EXISTS account_snapshots (
    id          BIGSERIAL    PRIMARY KEY,
    account_id  VARCHAR(64)  NOT NULL,
    equity      DOUBLE PRECISION NOT NULL DEFAULT 0,
    available   DOUBLE PRECISION NOT NULL DEFAULT 0,
    margin      DOUBLE PRECISION NOT NULL DEFAULT 0,
    unrealized_pl DOUBLE PRECISION NOT NULL DEFAULT 0,
    currency    VARCHAR(8)   NOT NULL DEFAULT 'USDT',
    positions   INT          NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ  DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_account_snapshots_account ON account_snapshots (account_id, created_at DESC);

CREATE TABLE IF NOT EXISTS paper_id_counter (
    account_id  VARCHAR(64) PRIMARY KEY,
    next_id     BIGINT NOT NULL DEFAULT 0
);

-- Add account_id column to trade_records if not exists (idempotent).
DO $$ BEGIN
    ALTER TABLE trade_records ADD COLUMN IF NOT EXISTS account_id VARCHAR(64) NOT NULL DEFAULT '';
EXCEPTION WHEN undefined_table THEN NULL;
END $$;
CREATE INDEX IF NOT EXISTS idx_trade_records_account ON trade_records (account_id);
`

// Migrate creates tables if they don't exist.
func (s *PaperPGStore) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, paperMigrationSQL)
	return err
}

// ---------- Position persistence ----------

// LoadPositions loads positions for an account.
func (s *PaperPGStore) LoadPositions(ctx context.Context, accountID string) ([]execution.Position, error) {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		SELECT symbol, pos_side, quantity, avg_price, mark_price,
		       leverage, realized_pnl, unrealized_pnl, margin, updated_at
		FROM paper_positions WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []execution.Position
	for rows.Next() {
		var p execution.Position
		if err := rows.Scan(
			&p.Symbol, &p.PosSide, &p.Quantity, &p.AvgPrice, &p.MarkPrice,
			&p.Leverage, &p.RealizedPnL, &p.UnrealizedPnL, &p.Margin, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// ---------- Open order persistence ----------

// LoadOpenOrders loads open orders for an account.
func (s *PaperPGStore) LoadOpenOrders(ctx context.Context, accountID string) ([]execution.OrderRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		SELECT order_id, symbol, side, pos_side, order_type,
		       quantity, price, stop_loss, take_profit, leverage, status,
		       client_ord_id, time_in_force, created_at, updated_at
		FROM paper_orders
		WHERE account_id = $1 AND status IN ('open', 'accepted', 'triggered')`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []execution.OrderRecord
	for rows.Next() {
		var o execution.OrderRecord
		if err := rows.Scan(
			&o.Intent.ID, &o.Intent.Symbol, &o.Intent.Side, &o.Intent.PosSide,
			&o.Intent.OrderType, &o.Intent.Quantity, &o.Intent.Price,
			&o.Intent.StopLoss, &o.Intent.TakeProfit, &o.Intent.Leverage, &o.Status,
			&o.Intent.ClientOrdID, &o.Intent.TimeInForce, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan order: %w", err)
		}
		result = append(result, o)
	}
	return result, rows.Err()
}

// ---------- Account snapshots ----------

// SaveSnapshot records an equity snapshot for the account and cleans up
// snapshots older than 30 days to prevent unbounded table growth.
func (s *PaperPGStore) SaveSnapshot(ctx context.Context, snap AccountSnapshot) error {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()

	_, err := s.pool.Exec(ctx, `
		INSERT INTO account_snapshots
			(account_id, equity, available, margin, unrealized_pl, currency, positions)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		snap.AccountID, snap.Equity, snap.Available, snap.Margin,
		snap.UnrealizedPL, snap.Currency, snap.Positions,
	)
	if err != nil {
		return err
	}

	// Best-effort cleanup: remove snapshots older than 30 days.
	_, _ = s.pool.Exec(ctx, `
		DELETE FROM account_snapshots
		WHERE account_id = $1 AND created_at < NOW() - INTERVAL '30 days'`,
		snap.AccountID)

	return nil
}

// LoadLatestSnapshot returns the most recent snapshot for an account.
func (s *PaperPGStore) LoadLatestSnapshot(ctx context.Context, accountID string) (AccountSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()

	var snap AccountSnapshot
	err := s.pool.QueryRow(ctx, `
		SELECT account_id, equity, available, margin, unrealized_pl, currency, positions, created_at
		FROM account_snapshots
		WHERE account_id = $1
		ORDER BY created_at DESC LIMIT 1`, accountID).Scan(
		&snap.AccountID, &snap.Equity, &snap.Available, &snap.Margin,
		&snap.UnrealizedPL, &snap.Currency, &snap.Positions, &snap.CreatedAt,
	)
	if err != nil {
		return AccountSnapshot{}, err
	}
	return snap, nil
}

// LoadSnapshots returns snapshots for an account, most recent first.
func (s *PaperPGStore) LoadSnapshots(ctx context.Context, accountID string, limit int) ([]AccountSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()

	q := `SELECT account_id, equity, available, margin, unrealized_pl, currency, positions, created_at
	      FROM account_snapshots WHERE account_id = $1 ORDER BY created_at DESC`
	args := []any{accountID}
	if limit > 0 {
		q += " LIMIT $2"
		args = append(args, limit)
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AccountSnapshot
	for rows.Next() {
		var snap AccountSnapshot
		if err := rows.Scan(
			&snap.AccountID, &snap.Equity, &snap.Available, &snap.Margin,
			&snap.UnrealizedPL, &snap.Currency, &snap.Positions, &snap.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, snap)
	}
	return result, rows.Err()
}

// ---------- Atomic state save ----------

// SaveStateAtomic saves positions, open orders, and the ID counter in a single transaction.
// This prevents partial state (positions saved but orders not) if the process
// crashes mid-save.
func (s *PaperPGStore) SaveStateAtomic(ctx context.Context, accountID string, positions []execution.Position, orders []execution.OrderRecord, nextID int64) error {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Delete + insert positions.
	if _, err := tx.Exec(ctx, `DELETE FROM paper_positions WHERE account_id = $1`, accountID); err != nil {
		return fmt.Errorf("delete positions: %w", err)
	}
	for _, p := range positions {
		if p.Quantity == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO paper_positions
				(account_id, symbol, pos_side, quantity, avg_price, mark_price,
				 leverage, realized_pnl, unrealized_pnl, margin, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			accountID, p.Symbol, p.PosSide, p.Quantity, p.AvgPrice, p.MarkPrice,
			p.Leverage, p.RealizedPnL, p.UnrealizedPnL, p.Margin, p.UpdatedAt,
		); err != nil {
			return fmt.Errorf("insert position %s/%s: %w", p.Symbol, p.PosSide, err)
		}
	}

	// Delete + insert open orders.
	if _, err := tx.Exec(ctx, `DELETE FROM paper_orders WHERE account_id = $1 AND status IN ('open', 'accepted', 'triggered')`, accountID); err != nil {
		return fmt.Errorf("delete open orders: %w", err)
	}
	for _, o := range orders {
		switch o.Status {
		case execution.OrderStatusOpen, execution.OrderStatusAccepted, execution.OrderStatusTriggered:
		default:
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO paper_orders
				(order_id, account_id, symbol, side, pos_side, order_type,
				 quantity, price, stop_loss, take_profit, leverage, status,
				 client_ord_id, time_in_force, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			o.Intent.ID, accountID, o.Intent.Symbol, o.Intent.Side, o.Intent.PosSide,
			o.Intent.OrderType, o.Intent.Quantity, o.Intent.Price,
			o.Intent.StopLoss, o.Intent.TakeProfit, o.Intent.Leverage, o.Status,
			o.Intent.ClientOrdID, o.Intent.TimeInForce, o.CreatedAt, o.UpdatedAt,
		); err != nil {
			return fmt.Errorf("insert order %s: %w", o.Intent.ID, err)
		}
	}

	// Persist ID counter so it survives even when all orders are filled.
	if nextID > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO paper_id_counter (account_id, next_id) VALUES ($1, $2)
			ON CONFLICT (account_id) DO UPDATE SET next_id = EXCLUDED.next_id`,
			accountID, nextID); err != nil {
			return fmt.Errorf("save id counter: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// ---------- Cleanup ----------

// DeleteAccountState removes all persisted state for an account (positions + open orders).
// Used when resetting a paper account.
func (s *PaperPGStore) DeleteAccountState(ctx context.Context, accountID string) error {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, _ = tx.Exec(ctx, `DELETE FROM paper_positions WHERE account_id = $1`, accountID)
	_, _ = tx.Exec(ctx, `DELETE FROM paper_orders WHERE account_id = $1`, accountID)

	return tx.Commit(ctx)
}

// ---------- Types ----------

// AccountSnapshot holds a point-in-time account balance snapshot.
type AccountSnapshot struct {
	AccountID    string    `json:"account_id"`
	Equity       float64   `json:"equity"`
	Available    float64   `json:"available"`
	Margin       float64   `json:"margin"`
	UnrealizedPL float64   `json:"unrealized_pl"`
	Currency     string    `json:"currency"`
	Positions    int       `json:"positions"`
	CreatedAt    time.Time `json:"created_at"`
}

// ---------- ID counter persistence ----------

// SaveNextID persists the current order ID counter for an account.
// This survives restarts even when all orders are filled (and purged from paper_orders).
func (s *PaperPGStore) SaveNextID(ctx context.Context, accountID string, nextID int64) error {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()

	_, err := s.pool.Exec(ctx, `
		INSERT INTO paper_id_counter (account_id, next_id) VALUES ($1, $2)
		ON CONFLICT (account_id) DO UPDATE SET next_id = EXCLUDED.next_id`,
		accountID, nextID)
	return err
}

// LoadNextID loads the persisted order ID counter for an account.
// Returns 0 if no counter was saved (fresh account).
func (s *PaperPGStore) LoadNextID(ctx context.Context, accountID string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, pgQueryTimeout)
	defer cancel()

	var nextID int64
	err := s.pool.QueryRow(ctx, `
		SELECT next_id FROM paper_id_counter WHERE account_id = $1`, accountID).Scan(&nextID)
	if err != nil {
		// pgx returns error for no rows; treat as fresh account.
		return 0, nil
	}
	return nextID, nil
}
