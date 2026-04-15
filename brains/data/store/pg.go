package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore implements Store backed by PostgreSQL (via pgx).
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore creates a new PGStore and pings the database.
func NewPGStore(ctx context.Context, connString string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &PGStore{pool: pool}, nil
}

// ---------------------------------------------------------------------------
// Migrate — idempotent DDL
// ---------------------------------------------------------------------------

const migrationSQL = `
CREATE TABLE IF NOT EXISTS candles (
    inst_id   VARCHAR(32) NOT NULL,
    bar       VARCHAR(8)  NOT NULL,
    ts        BIGINT      NOT NULL,
    o         DOUBLE PRECISION NOT NULL,
    h         DOUBLE PRECISION NOT NULL,
    l         DOUBLE PRECISION NOT NULL,
    c         DOUBLE PRECISION NOT NULL,
    vol       DOUBLE PRECISION NOT NULL,
    vol_ccy   DOUBLE PRECISION,
    PRIMARY KEY (inst_id, bar, ts)
);
CREATE INDEX IF NOT EXISTS idx_candles_ts ON candles (bar, ts DESC);

CREATE TABLE IF NOT EXISTS vectors (
    id         BIGSERIAL PRIMARY KEY,
    collection VARCHAR(32) NOT NULL,
    inst_id    VARCHAR(32) NOT NULL,
    timeframe  VARCHAR(8),
    ts         BIGINT NOT NULL,
    vector     BYTEA NOT NULL,
    metadata   JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_vectors_lookup ON vectors (collection, inst_id, timeframe, ts DESC);

CREATE TABLE IF NOT EXISTS backfill_progress (
    inst_id    VARCHAR(32) NOT NULL,
    timeframe  VARCHAR(8)  NOT NULL,
    latest_ts  BIGINT      NOT NULL,
    newest_ts  BIGINT      NOT NULL DEFAULT 0,
    bar_count  INT         NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (inst_id, timeframe)
);
-- Migration: add newest_ts column for existing tables.
ALTER TABLE backfill_progress ADD COLUMN IF NOT EXISTS newest_ts BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS active_instruments (
    id           BIGSERIAL PRIMARY KEY,
    inst_id      VARCHAR(32) NOT NULL,
    vol_usdt_24h DOUBLE PRECISION NOT NULL,
    rank         INT NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_active_inst_time ON active_instruments (refreshed_at DESC);

CREATE TABLE IF NOT EXISTS validator_alerts (
    id           BIGSERIAL PRIMARY KEY,
    level        VARCHAR(16) NOT NULL,
    alert_type   VARCHAR(32) NOT NULL,
    symbol       VARCHAR(32),
    detail       TEXT,
    event_ts     TIMESTAMPTZ,
    created_at   TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_validator_alerts_time ON validator_alerts (created_at DESC);
`

func (s *PGStore) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, migrationSQL)
	return err
}

// ---------------------------------------------------------------------------
// CandleStore
// ---------------------------------------------------------------------------

func (s *PGStore) BatchInsert(ctx context.Context, candles []Candle) error {
	if len(candles) == 0 {
		return nil
	}
	const q = `INSERT INTO candles (inst_id, bar, ts, o, h, l, c, vol, vol_ccy)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (inst_id, bar, ts) DO UPDATE SET o=$4, h=$5, l=$6, c=$7, vol=$8, vol_ccy=$9`
	b := &pgx.Batch{}
	for _, c := range candles {
		b.Queue(q, c.InstID, c.Bar, c.Timestamp, c.Open, c.High, c.Low, c.Close, c.Volume, c.VolumeCcy)
	}
	br := s.pool.SendBatch(ctx, b)
	return br.Close()
}

func (s *PGStore) Upsert(ctx context.Context, c Candle) error {
	const q = `
		INSERT INTO candles (inst_id, bar, ts, o, h, l, c, vol, vol_ccy)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (inst_id, bar, ts)
		DO UPDATE SET o=$4, h=$5, l=$6, c=$7, vol=$8, vol_ccy=$9`
	_, err := s.pool.Exec(ctx, q,
		c.InstID, c.Bar, c.Timestamp,
		c.Open, c.High, c.Low, c.Close, c.Volume, c.VolumeCcy,
	)
	return err
}

func (s *PGStore) QueryRange(ctx context.Context, instID, bar string, from, to int64) ([]Candle, error) {
	const q = `
		SELECT inst_id, bar, ts, o, h, l, c, vol, COALESCE(vol_ccy, 0)
		FROM candles
		WHERE inst_id=$1 AND bar=$2 AND ts BETWEEN $3 AND $4
		ORDER BY ts`
	rows, err := s.pool.Query(ctx, q, instID, bar, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Candle
	for rows.Next() {
		var c Candle
		if err := rows.Scan(&c.InstID, &c.Bar, &c.Timestamp,
			&c.Open, &c.High, &c.Low, &c.Close, &c.Volume, &c.VolumeCcy); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *PGStore) LatestTimestamp(ctx context.Context, instID, bar string) (int64, error) {
	const q = `SELECT COALESCE(MAX(ts), 0) FROM candles WHERE inst_id=$1 AND bar=$2`
	var ts int64
	err := s.pool.QueryRow(ctx, q, instID, bar).Scan(&ts)
	return ts, err
}

func (s *PGStore) DeleteBefore(ctx context.Context, bar string, before int64) error {
	const q = `DELETE FROM candles WHERE bar=$1 AND ts < $2`
	_, err := s.pool.Exec(ctx, q, bar, before)
	return err
}

// ---------------------------------------------------------------------------
// VectorStore
// ---------------------------------------------------------------------------

func (s *PGStore) Insert(ctx context.Context, vec FeatureVector) error {
	meta, err := json.Marshal(vec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	const q = `
		INSERT INTO vectors (collection, inst_id, timeframe, ts, vector, metadata)
		VALUES ($1,$2,$3,$4,$5,$6)`
	_, err = s.pool.Exec(ctx, q,
		vec.Collection, vec.InstID, vec.Timeframe, vec.Timestamp, vec.Vector, meta)
	return err
}

func (s *PGStore) QueryLatest(ctx context.Context, collection, instID, timeframe string) (*FeatureVector, error) {
	const q = `
		SELECT collection, inst_id, timeframe, ts, vector, metadata
		FROM vectors
		WHERE collection=$1 AND inst_id=$2 AND timeframe=$3
		ORDER BY ts DESC
		LIMIT 1`
	var v FeatureVector
	var meta []byte
	err := s.pool.QueryRow(ctx, q, collection, instID, timeframe).Scan(
		&v.Collection, &v.InstID, &v.Timeframe, &v.Timestamp, &v.Vector, &meta,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if meta != nil {
		if err := json.Unmarshal(meta, &v.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}
	return &v, nil
}

func (s *PGStore) DeleteVectorsBefore(ctx context.Context, collection string, before int64) error {
	const q = `DELETE FROM vectors WHERE collection=$1 AND ts < $2`
	_, err := s.pool.Exec(ctx, q, collection, before)
	return err
}

// ---------------------------------------------------------------------------
// BackfillStore
// ---------------------------------------------------------------------------

func (s *PGStore) GetProgress(ctx context.Context, instID, timeframe string) (*BackfillProgress, error) {
	const q = `
		SELECT inst_id, timeframe, latest_ts, newest_ts, bar_count
		FROM backfill_progress
		WHERE inst_id=$1 AND timeframe=$2`
	var p BackfillProgress
	err := s.pool.QueryRow(ctx, q, instID, timeframe).Scan(
		&p.InstID, &p.Timeframe, &p.LatestTS, &p.NewestTS, &p.BarCount,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func (s *PGStore) SaveProgress(ctx context.Context, p BackfillProgress) error {
	const q = `
		INSERT INTO backfill_progress (inst_id, timeframe, latest_ts, newest_ts, bar_count, updated_at)
		VALUES ($1,$2,$3,$4,$5, NOW())
		ON CONFLICT (inst_id, timeframe)
		DO UPDATE SET latest_ts=$3, newest_ts=$4, bar_count=$5, updated_at=NOW()`
	_, err := s.pool.Exec(ctx, q, p.InstID, p.Timeframe, p.LatestTS, p.NewestTS, p.BarCount)
	return err
}

// ---------------------------------------------------------------------------
// AlertStore
// ---------------------------------------------------------------------------

func (s *PGStore) InsertAlert(ctx context.Context, a AlertRecord) error {
	const q = `
		INSERT INTO validator_alerts (level, alert_type, symbol, detail, event_ts)
		VALUES ($1, $2, $3, $4, TO_TIMESTAMP($5::DOUBLE PRECISION / 1000))`
	_, err := s.pool.Exec(ctx, q, a.Level, a.AlertType, a.Symbol, a.Detail, a.EventTS)
	return err
}

// ---------------------------------------------------------------------------
// ActiveInstrumentStore
// ---------------------------------------------------------------------------

func (s *PGStore) InsertActiveInstruments(ctx context.Context, records []ActiveInstrumentRecord) error {
	if len(records) == 0 {
		return nil
	}
	const q = `
		INSERT INTO active_instruments (inst_id, vol_usdt_24h, rank, refreshed_at)
		VALUES ($1, $2, $3, TO_TIMESTAMP($4::DOUBLE PRECISION / 1000))`
	b := &pgx.Batch{}
	for _, r := range records {
		b.Queue(q, r.InstID, r.VolUSDT24h, r.Rank, r.RefreshedAt)
	}
	br := s.pool.SendBatch(ctx, b)
	return br.Close()
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func (s *PGStore) Close() error {
	s.pool.Close()
	return nil
}
