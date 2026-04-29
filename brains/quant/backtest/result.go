package backtest

import (
	"fmt"
	"sort"
	"time"

	"github.com/leef-l/brain/brains/quant/strategy"
)

// Candle is the backtest candle format (same fields as strategy.Candle).
type Candle = strategy.Candle

// Trade records a single simulated trade.
type Trade struct {
	EntryBar   int
	ExitBar    int
	Direction  strategy.Direction
	EntryPrice float64
	ExitPrice  float64
	Quantity   float64
	PnL        float64
	PnLPct     float64
	Reason     string
	EntryTime  int64
	ExitTime   int64
}

// BacktestResult is the comprehensive backtest output with performance metrics.
type BacktestResult struct {
	Symbol           string
	Timeframe        string
	Bars             int
	Trades           []Trade
	TotalReturn      float64
	WinRate          float64
	AvgWin           float64
	AvgLoss          float64
	ProfitFactor     float64
	MaxDrawdown      float64 // as fraction (0.1 = 10%)
	SharpeRatio      float64 // annualized
	CalmarRatio      float64 // annualized return / max drawdown
	InitialEquity    float64
	FinalEquity      float64
	Duration         time.Duration // wall-clock time of backtest
	DailyEquityCurve []float64     // equity at end of each trading day
}

// String returns a human-readable summary of the result.
func (r *BacktestResult) String() string {
	return fmt.Sprintf(
		"Backtest: %s %s | %d bars, %d trades\n"+
			"Return: %.2f%% | Win Rate: %.1f%% | PF: %.2f\n"+
			"Max DD: %.2f%% | Sharpe: %.2f | Calmar: %.2f\n"+
			"Equity: %.2f → %.2f | Time: %s",
		r.Symbol, r.Timeframe, r.Bars, len(r.Trades),
		r.TotalReturn*100, r.WinRate*100, r.ProfitFactor,
		r.MaxDrawdown*100, r.SharpeRatio, r.CalmarRatio,
		r.InitialEquity, r.FinalEquity, r.Duration.Round(time.Millisecond),
	)
}

// SortTradesByPnL returns trades sorted by PnL descending.
func (r *BacktestResult) SortTradesByPnL() []Trade {
	sorted := append([]Trade(nil), r.Trades...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].PnL > sorted[j].PnL
	})
	return sorted
}
