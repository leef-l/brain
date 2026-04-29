// Package backtest provides a historical backtesting engine for the quant brain's
// strategy pipeline. It replays candle data bar-by-bar, building Snapshot
// MarketViews for the strategy pool, and tracks simulated trades to produce
// performance statistics.
package backtest

import (
	"fmt"
	"time"

	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/strategy"
)

// Config controls backtest behavior.
type Config struct {
	Symbol    string
	Timeframe string // primary timeframe for strategy evaluation

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

// BacktestEngine runs a backtest over historical candle data.
type BacktestEngine struct {
	config     Config
	aggregator *strategy.RegimeAwareAggregator
	guard      risk.Guard
	sizer      risk.PositionSizer
}

// NewEngine creates a backtest engine with the given config.
func NewEngine(cfg Config) *BacktestEngine {
	if cfg.WarmupBars <= 0 {
		cfg.WarmupBars = 60
	}
	if cfg.Timeframe == "" {
		cfg.Timeframe = "1H"
	}

	guard := risk.DefaultGuard()
	guard.MaxLeverage = cfg.MaxLeverage

	return &BacktestEngine{
		config:     cfg,
		aggregator: strategy.NewRegimeAwareAggregator(),
		guard:      guard,
		sizer:      risk.DefaultPositionSizer(),
	}
}

// SetAggregator overrides the default regime-aware aggregator.
func (e *BacktestEngine) SetAggregator(agg *strategy.RegimeAwareAggregator) {
	if agg != nil {
		e.aggregator = agg
	}
}

// Run executes the backtest over the provided candles.
//
// Parameters:
//   - strategies: list of strategies to use. nil or empty uses the default pool.
//   - candles:    must be sorted by timestamp ascending.
//   - initialEquity: starting capital. <=0 falls back to 10000.
//   - startTime, endTime: optional time range filter. Zero values disable filtering.
func (e *BacktestEngine) Run(
	strategies []strategy.Strategy,
	candles []Candle,
	initialEquity float64,
	startTime, endTime time.Time,
) (*BacktestResult, error) {
	// Resolve initial equity
	if initialEquity <= 0 {
		initialEquity = 10000
	}

	// Filter candles by time range
	filtered := filterCandlesByTime(candles, startTime, endTime)
	if len(filtered) < e.config.WarmupBars+10 {
		return nil, fmt.Errorf("need at least %d candles after warmup, got %d",
			e.config.WarmupBars+10, len(filtered))
	}

	// Build strategy pool
	pool := strategy.DefaultPool()
	if len(strategies) > 0 {
		pool = strategy.NewPool(strategies...)
	}

	start := time.Now()
	equity := initialEquity

	var trades []Trade
	var equitySeries []equityPoint

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

	for i := e.config.WarmupBars; i < len(filtered); i++ {
		bar := filtered[i]

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

				pnlPct := 0.0
				if pos.entryPrice > 0 && pos.quantity > 0 {
					pnlPct = pnl / (pos.entryPrice * pos.quantity)
				}
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
		window := filtered[windowStart:windowEnd]

		view := strategy.Snapshot{
			SymbolValue:    e.config.Symbol,
			TimeframeValue: e.config.Timeframe,
			CandlesByTimeframe: map[string][]strategy.Candle{
				e.config.Timeframe: window,
			},
			CurrentPriceValue: bar.Close,
		}

		// Run strategies (uses computeLegacy path since HasFeatureView=false)
		signals := pool.Compute(view)
		agg := e.aggregator.Aggregate(view, signals, strategy.ReviewContext{})

		// Try to open a new position if flat
		if pos == nil && agg.Direction != strategy.DirectionHold {
			best := bestSignalForDirection(agg)
			if best.Entry <= 0 || best.StopLoss <= 0 {
				// Record equity even when no trade
				equitySeries = append(equitySeries, equityPoint{timestamp: bar.Timestamp, equity: equity})
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
				equitySeries = append(equitySeries, equityPoint{timestamp: bar.Timestamp, equity: equity})
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
		equitySeries = append(equitySeries, equityPoint{timestamp: bar.Timestamp, equity: currentEquity})
	}

	// Close any remaining position at last bar's close
	if pos != nil {
		lastBar := filtered[len(filtered)-1]
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

		pnlPct := 0.0
		if pos.entryPrice > 0 && pos.quantity > 0 {
			pnlPct = pnl / (pos.entryPrice * pos.quantity)
		}

		trades = append(trades, Trade{
			EntryBar:   pos.entryBar,
			ExitBar:    len(filtered) - 1,
			Direction:  pos.direction,
			EntryPrice: pos.entryPrice,
			ExitPrice:  exitPrice,
			Quantity:   pos.quantity,
			PnL:        pnl,
			PnLPct:     pnlPct,
			Reason:     "end_of_data",
			EntryTime:  pos.entryTime,
			ExitTime:   lastBar.Timestamp,
		})
	}

	report := &BacktestResult{
		Symbol:        e.config.Symbol,
		Timeframe:     e.config.Timeframe,
		Bars:          len(filtered),
		Trades:        trades,
		InitialEquity: initialEquity,
		FinalEquity:   equity,
		TotalReturn:   (equity - initialEquity) / initialEquity,
		Duration:      time.Since(start),
	}

	// Compute all performance metrics
	computePerformanceStats(report, equitySeries)

	return report, nil
}

// equityPoint captures equity at a specific timestamp.
type equityPoint struct {
	timestamp int64
	equity    float64
}

// filterCandlesByTime returns candles within [start, end].
// Zero start/end means no bound on that side.
func filterCandlesByTime(candles []Candle, start, end time.Time) []Candle {
	if (start.IsZero() || start.Equal(time.Time{})) && (end.IsZero() || end.Equal(time.Time{})) {
		return candles
	}
	var out []Candle
	for _, c := range candles {
		ts := time.UnixMilli(c.Timestamp)
		if !start.IsZero() && ts.Before(start) {
			continue
		}
		if !end.IsZero() && ts.After(end) {
			continue
		}
		out = append(out, c)
	}
	return out
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
