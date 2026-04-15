// Package backtest provides a historical backtesting engine for the quant brain's
// strategy pipeline. It replays candle data bar-by-bar, building Snapshot
// MarketViews for the strategy pool, and tracks simulated trades to produce
// performance statistics.
package backtest

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
)

// Candle is the backtest candle format (same fields as strategy.Candle).
type Candle = strategy.Candle

// Config controls backtest behavior.
type Config struct {
	Symbol    string
	Timeframe string // primary timeframe for strategy evaluation

	// InitialEquity in base currency.
	InitialEquity float64

	// MaxLeverage allowed in the backtest.
	MaxLeverage int

	// SlippageBps simulated slippage in basis points.
	SlippageBps float64

	// FeeBps simulated fee in basis points.
	FeeBps float64

	// WarmupBars is how many bars to skip before generating signals
	// (to allow indicators to stabilize). Default: 60.
	WarmupBars int
}

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

// Report is the backtest output.
type Report struct {
	Symbol        string
	Timeframe     string
	Bars          int
	Trades        []Trade
	TotalReturn   float64 // final equity / initial - 1
	WinRate       float64
	AvgWin        float64
	AvgLoss       float64
	ProfitFactor  float64
	MaxDrawdown   float64 // as fraction (0.1 = 10%)
	SharpeRatio   float64 // annualized, assuming 252 trading days
	InitialEquity float64
	FinalEquity   float64
	Duration      time.Duration // wall-clock time of backtest
}

// Engine runs a backtest over historical candle data.
type Engine struct {
	config     Config
	pool       *strategy.Pool
	aggregator *strategy.RegimeAwareAggregator
	guard      risk.Guard
	sizer      risk.PositionSizer
}

// NewEngine creates a backtest engine with the given config.
func NewEngine(cfg Config) *Engine {
	if cfg.InitialEquity <= 0 {
		cfg.InitialEquity = 10000
	}
	if cfg.MaxLeverage <= 0 {
		cfg.MaxLeverage = 1
	}
	if cfg.WarmupBars <= 0 {
		cfg.WarmupBars = 60
	}
	if cfg.Timeframe == "" {
		cfg.Timeframe = "1H"
	}

	guard := risk.DefaultGuard()
	guard.MaxLeverage = cfg.MaxLeverage

	return &Engine{
		config:     cfg,
		pool:       strategy.DefaultPool(),
		aggregator: strategy.NewRegimeAwareAggregator(),
		guard:      guard,
		sizer:      risk.DefaultPositionSizer(),
	}
}

// SetPool overrides the default strategy pool.
func (e *Engine) SetPool(pool *strategy.Pool) { e.pool = pool }

// Run executes the backtest over the provided candles.
// candles must be sorted by timestamp ascending.
func (e *Engine) Run(candles []Candle) (*Report, error) {
	if len(candles) < e.config.WarmupBars+10 {
		return nil, fmt.Errorf("need at least %d candles, got %d",
			e.config.WarmupBars+10, len(candles))
	}

	start := time.Now()
	equity := e.config.InitialEquity
	peakEquity := equity
	maxDrawdown := 0.0

	var trades []Trade
	var equityCurve []float64

	// Track open position
	type openPos struct {
		direction  strategy.Direction
		entryPrice float64
		stopLoss   float64
		takeProfit float64
		quantity   float64
		entryBar   int
		entryTime  int64
	}
	var pos *openPos

	slippage := e.config.SlippageBps / 10000
	fee := e.config.FeeBps / 10000

	for i := e.config.WarmupBars; i < len(candles); i++ {
		bar := candles[i]

		// Check stop-loss / take-profit on open position
		if pos != nil {
			exitPrice := 0.0
			exitReason := ""

			if pos.direction == strategy.DirectionLong {
				if bar.Low <= pos.stopLoss {
					exitPrice = pos.stopLoss
					exitReason = "stop_loss"
				} else if bar.High >= pos.takeProfit {
					exitPrice = pos.takeProfit
					exitReason = "take_profit"
				}
			} else {
				if bar.High >= pos.stopLoss {
					exitPrice = pos.stopLoss
					exitReason = "stop_loss"
				} else if bar.Low <= pos.takeProfit {
					exitPrice = pos.takeProfit
					exitReason = "take_profit"
				}
			}

			if exitPrice > 0 {
				// Apply slippage against us
				if pos.direction == strategy.DirectionLong {
					exitPrice *= (1 - slippage)
				} else {
					exitPrice *= (1 + slippage)
				}

				pnl := 0.0
				if pos.direction == strategy.DirectionLong {
					pnl = (exitPrice - pos.entryPrice) * pos.quantity
				} else {
					pnl = (pos.entryPrice - exitPrice) * pos.quantity
				}
				pnl -= exitPrice * pos.quantity * fee // exit fee

				pnlPct := pnl / (pos.entryPrice * pos.quantity)
				equity += pnl

				trades = append(trades, Trade{
					EntryBar:   pos.entryBar,
					ExitBar:    i,
					Direction:  pos.direction,
					EntryPrice: pos.entryPrice,
					ExitPrice:  exitPrice,
					Quantity:   pos.quantity,
					PnL:        pnl,
					PnLPct:     pnlPct,
					Reason:     exitReason,
					EntryTime:  pos.entryTime,
					ExitTime:   bar.Timestamp,
				})
				pos = nil
			}
		}

		// Build MarketView from historical candles
		windowEnd := i + 1
		windowStart := 0
		if windowEnd > 200 {
			windowStart = windowEnd - 200
		}
		window := candles[windowStart:windowEnd]

		view := strategy.Snapshot{
			SymbolValue:    e.config.Symbol,
			TimeframeValue: e.config.Timeframe,
			CandlesByTimeframe: map[string][]strategy.Candle{
				e.config.Timeframe: window,
			},
			CurrentPriceValue: bar.Close,
		}

		// Run strategies (uses computeLegacy path since HasFeatureView=false)
		signals := e.pool.Compute(view)
		agg := e.aggregator.Aggregate(view, signals, strategy.ReviewContext{})

		// Try to open a new position if flat
		if pos == nil && agg.Direction != strategy.DirectionHold {
			best := bestSignalForDirection(agg)
			if best.Entry <= 0 || best.StopLoss <= 0 {
				continue
			}

			// Size the position
			sizeReq := risk.SizeRequest{
				AccountEquity: equity,
				Signal:        best,
				WinRate:       0.5,
				AvgWin:        1.5,
				AvgLoss:       1.0,
			}
			sized, err := e.sizer.Size(sizeReq)
			if err != nil || sized.Quantity <= 0 {
				continue
			}

			// Apply entry slippage
			entryPrice := best.Entry
			if agg.Direction == strategy.DirectionLong {
				entryPrice *= (1 + slippage)
			} else {
				entryPrice *= (1 - slippage)
			}

			// Entry fee
			equity -= entryPrice * sized.Quantity * fee

			pos = &openPos{
				direction:  agg.Direction,
				entryPrice: entryPrice,
				stopLoss:   best.StopLoss,
				takeProfit: best.TakeProfit,
				quantity:   sized.Quantity,
				entryBar:   i,
				entryTime:  bar.Timestamp,
			}
		}

		// Track equity curve
		currentEquity := equity
		if pos != nil {
			if pos.direction == strategy.DirectionLong {
				currentEquity += (bar.Close - pos.entryPrice) * pos.quantity
			} else {
				currentEquity += (pos.entryPrice - bar.Close) * pos.quantity
			}
		}
		equityCurve = append(equityCurve, currentEquity)

		if currentEquity > peakEquity {
			peakEquity = currentEquity
		}
		dd := 0.0
		if peakEquity > 0 {
			dd = (peakEquity - currentEquity) / peakEquity
		}
		if dd > maxDrawdown {
			maxDrawdown = dd
		}
	}

	// Close any remaining position at last bar's close
	if pos != nil {
		lastBar := candles[len(candles)-1]
		exitPrice := lastBar.Close
		if pos.direction == strategy.DirectionLong {
			exitPrice *= (1 - slippage)
		} else {
			exitPrice *= (1 + slippage)
		}

		pnl := 0.0
		if pos.direction == strategy.DirectionLong {
			pnl = (exitPrice - pos.entryPrice) * pos.quantity
		} else {
			pnl = (pos.entryPrice - exitPrice) * pos.quantity
		}
		pnl -= exitPrice * pos.quantity * fee
		equity += pnl

		trades = append(trades, Trade{
			EntryBar:   pos.entryBar,
			ExitBar:    len(candles) - 1,
			Direction:  pos.direction,
			EntryPrice: pos.entryPrice,
			ExitPrice:  exitPrice,
			Quantity:   pos.quantity,
			PnL:        pnl,
			PnLPct:     pnl / (pos.entryPrice * pos.quantity),
			Reason:     "end_of_data",
			EntryTime:  pos.entryTime,
			ExitTime:   lastBar.Timestamp,
		})
	}

	report := &Report{
		Symbol:        e.config.Symbol,
		Timeframe:     e.config.Timeframe,
		Bars:          len(candles),
		Trades:        trades,
		InitialEquity: e.config.InitialEquity,
		FinalEquity:   equity,
		TotalReturn:   (equity - e.config.InitialEquity) / e.config.InitialEquity,
		MaxDrawdown:   maxDrawdown,
		Duration:      time.Since(start),
	}

	computeStats(report)
	if len(equityCurve) > 1 {
		report.SharpeRatio = computeSharpe(equityCurve)
	}

	return report, nil
}

// computeStats fills in WinRate, AvgWin, AvgLoss, ProfitFactor.
func computeStats(r *Report) {
	if len(r.Trades) == 0 {
		return
	}

	wins := 0
	totalWin := 0.0
	totalLoss := 0.0

	for _, t := range r.Trades {
		if t.PnL > 0 {
			wins++
			totalWin += t.PnL
		} else if t.PnL < 0 {
			totalLoss += math.Abs(t.PnL)
		}
	}

	r.WinRate = float64(wins) / float64(len(r.Trades))

	if wins > 0 {
		r.AvgWin = totalWin / float64(wins)
	}
	losses := len(r.Trades) - wins
	if losses > 0 {
		r.AvgLoss = totalLoss / float64(losses)
	}
	if totalLoss > 0 {
		r.ProfitFactor = totalWin / totalLoss
	}
}

// computeSharpe computes annualized Sharpe ratio from equity curve.
func computeSharpe(equity []float64) float64 {
	if len(equity) < 2 {
		return 0
	}

	returns := make([]float64, len(equity)-1)
	for i := 1; i < len(equity); i++ {
		if equity[i-1] > 0 {
			returns[i-1] = (equity[i] - equity[i-1]) / equity[i-1]
		}
	}

	n := float64(len(returns))
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= n

	variance := 0.0
	for _, r := range returns {
		d := r - mean
		variance += d * d
	}
	variance /= n

	stdDev := math.Sqrt(variance)
	if stdDev == 0 {
		return 0
	}

	// Annualize: assume bars are roughly daily-ish
	// For 1H bars: ~6 bars/day on crypto, ~252 trading days
	annualFactor := math.Sqrt(252 * 6)
	return (mean / stdDev) * annualFactor
}

// bestSignalForDirection picks the highest-confidence signal matching direction.
func bestSignalForDirection(agg strategy.AggregatedSignal) strategy.Signal {
	var best strategy.Signal
	for _, s := range agg.Signals {
		if s.Direction == agg.Direction && s.Confidence > best.Confidence {
			best = s
		}
	}
	return best
}

// String returns a human-readable summary of the report.
func (r *Report) String() string {
	return fmt.Sprintf(
		"Backtest: %s %s | %d bars, %d trades\n"+
			"Return: %.2f%% | Win Rate: %.1f%% | PF: %.2f\n"+
			"Max DD: %.2f%% | Sharpe: %.2f\n"+
			"Equity: %.2f → %.2f | Time: %s",
		r.Symbol, r.Timeframe, r.Bars, len(r.Trades),
		r.TotalReturn*100, r.WinRate*100, r.ProfitFactor,
		r.MaxDrawdown*100, r.SharpeRatio,
		r.InitialEquity, r.FinalEquity, r.Duration.Round(time.Millisecond),
	)
}

// SortTradesByPnL returns trades sorted by PnL descending.
func (r *Report) SortTradesByPnL() []Trade {
	sorted := append([]Trade(nil), r.Trades...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].PnL > sorted[j].PnL
	})
	return sorted
}
