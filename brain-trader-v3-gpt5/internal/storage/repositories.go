package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Candle models trader.candles.
type Candle struct {
	InstID    string
	Bar       string
	TS        int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	VolumeCcy *float64
}

// Vector models trader.vectors.
type Vector struct {
	ID         int64
	Collection string
	InstID     string
	Timeframe  sql.NullString
	TS         int64
	Vector     []byte
	Metadata   json.RawMessage
	CreatedAt  time.Time
}

// VectorLabel models trader.vector_labels.
type VectorLabel struct {
	VectorID   int64
	Ret5m      sql.NullFloat64
	Ret15m     sql.NullFloat64
	Ret1h      sql.NullFloat64
	Ret4h      sql.NullFloat64
	Ret24h     sql.NullFloat64
	MaxUp24h   sql.NullFloat64
	MaxDown24h sql.NullFloat64
	LabeledAt  time.Time
}

// Trade models trader.trades.
type Trade struct {
	ID            int64
	Mode          string
	InstID        string
	Direction     string
	Leverage      int
	EntryPrice    float64
	ExitPrice     sql.NullFloat64
	Quantity      string
	PnlPct        sql.NullFloat64
	PnlUSDT       sql.NullFloat64
	HoldSeconds   sql.NullInt64
	Strategy      string
	EntryReason   sql.NullString
	ExitReason    sql.NullString
	MaxProfitPct  sql.NullFloat64
	MaxLossPct    sql.NullFloat64
	EntryVectorID sql.NullInt64
	OpenedAt      time.Time
	ClosedAt      sql.NullTime
	CreatedAt     time.Time
}

// StrategyDaily models trader.strategy_daily.
type StrategyDaily struct {
	Strategy string
	Date     time.Time
	Mode     string
	Trades   int
	Wins     int
	PnlPct   float64
	Sharpe   sql.NullFloat64
	MaxDD    sql.NullFloat64
	Weight   sql.NullFloat64
}

// SystemState models trader.system_state.
type SystemState struct {
	Key       string
	Value     json.RawMessage
	UpdatedAt time.Time
}

// Repositories bundles the table-level repositories.
type Repositories struct {
	db     *sql.DB
	schema string
}

// CandleRepository persists candle history.
type CandleRepository interface {
	Upsert(ctx context.Context, candle Candle) error
	Get(ctx context.Context, instID, bar string, ts int64) (*Candle, error)
	ListRange(ctx context.Context, instID, bar string, fromTS, toTS int64, limit int) ([]Candle, error)
	DeleteBefore(ctx context.Context, bar string, beforeTS int64) (int64, error)
}

// VectorRepository persists embeddings and lookup rows.
type VectorRepository interface {
	Insert(ctx context.Context, vector Vector) (int64, error)
	Get(ctx context.Context, id int64) (*Vector, error)
	ListByLookup(ctx context.Context, collection, instID, timeframe string, limit int) ([]Vector, error)
}

// VectorLabelRepository persists supervised labels.
type VectorLabelRepository interface {
	Upsert(ctx context.Context, label VectorLabel) error
	Get(ctx context.Context, vectorID int64) (*VectorLabel, error)
}

// TradeRepository persists trade records.
type TradeRepository interface {
	Insert(ctx context.Context, trade Trade) (int64, error)
	Get(ctx context.Context, id int64) (*Trade, error)
	ListOpen(ctx context.Context, instID string) ([]Trade, error)
	UpdateExit(ctx context.Context, trade Trade) error
}

// StrategyDailyRepository persists daily strategy stats.
type StrategyDailyRepository interface {
	Upsert(ctx context.Context, stat StrategyDaily) error
	Get(ctx context.Context, strategy string, date time.Time, mode string) (*StrategyDaily, error)
}

// SystemStateRepository persists key/value runtime state.
type SystemStateRepository interface {
	Put(ctx context.Context, state SystemState) error
	Get(ctx context.Context, key string) (*SystemState, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context) ([]SystemState, error)
}

// Candles returns the candle repository.
func (r *Repositories) Candles() CandleRepository {
	return &candleRepo{db: r.db, schema: r.schema}
}

// Vectors returns the vector repository.
func (r *Repositories) Vectors() VectorRepository {
	return &vectorRepo{db: r.db, schema: r.schema}
}

// VectorLabels returns the vector label repository.
func (r *Repositories) VectorLabels() VectorLabelRepository {
	return &vectorLabelRepo{db: r.db, schema: r.schema}
}

// Trades returns the trade repository.
func (r *Repositories) Trades() TradeRepository {
	return &tradeRepo{db: r.db, schema: r.schema}
}

// StrategyDaily returns the strategy stats repository.
func (r *Repositories) StrategyDaily() StrategyDailyRepository {
	return &strategyDailyRepo{db: r.db, schema: r.schema}
}

// SystemState returns the runtime state repository.
func (r *Repositories) SystemState() SystemStateRepository {
	return &systemStateRepo{db: r.db, schema: r.schema}
}

type candleRepo struct {
	db     *sql.DB
	schema string
}

func (r *candleRepo) Upsert(ctx context.Context, candle Candle) error {
	query := fmt.Sprintf(`
INSERT INTO %s.candles (inst_id, bar, ts, o, h, l, c, vol, vol_ccy)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (inst_id, bar, ts) DO UPDATE
SET o = EXCLUDED.o,
    h = EXCLUDED.h,
    l = EXCLUDED.l,
    c = EXCLUDED.c,
    vol = EXCLUDED.vol,
    vol_ccy = EXCLUDED.vol_ccy`, r.schema)
	_, err := r.db.ExecContext(ctx, query, candle.InstID, candle.Bar, candle.TS, candle.Open, candle.High, candle.Low, candle.Close, candle.Volume, candle.VolumeCcy)
	return err
}

func (r *candleRepo) Get(ctx context.Context, instID, bar string, ts int64) (*Candle, error) {
	query := fmt.Sprintf(`
SELECT inst_id, bar, ts, o, h, l, c, vol, vol_ccy
FROM %s.candles
WHERE inst_id = $1 AND bar = $2 AND ts = $3`, r.schema)
	row := r.db.QueryRowContext(ctx, query, instID, bar, ts)
	var candle Candle
	if err := row.Scan(&candle.InstID, &candle.Bar, &candle.TS, &candle.Open, &candle.High, &candle.Low, &candle.Close, &candle.Volume, &candle.VolumeCcy); err != nil {
		return nil, err
	}
	return &candle, nil
}

func (r *candleRepo) ListRange(ctx context.Context, instID, bar string, fromTS, toTS int64, limit int) ([]Candle, error) {
	if limit <= 0 {
		limit = 500
	}
	query := fmt.Sprintf(`
SELECT inst_id, bar, ts, o, h, l, c, vol, vol_ccy
FROM %s.candles
WHERE inst_id = $1 AND bar = $2 AND ts BETWEEN $3 AND $4
ORDER BY ts DESC
LIMIT $5`, r.schema)
	rows, err := r.db.QueryContext(ctx, query, instID, bar, fromTS, toTS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candles []Candle
	for rows.Next() {
		var candle Candle
		if err := rows.Scan(&candle.InstID, &candle.Bar, &candle.TS, &candle.Open, &candle.High, &candle.Low, &candle.Close, &candle.Volume, &candle.VolumeCcy); err != nil {
			return nil, err
		}
		candles = append(candles, candle)
	}
	return candles, rows.Err()
}

func (r *candleRepo) DeleteBefore(ctx context.Context, bar string, beforeTS int64) (int64, error) {
	query := fmt.Sprintf(`DELETE FROM %s.candles WHERE bar = $1 AND ts < $2`, r.schema)
	result, err := r.db.ExecContext(ctx, query, bar, beforeTS)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type vectorRepo struct {
	db     *sql.DB
	schema string
}

func (r *vectorRepo) Insert(ctx context.Context, vector Vector) (int64, error) {
	query := fmt.Sprintf(`
INSERT INTO %s.vectors (collection, inst_id, timeframe, ts, vector, metadata)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id`, r.schema)
	var id int64
	err := r.db.QueryRowContext(ctx, query, vector.Collection, vector.InstID, nullStringPtr(vector.Timeframe), vector.TS, vector.Vector, vector.Metadata).Scan(&id)
	return id, err
}

func (r *vectorRepo) Get(ctx context.Context, id int64) (*Vector, error) {
	query := fmt.Sprintf(`
SELECT id, collection, inst_id, timeframe, ts, vector, metadata, created_at
FROM %s.vectors
WHERE id = $1`, r.schema)
	row := r.db.QueryRowContext(ctx, query, id)
	var vector Vector
	if err := row.Scan(&vector.ID, &vector.Collection, &vector.InstID, &vector.Timeframe, &vector.TS, &vector.Vector, &vector.Metadata, &vector.CreatedAt); err != nil {
		return nil, err
	}
	return &vector, nil
}

func (r *vectorRepo) ListByLookup(ctx context.Context, collection, instID, timeframe string, limit int) ([]Vector, error) {
	if limit <= 0 {
		limit = 100
	}
	query := fmt.Sprintf(`
SELECT id, collection, inst_id, timeframe, ts, vector, metadata, created_at
FROM %s.vectors
WHERE collection = $1 AND inst_id = $2 AND timeframe IS NOT DISTINCT FROM $3
ORDER BY ts DESC
LIMIT $4`, r.schema)
	rows, err := r.db.QueryContext(ctx, query, collection, instID, nullString(timeframe), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vectors []Vector
	for rows.Next() {
		var vector Vector
		if err := rows.Scan(&vector.ID, &vector.Collection, &vector.InstID, &vector.Timeframe, &vector.TS, &vector.Vector, &vector.Metadata, &vector.CreatedAt); err != nil {
			return nil, err
		}
		vectors = append(vectors, vector)
	}
	return vectors, rows.Err()
}

type vectorLabelRepo struct {
	db     *sql.DB
	schema string
}

func (r *vectorLabelRepo) Upsert(ctx context.Context, label VectorLabel) error {
	query := fmt.Sprintf(`
INSERT INTO %s.vector_labels (vector_id, ret_5m, ret_15m, ret_1h, ret_4h, ret_24h, max_up_24h, max_down_24h)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (vector_id) DO UPDATE
SET ret_5m = EXCLUDED.ret_5m,
    ret_15m = EXCLUDED.ret_15m,
    ret_1h = EXCLUDED.ret_1h,
    ret_4h = EXCLUDED.ret_4h,
    ret_24h = EXCLUDED.ret_24h,
    max_up_24h = EXCLUDED.max_up_24h,
    max_down_24h = EXCLUDED.max_down_24h,
    labeled_at = NOW()`, r.schema)
	_, err := r.db.ExecContext(ctx, query, label.VectorID, label.Ret5m, label.Ret15m, label.Ret1h, label.Ret4h, label.Ret24h, label.MaxUp24h, label.MaxDown24h)
	return err
}

func (r *vectorLabelRepo) Get(ctx context.Context, vectorID int64) (*VectorLabel, error) {
	query := fmt.Sprintf(`
SELECT vector_id, ret_5m, ret_15m, ret_1h, ret_4h, ret_24h, max_up_24h, max_down_24h, labeled_at
FROM %s.vector_labels
WHERE vector_id = $1`, r.schema)
	row := r.db.QueryRowContext(ctx, query, vectorID)
	var label VectorLabel
	if err := row.Scan(&label.VectorID, &label.Ret5m, &label.Ret15m, &label.Ret1h, &label.Ret4h, &label.Ret24h, &label.MaxUp24h, &label.MaxDown24h, &label.LabeledAt); err != nil {
		return nil, err
	}
	return &label, nil
}

type tradeRepo struct {
	db     *sql.DB
	schema string
}

func (r *tradeRepo) Insert(ctx context.Context, trade Trade) (int64, error) {
	query := fmt.Sprintf(`
INSERT INTO %s.trades (
	mode, inst_id, direction, leverage, entry_price, exit_price, quantity,
	pnl_pct, pnl_usdt, hold_seconds, strategy, entry_reason, exit_reason,
	max_profit_pct, max_loss_pct, entry_vector_id, opened_at, closed_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7,
	$8, $9, $10, $11, $12, $13,
	$14, $15, $16, $17, $18
) RETURNING id`, r.schema)
	var id int64
	err := r.db.QueryRowContext(ctx, query,
		trade.Mode, trade.InstID, trade.Direction, trade.Leverage, trade.EntryPrice, trade.ExitPrice, trade.Quantity,
		trade.PnlPct, trade.PnlUSDT, trade.HoldSeconds, trade.Strategy, trade.EntryReason, trade.ExitReason,
		trade.MaxProfitPct, trade.MaxLossPct, trade.EntryVectorID, trade.OpenedAt, trade.ClosedAt,
	).Scan(&id)
	return id, err
}

func (r *tradeRepo) Get(ctx context.Context, id int64) (*Trade, error) {
	query := fmt.Sprintf(`
SELECT id, mode, inst_id, direction, leverage, entry_price, exit_price, quantity,
       pnl_pct, pnl_usdt, hold_seconds, strategy, entry_reason, exit_reason,
       max_profit_pct, max_loss_pct, entry_vector_id, opened_at, closed_at, created_at
FROM %s.trades
WHERE id = $1`, r.schema)
	row := r.db.QueryRowContext(ctx, query, id)
	var trade Trade
	if err := row.Scan(
		&trade.ID, &trade.Mode, &trade.InstID, &trade.Direction, &trade.Leverage, &trade.EntryPrice, &trade.ExitPrice, &trade.Quantity,
		&trade.PnlPct, &trade.PnlUSDT, &trade.HoldSeconds, &trade.Strategy, &trade.EntryReason, &trade.ExitReason,
		&trade.MaxProfitPct, &trade.MaxLossPct, &trade.EntryVectorID, &trade.OpenedAt, &trade.ClosedAt, &trade.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &trade, nil
}

func (r *tradeRepo) ListOpen(ctx context.Context, instID string) ([]Trade, error) {
	query := fmt.Sprintf(`
SELECT id, mode, inst_id, direction, leverage, entry_price, exit_price, quantity,
       pnl_pct, pnl_usdt, hold_seconds, strategy, entry_reason, exit_reason,
       max_profit_pct, max_loss_pct, entry_vector_id, opened_at, closed_at, created_at
FROM %s.trades
WHERE inst_id = $1 AND closed_at IS NULL
ORDER BY opened_at DESC`, r.schema)
	rows, err := r.db.QueryContext(ctx, query, instID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var trades []Trade
	for rows.Next() {
		var trade Trade
		if err := rows.Scan(
			&trade.ID, &trade.Mode, &trade.InstID, &trade.Direction, &trade.Leverage, &trade.EntryPrice, &trade.ExitPrice, &trade.Quantity,
			&trade.PnlPct, &trade.PnlUSDT, &trade.HoldSeconds, &trade.Strategy, &trade.EntryReason, &trade.ExitReason,
			&trade.MaxProfitPct, &trade.MaxLossPct, &trade.EntryVectorID, &trade.OpenedAt, &trade.ClosedAt, &trade.CreatedAt,
		); err != nil {
			return nil, err
		}
		trades = append(trades, trade)
	}
	return trades, rows.Err()
}

func (r *tradeRepo) UpdateExit(ctx context.Context, trade Trade) error {
	query := fmt.Sprintf(`
UPDATE %s.trades
SET exit_price = $2,
    pnl_pct = $3,
    pnl_usdt = $4,
    hold_seconds = $5,
    exit_reason = $6,
    max_profit_pct = $7,
    max_loss_pct = $8,
    closed_at = $9
WHERE id = $1`, r.schema)
	_, err := r.db.ExecContext(ctx, query,
		trade.ID, trade.ExitPrice, trade.PnlPct, trade.PnlUSDT, trade.HoldSeconds, trade.ExitReason, trade.MaxProfitPct, trade.MaxLossPct, trade.ClosedAt,
	)
	return err
}

type strategyDailyRepo struct {
	db     *sql.DB
	schema string
}

func (r *strategyDailyRepo) Upsert(ctx context.Context, stat StrategyDaily) error {
	query := fmt.Sprintf(`
INSERT INTO %s.strategy_daily (strategy, date, mode, trades, wins, pnl_pct, sharpe, max_dd, weight)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (strategy, date, mode) DO UPDATE
SET trades = EXCLUDED.trades,
    wins = EXCLUDED.wins,
    pnl_pct = EXCLUDED.pnl_pct,
    sharpe = EXCLUDED.sharpe,
    max_dd = EXCLUDED.max_dd,
    weight = EXCLUDED.weight`, r.schema)
	_, err := r.db.ExecContext(ctx, query, stat.Strategy, dateOnly(stat.Date), stat.Mode, stat.Trades, stat.Wins, stat.PnlPct, stat.Sharpe, stat.MaxDD, stat.Weight)
	return err
}

func (r *strategyDailyRepo) Get(ctx context.Context, strategy string, date time.Time, mode string) (*StrategyDaily, error) {
	query := fmt.Sprintf(`
SELECT strategy, date, mode, trades, wins, pnl_pct, sharpe, max_dd, weight
FROM %s.strategy_daily
WHERE strategy = $1 AND date = $2 AND mode = $3`, r.schema)
	row := r.db.QueryRowContext(ctx, query, strategy, dateOnly(date), mode)
	var stat StrategyDaily
	if err := row.Scan(&stat.Strategy, &stat.Date, &stat.Mode, &stat.Trades, &stat.Wins, &stat.PnlPct, &stat.Sharpe, &stat.MaxDD, &stat.Weight); err != nil {
		return nil, err
	}
	return &stat, nil
}

type systemStateRepo struct {
	db     *sql.DB
	schema string
}

func (r *systemStateRepo) Put(ctx context.Context, state SystemState) error {
	query := fmt.Sprintf(`
INSERT INTO %s.system_state (key, value, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = NOW()`, r.schema)
	_, err := r.db.ExecContext(ctx, query, state.Key, state.Value)
	return err
}

func (r *systemStateRepo) Get(ctx context.Context, key string) (*SystemState, error) {
	query := fmt.Sprintf(`
SELECT key, value, updated_at
FROM %s.system_state
WHERE key = $1`, r.schema)
	row := r.db.QueryRowContext(ctx, query, key)
	var state SystemState
	if err := row.Scan(&state.Key, &state.Value, &state.UpdatedAt); err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *systemStateRepo) Delete(ctx context.Context, key string) error {
	query := fmt.Sprintf(`DELETE FROM %s.system_state WHERE key = $1`, r.schema)
	_, err := r.db.ExecContext(ctx, query, key)
	return err
}

func (r *systemStateRepo) List(ctx context.Context) ([]SystemState, error) {
	query := fmt.Sprintf(`
SELECT key, value, updated_at
FROM %s.system_state
ORDER BY key ASC`, r.schema)
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []SystemState
	for rows.Next() {
		var state SystemState
		if err := rows.Scan(&state.Key, &state.Value, &state.UpdatedAt); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func nullString(value string) sql.NullString {
	if strings.TrimSpace(value) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nullStringPtr(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func dateOnly(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
