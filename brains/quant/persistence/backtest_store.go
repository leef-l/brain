package persistence

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/leef-l/brain/brains/quant/backtest"
)

// BacktestResult is the comprehensive persisted backtest output.
type BacktestResult struct {
	ID           string
	Symbol       string
	Timeframe    string
	StartTime    time.Time
	EndTime      time.Time
	Bars         int
	TradesCount  int
	TotalReturn  float64
	WinRate      float64
	ProfitFactor float64
	MaxDrawdown  float64
	SharpeRatio  float64
	CalmarRatio  float64
	EquityCurve  []float64
	Trades       []backtest.Trade
	CreatedAt    time.Time
}

// BacktestSummary is a lightweight view of a backtest result.
type BacktestSummary struct {
	ID          string
	Symbol      string
	Timeframe   string
	StartTime   time.Time
	EndTime     time.Time
	TotalReturn float64
	WinRate     float64
	SharpeRatio float64
}

// BacktestStore persists backtest results.
type BacktestStore interface {
	SaveResult(result BacktestResult) (string, error)
	GetResult(id string) (*BacktestResult, error)
	ListResults(symbol string, limit int) ([]BacktestSummary, error)
	DeleteResult(id string) error
}

// SQLiteBacktestStore implements BacktestStore using SQLite.
type SQLiteBacktestStore struct {
	db *sql.DB
}

// NewSQLiteBacktestStore creates a new SQLite-backed backtest store.
func NewSQLiteBacktestStore(db *sql.DB) *SQLiteBacktestStore {
	return &SQLiteBacktestStore{db: db}
}

// SaveResult persists a backtest result. If result.ID is empty, a new UUID is generated.
func (s *SQLiteBacktestStore) SaveResult(result BacktestResult) (string, error) {
	if result.ID == "" {
		result.ID = uuid.NewString()
	}
	equityJSON, _ := json.Marshal(result.EquityCurve)
	tradesJSON, _ := json.Marshal(result.Trades)
	if len(equityJSON) == 0 {
		equityJSON = []byte("[]")
	}
	if len(tradesJSON) == 0 {
		tradesJSON = []byte("[]")
	}

	createdAt := time.Now().UTC()
	if !result.CreatedAt.IsZero() {
		createdAt = result.CreatedAt.UTC()
	}
	tradesCount := result.TradesCount
	if tradesCount == 0 {
		tradesCount = len(result.Trades)
	}

	_, err := s.db.Exec(`
		INSERT INTO quant_backtest_results
			(id, symbol, timeframe, start_time, end_time, bars, trades_count,
			 total_return, win_rate, profit_factor, max_drawdown, sharpe_ratio, calmar_ratio,
			 equity_curve, trades_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			symbol        = excluded.symbol,
			timeframe     = excluded.timeframe,
			start_time    = excluded.start_time,
			end_time      = excluded.end_time,
			bars          = excluded.bars,
			trades_count  = excluded.trades_count,
			total_return  = excluded.total_return,
			win_rate      = excluded.win_rate,
			profit_factor = excluded.profit_factor,
			max_drawdown  = excluded.max_drawdown,
			sharpe_ratio  = excluded.sharpe_ratio,
			calmar_ratio  = excluded.calmar_ratio,
			equity_curve  = excluded.equity_curve,
			trades_json   = excluded.trades_json`,
		result.ID, result.Symbol, result.Timeframe,
		result.StartTime.Unix(), result.EndTime.Unix(),
		result.Bars, tradesCount,
		result.TotalReturn, result.WinRate, result.ProfitFactor,
		result.MaxDrawdown, result.SharpeRatio, result.CalmarRatio,
		string(equityJSON), string(tradesJSON), createdAt.Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("backtest_store: save result: %w", err)
	}
	return result.ID, nil
}

// GetResult retrieves a backtest result by ID.
func (s *SQLiteBacktestStore) GetResult(id string) (*BacktestResult, error) {
	row := s.db.QueryRow(`
		SELECT id, symbol, timeframe, start_time, end_time, bars, trades_count,
		       total_return, win_rate, profit_factor, max_drawdown, sharpe_ratio, calmar_ratio,
		       equity_curve, trades_json, created_at
		FROM quant_backtest_results WHERE id = ?`, id)

	var r BacktestResult
	var startUnix, endUnix, createdUnix int64
	var equityStr, tradesStr string
	err := row.Scan(&r.ID, &r.Symbol, &r.Timeframe, &startUnix, &endUnix, &r.Bars, &r.TradesCount,
		&r.TotalReturn, &r.WinRate, &r.ProfitFactor, &r.MaxDrawdown, &r.SharpeRatio, &r.CalmarRatio,
		&equityStr, &tradesStr, &createdUnix)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("backtest_store: result %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("backtest_store: get result: %w", err)
	}

	r.StartTime = time.Unix(startUnix, 0).UTC()
	r.EndTime = time.Unix(endUnix, 0).UTC()
	r.CreatedAt = time.Unix(createdUnix, 0).UTC()
	if equityStr != "" {
		_ = json.Unmarshal([]byte(equityStr), &r.EquityCurve)
	}
	if tradesStr != "" {
		_ = json.Unmarshal([]byte(tradesStr), &r.Trades)
	}
	return &r, nil
}

// ListResults returns backtest summaries, optionally filtered by symbol.
func (s *SQLiteBacktestStore) ListResults(symbol string, limit int) ([]BacktestSummary, error) {
	q := `SELECT id, symbol, timeframe, start_time, end_time, total_return, win_rate, sharpe_ratio
	      FROM quant_backtest_results WHERE 1=1`
	args := []any{}

	if symbol != "" {
		q += " AND symbol = ?"
		args = append(args, symbol)
	}
	q += " ORDER BY created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("backtest_store: list results: %w", err)
	}
	defer rows.Close()

	var out []BacktestSummary
	for rows.Next() {
		var sum BacktestSummary
		var startUnix, endUnix int64
		if err := rows.Scan(&sum.ID, &sum.Symbol, &sum.Timeframe, &startUnix, &endUnix,
			&sum.TotalReturn, &sum.WinRate, &sum.SharpeRatio); err != nil {
			return nil, fmt.Errorf("backtest_store: scan summary: %w", err)
		}
		sum.StartTime = time.Unix(startUnix, 0).UTC()
		sum.EndTime = time.Unix(endUnix, 0).UTC()
		out = append(out, sum)
	}
	return out, rows.Err()
}

// DeleteResult removes a backtest result by ID.
func (s *SQLiteBacktestStore) DeleteResult(id string) error {
	_, err := s.db.Exec(`DELETE FROM quant_backtest_results WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("backtest_store: delete result: %w", err)
	}
	return nil
}
