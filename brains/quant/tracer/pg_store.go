package tracer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGTraceStore persists signal traces to PostgreSQL.
type PGTraceStore struct {
	pool *pgxpool.Pool
}

// NewPGTraceStore creates a trace store from an existing connection pool.
func NewPGTraceStore(pool *pgxpool.Pool) *PGTraceStore {
	return &PGTraceStore{pool: pool}
}

const traceMigrationSQL = `
CREATE TABLE IF NOT EXISTS signal_traces (
    trace_id     VARCHAR(128) PRIMARY KEY,
    ts           TIMESTAMPTZ  NOT NULL,
    symbol       VARCHAR(32)  NOT NULL,
    snapshot_seq BIGINT       NOT NULL DEFAULT 0,
    price        DOUBLE PRECISION NOT NULL DEFAULT 0,
    outcome      VARCHAR(32)  NOT NULL DEFAULT '',
    signals_json JSONB,
    aggregated_json JSONB,
    global_risk_json JSONB,
    accounts_json JSONB,
    created_at   TIMESTAMPTZ  DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_signal_traces_symbol ON signal_traces (symbol, ts DESC);
CREATE INDEX IF NOT EXISTS idx_signal_traces_outcome ON signal_traces (outcome, ts DESC);
CREATE INDEX IF NOT EXISTS idx_signal_traces_ts ON signal_traces (ts DESC);
`

// Migrate creates the signal_traces table.
func (s *PGTraceStore) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, traceMigrationSQL)
	return err
}

func (s *PGTraceStore) Save(ctx context.Context, trace *SignalTrace) error {
	signalsJSON, _ := json.Marshal(trace.Signals)
	aggJSON, _ := json.Marshal(trace.Aggregated)
	globalJSON, _ := json.Marshal(trace.GlobalRisk)
	accountsJSON, _ := json.Marshal(trace.AccountResults)

	const q = `
		INSERT INTO signal_traces
			(trace_id, ts, symbol, snapshot_seq, price, outcome,
			 signals_json, aggregated_json, global_risk_json, accounts_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (trace_id) DO NOTHING`

	_, err := s.pool.Exec(ctx, q,
		trace.TraceID,
		trace.Timestamp,
		trace.Symbol,
		trace.SnapshotSeq,
		trace.Price,
		trace.Outcome,
		signalsJSON,
		aggJSON,
		globalJSON,
		accountsJSON,
	)
	return err
}

func (s *PGTraceStore) Query(ctx context.Context, f TraceFilter) ([]SignalTrace, error) {
	q := `SELECT trace_id, ts, symbol, snapshot_seq, price, outcome,
	             signals_json, aggregated_json, global_risk_json, accounts_json
	      FROM signal_traces WHERE 1=1`
	args := []any{}
	idx := 1

	if f.Symbol != "" {
		q += fmt.Sprintf(" AND symbol=$%d", idx)
		args = append(args, f.Symbol)
		idx++
	}
	if f.Outcome != "" {
		q += fmt.Sprintf(" AND outcome=$%d", idx)
		args = append(args, f.Outcome)
		idx++
	}
	if !f.Since.IsZero() {
		q += fmt.Sprintf(" AND ts >= $%d", idx)
		args = append(args, f.Since)
		idx++
	}

	q += " ORDER BY ts DESC"

	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d", idx)
		args = append(args, f.Limit)
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SignalTrace
	for rows.Next() {
		var t SignalTrace
		var signalsJSON, aggJSON, globalJSON, accountsJSON []byte
		if err := rows.Scan(
			&t.TraceID, &t.Timestamp, &t.Symbol, &t.SnapshotSeq,
			&t.Price, &t.Outcome,
			&signalsJSON, &aggJSON, &globalJSON, &accountsJSON,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if err := json.Unmarshal(signalsJSON, &t.Signals); err != nil {
			return nil, fmt.Errorf("unmarshal signals for trace %s: %w", t.TraceID, err)
		}
		if err := json.Unmarshal(aggJSON, &t.Aggregated); err != nil {
			return nil, fmt.Errorf("unmarshal aggregated for trace %s: %w", t.TraceID, err)
		}
		if err := json.Unmarshal(globalJSON, &t.GlobalRisk); err != nil {
			return nil, fmt.Errorf("unmarshal global_risk for trace %s: %w", t.TraceID, err)
		}
		if err := json.Unmarshal(accountsJSON, &t.AccountResults); err != nil {
			return nil, fmt.Errorf("unmarshal accounts for trace %s: %w", t.TraceID, err)
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *PGTraceStore) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM signal_traces").Scan(&n)
	return n, err
}

var _ Store = (*PGTraceStore)(nil)
